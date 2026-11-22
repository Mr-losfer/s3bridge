// Package upload 实现「第三方上传 → 统一标准输出 → 本地按 bucket/文件类型归档」。
//
// 先流式落临时文件（同时算 SHA-256、嗅探 MIME），再以文件流调用插件；
// 插件成功后才提交到最终路径并写入对象元数据。这样既避免大文件占用内存，
// 也保证上传列表里不会出现"第三方其实失败"的脏记录。
package upload

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

var (
	ErrBucketMissing  = errors.New("存储桶不存在")
	ErrForbidden      = errors.New("无权访问该存储桶")
	ErrFileTooLarge   = errors.New("文件超过允许的大小")
	ErrObjectNotFound = errors.New("对象不存在")
)

type Service struct {
	st        store.Store
	mgr       *plugin.Manager
	objectDir string
	maxBytes  int64
	links     *LinkSigner
}

// New 创建上传服务。masterKey 用于签发对象直链，ttl 为直链有效期。
func New(st store.Store, mgr *plugin.Manager, objectDir string, maxBytes int64, masterKey []byte, linkTTL time.Duration) *Service {
	return &Service{
		st: st, mgr: mgr, objectDir: objectDir, maxBytes: maxBytes,
		links: NewLinkSigner(masterKey, linkTTL),
	}
}

type PutRequest struct {
	Reader    io.Reader
	FileName  string
	MimeType  string
	Size      int64
	Bucket    string
	ObjectKey string
	PluginID  string
	OwnerID   string
	IsAdmin   bool
	// Overwrite 表示同 bucket+key 已存在时覆盖旧对象，而不是追加一条新记录。
	// 开放上传、WebDAV/FTP 的 PUT 都要求覆盖语义。
	Overwrite bool
}

// Put 完成一次聚合上传：落盘 → 调度插件 → 提交归档。
func (s *Service) Put(ctx context.Context, req PutRequest) (*store.Object, error) {
	bucket, err := s.prepareBucket(req)
	if err != nil {
		return nil, err
	}
	// 先落盘再判类型：MIME 嗅探需要文件头，插件调度依赖类型，
	// 因此把「选插件」推迟到 stage 之后。
	st, err := s.stage(req)
	if err != nil {
		return nil, err
	}
	defer st.cleanup()

	// 按文件类型选择插件：显式指定时校验其承接能力，否则在同类插件中取权重最高者。
	pluginID, err := s.mgr.Select(req.PluginID, st.category)
	if err != nil {
		return nil, err
	}
	key, err := s.objectKey(req.ObjectKey, st)
	if err != nil {
		return nil, err
	}

	// 插件以文件路径读取内容；重新打开而不是共用 stage 的句柄，
	// 避免偏移量受嗅探影响。
	f, err := os.Open(st.path)
	if err != nil {
		return nil, fmt.Errorf("打开临时文件: %w", err)
	}
	defer f.Close()

	result, err := s.mgr.Upload(ctx, pluginID, plugin.UploadRequest{
		FileName: req.FileName, MimeType: st.mimeType, Size: st.size,
		Reader: f, Path: st.path, Bucket: bucket, ObjectKey: key, UserID: req.OwnerID,
	})
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("插件未返回上传结果")
	}

	return s.commit(st, commitInfo{
		bucket: bucket, key: key,
		fileName: pickString(req.FileName, result.FileName),
		mimeType: pickString(st.mimeType, result.MimeType),
		category: st.category,
		provider: result.Provider, pluginID: pluginID,
		remoteURL: result.URL, remoteKey: result.ObjectKey,
		expireAt: result.URLExpireAt, ownerID: req.OwnerID,
		overwrite: req.Overwrite,
	})
}

// Get 返回对象元数据与本地文件路径，供 S3 GetObject / 后台下载使用。
func (s *Service) Get(bucket, key string, userID string, isAdmin bool) (*store.Object, string, error) {
	obj, path, err := s.GetAny(bucket, key)
	if err != nil {
		return nil, "", err
	}
	if err := s.authorizeBucket(bucket, userID, isAdmin, store.PermUploadRead); err != nil {
		return nil, "", err
	}
	return obj, path, nil
}

// GetAny 读取对象，不做归属校验。调用方必须先完成鉴权：
// 签名直链、公开读桶、WebDAV/FTP 的 API Key 会话都走这里。
func (s *Service) GetAny(bucket, key string) (*store.Object, string, error) {
	key, err := SanitizeKey(key)
	if err != nil {
		return nil, "", err
	}
	var obj store.Object
	err = s.st.View(func(st *store.State) error {
		o := store.ObjectByKey(st, bucket, key)
		if o == nil {
			return ErrObjectNotFound
		}
		obj = *o
		return nil
	})
	if err != nil {
		return nil, "", err
	}
	path := filepath.Join(s.objectDir, obj.LocalPath)
	if !withinDir(s.objectDir, path) {
		return nil, "", errors.New("对象路径越界")
	}
	s.Decorate(&obj)
	return &obj, path, nil
}

