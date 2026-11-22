package api_test

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mr-losfer/s3bridge/internal/api"
	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/config"
)

// pngBytes 是一段带合法 PNG 文件头的内容，用于 MIME 嗅探与相册归类。
var pngBytes = append([]byte("\x89PNG\r\n\x1a\n"), []byte("s3bridge-test")...)

func newTestServer(t *testing.T) (*app.Core, *httptest.Server, string) {
	t.Helper()
	t.Setenv("S3BRIDGE_DATA_DIR", t.TempDir())
	t.Setenv("S3BRIDGE_BOOTSTRAP_PASSWORD", "admin-pass-123")
	t.Setenv("S3BRIDGE_MASTER_KEY", "public-api-test-master-key")
	t.Setenv("S3BRIDGE_ADDR", "127.0.0.1:0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	core, err := app.New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	srv := httptest.NewServer(api.New(core))
	t.Cleanup(srv.Close)

	status, body := doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/auth/login", "",
		map[string]any{"username": "admin", "password": "admin-pass-123"})
	if status != http.StatusOK {
		t.Fatalf("登录失败: %d %s", status, body)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &login); err != nil || login.Token == "" {
		t.Fatalf("登录响应异常: %s", body)
	}
	return core, srv, login.Token
}

func createAPIKey(t *testing.T, srv *httptest.Server, token string) string {
	t.Helper()
	status, body := doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/keys", token,
		map[string]any{"name": "外部程序"})
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("创建 API Key 失败: %d %s", status, body)
	}
	var out struct {
		Secret string `json:"secretAccessKey"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil || out.Secret == "" {
		t.Fatalf("密钥响应异常: %s", body)
	}
	if !strings.HasPrefix(out.Secret, "sk_") {
		t.Fatalf("API Key 应以 sk_ 开头: %s", out.Secret)
	}
	return out.Secret
}

// TestOpenUploadAndDirectLink 覆盖「一个 API Key 上传 → 拿回直链」的完整链路：
// 直链无需登录即可访问，且支持 Range（视频拖动依赖它）。
func TestOpenUploadAndDirectLink(t *testing.T) {
	_, srv, token := newTestServer(t)
	key := createAPIKey(t, srv, token)

	// ---- 无密钥应被拒绝 ----
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/open/upload", bytes.NewReader(pngBytes))
	req.Header.Set("X-File-Name", "noauth.png")
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("无 API Key 应返回 401，实际 %d", res.StatusCode)
	}

	// ---- multipart 上传 ----
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	_ = mw.WriteField("bucket", "default")
	_ = mw.WriteField("path", "open/demo.png")
	fw, err := mw.CreateFormFile("file", "demo.png")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = fw.Write(pngBytes)
	_ = mw.Close()

	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/open/upload?bucket=default&path=open/demo.png", &buf)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("X-API-Key", key)
	res, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("开放上传失败: %d %s", res.StatusCode, raw)
	}
	var up struct {
		URL    string `json:"url"`
		Key    string `json:"key"`
		SHA256 string `json:"sha256"`
		Size   int64  `json:"size"`
	}
	if err := json.Unmarshal(raw, &up); err != nil {
		t.Fatalf("上传响应不是 JSON: %s", raw)
	}
	if up.URL == "" || !strings.Contains(up.URL, "/f/default/open/demo.png") {
		t.Fatalf("直链不符合预期: %s", up.URL)
	}
	if up.Size != int64(len(pngBytes)) || len(up.SHA256) != 64 {
		t.Fatalf("元信息异常: %+v", up)
	}

	// ---- 直链匿名可读 ----
	res, err = srv.Client().Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK || !bytes.Equal(got, pngBytes) {
		t.Fatalf("直链读取失败: %d, %d 字节", res.StatusCode, len(got))
	}

	// ---- Range 分段 ----
	req, _ = http.NewRequest(http.MethodGet, up.URL, nil)
	req.Header.Set("Range", "bytes=0-7")
	res, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusPartialContent || !bytes.Equal(part, pngBytes[:8]) {
		t.Fatalf("Range 响应异常: %d %q", res.StatusCode, part)
	}

	// ---- 同键覆盖：直链必须读到新内容，而不是第一次的旧对象 ----
	newer := append([]byte("\x89PNG\r\n\x1a\n"), []byte("v2")...)
	req, _ = http.NewRequest(http.MethodPost, srv.URL+"/api/open/upload?bucket=default&path=open/demo.png",
		bytes.NewReader(newer))
	req.Header.Set("X-API-Key", key)
	req.Header.Set("X-File-Name", "demo.png")
	res, err = srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("覆盖上传失败: %d", res.StatusCode)
	}
	res, err = srv.Client().Get(up.URL)
	if err != nil {
		t.Fatal(err)
	}
	got, _ = io.ReadAll(res.Body)
	_ = res.Body.Close()
	if !bytes.Equal(got, newer) {
		t.Fatalf("同键覆盖后直链仍是旧内容: %q", got)
	}

	// ---- 篡改签名应被拒绝 ----
	bad := strings.Replace(up.URL, "s=", "s=x", 1)
	res, err = srv.Client().Get(bad)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusForbidden {
		t.Fatalf("伪造签名应返回 403，实际 %d", res.StatusCode)
	}

	// ---- 列表里带直链与预览字段 ----
	status, body := doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/uploads", token, nil)
	if status != http.StatusOK {
		t.Fatalf("列表失败: %d %s", status, body)
	}
	var list struct {
		Items []struct {
			DirectPath       string `json:"directPath"`
			PreviewURL       string `json:"previewUrl"`
			PreviewNoReferer bool   `json:"previewNoReferer"`
			LocalPath        string `json:"localPath"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || len(list.Items) == 0 {
		t.Fatalf("列表响应异常: %s", body)
	}
	it := list.Items[0]
	if it.DirectPath == "" || it.LocalPath == "" {
		t.Fatalf("列表缺少直链/归档路径: %+v", it)
	}
	if it.PreviewURL != "" || it.PreviewNoReferer {
		t.Fatalf("本地对象不应有上游预览地址: %+v", it)
	}

	// ---- 相册接口同样带直链 ----
	status, body = doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/gallery", token, nil)
	if status != http.StatusOK {
		t.Fatalf("相册失败: %d %s", status, body)
	}
	var gal struct {
		Items []struct {
			DirectPath string `json:"directPath"`
			SHA256     string `json:"sha256"`
		} `json:"items"`
	}
	if err := json.Unmarshal([]byte(body), &gal); err != nil || len(gal.Items) == 0 {
		t.Fatalf("相册响应异常: %s", body)
	}
	if gal.Items[0].DirectPath == "" || gal.Items[0].SHA256 == "" {
		t.Fatalf("相册缺少直链或哈希: %+v", gal.Items[0])
	}
}

