package api_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// TestPluginDevelopmentViaMCP 证明 Agent 仅通过 MCP 即可完成插件开发全生命周期：
// 读模板 → 创建 → 读源码 → 差量改码 → 乐观锁 → 静态校验 → 配参数 → 调试
// → 真实上传 → 查看归档 → 导出 Go 源码 → 全量换码 → 删除 → 审计。
func TestPluginDevelopmentViaMCP(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/upload" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if got := r.Header.Get("X-Stage"); got != "v2" {
			t.Errorf("差量修改的请求头未生效: %q", got)
		}
		if got := r.Header.Get("X-Token"); got != "agent-token" {
			t.Errorf("参数模板未生效: %q", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("上游解析 multipart 失败: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":{"url":"https://cdn.example.com/x.png","objectKey":"attachments/x.png","fileInfo":{"fileName":"x.png","fileSize":7,"mimeType":"image/png"}}}`)
	}))
	defer upstream.Close()

	srv, adminToken := newKeyTestServer(t)
	client := srv.Client()

	// call 走真实 /mcp JSON-RPC 端点，返回工具原始文本、解析后的对象与是否报错。
	call := func(name string, args map[string]any, token string) (string, any, bool) {
		t.Helper()
		status, body := doJSON(t, client, http.MethodPost, srv.URL+"/mcp", token, map[string]any{
			"jsonrpc": "2.0", "id": 1, "method": "tools/call",
			"params": map[string]any{"name": name, "arguments": args},
		})
		if status != http.StatusOK {
			t.Fatalf("MCP %s 调用失败: %d %s", name, status, body)
		}
		var env struct {
			Result struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError bool `json:"isError"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal([]byte(body), &env); err != nil {
			t.Fatalf("解析 MCP 响应失败: %v: %s", err, body)
		}
		if env.Error != nil {
			return "", nil, true
		}
		text := ""
		if len(env.Result.Content) > 0 {
			text = env.Result.Content[0].Text
		}
		var out any
		_ = json.Unmarshal([]byte(text), &out)
		return text, out, env.Result.IsError
	}
	mustOK := func(name string, args map[string]any) (string, any) {
		t.Helper()
		text, out, isErr := call(name, args, adminToken)
		if isErr {
			t.Fatalf("MCP %s 返回错误: %s", name, text)
		}
		return text, out
	}
	obj := func(v any) map[string]any {
		m, ok := v.(map[string]any)
		if !ok {
			t.Fatalf("期望对象，实际 %T: %v", v, v)
		}
		return m
	}

	// ---- 0. 工具清单包含全部插件开发能力 ----
	status, listBody := doJSON(t, client, http.MethodPost, srv.URL+"/mcp", adminToken,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	if status != http.StatusOK {
		t.Fatalf("tools/list 失败: %d %s", status, listBody)
	}
	for _, tool := range []string{
		"plugin_create", "plugin_code_get", "plugin_code_upload", "plugin_code_patch",
		"plugin_validate", "plugin_export", "plugin_delete",
	} {
		if !strings.Contains(listBody, tool) {
			t.Fatalf("tools/list 缺少 %s", tool)
		}
	}

	// ---- 1. 内置插件提供只读的等价声明式源码 ----
	_, out := mustOK("plugin_code_get", map[string]any{"id": "workbuddy"})
	builtin := obj(out)
	if builtin["editable"] != false || builtin["custom"] != false {
		t.Fatalf("内置插件源码应为只读: %+v", builtin)
	}
	builtinSrc, _ := builtin["sourceJSON"].(string)
	if !strings.Contains(builtinSrc, "workbuddy-template") || !strings.Contains(builtinSrc, "data.url") {
		t.Fatalf("内置插件等价源码不完整: %s", builtinSrc[:min(200, len(builtinSrc))])
	}

	// ---- 2. 从零创建插件（附完整源码）----
	def := map[string]any{
		"kind": "agent-up", "name": "Agent 上传器", "version": "1.0.0",
		"params": []any{
			map[string]any{"key": "base_url", "label": "服务地址", "type": "url", "required": true},
			map[string]any{"key": "token", "label": "Token", "type": "secret", "sensitive": true},
		},
		"request": map[string]any{
			"method": "POST", "url": "{{base_url}}/upload", "mode": "multipart", "fileField": "file",
			"headers": map[string]any{"X-Token": "{{token}}"},
		},
		"response": map[string]any{
			"format": "json", "codePath": "code", "successCode": "0", "messagePath": "msg",
			"urlPath": "data.url", "objectKeyPath": "data.objectKey",
			"fileNamePath": "data.fileInfo.fileName", "sizePath": "data.fileInfo.fileSize",
			"mimeTypePath": "data.fileInfo.mimeType",
		},
	}
	_, out = mustOK("plugin_create", map[string]any{
		"kind": "agent-up", "name": "Agent 上传器", "definition": def, "enabled": true,
		"priority": 260, "fileTypes": []any{"image", "document"},
	})
	if obj(out)["created"] != "agent-up" {
		t.Fatalf("创建失败: %+v", out)
	}
	createdPlugin := obj(obj(out)["plugin"])
	if createdPlugin["priority"] != float64(260) {
		t.Fatalf("创建时权重未生效: %+v", createdPlugin)
	}
	if types, ok := createdPlugin["fileTypes"].([]any); !ok || len(types) != 2 || types[0] != "image" {
		t.Fatalf("创建时文件类型未生效: %+v", createdPlugin)
	}

	// ---- 3. 读回源码 ----
	_, out = mustOK("plugin_code_get", map[string]any{"id": "agent-up"})
	mine := obj(out)
	if mine["editable"] != true || mine["custom"] != true {
		t.Fatalf("自定义插件源码应可编辑: %+v", mine)
	}
	sourceJSON, _ := mine["sourceJSON"].(string)
	if !strings.Contains(sourceJSON, "{{base_url}}/upload") {
		t.Fatalf("源码读取异常: %s", sourceJSON)
	}

	// ---- 4. 差量修改：点号路径 + params 按 key upsert ----
	_, out = mustOK("plugin_code_patch", map[string]any{
		"id": "agent-up",
		"patch": map[string]any{
			"request.headers.X-Stage": "v2",
			"name":                    "Agent 上传器 v2",
			"params": []any{
				map[string]any{"key": "x_trace", "label": "Trace", "type": "text"},
			},
		},
	})
	_, out = mustOK("plugin_code_get", map[string]any{"id": "agent-up"})
	sourceJSON, _ = obj(out)["sourceJSON"].(string)
	for _, want := range []string{"X-Stage", "v2", "Agent 上传器 v2", "x_trace", "base_url", "token"} {
		if !strings.Contains(sourceJSON, want) {
			t.Fatalf("差量修改未保留/写入 %q: %s", want, sourceJSON)
		}
	}

	// ---- 5. 乐观锁：过期 revision 必须被拒绝 ----
	_, _, isErr := call("plugin_code_patch", map[string]any{
		"id": "agent-up", "patch": map[string]any{"name": "冲突"}, "expectedRevision": 999,
	}, adminToken)
	if !isErr {
		t.Fatal("过期 revision 应触发版本冲突")
	}

	// ---- 6. 静态校验（不落库）----
	_, out = mustOK("plugin_validate", map[string]any{
		"definition": sourceJSON,
		"params":     map[string]any{"base_url": upstream.URL, "token": "agent-token"},
	})
	if obj(out)["valid"] != true {
		t.Fatalf("源码校验未通过: %+v", out)
	}

	// ---- 7. 配置参数 ----
	mustOK("plugin_params_set", map[string]any{
		"id": "agent-up", "params": map[string]any{"base_url": upstream.URL, "token": "agent-token"},
	})

	// ---- 8. 调试（无副作用探活）----
	debugText, _ := mustOK("plugin_debug", map[string]any{"id": "agent-up"})
	if !strings.Contains(debugText, "参数校验") {
		t.Fatalf("调试报告异常: %s", debugText)
	}

	// ---- 9. 真实上传：走刚创建的动态插件 ----
	content := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	uploadText, out := mustOK("upload_test", map[string]any{
		"fileName": "x.png", "contentType": "image/png", "contentBase64": content,
		"bucket": "default", "pluginId": "agent-up",
	})
	uploaded := obj(out)
	if uploaded["provider"] != "agent-up" || uploaded["fileName"] != "x.png" {
		t.Fatalf("上传未走自定义插件: %s", uploadText)
	}

	// ---- 10. 归档列表可按插件过滤 ----
	listText, _ := mustOK("uploads_list", map[string]any{"plugin": "agent-up"})
	if !strings.Contains(listText, "x.png") {
		t.Fatalf("上传归档未落库: %s", listText)
	}

	// ---- 11. 导出为可内置化的 Go 源码 ----
	_, out = mustOK("plugin_export", map[string]any{"id": "agent-up", "format": "go"})
	goSrc, _ := obj(out)["content"].(string)
	if !strings.Contains(goSrc, "func Descriptor()") || !strings.Contains(goSrc, "sourceJSON") {
		t.Fatalf("Go 源码导出异常: %s", goSrc)
	}

	// ---- 12. 全量替换源码 ----
	def["name"] = "Agent 上传器 v3"
	def["request"].(map[string]any)["headers"] = map[string]any{
		"X-Token": "{{token}}", "X-Stage": "v2",
	}
	_, out = mustOK("plugin_code_upload", map[string]any{"id": "agent-up", "definition": def})
	if obj(obj(out)["plugin"])["name"] != "Agent 上传器 v3" {
		t.Fatalf("全量替换未生效: %+v", out)
	}
	_, out = mustOK("plugin_code_get", map[string]any{"id": "agent-up"})
	sourceJSON, _ = obj(out)["sourceJSON"].(string)
	if !strings.Contains(sourceJSON, "Agent 上传器 v3") || !strings.Contains(sourceJSON, "X-Stage") {
		t.Fatalf("全量替换后源码异常: %s", sourceJSON)
	}

	// ---- 13. 内置插件不可删，自定义可删 ----
	_, _, isErr = call("plugin_delete", map[string]any{"id": "workbuddy"}, adminToken)
	if !isErr {
		t.Fatal("内置插件不应允许删除")
	}
	_, out = mustOK("plugin_delete", map[string]any{"id": "agent-up"})
	if obj(out)["deleted"] != "agent-up" {
		t.Fatalf("删除失败: %+v", out)
	}
	listText, _ = mustOK("plugin_list", map[string]any{})
	if strings.Contains(listText, "agent-up") {
		t.Fatalf("删除后插件仍在列表: %s", listText)
	}

	// ---- 14. 变更审计完整 ----
	changesText, _ := mustOK("plugin_changes", map[string]any{"id": "agent-up"})
	for _, action := range []string{"create", "code_patch", "code", "delete"} {
		if !strings.Contains(changesText, action) {
			t.Fatalf("审计记录缺少 %s: %s", action, changesText)
		}
	}
}
