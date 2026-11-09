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

// TestEndToEnd 覆盖：登录 → 插件参数配置 → 调试 → 上传(经 mock 上游) →
// 列表/存储桶 → API Key → S3 标准接口取回 → MCP 工具清单。
func TestEndToEnd(t *testing.T) {
	t.Setenv("S3BRIDGE_DATA_DIR", t.TempDir())
	t.Setenv("S3BRIDGE_BOOTSTRAP_PASSWORD", "admin-pass-123")
	t.Setenv("S3BRIDGE_MASTER_KEY", "integration-master-key")
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

	// ---- mock WorkBuddy 上游 ----
	fileContent := []byte("PNGDATA")
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			w.WriteHeader(http.StatusOK)
			return
		}
		if !strings.HasPrefix(r.Header.Get("Authorization"), "Bearer ") {
			t.Errorf("上游缺少 Authorization 头")
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("上游解析 multipart 失败: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, fmt.Sprintf(`{"code":0,"msg":"OK","requestId":"req-1","data":{"success":true,
			"url":"https://cos.example.com/test.png?q-sign-time=%d%%3B%d&q-signature=abc",
			"objectKey":"attachments/test.png","fileInfo":{"fileName":"test.png","fileSize":%d,"mimeType":"image/png"}}}`,
			time.Now().Unix(), time.Now().Add(time.Hour).Unix(), len(fileContent)))
	}))
	defer upstream.Close()

	fakeJWT := func() string {
		h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256"}`))
		p := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"sub":"user-1"}`, time.Now().Add(24*time.Hour).Unix())))
		return "Bearer " + h + "." + p + ".sig"
	}
	if _, err := core.Plugins.SetParams("workbuddy", map[string]string{
		"base_url":         upstream.URL,
		"authorization":    fakeJWT(),
		"x_user_id":        "user-1",
		"token_expires_at": fmt.Sprintf("%d", time.Now().Add(24*time.Hour).Unix()),
	}, "test"); err != nil {
		t.Fatalf("配置插件参数: %v", err)
	}

	srv := httptest.NewServer(api.New(core))
	defer srv.Close()
	client := srv.Client()

	// ---- 1. 登录 ----
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

	// ---- 2. 未登录访问被拒 ----
	if status, _ := doJSON(t, client, http.MethodGet, srv.URL+"/api/plugins", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实际 %d", status)
	}

	// ---- 3. 插件列表 ----
	status, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/plugins", token, nil)
	if status != http.StatusOK {
		t.Fatalf("插件列表失败: %d %s", status, body)
	}
	var plugins []map[string]any
	if err := json.Unmarshal([]byte(body), &plugins); err != nil {
		t.Fatal(err)
	}
	if len(plugins) == 0 || plugins[0]["id"] != "workbuddy" {
		t.Fatalf("内置插件未注册: %s", body)
	}
	if _, ok := plugins[0]["manifest"]; !ok {
		t.Fatal("插件视图缺少 manifest")
	}
	if _, ok := plugins[0]["grouped"]; !ok {
		t.Fatal("插件视图缺少分组参数 Schema")
	}

	// ---- 4. 插件调试（含上游连通性） ----
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/api/plugins/workbuddy/debug", token, map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("调试失败: %d %s", status, body)
	}
	var debug struct {
		Healthy bool `json:"healthy"`
		Steps   []struct {
			Name   string `json:"name"`
			Status string `json:"status"`
		} `json:"steps"`
	}
	_ = json.Unmarshal([]byte(body), &debug)
	if !debug.Healthy || len(debug.Steps) < 3 {
		t.Fatalf("调试结果异常: %s", body)
	}

	// ---- 5. 后台上传图片测试 ----
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	h := make(textproto.MIMEHeader)
	h.Set("Content-Disposition", `form-data; name="file"; filename="test.png"`)
	h.Set("Content-Type", "image/png")
	part, err := mw.CreatePart(h)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = part.Write(fileContent)
	_ = mw.WriteField("bucket", "default")
	_ = mw.WriteField("objectKey", "images/test.png")
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, srv.URL+"/api/upload", &buf)
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	uploadBody, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusCreated {
		t.Fatalf("上传失败: %d %s", resp.StatusCode, uploadBody)
	}
	var obj struct {
		ObjectKey string `json:"objectKey"`
		Category  string `json:"category"`
		Provider  string `json:"provider"`
		Bucket    string `json:"bucket"`
	}
	if err := json.Unmarshal(uploadBody, &obj); err != nil {
		t.Fatal(err)
	}
	if obj.ObjectKey != "images/test.png" || obj.Category != "image" || obj.Provider != "workbuddy" {
		t.Fatalf("标准输出映射异常: %+v", obj)
	}

	// ---- 6. 上传列表按类型 / bucket 过滤 ----
	status, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/uploads?bucket=default&category=image", token, nil)
	if status != http.StatusOK {
		t.Fatalf("上传列表失败: %d %s", status, body)
	}
	var list struct {
		Total      int              `json:"total"`
		Items      []map[string]any `json:"items"`
		Categories map[string]int   `json:"categories"`
	}
	_ = json.Unmarshal([]byte(body), &list)
	if list.Total != 1 || len(list.Items) != 1 || list.Categories["image"] != 1 {
		t.Fatalf("按文件类型归档异常: %s", body)
	}

	// ---- 7. 存储桶 ----
	status, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/buckets", token, nil)
	if status != http.StatusOK || !strings.Contains(body, "default") {
		t.Fatalf("存储桶列表异常: %d %s", status, body)
	}

	// ---- 8. 创建 API Key（S3 凭据） ----
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/api/keys", token,
		map[string]any{"name": "integration"})
	if status != http.StatusOK {
		t.Fatalf("创建密钥失败: %d %s", status, body)
	}
	var keyOut struct {
		SecretAccessKey string `json:"secretAccessKey"`
		AccessKeyID     string `json:"accessKeyId"`
	}
	if err := json.Unmarshal([]byte(body), &keyOut); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(keyOut.SecretAccessKey, "sk_") || !strings.HasPrefix(keyOut.AccessKeyID, "AK_") {
		t.Fatalf("S3 凭据格式异常: %s", body)
	}

	// ---- 9. 通过 S3 标准接口取回对象 ----
	s3req, _ := http.NewRequest(http.MethodGet, srv.URL+"/s3/default/images/test.png", nil)
	s3req.Header.Set("Authorization", "Bearer "+keyOut.SecretAccessKey)
	s3resp, err := client.Do(s3req)
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(s3resp.Body)
	_ = s3resp.Body.Close()
	if s3resp.StatusCode != http.StatusOK || string(got) != string(fileContent) {
		t.Fatalf("S3 GetObject 异常: %d %q", s3resp.StatusCode, got)
	}
	if s3resp.Header.Get("ETag") == "" || s3resp.Header.Get("x-amz-meta-provider") != "workbuddy" {
		t.Fatalf("S3 元数据头缺失: %+v", s3resp.Header)
	}

	// ---- 10. ListObjectsV2 ----
	s3req, _ = http.NewRequest(http.MethodGet, srv.URL+"/s3/default?list-type=2", nil)
	s3req.Header.Set("Authorization", "Bearer "+keyOut.SecretAccessKey)
	s3resp, err = client.Do(s3req)
	if err != nil {
		t.Fatal(err)
	}
	listXML, _ := io.ReadAll(s3resp.Body)
	_ = s3resp.Body.Close()
	if s3resp.StatusCode != http.StatusOK || !strings.Contains(string(listXML), "<Key>images/test.png</Key>") {
		t.Fatalf("ListObjectsV2 异常: %d %s", s3resp.StatusCode, listXML)
	}

	// ---- 11. MCP 工具清单 ----
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/mcp", token,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	if status != http.StatusOK {
		t.Fatalf("MCP 失败: %d %s", status, body)
	}
	var mcpResp struct {
		Result struct {
			Tools []map[string]any `json:"tools"`
		} `json:"result"`
	}
	if err := json.Unmarshal([]byte(body), &mcpResp); err != nil {
		t.Fatal(err)
	}
	if len(mcpResp.Result.Tools) < 25 {
		t.Fatalf("MCP 工具数量不足: %d", len(mcpResp.Result.Tools))
	}

	// ---- 12. 健康检查与告警 ----
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/api/plugins/workbuddy/check", token, map[string]any{})
	if status != http.StatusOK {
		t.Fatalf("健康检测失败: %d %s", status, body)
	}
	if !strings.Contains(body, "healthy") {
		t.Fatalf("健康状态未返回: %s", body)
	}
}

func doJSON(t *testing.T, client *http.Client, method, url, token string, payload any) (int, string) {
	t.Helper()
	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequest(method, url, body)
	if err != nil {
		t.Fatal(err)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}