// VerifyLink 校验签名直链；签名器未配置时恒为 false。
func (s *Service) VerifyLink(bucket, key string, exp int64, sig string) bool {
	return s.links != nil && s.links.Verify(bucket, key, exp, sig)
}

// PublicRead 报告存储桶是否允许匿名读取。
func (s *Service) PublicRead(bucket string) bool {
	var ok bool
	_ = s.st.View(func(st *store.State) error {
		if b := store.BucketByName(st, bucket); b != nil {
			ok = b.PublicRead
		}
		return nil
	})
	return ok
}

// BucketNames 返回全部桶名，供 WebDAV/FTP 的根目录列举。
func (s *Service) BucketNames() []string {
	var out []string
	_ = s.st.View(func(st *store.State) error {
		for _, b := range st.Buckets {
			out = append(out, b.Name)
		}
		return nil
	})
	return out
}

// ListFilter 支持按 bucket、文件类型、插件、用户、关键字过滤。
type ListFilter struct {
	Bucket   string
	Category store.FileCategory
	PluginID string
	OwnerID  string
	Keyword  string
	Limit    int
	Offset   int
	IsAdmin  bool
}

type ListResult struct {
	Items      []store.Object `json:"items"`
	Total      int            `json:"total"`
	Categories map[string]int `json:"categories"`
	Buckets    map[string]int `json:"buckets"`
	TotalBytes int64          `json:"totalBytes"`
}

func (s *Service) List(f ListFilter) (ListResult, error) {
	res := ListResult{Items: []store.Object{}, Categories: map[string]int{}, Buckets: map[string]int{}}
	err := s.st.View(func(st *store.State) error {
		for i := len(st.Objects) - 1; i >= 0; i-- {
			o := st.Objects[i]
			if f.Bucket != "" && o.Bucket != f.Bucket {
				continue
			}
			if !f.IsAdmin && f.OwnerID != "" && o.OwnerID != f.OwnerID {
				continue
			}
			if f.Category != "" && o.Category != f.Category {
				continue
			}
			if f.PluginID != "" && o.PluginID != f.PluginID {
				continue
			}
			if f.Keyword != "" && !strings.Contains(strings.ToLower(o.FileName+o.ObjectKey), strings.ToLower(f.Keyword)) {
				continue
			}
			res.Total++
			res.Categories[string(o.Category)]++
			res.Buckets[o.Bucket]++
			res.TotalBytes += o.Size
			if f.Offset > 0 && res.Total <= f.Offset {
				continue
			}
			if f.Limit > 0 && len(res.Items) >= f.Limit {
				continue
			}
			res.Items = append(res.Items, o)
			s.Decorate(&res.Items[len(res.Items)-1])
		}
		return nil
	})
	return res, err
}

// Delete 删除对象元数据与本地文件。
func (s *Service) Delete(bucket, key, userID string, isAdmin bool) error {
	obj, path, err := s.Get(bucket, key, userID, isAdmin)
	if err != nil {
		return err
	}
	if !isAdmin && obj.OwnerID != userID {
		return ErrForbidden
	}
	if err := s.st.Update(func(st *store.State) error {
		for i := range st.Objects {
			if st.Objects[i].ID == obj.ID {
				st.Objects = append(st.Objects[:i], st.Objects[i+1:]...)
				return nil
			}
		}
		return ErrObjectNotFound
	}); err != nil {
		return err
	}
	if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("删除本地文件: %w", err)
	}
	return nil
}

func (s *Service) authorizeBucket(bucket, userID string, isAdmin bool, perm store.Permission) error {
	_ = perm // 权限在 API 层按 Principal 校验；此处只做数据归属校验
	var b *store.Bucket
	err := s.st.View(func(st *store.State) error {
		found := store.BucketByName(st, bucket)
		if found == nil {
			return ErrBucketMissing
		}
		copyB := *found
		b = &copyB
		return nil
	})
	if err != nil {
		return err
	}
	if !isAdmin && b.OwnerID != userID && !b.PublicRead {
		return ErrForbidden
	}
	return nil
}

// ---------- 文件类型归档 ----------

