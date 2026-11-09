package mcp

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/config"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	t.Setenv("S3BRIDGE_DATA_DIR", t.TempDir())
	t.Setenv("S3BRIDGE_BOOTSTRAP_PASSWORD", "password123")
	t.Setenv("S3BRIDGE_MASTER_KEY", "mcp-test-master-key")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	core, err := app.New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = core.Close() })
	return New(core)
}

func adminPrincipal() *auth.Principal {
	p := &auth.Principal{UserID: "usr_admin", Username: "admin", Role: store.RoleAdmin, Via: "test", Perms: map[store.Permission]bool{}}
	for _, perm := range store.AllPermissions {
		p.Perms[perm] = true
	}
	return p
}

func rpcCall(t *testing.T, s *Server, msg string) map[string]any {
	t.Helper()
	out := s.Handle(context.Background(), []byte(msg), adminPrincipal())
	if len(out) == 0 {
		t.Fatal("期望响应但得到空（notification）")
	}
	var v map[string]any
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatalf("响应不是 JSON: %s", out)
	}
	return v
}

func TestMCPInitialize(t *testing.T) {
	s := newTestServer(t)
	resp := rpcCall(t, s, `{"jsonrpc":"2.0","id":1,"method":"initialize","params":{}}`)
	result, ok := resp["result"].(map[string]any)
	if !ok {
		t.Fatalf("initialize result 异常: %+v", resp)
	}
	if result["protocolVersion"] != ProtocolVersion {
		t.Fatalf("协议版本 = %v", result["protocolVersion"])
	}
	if _, ok := result["capabilities"].(map[string]any)["tools"]; !ok {
		t.Fatal("capabilities 缺少 tools")
	}
}

func TestMCPToolsListCoversRequiredCapabilities(t *testing.T) {
	s := newTestServer(t)
	resp := rpcCall(t, s, `{"jsonrpc":"2.0","id":2,"method":"tools/list","params":{}}`)
	result := resp["result"].(map[string]any)
	tools := result["tools"].([]any)
	if len(tools) < 25 {
		t.Fatalf("工具数量不足: %d", len(tools))
	}
	names := map[string]bool{}
	for _, item := range tools {
		tool := item.(map[string]any)
		if _, ok := tool["inputSchema"]; !ok {
			t.Fatalf("工具 %v 缺少 inputSchema", tool["name"])
		}
		names[tool["name"].(string)] = true
	}
	for _, required := range []string{
		"site_info", "plugin_docs", "s3_docs", "plugin_debug",
		"plugin_patch", "plugin_replace", "plugin_update", "plugin_params_set",
		"plugin_health_config", "upload_test", "alerts_list", "user_create",
	} {
		if !names[required] {
			t.Errorf("缺少必需工具: %s", required)
		}
	}
}

func TestMCPToolCallSiteInfo(t *testing.T) {
	s := newTestServer(t)
	resp := rpcCall(t, s, `{"jsonrpc":"2.0","id":3,"method":"tools/call","params":{"name":"site_info","arguments":{}}}`)
	result := resp["result"].(map[string]any)
	if result["isError"] == true {
		t.Fatalf("site_info 失败: %+v", result)
	}
	content := result["content"].([]any)
	if len(content) == 0 || content[0].(map[string]any)["type"] != "text" {
		t.Fatalf("content 异常: %+v", content)
	}
}

func TestMCPToolCallUnknown(t *testing.T) {
	s := newTestServer(t)
	resp := rpcCall(t, s, `{"jsonrpc":"2.0","id":4,"method":"tools/call","params":{"name":"does_not_exist","arguments":{}}}`)
	result := resp["result"].(map[string]any)
	if result["isError"] != true {
		t.Fatal("未知工具应返回 isError")
	}
}

func TestMCPNotificationHasNoResponse(t *testing.T) {
	s := newTestServer(t)
	out := s.Handle(context.Background(), []byte(`{"jsonrpc":"2.0","method":"notifications/initialized"}`), adminPrincipal())
	if out != nil {
		t.Fatalf("notification 不应有响应: %s", out)
	}
}

func TestMCPUnknownMethod(t *testing.T) {
	s := newTestServer(t)
	resp := rpcCall(t, s, `{"jsonrpc":"2.0","id":5,"method":"nope"}`)
	errObj := resp["error"].(map[string]any)
	if errObj["code"].(float64) != -32601 {
		t.Fatalf("错误码异常: %+v", errObj)
	}
}

func TestMCPResources(t *testing.T) {
	s := newTestServer(t)
	resp := rpcCall(t, s, `{"jsonrpc":"2.0","id":6,"method":"resources/list"}`)
	resources := resp["result"].(map[string]any)["resources"].([]any)
	if len(resources) < 4 {
		t.Fatalf("文档资源数量不足: %d", len(resources))
	}
	if body, ok := s.Resource("docs://plugin-development"); !ok || body == "" {
		t.Fatal("插件开发文档资源不可用")
	}
	if _, ok := s.Resource("docs://../secret"); ok {
		t.Fatal("目录穿越必须被拒绝")
	}
}
