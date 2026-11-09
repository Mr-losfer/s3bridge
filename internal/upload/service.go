// Package upload 实现「第三方上传 → 统一标准输出 → 本地按 bucket/文件类型归档」。
//
// 先流式落临时文件（同时算 SHA-256、嗅探 MIME），再以文件流调用插件；
// 插件成功后才提交到最终路径并写入对象元数据。这样既避免大文件占用内存，
// 也保证上传列表里不会出现"第三方其实失败"的脏记录。
package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
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
}

func New(st store.Store, mgr *plugin.Manager, objectDir string, maxBytes int64) *Service {
	return &Service{st: st, mgr: mgr, objectDir: objectDir, maxBytes: maxBytes}
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
}

// Put 完成一次聚合上传。
func (s *Service) Put(ctx context.Context, req PutRequest) (*store.Object, error) {
	bucket := strings.TrimSpace(req.Bucket)
	if bucket == "" {
		bucket = "default"
	}
	if err := ValidateBucketName(bucket); err != nil {
		return nil, err
	}
	if err := s.authorizeBucket(bucket, req.OwnerID, req.IsAdmin, store.PermUploadWrite); err != nil {
		return nil, err
	}

	pluginID, err := s.mgr.Select(req.PluginID)
	if err != nil {
		return nil, err
	}

	// 1) 流式写入临时文件
	tmp, err := os.CreateTemp(s.objectDir, ".upload-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(req.Reader, s.maxBytes+1))
	if err != nil {
		_ = tmp.Close()
		return nil, fmt.Errorf("接收上传数据: %w", err)
	}
	if written > s.maxBytes {
		_ = tmp.Close()
		return nil, fmt.Errorf("%w: %d > %d 字节", ErrFileTooLarge, written, s.maxBytes)
	}
	if written == 0 {
		_ = tmp.Close()
		return nil, errors.New("文件内容为空")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	mimeType := strings.TrimSpace(req.MimeType)
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = sniffMIME(tmp)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		return nil, err
	}
	digest := hex.EncodeToString(hasher.Sum(nil))
	category := CategoryOf(mimeType, req.FileName)

	key := strings.TrimSpace(req.ObjectKey)
	if key == "" {
		// 桶内对象键同样按文件类型分组，让 S3 的 prefix 过滤与后台「按类型保存」直觉一致。
		// bucket 由 S3 路径本身承载，不重复写进 key；磁盘归档路径见下方 rel。
		key = fmt.Sprintf("%s/%s/%s_%s", string(category),
			time.Now().UTC().Format("200601"), digest[:10], safeBase(req.FileName))
	}
	key, err = SanitizeKey(key)
	if err != nil {
		_ = tmp.Close()
		return nil, err
	}

	// 2) 调用插件，转换为统一标准输出
	result, err := s.mgr.Upload(ctx, pluginID, plugin.UploadRequest{
		FileName: req.FileName, MimeType: mimeType, Size: written,
		Reader: tmp, Bucket: bucket, ObjectKey: key, UserID: req.OwnerID,
	})
	_ = tmp.Close()
	if err != nil {
		return nil, err
	}
	if result == nil {
		return nil, errors.New("插件未返回上传结果")
	}

	// 3) 提交到本地归档路径：{objectDir}/{bucket}/{category}/{yyyymm}/{hash}-{name}
	rel := filepath.Join(bucket, string(category), time.Now().UTC().Format("200601"),
		digest[:12]+"-"+safeBase(req.FileName))
	finalPath := filepath.Join(s.objectDir, rel)
	if !withinDir(s.objectDir, finalPath) {
		return nil, errors.New("归档路径越界")
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return nil, fmt.Errorf("创建归档目录: %w", err)
	}
	if err := os.Rename(tmpName, finalPath); err != nil {
		return nil, fmt.Errorf("提交归档文件: %w", err)
	}

	obj := &store.Object{
		ID: "obj_" + digest[:16], Bucket: bucket, ObjectKey: key,
		FileName: pickString(req.FileName, result.FileName),
		MimeType: pickString(mimeType, result.MimeType),
		Category: category, Size: written, SHA256: digest,
		Provider: result.Provider, PluginID: pluginID,
		RemoteURL: result.URL, RemoteKey: result.ObjectKey,
		URLExpireAt: result.URLExpireAt,
		LocalPath:   rel, OwnerID: req.OwnerID, CreatedAt: store.NowUTC(),
	}
	if err := s.st.Update(func(st *store.State) error {
		if store.BucketByName(st, bucket) == nil {
			return ErrBucketMissing
		}
		st.Objects = append(st.Objects, *obj)
		return nil
	}); err != nil {
		_ = os.Remove(finalPath)
		return nil, err
	}
	return obj, nil
}

// Get 返回对象元数据与本地文件路径，供 S3 GetObject / 后台下载使用。
func (s *Service) Get(bucket, key string, userID string, isAdmin bool) (*store.Object, string, error) {
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
	if err := s.authorizeBucket(bucket, userID, isAdmin, store.PermUploadRead); err != nil {
		return nil, "", err
	}
	path := filepath.Join(s.objectDir, obj.LocalPath)
	if !withinDir(s.objectDir, path) {
		return nil, "", errors.New("对象路径越界")
	}
	return &obj, path, nil
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
