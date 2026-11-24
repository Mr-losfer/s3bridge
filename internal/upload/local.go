package upload

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// staged 是已经写完并完成哈希、MIME 嗅探的临时文件。
// 上传链路的两条分支（走插件 / 直接归档）共用这一段，避免大文件处理逻辑分叉。
type staged struct {
	path     string
	size     int64
	sha256   string
	mimeType string
	category store.FileCategory
	fileName string
}

// cleanup 删除临时文件；提交成功后路径已被 rename，调用无副作用。
func (st *staged) cleanup() { _ = os.Remove(st.path) }

// stage 把请求体流式写入临时文件，同时计算 SHA-256 并嗅探 MIME。
func (s *Service) stage(req PutRequest) (*staged, error) {
	tmp, err := os.CreateTemp(s.objectDir, ".upload-*")
	if err != nil {
		return nil, fmt.Errorf("创建临时文件: %w", err)
	}
	tmpName := tmp.Name()
	// 插件是独立进程（工作目录为插件文件夹），必须给它绝对路径才能打开文件。
	if abs, err := filepath.Abs(tmpName); err == nil {
		tmpName = abs
	}
	hasher := sha256.New()
	written, err := io.Copy(io.MultiWriter(tmp, hasher), io.LimitReader(req.Reader, s.maxBytes+1))
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("接收上传数据: %w", err)
	}
	if written > s.maxBytes {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, fmt.Errorf("%w: %d > %d 字节", ErrFileTooLarge, written, s.maxBytes)
	}
	if written == 0 {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, errors.New("文件内容为空")
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return nil, err
	}
	mimeType := strings.TrimSpace(req.MimeType)
	if mimeType == "" || mimeType == "application/octet-stream" {
		mimeType = sniffMIME(tmp)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return nil, err
	}
	return &staged{
		path:     tmpName,
		size:     written,
		sha256:   hex.EncodeToString(hasher.Sum(nil)),
		mimeType: mimeType,
		category: CategoryOf(mimeType, req.FileName),
		fileName: pickString(req.FileName, "file"),
	}, nil
}

// prepareBucket 归一化并校验桶名与写权限。
func (s *Service) prepareBucket(req PutRequest) (string, error) {
	bucket := strings.TrimSpace(req.Bucket)
	if bucket == "" {
		bucket = "default"
	}
	if err := ValidateBucketName(bucket); err != nil {
		return "", err
	}
	if err := s.authorizeBucket(bucket, req.OwnerID, req.IsAdmin, store.PermUploadWrite); err != nil {
		return "", err
	}
	return bucket, nil
}

// objectKey 生成或校验对象键。桶内键按文件类型分组，
// 让 S3 的 prefix 过滤与后台「按类型保存」的直觉一致。
func (s *Service) objectKey(raw string, st *staged) (string, error) {
	key := strings.TrimSpace(raw)
	if key == "" {
		key = fmt.Sprintf("%s/%s/%s_%s", string(st.category),
			time.Now().UTC().Format("200601"), st.sha256[:10], safeBase(st.fileName))
	}
	return SanitizeKey(key)
}

type commitInfo struct {
	bucket    string
	key       string
	fileName  string
	mimeType  string
	category  store.FileCategory
	provider  string
	pluginID  string
	remoteURL string
	remoteKey string
	expireAt  *time.Time
	ownerID   string
	overwrite bool
}

