package api_test

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

// noRedirect 用于断言 302 本身，而不是被 client 自动跟随到上游。
func noRedirect(srv *httptest.Server) *http.Client {
	return &http.Client{
		Transport: srv.Client().Transport,
		CheckRedirect: func(*http.Request, []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func updateSettings(t *testing.T, core *app.Core, fn func(*store.Settings)) {
	t.Helper()
	if err := core.Store.Update(func(st *store.State) error {
		fn(&st.Settings)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func makePublic(t *testing.T, core *app.Core, bucket string) {
	t.Helper()
	if err := core.Store.Update(func(st *store.State) error {
		if b := store.BucketByName(st, bucket); b != nil {
			b.PublicRead = true
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// seedObject 直接往状态里塞一条对象记录，并按 commit 的目录约定落一个本地文件。
func seedObject(t *testing.T, core *app.Core, bucket, key string, obj store.Object, content []byte) {
	t.Helper()
	rel := filepath.Join(bucket, "image", time.Now().UTC().Format("200601"), "seed-"+path.Base(key))
	if content != nil {
		abs := filepath.Join(core.Cfg.ObjectDir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(abs, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	obj.ID = "obj_seed_" + strings.ReplaceAll(key, "/", "_")
	obj.Bucket, obj.ObjectKey = bucket, key
	obj.FileName, obj.LocalPath = path.Base(key), rel
	if content != nil {
		obj.Size = int64(len(content))
	}
	if obj.Category == "" {
		obj.Category = store.CategoryImage
	}
	if obj.MimeType == "" {
		obj.MimeType = "image/png"
	}
	if obj.Provider == "" {
		obj.Provider = "local"
	}
	if obj.CreatedAt.IsZero() {
		obj.CreatedAt = store.NowUTC()
	}
	if err := core.Store.Update(func(st *store.State) error {
		st.Objects = append(st.Objects, obj)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

// TestDirectLinkRedirectsToUpstream 验证默认访问方式：有上游直链就 302，
// 本机只回一个短响应，不转发图片/视频的字节。
func TestDirectLinkRedirectsToUpstream(t *testing.T) {
	core, srv, _ := newTestServer(t)
	makePublic(t, core, "default")
	upstream := "https://cdn.example.com/path/a.png"
	seedObject(t, core, "default", "image/202611/a.png",
		store.Object{RemoteURL: upstream}, pngBytes)

	res, err := noRedirect(srv).Get(srv.URL + "/f/default/image/202611/a.png")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound {
		t.Fatalf("默认应 302 到上游，实际 %d %s", res.StatusCode, body)
	}
	if got := res.Header.Get("Location"); got != upstream {
		t.Fatalf("Location = %q, want %q", got, upstream)
	}
	if got := res.Header.Get("X-Access-Mode"); got != store.AccessRedirect {
		t.Fatalf("X-Access-Mode = %q", got)
	}
}

// TestDirectLinkProxyMode 验证「本站中转」：由本机拉取上游并原样转发，
// 且 Range 必须能透传，否则视频拖不动。
func TestDirectLinkProxyMode(t *testing.T) {
	core, srv, _ := newTestServer(t)
	makePublic(t, core, "default")

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if rg := r.Header.Get("Range"); rg != "" {
			w.Header().Set("Content-Range", "bytes 0-3/10")
			w.Header().Set("Accept-Ranges", "bytes")
			w.WriteHeader(http.StatusPartialContent)
			_, _ = w.Write([]byte("0123"))
			return
		}
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("0123456789"))
	}))
	defer upstream.Close()

	key := "image/202611/proxy.png"
	seedObject(t, core, "default", key, store.Object{RemoteURL: upstream.URL + "/obj.png"}, []byte("local-copy"))
	updateSettings(t, core, func(s *store.Settings) { s.AccessMode = store.AccessProxy })

	res, err := srv.Client().Get(srv.URL + "/f/default/" + key)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "0123456789" {
		t.Fatalf("中转输出异常: %d %q", res.StatusCode, body)
	}
	if got := res.Header.Get("X-Access-Mode"); got != store.AccessProxy {
		t.Fatalf("X-Access-Mode = %q", got)
	}

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/f/default/"+key, nil)
	req.Header.Set("Range", "bytes=0-3")
	res, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusPartialContent || string(part) != "0123" {
		t.Fatalf("中转 Range 异常: %d %q", res.StatusCode, part)
	}

	// 上游挂掉时应回退本地归档，而不是给用户一个 502 破图。
	upstream.Close()
	res, err = srv.Client().Get(srv.URL + "/f/default/" + key)
	if err != nil {
		t.Fatal(err)
	}
	body, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || string(body) != "local-copy" {
		t.Fatalf("上游不可用时应回退本地: %d %q", res.StatusCode, body)
	}
}

// TestInvalidObjectPlaceholder 验证失效对象只返回占位图：
// 没配失效图用内置 SVG，配了就 302 过去，且不会指向自己造成死循环。
func TestInvalidObjectPlaceholder(t *testing.T) {
	core, srv, _ := newTestServer(t)
	makePublic(t, core, "default")
	key := "image/202611/dead.png"
	seedObject(t, core, "default", key,
		store.Object{Validity: store.ObjectValidity{Status: store.ValidityInvalid}}, pngBytes)

	res, err := srv.Client().Get(srv.URL + "/f/default/" + key)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("内置占位应返回 200，实际 %d", res.StatusCode)
	}
	if !strings.HasPrefix(res.Header.Get("Content-Type"), "image/svg+xml") {
		t.Fatalf("内置占位类型错误: %s", res.Header.Get("Content-Type"))
	}
	if res.Header.Get("X-Object-Invalid") != "1" {
		t.Fatalf("缺少失效标记头")
	}
	if !strings.Contains(string(body), "文件已失效") {
		t.Fatalf("内置占位内容异常: %d 字节", len(body))
	}

	// 配置失效图后应 302 过去
	dead := "https://img.example.com/dead.png"
	updateSettings(t, core, func(s *store.Settings) { s.InvalidImageURL = dead })
	res, err = noRedirect(srv).Get(srv.URL + "/f/default/" + key)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusFound || res.Header.Get("Location") != dead {
		t.Fatalf("失效图跳转异常: %d %q", res.StatusCode, res.Header.Get("Location"))
	}

	// 失效图指向对象自己时退回内置占位，避免 302 死循环
	updateSettings(t, core, func(s *store.Settings) {
		s.InvalidImageURL = "/f/default/" + key
	})
	res, err = noRedirect(srv).Get(srv.URL + "/f/default/" + key)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("自指失效图应退回内置占位，实际 %d", res.StatusCode)
	}
}

// TestOpenUploadRandomBucket 验证「未指定 bucket 时随机落桶」，
// 显式指定时仍然严格按请求走。
func TestOpenUploadRandomBucket(t *testing.T) {
	core, srv, token := newTestServer(t)
	key := createAPIKey(t, srv, token)
	for _, name := range []string{"b1", "b2"} {
		if status, body := doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/buckets", token,
			map[string]any{"name": name}); status != http.StatusOK && status != http.StatusCreated {
			t.Fatalf("建桶失败: %d %s", status, body)
		}
	}
	updateSettings(t, core, func(s *store.Settings) { s.OpenUploadRandomBucket = true })

	seen := map[string]bool{}
	for i := 0; i < 18; i++ {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/open/upload", bytes.NewReader(pngBytes))
		req.Header.Set("X-API-Key", key)
		req.Header.Set("X-File-Name", "rand.png")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var out struct {
			Bucket  string `json:"bucket"`
			Storage string `json:"storage"`
		}
		_ = json.NewDecoder(res.Body).Decode(&out)
		_ = res.Body.Close()
		if res.StatusCode != http.StatusCreated {
			t.Fatalf("随机桶上传失败: %d", res.StatusCode)
		}
		if out.Storage != "local" {
			t.Fatalf("没有可用插件时应落本地，实际 %q", out.Storage)
		}
		seen[out.Bucket] = true
	}
	if len(seen) < 2 {
		t.Fatalf("18 次上传只落到一个桶: %v", seen)
	}

	// 显式 bucket 不能被随机覆盖
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/open/upload?bucket=b2", bytes.NewReader(pngBytes))
	req.Header.Set("X-API-Key", key)
	req.Header.Set("X-File-Name", "fixed.png")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	var out struct {
		Bucket string `json:"bucket"`
	}
	_ = json.NewDecoder(res.Body).Decode(&out)
	_ = res.Body.Close()
	if out.Bucket != "b2" {
		t.Fatalf("显式 bucket 被覆盖: %q", out.Bucket)
	}
}

// TestOpenUploadPluginFallback 验证「插件失败 → 本地归档」，
// 以及关闭回退后应该明确报错而不是假装成功。
func TestOpenUploadPluginFallback(t *testing.T) {
	core, srv, token := newTestServer(t)
	key := createAPIKey(t, srv, token)

	// 把一个插件标成健康，但把上传地址指向必然拒绝连接的端口。
	now := store.NowUTC()
	if err := core.Store.Update(func(st *store.State) error {
		p := store.PluginByID(st, "workbuddy")
		if p == nil {
			t.Fatal("缺少内置插件")
		}
		p.Status = store.StatusHealthy
		p.LastSuccessAt = &now
		if p.Params == nil {
			p.Params = map[string]string{}
		}
		p.Params["base_url"] = "http://127.0.0.1:1"
		p.Params["upload_path"] = "/upload"
		p.Params["file_field"] = "file"
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	upload := func() (int, map[string]any) {
		req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/open/upload", bytes.NewReader(pngBytes))
		req.Header.Set("X-API-Key", key)
		req.Header.Set("X-File-Name", "fb.png")
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(res.Body).Decode(&out)
		return res.StatusCode, out
	}

	status, out := upload()
	if status != http.StatusCreated {
		t.Fatalf("插件失败时应回退本地并成功，实际 %d %v", status, out)
	}
	if out["storage"] != "local" || out["provider"] != "local" {
		t.Fatalf("回退后应记录为本地: %v", out)
	}

	updateSettings(t, core, func(s *store.Settings) { s.OpenUploadFallbackLocal = false })
	if status, out = upload(); status < 400 {
		t.Fatalf("关闭回退后应报错，实际 %d %v", status, out)
	}
}
