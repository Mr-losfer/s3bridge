package s3gateway

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/upload"
)

// Gateway 实现 S3 兼容接口的 HTTP 入口，挂载在 /s3/ 下。
type Gateway struct {
	st        store.Store
	authSvc   *auth.Service
	uploads   *upload.Service
	objectDir string
	maxBytes  int64
	verifier  *verifier
	region    string
}

func New(st store.Store, authSvc *auth.Service, uploads *upload.Service, objectDir string, maxBytes int64, region string) *Gateway {
	if region == "" {
		region = "us-east-1"
	}
	return &Gateway{
		st: st, authSvc: authSvc, uploads: uploads,
		objectDir: objectDir, maxBytes: maxBytes, region: region,
		verifier: &verifier{st: st, authSvc: authSvc, region: region, service: "s3", maxSkew: 15 * time.Minute},
	}
}

func (g *Gateway) Region() string { return g.region }

func (g *Gateway) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	requestID := "s3_" + strconv.FormatInt(time.Now().UnixNano(), 36)
	w.Header().Set("x-amz-request-id", requestID)
	w.Header().Set("Server", "S3Bridge")

	rest := strings.TrimPrefix(r.URL.Path, "/s3")
	rest = strings.TrimPrefix(rest, "/")
	bucket, objectKey := splitPath(rest)

	// PUT 需要先缓冲请求体以计算 payload hash；验证通过后才执行任何写操作。
	var bodyFile *os.File
	var payloadHashes []string
	if r.Method == http.MethodPut {
		tmp, hash, err := g.bufferBody(r)
		if err != nil {
			g.fail(w, http.StatusBadRequest, "InvalidRequest", err.Error(), requestID)
			return
		}
		bodyFile = tmp
		defer func() {
			name := tmp.Name()
			_ = tmp.Close()
			_ = os.Remove(name)
		}()
		declared := r.Header.Get("X-Amz-Content-Sha256")
		if declared != "" && declared != unsignedPL && !strings.HasPrefix(declared, "STREAMING-") {
			if declared != hash {
				g.fail(w, http.StatusBadRequest, "XAmzContentSHA256Mismatch", "请求体哈希与声明不一致", requestID)
				return
			}
			payloadHashes = append(payloadHashes, declared)
		}
		payloadHashes = append(payloadHashes, hash)
	} else if h := r.Header.Get("X-Amz-Content-Sha256"); h != "" {
		payloadHashes = append(payloadHashes, h)
	}

	result, err := g.verifier.verify(r, payloadHashes)
	if err != nil {
		code, status := "AccessDenied", http.StatusForbidden
		switch {
		case errors.Is(err, ErrUnknownKey), errors.Is(err, auth.ErrUnauthorized):
			code, status = "InvalidAccessKeyId", http.StatusForbidden
		case errors.Is(err, ErrNoSignature), errors.Is(err, ErrBadSignature):
			code, status = "SignatureDoesNotMatch", http.StatusForbidden
		case errors.Is(err, ErrExpired):
			code, status = "RequestTimeTooSkewed", http.StatusForbidden
		}
		g.fail(w, status, code, err.Error(), requestID)
		return
	}
	p := result.Principal
	if !p.Can(store.PermS3Access) {
		g.fail(w, http.StatusForbidden, "AccessDenied", "该密钥未授予 s3:access 权限", requestID)
		return
	}

	ctx := r.Context()
	switch {
	case bucket == "":
		g.handleBuckets(w, r, p, requestID)
	case objectKey == "":
		g.handleBucket(w, r, p, bucket, bodyFile, requestID)
	default:
		g.handleObject(ctx, w, r, p, bucket, objectKey, bodyFile, requestID)
	}
}

func splitPath(rest string) (bucket, key string) {
	if rest == "" {
		return "", ""
	}
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i], strings.TrimPrefix(rest[i+1:], "/")
	}
	return rest, ""
}