// commit 把临时文件移动到归档路径并写入对象元数据。
func (s *Service) commit(st *staged, info commitInfo) (*store.Object, error) {
	rel := filepath.Join(info.bucket, string(info.category), time.Now().UTC().Format("200601"),
		st.sha256[:12]+"-"+safeBase(info.fileName))
	finalPath := filepath.Join(s.objectDir, rel)
	if !withinDir(s.objectDir, finalPath) {
		return nil, errors.New("归档路径越界")
	}
	if err := os.MkdirAll(filepath.Dir(finalPath), 0o700); err != nil {
		return nil, fmt.Errorf("创建归档目录: %w", err)
	}
	if err := os.Rename(st.path, finalPath); err != nil {
		return nil, fmt.Errorf("提交归档文件: %w", err)
	}

	obj := &store.Object{
		ID: "obj_" + st.sha256[:16], Bucket: info.bucket, ObjectKey: info.key,
		FileName: info.fileName, MimeType: info.mimeType,
		Category: info.category, Size: st.size, SHA256: st.sha256,
		Provider: info.provider, PluginID: info.pluginID,
		RemoteURL: info.remoteURL, RemoteKey: info.remoteKey,
		URLExpireAt: info.expireAt,
		LocalPath:   rel, OwnerID: info.ownerID, CreatedAt: store.NowUTC(),
	}
	// 覆盖写入时先摘掉同 key 的旧记录，再追加新记录：
	// 否则 ObjectByKey 永远取到旧对象，直链看起来「没更新」。
	var oldPaths []string
	if err := s.st.Update(func(st2 *store.State) error {
		if store.BucketByName(st2, info.bucket) == nil {
			return ErrBucketMissing
		}
		if info.overwrite {
			// 同一个 key 可能因为历史版本留下多条记录（早期是追加语义），
			// 这里一次清干净，避免 ObjectByKey 取到旧的那条。
			kept := st2.Objects[:0]
			for _, o := range st2.Objects {
				if o.Bucket == info.bucket && o.ObjectKey == info.key {
					oldPaths = append(oldPaths, filepath.Join(s.objectDir, o.LocalPath))
					continue
				}
				kept = append(kept, o)
			}
			st2.Objects = kept
		}
		st2.Objects = append(st2.Objects, *obj)
		return nil
	}); err != nil {
		_ = os.Remove(finalPath)
		return nil, err
	}
	for _, old := range oldPaths {
		if old != finalPath {
			_ = os.Remove(old)
		}
	}
	s.Decorate(obj)
	return obj, nil
}

// PutLocal 直接归档到本站，不调用第三方插件。
//
// WebDAV / FTP / 开放上传接口使用它：这类写入没有可转发的上游，
// 文件就存进本地桶；读取和下载与插件上传的对象完全一致。
func (s *Service) PutLocal(_ context.Context, req PutRequest) (*store.Object, error) {
	bucket, st, err := s.prepare(req)
	if err != nil {
		return nil, err
	}
	defer st.cleanup()
	return s.commitLocal(st, req, bucket)
}

// commitLocal 把已落盘的文件直接归档到本地，不经过插件。
func (s *Service) commitLocal(st *staged, req PutRequest, bucket string) (*store.Object, error) {
	key, err := s.objectKey(req.ObjectKey, st)
	if err != nil {
		return nil, err
	}
	return s.commit(st, commitInfo{
		bucket: bucket, key: key, fileName: st.fileName,
		mimeType: st.mimeType, category: st.category,
		provider: "local", ownerID: req.OwnerID, overwrite: req.Overwrite,
	})
}

// MimeFromName 按扩展名推断类型，供 FTP/WebDAV 的 PUT 使用。
func MimeFromName(name string) string { return mimeFromName(name) }

// ServeFile 以支持 Range 的方式输出本地对象，供直链与 WebDAV 复用。
func ServeFile(w http.ResponseWriter, r *http.Request, obj *store.Object, path string, cacheControl string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if obj.MimeType != "" {
		w.Header().Set("Content-Type", obj.MimeType)
	}
	w.Header().Set("ETag", `"`+obj.SHA256+`"`)
	w.Header().Set("Accept-Ranges", "bytes")
	if cacheControl != "" {
		w.Header().Set("Cache-Control", cacheControl)
	}
	http.ServeContent(w, r, obj.FileName, obj.CreatedAt, f)
	return nil
}

// CreateBucket 创建存储桶（WebDAV 的 MKCOL 用）。已存在时视为成功。
func (s *Service) CreateBucket(name, ownerID string) error {
	if err := ValidateBucketName(name); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte(name))
	id := "bkt_" + hex.EncodeToString(digest[:6])
	return s.st.Update(func(st *store.State) error {
		if store.BucketByName(st, name) != nil {
			return nil
		}
		st.Buckets = append(st.Buckets, store.Bucket{
			ID: id, Name: name, OwnerID: ownerID, CreatedAt: store.NowUTC(),
		})
		return nil
	})
}