func CategoryOf(mimeType, fileName string) store.FileCategory {
	m := strings.ToLower(strings.TrimSpace(strings.Split(mimeType, ";")[0]))
	ext := strings.ToLower(filepath.Ext(fileName))
	switch {
	case strings.HasPrefix(m, "image/"):
		return store.CategoryImage
	case strings.HasPrefix(m, "video/"):
		return store.CategoryVideo
	case strings.HasPrefix(m, "audio/"):
		return store.CategoryAudio
	case strings.HasPrefix(m, "text/"),
		strings.Contains(m, "pdf"), strings.Contains(m, "word"),
		strings.Contains(m, "excel"), strings.Contains(m, "sheet"),
		strings.Contains(m, "presentation"), strings.Contains(m, "json"),
		strings.Contains(m, "xml"), strings.Contains(m, "csv"),
		strings.Contains(m, "markdown"):
		return store.CategoryDoc
	case strings.Contains(m, "zip"), strings.Contains(m, "tar"),
		strings.Contains(m, "gzip"), strings.Contains(m, "compressed"),
		strings.Contains(m, "7z"), strings.Contains(m, "rar"):
		return store.CategoryArchive
	}
	switch ext {
	case ".png", ".jpg", ".jpeg", ".gif", ".webp", ".svg", ".bmp", ".ico", ".avif", ".heic":
		return store.CategoryImage
	case ".mp4", ".mov", ".mkv", ".webm", ".avi":
		return store.CategoryVideo
	case ".mp3", ".wav", ".flac", ".aac", ".m4a":
		return store.CategoryAudio
	case ".pdf", ".doc", ".docx", ".xls", ".xlsx", ".ppt", ".pptx", ".txt", ".md", ".csv", ".json":
		return store.CategoryDoc
	case ".zip", ".tar", ".gz", ".7z", ".rar":
		return store.CategoryArchive
	}
	return store.CategoryOther
}

func sniffMIME(f *os.File) string {
	buf := make([]byte, 512)
	n, err := f.Read(buf)
	if err != nil && n == 0 {
		return "application/octet-stream"
	}
	_, _ = f.Seek(0, io.SeekStart)
	return http.DetectContentType(buf[:n])
}

// SanitizeKey 拒绝路径穿越与非法字符，是对象键的唯一入口。
func SanitizeKey(key string) (string, error) {
	key = strings.TrimSpace(strings.ReplaceAll(key, "\\", "/"))
	if key == "" {
		return "", errors.New("对象键不能为空")
	}
	if strings.HasPrefix(key, "/") || filepath.IsAbs(key) || strings.Contains(key, "://") {
		return "", errors.New("对象键不能是绝对路径")
	}
	parts := strings.Split(key, "/")
	clean := make([]string, 0, len(parts))
	for _, p := range parts {
		if p == "" || p == "." || p == ".." {
			continue
		}
		if strings.ContainsAny(p, "\x00\r\n") {
			return "", errors.New("对象键包含非法控制字符")
		}
		clean = append(clean, p)
	}
	if len(clean) == 0 {
		return "", errors.New("对象键不合法")
	}
	out := strings.Join(clean, "/")
	if len(out) > 512 {
		return "", errors.New("对象键过长")
	}
	return out, nil
}

func ValidateBucketName(name string) error {
	if len(name) < 2 || len(name) > 63 {
		return errors.New("存储桶名称长度需为 2~63")
	}
	for _, r := range name {
		if !(r >= 'a' && r <= 'z') && !(r >= '0' && r <= '9') && r != '-' && r != '_' && r != '.' {
			return errors.New("存储桶名称只能包含小写字母、数字、- _ .")
		}
	}
	return nil
}

func safeBase(name string) string {
	name = filepath.Base(strings.ReplaceAll(name, "\\", "/"))
	var b strings.Builder
	for _, r := range name {
		if r == '/' || r == '\\' || r == 0 || r < 32 {
			b.WriteRune('_')
		} else {
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), ". ")
	if out == "" {
		return "file"
	}
	if len(out) > 120 {
		out = out[:120]
	}
	return out
}

func withinDir(root, path string) bool {
	absRoot, err := filepath.Abs(root)
	if err != nil {
		return false
	}
	absPath, err := filepath.Abs(path)
	if err != nil {
		return false
	}
	rel, err := filepath.Rel(absRoot, absPath)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

func pickString(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func mimeFromName(name string) string {
	if m := mime.TypeByExtension(filepath.Ext(name)); m != "" {
		return m
	}
	return "application/octet-stream"
}

// SortedCategories 返回固定顺序的类型列表，供前端筛选器使用。
func SortedCategories() []string {
	out := make([]string, 0, len(store.AllCategories))
	for _, c := range store.AllCategories {
		out = append(out, string(c))
	}
	sort.Strings(out)
	return out
}
