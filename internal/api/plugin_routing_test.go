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
	"net/textproto"
	"strings"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/api"
	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/config"
)

// TestPluginFileTypeRouting 验证：插件可配置权重与可承接文件类型，
// 上传时按文件类型过滤插件，显式指定不匹配的插件会被拒绝。
func TestPluginFileTypeRouting(t *testing.T) {
	t.Setenv("S3BRIDGE_DATA_DIR", t.TempDir())
	t.Setenv("S3BRIDGE_BOOTSTRAP_PASSWORD", "admin-pass-123")
	t.Setenv("S3BRIDGE_MASTER_KEY", "routing-master-key")
	t.Setenv("S3BRIDGE_ADDR", "127.0.0.1:0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	core, err := app.New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		_ = r.ParseMultipartForm(1 << 20)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":{"success":true,"url":"https://cos.example.com/a.png","objectKey":"attachments/a.png","fileInfo":{"fileName":"a.png","fileSize":8,"mimeType":"image/png"}}}`)
	}))
	defer upstream.Close()

	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
	payload := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"sub":"u1"}`, time.Now().Add(time.Hour).Unix())))
	if _, err := core.Plugins.SetParams("workbuddy", map[string]string{
		"base_url": upstream.URL, "authorization": "Bearer " + header + "." + payload + ".sig",
		"x_user_id": "u1", "token_expires_at": fmt.Sprintf("%d", time.Now().Add(time.Hour).Unix()),
	}, "test"); err != nil {
		t.Fatal(err)
	}

	srv := httptest.NewServer(api.New(core))
	defer srv.Close()
	client := srv.Client()

	status, body := doJSON(t, client, http.MethodPost, srv.URL+"/api/auth/login", "",
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
	token := login.Token

	// ---- 差量修改权重与文件类型 ----
	status, body = doJSON(t, client, http.MethodPatch, srv.URL+"/api/plugins/workbuddy", token,
		map[string]any{"priority": 320, "fileTypes": []string{"image"}})
	if status != http.StatusOK {
		t.Fatalf("设置插件调度失败: %d %s", status, body)
	}
	var view struct {
		Priority  int      `json:"priority"`
		FileTypes []string `json:"fileTypes"`
	}
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatal(err)
	}
	if view.Priority != 320 || len(view.FileTypes) != 1 || view.FileTypes[0] != "image" {
		t.Fatalf("调度配置未生效: %s", body)
	}

	// ---- 文本文件显式指定图片插件应被拒绝 ----
	if status, body = uploadMultipart(t, client, srv.URL, token, "note.txt", "text/plain", []byte("hello")); status != http.StatusBadRequest {
		t.Fatalf("不支持的类型应返回 400，实际 %d %s", status, body)
	}
	if !strings.Contains(body, "不支持") {
		t.Fatalf("错误信息应说明类型不支持: %s", body)
	}

	// ---- 图片可正常上传 ----
	status, body = uploadMultipart(t, client, srv.URL, token, "a.png", "image/png", []byte("PNGDATA"))
	if status != http.StatusCreated {
		t.Fatalf("图片上传失败: %d %s", status, body)
	}
	var obj struct {
		Category string `json:"category"`
		PluginID string `json:"pluginId"`
	}
	if err := json.Unmarshal([]byte(body), &obj); err != nil {
		t.Fatal(err)
	}
	if obj.Category != "image" || obj.PluginID != "workbuddy" {
		t.Fatalf("上传路由异常: %+v", obj)
	}

	// ---- 恢复全部类型后文本文件可走同一插件 ----
	if status, body = doJSON(t, client, http.MethodPatch, srv.URL+"/api/plugins/workbuddy", token,
		map[string]any{"fileTypes": []string{}}); status != http.StatusOK {
		t.Fatalf("清空文件类型失败: %d %s", status, body)
	}
	if status, body = uploadMultipart(t, client, srv.URL, token, "note.txt", "text/plain", []byte("hello")); status != http.StatusCreated {
		t.Fatalf("全部类型应允许文档上传: %d %s", status, body)
	}
}

func uploadMultipart(t *testing.T, client *http.Client, base, token, name, mime string, content []byte) (int, string) {
	t.Helper()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="file"; filename="%s"`, name))
	h.Set("Content-Type", mime)
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(content)
	_ = mw.WriteField("bucket", "default")
	_ = mw.WriteField("plugin", "workbuddy")
	_ = mw.Close()

	req, err := http.NewRequest(http.MethodPost, base+"/api/upload", &buf)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}
