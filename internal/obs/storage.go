package obs

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// DiskUsage 是数据目录所在文件系统的容量快照。
type DiskUsage struct {
	Path        string  `json:"path"`
	TotalBytes  uint64  `json:"totalBytes"`
	FreeBytes   uint64  `json:"freeBytes"`
	UsedBytes   uint64  `json:"usedBytes"`
	UsedPercent float64 `json:"usedPercent"`
	Available   bool    `json:"available"`
}

// DiskStats 读取指定路径所在文件系统的容量。
func DiskStats(path string) DiskUsage {
	du := DiskUsage{Path: path}
	total, free, ok := statfs(path)
	if !ok {
		return du
	}
	du.Available = true
	du.TotalBytes, du.FreeBytes = total, free
	if total >= free {
		du.UsedBytes = total - free
	}
	if total > 0 {
		du.UsedPercent = float64(du.UsedBytes) / float64(total) * 100
	}
	return du
}

// ObjectRef 是体检报告里的对象引用（不含权限敏感字段）。
type ObjectRef struct {
	Bucket    string             `json:"bucket"`
	Key       string             `json:"objectKey"`
	FileName  string             `json:"fileName"`
	Size      int64              `json:"size"`
	Category  store.FileCategory `json:"category"`
	CreatedAt time.Time          `json:"createdAt"`
	OwnerID   string             `json:"ownerId,omitempty"`
	PluginID  string             `json:"pluginId,omitempty"`
	Reason    string             `json:"reason,omitempty"`
}

// BucketStat 是单个存储桶的容量与对象画像。
type BucketStat struct {
	Name         string         `json:"name"`
	Objects      int            `json:"objects"`
	Bytes        int64          `json:"bytes"`
	PublicRead   bool           `json:"publicRead"`
	OwnerID      string         `json:"ownerId"`
	Categories   map[string]int `json:"categories"`
	LastUploadAt *time.Time     `json:"lastUploadAt,omitempty"`
	// 健康度：0-100，按断链与孤儿比例扣分。
	Health int `json:"health"`
}

// CategoryStat 是文件类型分布。
type CategoryStat struct {
	Key     string `json:"key"`
	Label   string `json:"label"`
	Objects int    `json:"objects"`
	Bytes   int64  `json:"bytes"`
}

// DupGroup 是相同内容（SHA-256 一致）的重复对象组。
type DupGroup struct {
	SHA256      string      `json:"sha256"`
	Size        int64       `json:"size"`
	Count       int         `json:"count"`
	WastedBytes int64       `json:"wastedBytes"`
	Objects     []ObjectRef `json:"objects"`
}

// OrphanFile 是磁盘上存在、但元数据里没有记录的文件。
type OrphanFile struct {
	Path    string    `json:"path"`
	Size    int64     `json:"size"`
	ModTime time.Time `json:"modTime"`
}

// StorageReport 是一次存储体检的完整结果。
type StorageReport struct {
	GeneratedAt   time.Time      `json:"generatedAt"`
	DurationMs    int64          `json:"durationMs"`
	Objects       int            `json:"objects"`
	Bytes         int64          `json:"bytes"`
	Buckets       []BucketStat   `json:"buckets"`
	Categories    []CategoryStat `json:"categories"`
	TopObjects    []ObjectRef    `json:"topObjects"`
	Duplicates    []DupGroup     `json:"duplicates"`
	Orphans       []OrphanFile   `json:"orphans"`
	OrphanBytes   int64          `json:"orphanBytes"`
	OrphanScanned int            `json:"orphanScanned"`
	Missing       []ObjectRef    `json:"missing"`
	Stale         []ObjectRef    `json:"stale"`
	Expiring      []ObjectRef    `json:"expiring"`
	Disk          DiskUsage      `json:"disk"`
	ObjectDir     string         `json:"objectDir"`
	Truncated     bool           `json:"truncated"`
}

// AuditOptions 控制体检范围。
type AuditOptions struct {
	ObjectDir     string
	RetentionDays int
	MaxScanFiles  int
	TopLimit      int
}

