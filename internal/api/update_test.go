package api_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"

	"github.com/Mr-losfer/s3bridge/internal/api"
	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/config"
	"github.com/Mr-losfer/s3bridge/internal/updater"
)

const releaseBody = `## Changelog
### 新功能
* abc1234 feat: 首页版本检测与一键自动更新
* def5678 **重要：更新要点高亮显示**
### 安全
* 123abcd fix: 修复 CVE-2026-0001 权限提升风险
---
**安装**：见 [文档](https://example.com)
`

// TestUpdateAPI 覆盖更新状态的读取、检测、忽略、权限隔离与 MCP 暴露。
// 刻意让 mock Release 与当前版本一致（无可用更新），确保测试绝不会替换测试进程自身。
func TestUpdateAPI(t *testing.T) {
	assetName := updater.AssetName(runtime.GOOS, runtime.GOARCH)
	var gh *httptest.Server
	gh = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/releases/latest") {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"tag_name": "v1.0.0", "name": "S3Bridge v1.0.0", "body": releaseBody,
			"published_at": "2026-11-09T10:00:00Z",
			"html_url":     "https://github.com/Mr-losfer/s3bridge/releases/tag/v1.0.0",
			"prerelease":   false, "draft": false,
			"assets": []map[string]any{
				{"name": assetName, "browser_download_url": gh.URL + "/download/" + assetName, "size": 1024},
				{"name": "checksums.txt", "browser_download_url": gh.URL + "/download/checksums.txt", "size": 128},
			},
		})
	}))
	defer gh.Close()

	t.Setenv("S3BRIDGE_DATA_DIR", t.TempDir())
	t.Setenv("S3BRIDGE_BOOTSTRAP_PASSWORD", "admin-pass-123")
	t.Setenv("S3BRIDGE_MASTER_KEY", "update-test-master-key")
	t.Setenv("S3BRIDGE_UPDATE_API", gh.URL)
	t.Setenv("S3BRIDGE_UPDATE_AUTO_CHECK", "false")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	core, err := app.New(cfg, "v1.0.0")
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()

	srv := httptest.NewServer(api.New(core))
	defer srv.Close()
	client := srv.Client()

	// 1. 未登录不可读取更新状态
	status, _ := doJSON(t, client, http.MethodGet, srv.URL+"/api/update/status", "", nil)
	if status != http.StatusUnauthorized {
		t.Fatalf("未登录应返回 401，实际 %d", status)
	}

	// 2. 登录管理员
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

	// 3. 状态字段完整
	status, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/update/status", token, nil)
	if status != http.StatusOK {
		t.Fatalf("读取状态失败: %d %s", status, body)
	}
	var st struct {
		Current           string `json:"current"`
		Repo              string `json:"repo"`
		Strategy          string `json:"strategy"`
		RestartConfigured bool   `json:"restartConfigured"`
		UpdateAvailable   bool   `json:"updateAvailable"`
	}
	if err := json.Unmarshal([]byte(body), &st); err != nil {
		t.Fatal(err)
	}
	if st.Current != "v1.0.0" || st.Repo == "" || st.Strategy == "" {
		t.Fatalf("状态字段缺失：%+v", st)
	}
	if st.RestartConfigured {
		t.Fatal("未由 main 注入重启回调时应标记为 false")
	}

	// 4. 检测：返回结构化更新说明与重点数量
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/api/update/check", token,
		map[string]any{"force": true})
	if status != http.StatusOK {
		t.Fatalf("检测更新失败: %d %s", status, body)
	}
	var checked struct {
		CheckError      string `json:"checkError"`
		UpdateAvailable bool   `json:"updateAvailable"`
		Latest          struct {
			Tag            string `json:"tag"`
			HighlightCount int    `json:"highlightCount"`
			Notes          []struct {
				Title string `json:"title"`
				Items []struct {
					Important bool `json:"important"`
					Segments  []struct {
						Bold bool `json:"bold"`
					} `json:"segments"`
				} `json:"items"`
			} `json:"notes"`
		} `json:"latest"`
	}
	if err := json.Unmarshal([]byte(body), &checked); err != nil {
		t.Fatal(err)
	}
	if checked.CheckError != "" || checked.Latest.Tag != "v1.0.0" || checked.UpdateAvailable {
		t.Fatalf("检测结果异常：%+v", checked)
	}
	if checked.Latest.HighlightCount < 2 || len(checked.Latest.Notes) < 2 {
		t.Fatalf("更新要点未结构化：%+v", checked.Latest)
	}

	// 5. 忽略版本
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/api/update/dismiss", token,
		map[string]any{"tag": "v1.0.0"})
	if status != http.StatusOK || !strings.Contains(body, `"dismissed":true`) {
		t.Fatalf("忽略版本失败: %d %s", status, body)
	}

	// 6. 无可用更新时拒绝 apply
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/api/update/apply", token, map[string]any{})
	if status != http.StatusConflict {
		t.Fatalf("无更新时 apply 应返回 409，实际 %d %s", status, body)
	}

	// 7. 创建只读用户，验证 apply 的权限隔离
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/api/users", token,
		map[string]any{"username": "viewer", "password": "viewer-pass-123", "role": "viewer"})
	if status != http.StatusOK {
		t.Fatalf("创建只读用户失败: %d %s", status, body)
	}
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/api/auth/login", "",
		map[string]any{"username": "viewer", "password": "viewer-pass-123"})
	if status != http.StatusOK {
		t.Fatalf("只读用户登录失败: %d %s", status, body)
	}
	var viewer struct {
		Token string `json:"token"`
	}
	_ = json.Unmarshal([]byte(body), &viewer)
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/api/update/apply", viewer.Token, map[string]any{})
	if status != http.StatusForbidden {
		t.Fatalf("只读用户触发更新应返回 403，实际 %d %s", status, body)
	}
	// 只读用户仍可查看状态与更新内容
	status, _ = doJSON(t, client, http.MethodGet, srv.URL+"/api/update/status", viewer.Token, nil)
	if status != http.StatusOK {
		t.Fatalf("只读用户应可查看更新状态，实际 %d", status)
	}

	// 8. MCP 暴露 4 个更新工具，且 status 工具可调用
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/mcp", token,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	if status != http.StatusOK {
		t.Fatalf("MCP tools/list 失败: %d %s", status, body)
	}
	want := []string{"system_update_status", "system_update_check", "system_update_apply", "system_update_dismiss"}
	for _, name := range want {
		if !strings.Contains(body, `"`+name+`"`) {
			t.Fatalf("MCP 工具清单缺少 %s", name)
		}
	}
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/mcp", token,
		map[string]any{"jsonrpc": "2.0", "id": 2, "method": "tools/call",
			"params": map[string]any{"name": "system_update_status", "arguments": map[string]any{}}})
	if status != http.StatusOK || !strings.Contains(body, "current") || strings.Contains(body, `"isError":true`) {
		t.Fatalf("MCP system_update_status 异常: %d %s", status, body)
	}
}
