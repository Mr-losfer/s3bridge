package api

import (
	"context"
	"errors"
	"io"
	"math/rand"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/upload"
)

// publicObject 提供签名直链，浏览器与播放器无需 Authorization 头即可访问。
//
// 之所以单独开一个出口，而不是复用 /api/objects/download：
//   - <img>/<video> 无法附加请求头；
//   - 视频拖动进度条依赖 Range，服务端必须支持分段响应；
//   - 预览直连本站，不经过第三方也不额外占用 API 鉴权链路。
//
// 链接带 bucket/key/到期时间/HMAC 四个要素，服务端只验签、不回查会话；
// 公开读的桶可以省略签名，方便直接嵌入外部页面。
func (s *Server) publicObject(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/f/")
	bucket, key := splitObjectPath(rest)
	if bucket == "" || key == "" {
		http.Error(w, "对象不存在", http.StatusNotFound)
		return
	}
	q := r.URL.Query()
	exp, _ := strconv.ParseInt(q.Get("e"), 10, 64)
	sig := q.Get("s")
	signed := s.core.Uploads.VerifyLink(bucket, key, exp, sig)
	if !signed && !s.core.Uploads.PublicRead(bucket) {
		http.Error(w, "链接无效或已过期", http.StatusForbidden)
		return
	}
	obj, path, err := s.core.Uploads.GetAny(bucket, key)
	if err != nil {
		http.Error(w, "对象不存在", http.StatusNotFound)
		return
	}
	cache := "public, max-age=60"
	if signed {
		// 签名链接本身有有效期，浏览器可放心缓存一段时间。
		cache = "public, max-age=86400"
	}
	// 失效对象一律只回占位图：上游此时多半已经 404 或返回 HTML 错误页，
	// 继续跳过去只会让页面出现破图，也浪费一次外部请求。
	if obj.Validity.Status == store.ValidityInvalid {
		s.serveInvalid(w, r, bucket, key)
		return
	}
	// 有上游直链时按设置的访问方式出站。默认 302：浏览器直接连第三方，
	// 本机只回一个几百字节的响应，不承载图片/视频的字节流量。
	if up, ok := upload.UsableRemoteURL(obj); ok {
		if s.core.Settings().AccessMode == store.AccessProxy {
			s.proxyUpstream(w, r, obj, up, cache)
			return
		}
		w.Header().Set("Cache-Control", "private, max-age=60")
		w.Header().Set("X-Access-Mode", store.AccessRedirect)
		http.Redirect(w, r, up, http.StatusFound)
		return
	}
	w.Header().Set("Content-Disposition", "inline")
	if err := upload.ServeFile(w, r, obj, path, cache); err != nil {
		http.Error(w, "对象数据不可用", http.StatusNotFound)
	}
}

// invalidPlaceholderSVG 是未配置失效图时的内置占位（约 0.5 KB，不需要任何外部依赖）。
const invalidPlaceholderSVG = `<svg xmlns="http://www.w3.org/2000/svg" width="640" height="360" viewBox="0 0 640 360" role="img" aria-label="文件已失效">
<rect width="640" height="360" fill="#0f172a"/>
<g fill="none" stroke="#475569" stroke-width="10" stroke-linecap="round">
<rect x="238" y="108" width="164" height="132" rx="10"/>
<path d="M238 200l44-40 34 30 26-22 40 36"/></g>
<circle cx="292" cy="146" r="12" fill="#475569"/>
<path d="M250 262l140-140" stroke="#ef4444" stroke-width="12" stroke-linecap="round"/>
<text x="320" y="306" font-family="system-ui,sans-serif" font-size="20" fill="#94a3b8" text-anchor="middle">文件已失效</text>
</svg>`

