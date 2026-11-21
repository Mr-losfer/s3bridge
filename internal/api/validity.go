package api

import (
	"net/http"
	"os"

	"github.com/Mr-losfer/s3bridge/internal/validity"
)

// objectProbe 是图片有效性巡检专用的内部接口。
//
// 只接受本机回环地址 + HMAC 签名，且只用于「探测是否还能返回图片」：
// HEAD 请求由 http.ServeContent 处理，响应头带 Content-Type/Content-Length，
// 不会写出正文；GET 仅在客户端显式带 Range 时返回极小的分片。
func (s *Server) objectProbe(c *Ctx) error {
	if !validity.IsLoopback(c.R.RemoteAddr) {
		return errf(http.StatusForbidden, "forbidden", "仅允许本机探测")
	}
	bucket, key := c.Query("bucket", ""), c.Query("key", "")
	if bucket == "" || key == "" {
		return errf(http.StatusBadRequest, "bad_request", "需要 bucket 与 key")
	}
	ts := c.R.Header.Get("X-S3Bridge-Probe")
	sig := c.R.Header.Get("X-S3Bridge-Signature")
	if !validity.Verify(s.core.ProbeSecret, bucket, key, ts, sig) {
		return errf(http.StatusForbidden, "forbidden", "探测签名无效")
	}
	obj, path, err := s.core.Uploads.Get(bucket, key, "", true)
	if err != nil {
		return errf(http.StatusNotFound, "not_found", "%v", err)
	}
	f, err := os.Open(path)
	if err != nil {
		return errf(http.StatusNotFound, "not_found", "文件已丢失")
	}
	defer f.Close()
	if obj.MimeType != "" {
		c.W.Header().Set("Content-Type", obj.MimeType)
	}
	http.ServeContent(c.W, c.R, obj.FileName, obj.CreatedAt, f)
	return nil
}