// BuildStorageReport 执行一次完整存储体检。
//
// 复杂度：元数据聚合 O(对象数)；孤儿检测需要遍历对象目录（按 MaxScanFiles 截断），
// 断链检测对每个对象做一次 stat。因此该方法应在后台协程执行，避免阻塞请求。
func BuildStorageReport(objects []store.Object, buckets []store.Bucket, opts AuditOptions) *StorageReport {
	start := time.Now()
	if opts.MaxScanFiles <= 0 {
		opts.MaxScanFiles = 50000
	}
	if opts.TopLimit <= 0 {
		opts.TopLimit = 20
	}
	rep := &StorageReport{
		GeneratedAt: start.UTC(),
		ObjectDir:   opts.ObjectDir,
		Disk:        DiskStats(opts.ObjectDir),
		Buckets:     []BucketStat{},
		Categories:  []CategoryStat{},
		TopObjects:  []ObjectRef{},
		Duplicates:  []DupGroup{},
		Orphans:     []OrphanFile{},
		Missing:     []ObjectRef{},
		Stale:       []ObjectRef{},
		Expiring:    []ObjectRef{},
	}

	bucketIdx := map[string]*BucketStat{}
	for _, b := range buckets {
		bucketIdx[b.Name] = &BucketStat{
			Name: b.Name, PublicRead: b.PublicRead, OwnerID: b.OwnerID,
			Categories: map[string]int{}, Health: 100,
		}
	}
	catIdx := map[string]*CategoryStat{}
	for _, c := range store.AllCategories {
		catIdx[string(c)] = &CategoryStat{Key: string(c), Label: store.CategoryLabels[c]}
	}

	now := time.Now().UTC()
	byHash := map[string][]ObjectRef{}
	missingByBucket := map[string]int{}
	known := make(map[string]struct{}, len(objects))
	// Top N 用「升序小数组 + 二分插入」维护，避免对全部对象做一次 O(n log n) 排序。
	top := make([]ObjectRef, 0, opts.TopLimit)

	for _, o := range objects {
		rep.Objects++
		rep.Bytes += o.Size
		known[filepath.Clean(o.LocalPath)] = struct{}{}
		bs := bucketIdx[o.Bucket]
		if bs == nil {
			bs = &BucketStat{Name: o.Bucket, Categories: map[string]int{}, Health: 100}
			bucketIdx[o.Bucket] = bs
		}
		bs.Objects++
		bs.Bytes += o.Size
		bs.Categories[string(o.Category)]++
		if bs.LastUploadAt == nil || o.CreatedAt.After(*bs.LastUploadAt) {
			t := o.CreatedAt
			bs.LastUploadAt = &t
		}
		if cs := catIdx[string(o.Category)]; cs != nil {
			cs.Objects++
			cs.Bytes += o.Size
		}
		ref := ObjectRef{
			Bucket: o.Bucket, Key: o.ObjectKey, FileName: o.FileName,
			Size: o.Size, Category: o.Category, CreatedAt: o.CreatedAt,
			OwnerID: o.OwnerID, PluginID: o.PluginID,
		}
		if o.SHA256 != "" {
			byHash[o.SHA256] = append(byHash[o.SHA256], ref)
		}
		if opts.ObjectDir != "" {
			if _, err := os.Stat(filepath.Join(opts.ObjectDir, o.LocalPath)); err != nil {
				r := ref
				r.Reason = "元数据存在但文件已丢失"
				rep.Missing = append(rep.Missing, r)
				missingByBucket[o.Bucket]++
			}
		}
		if opts.RetentionDays > 0 && now.Sub(o.CreatedAt) > time.Duration(opts.RetentionDays)*24*time.Hour {
			r := ref
			r.Reason = "超过保留期限"
			rep.Stale = append(rep.Stale, r)
		}
		if o.URLExpireAt != nil {
			left := o.URLExpireAt.Sub(now)
			if left > 0 && left < 24*time.Hour {
				r := ref
				r.Reason = "签名地址将在 24 小时内过期"
				rep.Expiring = append(rep.Expiring, r)
			}
		}
		if len(top) < opts.TopLimit || ref.Size > top[0].Size {
			idx := sort.Search(len(top), func(i int) bool { return top[i].Size >= ref.Size })
			top = append(top, ObjectRef{})
			copy(top[idx+1:], top[idx:])
			top[idx] = ref
			if len(top) > opts.TopLimit {
				top = top[1:]
			}
		}
	}

	for _, bs := range bucketIdx {
		if bs.Objects > 0 {
			if n := missingByBucket[bs.Name]; n > 0 {
				bs.Health = 100 - int(float64(n)/float64(bs.Objects)*100)
			}
		}
		rep.Buckets = append(rep.Buckets, *bs)
	}
	sort.Slice(rep.Buckets, func(i, j int) bool { return rep.Buckets[i].Bytes > rep.Buckets[j].Bytes })
	for _, c := range catIdx {
		rep.Categories = append(rep.Categories, *c)
	}
	sort.Slice(rep.Categories, func(i, j int) bool { return rep.Categories[i].Bytes > rep.Categories[j].Bytes })

	sort.Slice(rep.Missing, func(i, j int) bool { return rep.Missing[i].Size > rep.Missing[j].Size })
	sort.Slice(rep.Stale, func(i, j int) bool { return rep.Stale[i].CreatedAt.Before(rep.Stale[j].CreatedAt) })
	sort.Slice(rep.Expiring, func(i, j int) bool { return rep.Expiring[i].Size > rep.Expiring[j].Size })

	// 重复内容：同一 SHA-256 出现多次即视为冗余存储。
	for hash, list := range byHash {
		if len(list) < 2 {
			continue
		}
		g := DupGroup{SHA256: hash, Count: len(list), Size: list[0].Size, Objects: list}
		g.WastedBytes = list[0].Size * int64(len(list)-1)
		rep.Duplicates = append(rep.Duplicates, g)
	}
	sort.Slice(rep.Duplicates, func(i, j int) bool { return rep.Duplicates[i].WastedBytes > rep.Duplicates[j].WastedBytes })
	if len(rep.Duplicates) > 50 {
		rep.Duplicates = rep.Duplicates[:50]
	}

	// Top 大文件：小数组已按体积升序，倒序输出。
	for i := len(top) - 1; i >= 0; i-- {
		rep.TopObjects = append(rep.TopObjects, top[i])
	}

	// 孤儿文件：磁盘有、元数据无。
	if opts.ObjectDir != "" {
		_ = filepath.WalkDir(opts.ObjectDir, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d == nil {
				return nil
			}
			if d.IsDir() {
				return nil
			}
			name := d.Name()
			if strings.HasPrefix(name, ".upload-") || strings.HasSuffix(name, ".tmp") {
				return nil
			}
			rep.OrphanScanned++
			if rep.OrphanScanned > opts.MaxScanFiles {
				rep.Truncated = true
				return filepath.SkipAll
			}
			rel, err := filepath.Rel(opts.ObjectDir, path)
			if err != nil {
				return nil
			}
			if _, ok := known[filepath.Clean(rel)]; ok {
				return nil
			}
			info, err := d.Info()
			if err != nil {
				return nil
			}
			rep.OrphanBytes += info.Size()
			if len(rep.Orphans) < 200 {
				rep.Orphans = append(rep.Orphans, OrphanFile{Path: rel, Size: info.Size(), ModTime: info.ModTime().UTC()})
			}
			return nil
		})
	}
	sort.Slice(rep.Orphans, func(i, j int) bool { return rep.Orphans[i].Size > rep.Orphans[j].Size })

	rep.DurationMs = time.Since(start).Milliseconds()
	return rep
}

// ---------- 异步体检任务 ----------

// Auditor 管理一次后台体检任务，保证同时只跑一个。
type Auditor struct {
	mu      sync.Mutex
	running bool
	report  *StorageReport
	err     string
	started time.Time
	done    time.Time
}

// NewAuditor 创建体检任务管理器。
func NewAuditor() *Auditor { return &Auditor{} }

// Start 在后台执行体检；若已有任务在跑则返回 false。
func (a *Auditor) Start(build func() *StorageReport) bool {
	a.mu.Lock()
	if a.running {
		a.mu.Unlock()
		return false
	}
	a.running = true
	a.err = ""
	a.started = time.Now()
	a.mu.Unlock()

	go func() {
		defer func() {
			if r := recover(); r != nil {
				a.mu.Lock()
				a.err = "体检任务异常中止"
				a.running = false
				a.mu.Unlock()
			}
		}()
		rep := build()
		a.mu.Lock()
		a.report = rep
		a.running = false
		a.done = time.Now()
		a.mu.Unlock()
	}()
	return true
}

// State 返回体检任务状态与最近一次结果。
func (a *Auditor) State() (running bool, report *StorageReport, errMsg string, started, done time.Time) {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.running, a.report, a.err, a.started, a.done
}
