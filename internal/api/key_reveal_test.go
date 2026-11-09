package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mr-losfer/s3bridge/internal/api"
	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/config"
)

// newKeyTestServer 启动一套独立的最小环境，返回测试服务与管理员令牌。
func newKeyTestServer(t *testing.T) (*httptest.Server, string) {
	t.Helper()
	t.Setenv("S3BRIDGE_DATA_DIR", t.TempDir())
	t.Setenv("S3BRIDGE_BOOTSTRAP_PASSWORD", "admin-pass-123")
	t.Setenv("S3BRIDGE_MASTER_KEY", "key-reveal-master-key")
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
		t.Fatalf("管理员登录失败: %d %s", status, body)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &login); err != nil || login.Token == "" {
		t.Fatalf("登录响应异常: %s", body)
	}
	return srv, login.Token
}

func createUser(t *testing.T, srv *httptest.Server, adminToken, username, role string) string {
	t.Helper()
	status, body := doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/users", adminToken,
		map[string]any{"username": username, "password": "user-pass-123", "role": role})
	if status != http.StatusOK {
		t.Fatalf("创建用户 %s 失败: %d %s", username, status, body)
	}
	status, body = doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/auth/login", "",
		map[string]any{"username": username, "password": "user-pass-123"})
	if status != http.StatusOK {
		t.Fatalf("用户 %s 登录失败: %d %s", username, status, body)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &login); err != nil || login.Token == "" {
		t.Fatalf("登录响应异常: %s", body)
	}
	return login.Token
}

func createKey(t *testing.T, srv *httptest.Server, token, name string) (id, access, secret string) {
	t.Helper()
	status, body := doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/api/keys", token,
		map[string]any{"name": name})
	if status != http.StatusOK {
		t.Fatalf("创建密钥失败: %d %s", status, body)
	}
	var out struct {
		ID              string `json:"id"`
		AccessKeyID     string `json:"accessKeyId"`
		SecretAccessKey string `json:"secretAccessKey"`
	}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		t.Fatal(err)
	}
	if out.ID == "" || !strings.HasPrefix(out.AccessKeyID, "AK_") || !strings.HasPrefix(out.SecretAccessKey, "sk_") {
		t.Fatalf("创建密钥响应异常: %s", body)
	}
	return out.ID, out.AccessKeyID, out.SecretAccessKey
}

// TestKeyRevealAnytime 验证明文可反复查看，且仅本人/管理员可见。
func TestKeyRevealAnytime(t *testing.T) {
	srv, adminToken := newKeyTestServer(t)
	aliceToken := createUser(t, srv, adminToken, "alice", "viewer")
	bobToken := createUser(t, srv, adminToken, "bob", "viewer")
	keyID, access, secret := createKey(t, srv, aliceToken, "alice-laptop")

	// 同一把密钥连续查看两次，明文必须与创建时完全一致。
	for i := 0; i < 2; i++ {
		status, body := doJSON(t, srv.Client(), http.MethodGet,
			srv.URL+"/api/keys/"+keyID+"/secret", aliceToken, nil)
		if status != http.StatusOK {
			t.Fatalf("第 %d 次查看自己的密钥失败: %d %s", i+1, status, body)
		}
		var got struct {
			ID              string `json:"id"`
			UserID          string `json:"userId"`
			AccessKeyID     string `json:"accessKeyId"`
			SecretAccessKey string `json:"secretAccessKey"`
			Region          string `json:"region"`
		}
		if err := json.Unmarshal([]byte(body), &got); err != nil {
			t.Fatal(err)
		}
		if got.ID != keyID || got.AccessKeyID != access || got.SecretAccessKey != secret {
			t.Fatalf("查看结果不一致: got=%s want=%s/%s", body, access, secret)
		}
		if got.UserID == "" || got.Region == "" {
			t.Fatalf("查看结果缺少归属或接入信息: %s", body)
		}
	}

	// 密钥列表仍不得泄露明文。
	status, body := doJSON(t, srv.Client(), http.MethodGet, srv.URL+"/api/keys", aliceToken, nil)
	if status != http.StatusOK || strings.Contains(body, secret) {
		t.Fatalf("密钥列表泄露明文: %d %s", status, body)
	}

	// 其他普通用户不可查看。
	status, body = doJSON(t, srv.Client(), http.MethodGet,
		srv.URL+"/api/keys/"+keyID+"/secret", bobToken, nil)
	if status != http.StatusForbidden {
		t.Fatalf("他人密钥应被拒绝，实际: %d %s", status, body)
	}

	// 管理员可查看。
	status, body = doJSON(t, srv.Client(), http.MethodGet,
		srv.URL+"/api/keys/"+keyID+"/secret", adminToken, nil)
	if status != http.StatusOK || !strings.Contains(body, secret) {
		t.Fatalf("管理员查看失败: %d %s", status, body)
	}

	// 不存在的密钥返回 404，未登录返回 401。
	status, _ = doJSON(t, srv.Client(), http.MethodGet,
		srv.URL+"/api/keys/key_notexist/secret", aliceToken, nil)
	if status != http.StatusNotFound {
		t.Fatalf("不存在的密钥应返回 404，实际 %d", status)
	}
	status, _ = doJSON(t, srv.Client(), http.MethodGet,
		srv.URL+"/api/keys/"+keyID+"/secret", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未登录应返回 401，实际 %d", status)
	}
}

// TestKeyRevealMCP 验证 MCP apikey_reveal 工具与 API Key 自身作为 Bearer 的鉴权路径。
func TestKeyRevealMCP(t *testing.T) {
	srv, adminToken := newKeyTestServer(t)
	aliceToken := createUser(t, srv, adminToken, "alice", "viewer")
	bobToken := createUser(t, srv, adminToken, "bob", "viewer")
	keyID, _, secret := createKey(t, srv, aliceToken, "alice-mcp")

	call := func(token, id string) (int, string) {
		return doJSON(t, srv.Client(), http.MethodPost, srv.URL+"/mcp", token,
			map[string]any{
				"jsonrpc": "2.0", "id": 1, "method": "tools/call",
				"params": map[string]any{"name": "apikey_reveal", "arguments": map[string]any{"id": id}},
			})
	}

	status, body := call(adminToken, keyID)
	if status != http.StatusOK || !strings.Contains(body, secret) {
		t.Fatalf("MCP 查看密钥失败: %d %s", status, body)
	}
	status, body = call(bobToken, keyID)
	if status != http.StatusOK || !strings.Contains(body, "无权查看他人密钥") {
		t.Fatalf("MCP 越权未被拦截: %d %s", status, body)
	}

	// 用 API Key 明文本身作为 Bearer（与 S3 / 客户端用法一致）也可查看自己。
	status, body = doJSON(t, srv.Client(), http.MethodGet,
		srv.URL+"/api/keys/"+keyID+"/secret", secret, nil)
	if status != http.StatusOK || !strings.Contains(body, secret) {
		t.Fatalf("API Key 自身鉴权查看失败: %d %s", status, body)
	}
}