func (g *Gateway) bufferBody(r *http.Request) (*os.File, string, error) {
	tmp, err := os.CreateTemp(g.objectDir, ".s3put-*")
	if err != nil {
		return nil, "", fmt.Errorf("创建临时文件: %w", err)
	}
	h := sha256.New()
	n, err := io.Copy(io.MultiWriter(tmp, h), io.LimitReader(r.Body, g.maxBytes+1))
	if err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, "", fmt.Errorf("读取请求体: %w", err)
	}
	if n > g.maxBytes {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, "", fmt.Errorf("请求体超过 %d 字节上限", g.maxBytes)
	}
	if _, err := tmp.Seek(0, io.SeekStart); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return nil, "", err
	}
	return tmp, hex.EncodeToString(h.Sum(nil)), nil
}

// ---------- Bucket 级操作 ----------

func (g *Gateway) handleBuckets(w http.ResponseWriter, r *http.Request, p *auth.Principal, requestID string) {
	if r.Method != http.MethodGet {
		g.fail(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "仅支持 GET /s3/", requestID)
		return
	}
	var buckets []store.Bucket
	_ = g.st.View(func(st *store.State) error {
		for _, b := range st.Buckets {
			if p.IsAdmin() || b.OwnerID == p.UserID || b.PublicRead {
				buckets = append(buckets, b)
			}
		}
		return nil
	})
	var sb strings.Builder
	sb.WriteString(`<ListAllMyBucketsResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	sb.WriteString("<Owner><ID>" + xmlEscape(p.UserID) + "</ID><DisplayName>" + xmlEscape(p.Username) + "</DisplayName></Owner><Buckets>")
	for _, b := range buckets {
		sb.WriteString("<Bucket><Name>" + xmlEscape(b.Name) + "</Name><CreationDate>" +
			b.CreatedAt.UTC().Format(time.RFC3339) + "</CreationDate></Bucket>")
	}
	sb.WriteString("</Buckets></ListAllMyBucketsResult>")
	writeXML(w, http.StatusOK, sb.String())
}

func (g *Gateway) handleBucket(w http.ResponseWriter, r *http.Request, p *auth.Principal, bucket string, body *os.File, requestID string) {
	switch r.Method {
	case http.MethodPut: // CreateBucket
		if !p.Can(store.PermBucketWrite) {
			g.fail(w, http.StatusForbidden, "AccessDenied", "缺少 bucket:write 权限", requestID)
			return
		}
		if err := upload.ValidateBucketName(bucket); err != nil {
			g.fail(w, http.StatusBadRequest, "InvalidBucketName", err.Error(), requestID)
			return
		}
		err := g.st.Update(func(st *store.State) error {
			if store.BucketByName(st, bucket) != nil {
				return errors.New("存储桶已存在")
			}
			st.Buckets = append(st.Buckets, store.Bucket{
				ID: "bkt_" + bucket, Name: bucket, OwnerID: p.UserID,
				Description: "通过 S3 接口创建", CreatedAt: store.NowUTC(),
			})
			return nil
		})
		if err != nil {
			g.fail(w, http.StatusConflict, "BucketAlreadyExists", err.Error(), requestID)
			return
		}
		w.Header().Set("Location", "/s3/"+bucket)
		w.WriteHeader(http.StatusOK)
	case http.MethodHead:
		if _, err := g.findBucket(bucket, p); err != nil {
			g.fail(w, http.StatusNotFound, "NoSuchBucket", err.Error(), requestID)
			return
		}
		w.WriteHeader(http.StatusOK)
	case http.MethodGet: // ListObjectsV2 / ListObjects
		if !p.Can(store.PermUploadRead) {
			g.fail(w, http.StatusForbidden, "AccessDenied", "缺少 upload:read 权限", requestID)
			return
		}
		if _, err := g.findBucket(bucket, p); err != nil {
			g.fail(w, http.StatusNotFound, "NoSuchBucket", err.Error(), requestID)
			return
		}
		g.listObjects(w, r, bucket, p)
	case http.MethodDelete:
		if !p.Can(store.PermBucketWrite) {
			g.fail(w, http.StatusForbidden, "AccessDenied", "缺少 bucket:write 权限", requestID)
			return
		}
		if err := g.st.Update(func(st *store.State) error {
			b := store.BucketByName(st, bucket)
			if b == nil {
				return errors.New("存储桶不存在")
			}
			if !p.IsAdmin() && b.OwnerID != p.UserID {
				return errors.New("无权删除他人存储桶")
			}
			for _, o := range st.Objects {
				if o.Bucket == bucket {
					return errors.New("存储桶非空，请先删除其中的对象")
				}
			}
			for i := range st.Buckets {
				if st.Buckets[i].Name == bucket {
					st.Buckets = append(st.Buckets[:i], st.Buckets[i+1:]...)
					break
				}
			}
			return nil
		}); err != nil {
			g.fail(w, http.StatusConflict, "BucketNotEmpty", err.Error(), requestID)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		g.fail(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "不支持的方法", requestID)
	}
}

func (g *Gateway) findBucket(name string, p *auth.Principal) (*store.Bucket, error) {
	var b *store.Bucket
	_ = g.st.View(func(st *store.State) error {
		if found := store.BucketByName(st, name); found != nil {
			copyB := *found
			b = &copyB
		}
		return nil
	})
	if b == nil {
		return nil, errors.New("存储桶不存在")
	}
	if !p.IsAdmin() && b.OwnerID != p.UserID && !b.PublicRead {
		return nil, errors.New("无权访问该存储桶")
	}
	return b, nil
}

func (g *Gateway) listObjects(w http.ResponseWriter, r *http.Request, bucket string, p *auth.Principal) {
	q := r.URL.Query()
	prefix := q.Get("prefix")
	maxKeys := 1000
	if n, err := strconv.Atoi(q.Get("max-keys")); err == nil && n > 0 && n <= 1000 {
		maxKeys = n
	}
	res, err := g.uploads.List(upload.ListFilter{
		Bucket: bucket, Keyword: prefix, Limit: maxKeys,
		OwnerID: p.UserID, IsAdmin: p.IsAdmin(),
	})
	if err != nil {
		g.fail(w, http.StatusInternalServerError, "InternalError", err.Error(), "")
		return
	}
	var sb strings.Builder
	sb.WriteString(`<ListBucketResult xmlns="http://s3.amazonaws.com/doc/2006-03-01/">`)
	sb.WriteString("<Name>" + xmlEscape(bucket) + "</Name>")
	sb.WriteString("<Prefix>" + xmlEscape(prefix) + "</Prefix>")
	sb.WriteString("<KeyCount>" + strconv.Itoa(len(res.Items)) + "</KeyCount>")
	sb.WriteString("<MaxKeys>" + strconv.Itoa(maxKeys) + "</MaxKeys>")
	sb.WriteString("<IsTruncated>false</IsTruncated>")
	for _, o := range res.Items {
		sb.WriteString("<Contents><Key>" + xmlEscape(o.ObjectKey) + "</Key>")
		sb.WriteString("<LastModified>" + o.CreatedAt.UTC().Format(time.RFC3339) + "</LastModified>")
		sb.WriteString(`<ETag>"` + o.SHA256 + `"</ETag>`)
		sb.WriteString("<Size>" + strconv.FormatInt(o.Size, 10) + "</Size>")
		sb.WriteString("<StorageClass>STANDARD</StorageClass><Owner><ID>" + xmlEscape(o.OwnerID) + "</ID></Owner></Contents>")
	}
	sb.WriteString("</ListBucketResult>")
	writeXML(w, http.StatusOK, sb.String())
}

// ---------- Object 级操作 ----------

func (g *Gateway) handleObject(ctx context.Context, w http.ResponseWriter, r *http.Request, p *auth.Principal, bucket, key string, body *os.File, requestID string) {
	switch r.Method {
	case http.MethodGet, http.MethodHead:
		if !p.Can(store.PermUploadRead) {
			g.fail(w, http.StatusForbidden, "AccessDenied", "缺少 upload:read 权限", requestID)
			return
		}
		obj, path, err := g.uploads.Get(bucket, key, p.UserID, p.IsAdmin())
		if err != nil {
			g.fail(w, http.StatusNotFound, "NoSuchKey", err.Error(), requestID)
			return
		}
		f, err := os.Open(path)
		if err != nil {
			g.fail(w, http.StatusNotFound, "NoSuchKey", "对象数据不可用", requestID)
			return
		}
		defer f.Close()
		w.Header().Set("Content-Type", obj.MimeType)
		w.Header().Set("Content-Length", strconv.FormatInt(obj.Size, 10))
		w.Header().Set("ETag", `"`+obj.SHA256+`"`)
		w.Header().Set("Last-Modified", obj.CreatedAt.UTC().Format(http.TimeFormat))
		w.Header().Set("x-amz-meta-provider", obj.Provider)
		w.Header().Set("x-amz-meta-category", string(obj.Category))
		if r.Method == http.MethodHead {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusOK)
		_, _ = io.Copy(w, f)

	case http.MethodPut:
		if !p.Can(store.PermUploadWrite) {
			g.fail(w, http.StatusForbidden, "AccessDenied", "缺少 upload:write 权限", requestID)
			return
		}
		if body == nil {
			g.fail(w, http.StatusBadRequest, "InvalidRequest", "缺少请求体", requestID)
			return
		}
		if _, err := body.Seek(0, io.SeekStart); err != nil {
			g.fail(w, http.StatusInternalServerError, "InternalError", err.Error(), requestID)
			return
		}
		fileName := filepath.Base(key)
		_, err := g.uploads.Put(ctx, upload.PutRequest{
			Reader: body, FileName: fileName,
			MimeType: r.Header.Get("Content-Type"),
			Size:     -1, Bucket: bucket, ObjectKey: key,
			OwnerID: p.UserID, IsAdmin: p.IsAdmin(),
		})
		if err != nil {
			g.fail(w, http.StatusBadRequest, "InvalidRequest", err.Error(), requestID)
			return
		}
		var created store.Object
		_ = g.st.View(func(st *store.State) error {
			if o := store.ObjectByKey(st, bucket, key); o != nil {
				created = *o
			}
			return nil
		})
		w.Header().Set("ETag", `"`+created.SHA256+`"`)
		w.WriteHeader(http.StatusOK)

	case http.MethodDelete:
		if !p.Can(store.PermUploadWrite) {
			g.fail(w, http.StatusForbidden, "AccessDenied", "缺少 upload:write 权限", requestID)
			return
		}
		if err := g.uploads.Delete(bucket, key, p.UserID, p.IsAdmin()); err != nil {
			g.fail(w, http.StatusNotFound, "NoSuchKey", err.Error(), requestID)
			return
		}
		w.WriteHeader(http.StatusNoContent)

	default:
		g.fail(w, http.StatusMethodNotAllowed, "MethodNotAllowed", "不支持的方法", requestID)
	}
}

// ---------- 响应工具 ----------

func (g *Gateway) fail(w http.ResponseWriter, status int, code, message, requestID string) {
	if status >= 500 {
		slog.Warn("S3 接口错误", "status", status, "code", code, "message", message)
	}
	body := "<Error><Code>" + xmlEscape(code) + "</Code><Message>" + xmlEscape(message) +
		"</Message><RequestId>" + xmlEscape(requestID) + "</RequestId></Error>"
	writeXML(w, status, body)
}

func writeXML(w http.ResponseWriter, status int, body string) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(status)
	_, _ = io.WriteString(w, xml.Header)
	_, _ = io.WriteString(w, body)
}

func xmlEscape(s string) string {
	var b strings.Builder
	_ = xml.EscapeText(&b, []byte(s))
	return b.String()
}