// serveInvalid 处理「巡检已判定失效」的对象：有配置就 302 到失效图，
// 没配置就吐内置 SVG 占位。两者都不用回源，也就不占带宽。
func (s *Server) serveInvalid(w http.ResponseWriter, r *http.Request, bucket, key string) {
	w.Header().Set("X-Object-Invalid", "1")
	// 占位不长期缓存：用户重新上传或补图后应马上恢复。
	w.Header().Set("Cache-Control", "public, max-age=60")
	if target := invalidImageURL(s.core.Settings().InvalidImageURL, bucket, key); target != "" {
		http.Redirect(w, r, target, http.StatusFound)
		return
	}
	w.Header().Set("Content-Type", "image/svg+xml; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(invalidPlaceholderSVG)))
	w.Header().Set("Content-Disposition", "inline")
	w.WriteHeader(http.StatusOK)
	if r.Method != http.MethodHead {
		_, _ = io.WriteString(w, invalidPlaceholderSVG)
	}
}

// invalidImageURL 规范化失效图地址，并挡掉「失效图指向自己」造成的重定向死循环。
func invalidImageURL(raw, bucket, key string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	if u, err := url.Parse(raw); err == nil && u.Path != "" && strings.HasPrefix(u.Path, "/f/") {
		if b, k := splitObjectPath(strings.TrimPrefix(u.Path, "/f/")); b == bucket && k == key {
			return ""
		}
	}
	return raw
}

// splitObjectPath 把 "bucket/a/b/c.png" 拆成桶名与对象键。
func splitObjectPath(rest string) (string, string) {
	rest = strings.TrimLeft(rest, "/")
	if i := strings.IndexByte(rest, '/'); i > 0 {
		return rest[:i], rest[i+1:]
	}
	return "", ""
}

// openUpload 是给外部程序用的上传入口：一个 API Key 就能传文件，
// 返回可直接嵌入页面/客户端的直链。
//
// 鉴权只看 API Key（X-API-Key / Bearer / Basic 密码三种写法），
// 不接受后台会话令牌，避免把管理会话泄露给脚本。
func (s *Server) openUpload(c *Ctx) error {
	p, err := s.openPrincipal(c.R)
	if err != nil {
		c.W.Header().Set("WWW-Authenticate", `Basic realm="S3Bridge"`)
		return errf(http.StatusUnauthorized, "unauthorized", "%v", err)
	}
	if !p.Can(store.PermUploadWrite) {
		return errf(http.StatusForbidden, "forbidden", "该 API Key 没有上传权限")
	}

	bucket := c.Query("bucket", "")
	key := c.Query("path", "")
	name := c.Query("name", "")

	var obj *store.Object
	ct := c.R.Header.Get("Content-Type")
	if strings.HasPrefix(strings.ToLower(ct), "multipart/form-data") {
		obj, err = s.openUploadMultipart(c, p.UserID, p.IsAdmin())
		if err != nil {
			return err
		}
	} else {
		fileName := firstNonEmpty(c.R.Header.Get("X-File-Name"), name, "upload.bin")
		obj, err = s.openStore(c.R.Context(), upload.PutRequest{
			Reader: c.R.Body, FileName: fileName, MimeType: c.R.Header.Get("Content-Type"),
			Bucket: bucket, ObjectKey: resolveKey(key, fileName),
			OwnerID: p.UserID, IsAdmin: p.IsAdmin(), Overwrite: true,
		}, bucket != "")
		if err != nil {
			return uploadError(err)
		}
	}

	base := requestBase(c.R)
	c.W.Header().Set("Location", base+obj.DirectPath)
	c.W.Header().Set("X-Object-Id", obj.ID)
	c.W.Header().Set("X-Object-Provider", obj.Provider)
	resp := map[string]any{
		"ok": true, "id": obj.ID, "bucket": obj.Bucket, "key": obj.ObjectKey,
		"fileName": obj.FileName, "size": obj.Size, "mimeType": obj.MimeType,
		"sha256": obj.SHA256, "category": obj.Category,
		// url 是本站直链：按系统设置会 302 到上游（默认）或由本站中转，
		// 调用方把它当永久地址用即可，不用关心后端换了哪个 CDN。
		"url": base + obj.DirectPath, "directPath": obj.DirectPath,
		"accessMode": s.core.Settings().AccessMode,
		// storage 说明这次实际落在哪：plugin 表示已上传到第三方并拿到上游地址，
		// local 表示只在本机归档。
		"storage": storageOf(obj), "provider": obj.Provider, "pluginId": obj.PluginID,
		"createdAt": obj.CreatedAt,
	}
	// 上游真实地址：调用方想绕开本站、直接把流量打到 CDN 时用这个。
	// 它可能是带时效的签名链接，过期时间一并给出（为空表示长期有效）。
	if obj.RemoteURL != "" {
		resp["upstreamUrl"] = obj.RemoteURL
		resp["upstreamExpiresAt"] = obj.URLExpireAt
	}
	c.JSON(http.StatusCreated, resp)
	return nil
}

// storageOf 把 provider 归一成两种落点，方便调用方判断。
func storageOf(obj *store.Object) string {
	if obj.Provider == "local" || obj.RemoteURL == "" {
		return "local"
	}
	return "plugin"
}

// openStore 执行开放上传策略：决定落哪个桶、用哪个插件、失败要不要本地兜底。
//
// 默认「随机桶 + 随机一个健康插件 + 插件不可用时回退本地」，
// 都可以在系统设置里改。显式指定 bucket 的请求不会被随机覆盖。
func (s *Server) openStore(ctx context.Context, req upload.PutRequest, explicitBucket bool) (*store.Object, error) {
	set := s.core.Settings()
	if !explicitBucket && req.Bucket == "" {
		if set.OpenUploadRandomBucket {
			req.Bucket = s.core.Uploads.RandomBucket()
		}
		if req.Bucket == "" {
			req.Bucket = set.OpenUploadBucket
		}
	}
	if req.Bucket == "" {
		return nil, errf(http.StatusNotFound, "bucket_not_found", "没有可用的存储桶，请先创建或指定 bucket")
	}

	if !set.OpenUploadUsePlugins {
		return s.core.Uploads.PutLocal(ctx, req)
	}
	obj, err := s.core.Uploads.PutAdaptive(ctx, req, func(cat store.FileCategory) string {
		ids := s.core.Plugins.HealthyIDs(cat)
		if len(ids) == 0 {
			return ""
		}
		return ids[rand.Intn(len(ids))]
	}, set.OpenUploadFallbackLocal)
	if err != nil {
		return nil, err
	}
	return obj, nil
}

// linkSign 为指定对象重新签发一个直链，供后台「复制直链」使用。
// 对象归属校验复用 upload.Get，避免拿到别人的对象权限。
func (s *Server) linkSign(c *Ctx) error {
	bucket, key := c.Query("bucket", ""), c.Query("key", "")
	if bucket == "" || key == "" {
		return errf(http.StatusBadRequest, "bad_request", "需要 bucket 与 key 参数")
	}
	obj, _, err := s.core.Uploads.Get(bucket, key, c.UserID(), c.IsAdmin())
	if err != nil {
		return errf(http.StatusNotFound, "not_found", "%v", err)
	}
	base := requestBase(c.R)
	resp := map[string]any{
		"url": base + obj.DirectPath, "directPath": obj.DirectPath,
		"accessMode": s.core.Settings().AccessMode,
		"expiresIn":  7 * 24 * 3600,
	}
	if obj.RemoteURL != "" {
		resp["upstreamUrl"] = obj.RemoteURL
		resp["upstreamExpiresAt"] = obj.URLExpireAt
	}
	c.JSON(http.StatusOK, resp)
	return nil
}

// openUploadMultipart 流式读取 multipart，文件字段之前的普通字段当作参数。
// 不调用 ParseMultipartForm，避免大文件被复制进内存或临时目录。
func (s *Server) openUploadMultipart(c *Ctx, ownerID string, isAdmin bool) (*store.Object, error) {
	mr, err := c.R.MultipartReader()
	if err != nil {
		return nil, errf(http.StatusBadRequest, "bad_request", "multipart 解析失败: %v", err)
	}
	bucket, key := c.Query("bucket", ""), c.Query("path", "")
	explicitBucket := bucket != ""
	for {
		part, err := mr.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, errf(http.StatusBadRequest, "bad_request", "读取 multipart: %v", err)
		}
		if part.FileName() == "" {
			val, _ := io.ReadAll(io.LimitReader(part, 4096))
			switch part.FormName() {
			case "bucket":
				if v := strings.TrimSpace(string(val)); v != "" {
					bucket, explicitBucket = v, true
				}
			case "path", "key":
				key = string(val)
			}
			_ = part.Close()
			continue
		}
		fileName := part.FileName()
		obj, err := s.openStore(c.R.Context(), upload.PutRequest{
			Reader: part, FileName: fileName, MimeType: part.Header.Get("Content-Type"),
			Bucket: bucket, ObjectKey: resolveKey(key, fileName),
			OwnerID: ownerID, IsAdmin: isAdmin, Overwrite: true,
		}, explicitBucket)
		_ = part.Close()
		if err != nil {
			return nil, uploadError(err)
		}
		return obj, nil
	}
	return nil, errf(http.StatusBadRequest, "no_file", "没有找到文件字段（表单字段名任意，需为文件）")
}

