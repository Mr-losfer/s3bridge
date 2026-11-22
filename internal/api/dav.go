package api

import (
	"encoding/xml"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/upload"
)

// WebDAV 接入：把存储桶直接挂成网络盘。
//
// 路径约定 /dav/{bucket}/{key}，根目录列出全部桶。认证复用 API Key：
// 客户端弹窗里用户名随意填，密码填 API Key（sk_ 开头）即可。
//
// 只实现日常同步需要的子集（PROPFIND/GET/PUT/DELETE/MKCOL/MOVE/COPY），
// 不实现锁服务：LOCK/UNLOCK 返回固定令牌，macOS 与 Windows 的资源管理器
// 都能正常读写，但不提供多人并发写保护。
const davPrefix = "/dav"

func (s *Server) dav(w http.ResponseWriter, r *http.Request) {
	if !s.core.Settings().WebDAVEnabled {
		http.Error(w, "WebDAV 已在系统设置中关闭", http.StatusNotFound)
		return
	}
	p, err := s.openPrincipal(r)
	if err != nil {
		w.Header().Set("WWW-Authenticate", `Basic realm="S3Bridge WebDAV"`)
		http.Error(w, err.Error(), http.StatusUnauthorized)
		return
	}

	// 路径 /dav/bucket/key → bucket, key
	rest := strings.TrimPrefix(r.URL.Path, davPrefix)
	rest = strings.TrimLeft(rest, "/")
	bucket, key := splitObjectPath(rest)
	key = strings.TrimSuffix(key, "/")
	isCollection := key == "" || strings.HasSuffix(r.URL.Path, "/")

	switch r.Method {
	case http.MethodOptions:
		writeDAVHeaders(w)
		w.WriteHeader(http.StatusOK)

	case "PROPFIND", "PROPPATCH":
		if !p.Can(store.PermUploadRead) {
			http.Error(w, "该 API Key 没有读取权限", http.StatusForbidden)
			return
		}
		if r.Method == "PROPPATCH" {
			writeDAVMultiStatus(w, "", nil) // 属性修改不支持，返回空 207 让客户端继续
			return
		}
		s.davPropfind(w, r, p, bucket, key)

	case http.MethodGet, http.MethodHead:
		if !p.Can(store.PermUploadRead) {
			http.Error(w, "该 API Key 没有读取权限", http.StatusForbidden)
			return
		}
		if bucket == "" || key == "" {
			http.Error(w, "这是目录，不能直接下载", http.StatusMethodNotAllowed)
			return
		}
		obj, path, err := s.core.Uploads.Get(bucket, key, p.UserID, p.IsAdmin())
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Disposition", "inline")
		w.Header().Set("DAV", "1,2")
		if err := upload.ServeFile(w, r, obj, path, "private, max-age=60"); err != nil {
			http.Error(w, "对象数据不可用", http.StatusNotFound)
		}

	case http.MethodPut:
		if !p.Can(store.PermUploadWrite) {
			http.Error(w, "该 API Key 没有写入权限", http.StatusForbidden)
			return
		}
		if bucket == "" || key == "" {
			http.Error(w, "PUT 必须指定文件路径", http.StatusBadRequest)
			return
		}
		obj, err := s.davWrite(r, bucket, key, p)
		if err != nil {
			http.Error(w, err.Error(), uploadStatus(err))
			return
		}
		if obj == nil {
			w.WriteHeader(http.StatusNoContent)
			return
		}
		w.Header().Set("ETag", `"`+obj.SHA256+`"`)
		w.WriteHeader(http.StatusCreated)

	case http.MethodDelete:
		if !p.Can(store.PermUploadWrite) {
			http.Error(w, "该 API Key 没有写入权限", http.StatusForbidden)
			return
		}
		if bucket == "" {
			http.Error(w, "不能删除根目录", http.StatusForbidden)
			return
		}
		if isCollection {
			n, err := s.davDeleteDir(bucket, key, p)
			if err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			if n == 0 {
				http.Error(w, "目录不存在或为空", http.StatusNotFound)
				return
			}
		} else if err := s.core.Uploads.Delete(bucket, key, p.UserID, p.IsAdmin()); err != nil {
			http.Error(w, err.Error(), webdavErrorStatus(err))
			return
		}
		w.WriteHeader(http.StatusNoContent)

	case "MKCOL":
		if !p.Can(store.PermUploadWrite) {
			http.Error(w, "该 API Key 没有写入权限", http.StatusForbidden)
			return
		}
		if bucket == "" {
			http.Error(w, "根目录已存在", http.StatusMethodNotAllowed)
			return
		}
		if key == "" {
			// 建桶：MKCOL /dav/{bucket}
			if err := s.core.Uploads.CreateBucket(bucket, p.UserID); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			w.WriteHeader(http.StatusCreated)
			return
		}
		// 对象存储没有真实目录，深层 MKCOL 视为成功，PUT 时自动落位。
		w.WriteHeader(http.StatusCreated)

	case "MOVE", "COPY":
		if !p.Can(store.PermUploadWrite) {
			http.Error(w, "该 API Key 没有写入权限", http.StatusForbidden)
			return
		}
		s.davCopyMove(w, r, p, bucket, key, r.Method == "MOVE")

	case "LOCK":
		// 返回一个固定令牌，客户端据此认为独占写已生效。
		w.Header().Set("Content-Type", "application/xml; charset=utf-8")
		writeDAVHeaders(w)
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, `<?xml version="1.0" encoding="utf-8"?>
<D:prop xmlns:D="DAV:"><D:lockdiscovery><D:activelock>
<D:locktype><D:write/></D:locktype><D:lockscope><D:exclusive/></D:lockscope>
<D:depth>infinity</D:depth><D:timeout>Second-3600</D:timeout>
<D:locktoken><D:href>urn:uuid:s3bridge-lock</D:href></D:locktoken>
</D:activelock></D:lockdiscovery></D:prop>`)

	case "UNLOCK":
		w.WriteHeader(http.StatusNoContent)

	default:
		writeDAVHeaders(w)
		w.WriteHeader(http.StatusMethodNotAllowed)
	}
}