// TestWebDAV 验证「用 API Key 把存储桶挂成网络盘」的主要动作。
func TestWebDAV(t *testing.T) {
	_, srv, token := newTestServer(t)
	key := createAPIKey(t, srv, token)
	auth := "Basic " + base64.StdEncoding.EncodeToString([]byte("user:"+key))

	do := func(method, path string, body io.Reader, headers map[string]string) (*http.Response, []byte) {
		req, err := http.NewRequest(method, srv.URL+path, body)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", auth)
		for k, v := range headers {
			req.Header.Set(k, v)
		}
		res, err := srv.Client().Do(req)
		if err != nil {
			t.Fatal(err)
		}
		out, _ := io.ReadAll(res.Body)
		_ = res.Body.Close()
		return res, out
	}

	// 未认证
	req, _ := http.NewRequest("PROPFIND", srv.URL+"/dav/", nil)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("WebDAV 未认证应返回 401，实际 %d", res.StatusCode)
	}

	// OPTIONS 声明 DAV 能力
	res, _ = do(http.MethodOptions, "/dav/", nil, nil)
	if res.StatusCode != http.StatusOK || !strings.Contains(res.Header.Get("DAV"), "1") {
		t.Fatalf("OPTIONS 异常: %d %q", res.StatusCode, res.Header.Get("DAV"))
	}

	// 根目录列出存储桶
	res, body := do("PROPFIND", "/dav/", nil, map[string]string{"Depth": "1"})
	if res.StatusCode != http.StatusMultiStatus || !strings.Contains(string(body), "default") {
		t.Fatalf("PROPFIND 根目录异常: %d %s", res.StatusCode, body)
	}

	// PUT 新建
	res, body = do(http.MethodPut, "/dav/default/dav-dir/hello.txt", strings.NewReader("hello webdav"),
		map[string]string{"Content-Type": "text/plain"})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("PUT 失败: %d %s", res.StatusCode, body)
	}

	// GET 取回
	res, body = do(http.MethodGet, "/dav/default/dav-dir/hello.txt", nil, nil)
	if res.StatusCode != http.StatusOK || string(body) != "hello webdav" {
		t.Fatalf("GET 失败: %d %q", res.StatusCode, body)
	}

	// PROPFIND 目录能看到文件
	res, body = do("PROPFIND", "/dav/default/dav-dir/", nil, map[string]string{"Depth": "1"})
	if res.StatusCode != http.StatusMultiStatus || !strings.Contains(string(body), "hello.txt") {
		t.Fatalf("PROPFIND 目录异常: %d %s", res.StatusCode, body)
	}

	// MOVE 改名
	res, body = do("MOVE", "/dav/default/dav-dir/hello.txt", nil, map[string]string{
		"Destination": srv.URL + "/dav/default/dav-dir/renamed.txt",
	})
	if res.StatusCode != http.StatusCreated {
		t.Fatalf("MOVE 失败: %d %s", res.StatusCode, body)
	}
	res, body = do(http.MethodGet, "/dav/default/dav-dir/renamed.txt", nil, nil)
	if res.StatusCode != http.StatusOK || string(body) != "hello webdav" {
		t.Fatalf("改名后读取失败: %d %q", res.StatusCode, body)
	}

	// DELETE
	res, _ = do(http.MethodDelete, "/dav/default/dav-dir/renamed.txt", nil, nil)
	if res.StatusCode != http.StatusNoContent {
		t.Fatalf("DELETE 失败: %d", res.StatusCode)
	}
}

// TestOpenUploadRespectsBucket 确认开放上传不会凭空创建桶。
func TestOpenUploadRespectsBucket(t *testing.T) {
	_, srv, token := newTestServer(t)
	key := createAPIKey(t, srv, token)

	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/open/upload?bucket=missing",
		bytes.NewReader(pngBytes))
	req.Header.Set("X-File-Name", "x.png")
	req.Header.Set("X-API-Key", key)
	res, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := io.ReadAll(res.Body)
	_ = res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Fatalf("不存在的桶应返回 404，实际 %d %s", res.StatusCode, raw)
	}
	if !strings.Contains(string(raw), "存储桶") {
		t.Fatalf("错误信息应说明桶不存在: %s", raw)
	}
	_ = fmt.Sprint()
}