// openPrincipal 解析 API Key。三种写法都支持，方便 curl、SDK 与浏览器工具。
func (s *Server) openPrincipal(r *http.Request) (*auth.Principal, error) {
	candidates := []string{
		r.Header.Get("X-API-Key"),
		r.Header.Get("Authorization"),
	}
	if user, pass, ok := r.BasicAuth(); ok {
		// Basic 里用户名随意，密码放 API Key；也兼容用户名放 Key 的写法。
		candidates = append(candidates, pass, user)
	}
	for _, raw := range candidates {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		raw = strings.TrimPrefix(raw, "ApiKey ")
		p, err := s.core.Auth.Authenticate("Bearer " + raw)
		if err != nil || p == nil || p.Via != "apikey" {
			continue
		}
		return p, nil
	}
	return nil, errors.New("需要 API Key：X-API-Key 头、Authorization: Bearer sk_… 或 Basic 密码")
}

// requestBase 还原外部可访问的站点根地址，兼容反向代理。
func requestBase(r *http.Request) string {
	scheme := "http"
	if r.TLS != nil {
		scheme = "https"
	}
	if v := r.Header.Get("X-Forwarded-Proto"); v != "" {
		scheme = strings.TrimSpace(strings.Split(v, ",")[0])
	}
	host := r.Host
	if v := r.Header.Get("X-Forwarded-Host"); v != "" {
		host = strings.TrimSpace(strings.Split(v, ",")[0])
	}
	return scheme + "://" + host
}

// resolveKey 处理 path 参数：以 / 结尾视为目录前缀，自动拼上文件名。
func resolveKey(key, fileName string) string {
	key = strings.TrimSpace(key)
	if key == "" {
		return ""
	}
	if strings.HasSuffix(key, "/") {
		return key + fileName
	}
	return key
}

func uploadError(err error) error {
	switch {
	case errors.Is(err, upload.ErrFileTooLarge):
		return errf(http.StatusRequestEntityTooLarge, "file_too_large", "%v", err)
	case errors.Is(err, upload.ErrBucketMissing):
		return errf(http.StatusNotFound, "bucket_not_found", "%v", err)
	case errors.Is(err, upload.ErrForbidden):
		return errf(http.StatusForbidden, "forbidden", "%v", err)
	case errors.Is(err, upload.ErrObjectNotFound):
		return errf(http.StatusNotFound, "not_found", "%v", err)
	}
	return errf(http.StatusBadRequest, "upload_failed", "%v", err)
}