func writeDAVHeaders(w http.ResponseWriter) {
	w.Header().Set("DAV", "1,2")
	w.Header().Set("MS-Author-Via", "DAV")
	w.Header().Set("Allow", "OPTIONS, GET, HEAD, PUT, DELETE, PROPFIND, PROPPATCH, MKCOL, MOVE, COPY, LOCK, UNLOCK")
}

// davWrite 写入对象；已存在时先删后写，保证 PUT 的覆盖语义。
func (s *Server) davWrite(r *http.Request, bucket, key string, p *auth.Principal) (*store.Object, error) {
	fileName := key
	if i := strings.LastIndexByte(key, '/'); i >= 0 {
		fileName = key[i+1:]
	}
	return s.core.Uploads.PutLocal(r.Context(), upload.PutRequest{
		Reader: r.Body, FileName: fileName, MimeType: r.Header.Get("Content-Type"),
		Bucket: bucket, ObjectKey: key, OwnerID: p.UserID, IsAdmin: p.IsAdmin(), Overwrite: true,
	})
}

// davDeleteDir 删除以 prefix 开头的所有对象。
func (s *Server) davDeleteDir(bucket, prefix string, p *auth.Principal) (int, error) {
	list, err := s.core.Uploads.List(upload.ListFilter{
		Bucket: bucket, OwnerID: p.UserID, IsAdmin: p.IsAdmin(),
	})
	if err != nil {
		return 0, err
	}
	n := 0
	for _, o := range list.Items {
		if !strings.HasPrefix(o.ObjectKey, prefix+"/") {
			continue
		}
		if err := s.core.Uploads.Delete(bucket, o.ObjectKey, p.UserID, p.IsAdmin()); err != nil {
			return n, err
		}
		n++
	}
	return n, nil
}

// davCopyMove 同桶内复制/移动。跨桶会改写 Destination 的桶名，同样支持。
func (s *Server) davCopyMove(w http.ResponseWriter, r *http.Request, p *auth.Principal, bucket, key string, move bool) {
	if bucket == "" || key == "" {
		http.Error(w, "需要指定源文件", http.StatusBadRequest)
		return
	}
	dest := r.Header.Get("Destination")
	if dest == "" {
		http.Error(w, "缺少 Destination 头", http.StatusBadRequest)
		return
	}
	u, err := url.Parse(dest)
	if err != nil {
		http.Error(w, "Destination 无法解析", http.StatusBadRequest)
		return
	}
	dstBucket, dstKey := splitObjectPath(strings.TrimPrefix(u.Path, davPrefix))
	if dstBucket == "" || dstKey == "" {
		http.Error(w, "Destination 必须在 /dav/{bucket}/{key} 下", http.StatusBadRequest)
		return
	}
	obj, srcPath, err := s.core.Uploads.Get(bucket, key, p.UserID, p.IsAdmin())
	if err != nil {
		http.Error(w, "源对象不存在", http.StatusNotFound)
		return
	}
	f, err := os.Open(srcPath)
	if err != nil {
		http.Error(w, "源对象数据不可用", http.StatusNotFound)
		return
	}
	defer f.Close()
	if _, err := s.core.Uploads.PutLocal(r.Context(), upload.PutRequest{
		Reader: f, FileName: dstKey[strings.LastIndex(dstKey, "/")+1:], MimeType: obj.MimeType,
		Bucket: dstBucket, ObjectKey: dstKey, OwnerID: p.UserID, IsAdmin: p.IsAdmin(), Overwrite: true,
	}); err != nil {
		http.Error(w, err.Error(), uploadStatus(err))
		return
	}
	if move {
		if err := s.core.Uploads.Delete(bucket, key, p.UserID, p.IsAdmin()); err != nil {
			http.Error(w, "复制成功但删除源文件失败: "+err.Error(), http.StatusInternalServerError)
			return
		}
	}
	w.WriteHeader(http.StatusCreated)
}

// ---------- PROPFIND ----------

type davProp struct {
	Href         string
	DisplayName  string
	Collection   bool
	Size         int64
	ContentType  string
	LastModified time.Time
	ETag         string
}

func (s *Server) davPropfind(w http.ResponseWriter, r *http.Request, p *auth.Principal, bucket, key string) {
	depth := r.Header.Get("Depth")
	props := []davProp{}

	switch {
	case bucket == "":
		// 根：列出所有存储桶
		props = append(props, davProp{Href: davPrefix + "/", DisplayName: "S3Bridge", Collection: true})
		if depth != "0" {
			names := s.core.Uploads.BucketNames()
			sort.Strings(names)
			for _, name := range names {
				props = append(props, davProp{
					Href:        davPrefix + "/" + url.PathEscape(name) + "/",
					DisplayName: name, Collection: true,
				})
			}
		}

	default:
		if !s.core.Uploads.BucketExists(bucket) {
			http.Error(w, "存储桶不存在", http.StatusNotFound)
			return
		}
		// 目录（桶根或对象键前缀）与文件分开处理：
		// 目录没有实体，不能走 Get，否则会误报 404。
		if key == "" || strings.HasSuffix(r.URL.Path, "/") {
			name, href := bucket, davPrefix+"/"+url.PathEscape(bucket)+"/"
			if key != "" {
				name = path.Base(key)
				href += escapeKey(key) + "/"
			}
			props = append(props, davProp{Href: href, DisplayName: name, Collection: true})
			if depth != "0" {
				props = append(props, s.davChildren(bucket, key, p)...)
			}
		} else {
			obj, _, err := s.core.Uploads.Get(bucket, key, p.UserID, p.IsAdmin())
			if err != nil {
				http.Error(w, "对象不存在", http.StatusNotFound)
				return
			}
			props = append(props, davFileProp(bucket, key, obj))
		}
	}

	writeDAVMultiStatus(w, r.URL.Path, props)
}

// davChildren 返回目录下的直接子项：对象键按 / 切成虚拟目录。
func (s *Server) davChildren(bucket, prefix string, p *auth.Principal) []davProp {
	list, err := s.core.Uploads.List(upload.ListFilter{
		Bucket: bucket, OwnerID: p.UserID, IsAdmin: p.IsAdmin(),
	})
	if err != nil {
		return nil
	}
	base := ""
	if prefix != "" {
		base = strings.TrimSuffix(prefix, "/") + "/"
	}
	seen := map[string]bool{}
	out := []davProp{}
	for _, o := range list.Items {
		if !strings.HasPrefix(o.ObjectKey, base) {
			continue
		}
		rest := strings.TrimPrefix(o.ObjectKey, base)
		if rest == "" {
			continue
		}
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			dir := rest[:i]
			if seen[dir] {
				continue
			}
			seen[dir] = true
			out = append(out, davProp{
				Href:        davPrefix + "/" + url.PathEscape(bucket) + "/" + escapeKey(base+dir) + "/",
				DisplayName: dir, Collection: true,
			})
			continue
		}
		obj := o
		out = append(out, davFileProp(bucket, o.ObjectKey, &obj))
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Href < out[j].Href })
	return out
}

func davFileProp(bucket, key string, obj *store.Object) davProp {
	href := davPrefix + "/" + url.PathEscape(bucket) + "/" + escapeKey(key)
	return davProp{
		Href: href, DisplayName: obj.FileName, Size: obj.Size,
		ContentType: obj.MimeType, LastModified: obj.CreatedAt, ETag: obj.SHA256,
	}
}

// escapeKey 逐段转义但保留 / 分隔符。
func escapeKey(key string) string {
	parts := strings.Split(key, "/")
	for i, p := range parts {
		parts[i] = url.PathEscape(p)
	}
	return strings.Join(parts, "/")
}

func writeDAVMultiStatus(w http.ResponseWriter, _ string, props []davProp) {
	w.Header().Set("Content-Type", "application/xml; charset=utf-8")
	w.WriteHeader(http.StatusMultiStatus)
	var b strings.Builder
	b.WriteString(`<?xml version="1.0" encoding="utf-8"?>` + "\n")
	b.WriteString(`<D:multistatus xmlns:D="DAV:">` + "\n")
	for _, p := range props {
		b.WriteString("  <D:response>\n    <D:href>")
		xml.EscapeText(&b, []byte(p.Href))
		b.WriteString("</D:href>\n    <D:propstat>\n      <D:prop>\n")
		b.WriteString("        <D:displayname>")
		xml.EscapeText(&b, []byte(p.DisplayName))
		b.WriteString("</D:displayname>\n")
		if p.Collection {
			b.WriteString("        <D:resourcetype><D:collection/></D:resourcetype>\n")
		} else {
			b.WriteString("        <D:resourcetype/>\n")
			fmt.Fprintf(&b, "        <D:getcontentlength>%d</D:getcontentlength>\n", p.Size)
			b.WriteString("        <D:getcontenttype>")
			xml.EscapeText(&b, []byte(p.ContentType))
			b.WriteString("</D:getcontenttype>\n")
			fmt.Fprintf(&b, "        <D:getlastmodified>%s</D:getlastmodified>\n",
				p.LastModified.UTC().Format(http.TimeFormat))
			fmt.Fprintf(&b, "        <D:getetag>&quot;%s&quot;</D:getetag>\n", p.ETag)
		}
		b.WriteString("      </D:prop>\n      <D:status>HTTP/1.1 200 OK</D:status>\n")
		b.WriteString("    </D:propstat>\n  </D:response>\n")
	}
	b.WriteString("</D:multistatus>")
	_, _ = io.WriteString(w, b.String())
}

func webdavErrorStatus(err error) int {
	switch {
	case errors.Is(err, upload.ErrObjectNotFound):
		return http.StatusNotFound
	case errors.Is(err, upload.ErrForbidden):
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}

func uploadStatus(err error) int {
	switch {
	case errors.Is(err, upload.ErrFileTooLarge):
		return http.StatusRequestEntityTooLarge
	case errors.Is(err, upload.ErrBucketMissing):
		return http.StatusConflict
	case errors.Is(err, upload.ErrForbidden):
		return http.StatusForbidden
	}
	return http.StatusBadRequest
}
