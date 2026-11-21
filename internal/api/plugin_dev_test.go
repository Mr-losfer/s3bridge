package api_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/Mr-losfer/s3bridge/internal/plugin/sdk"
)

// pluginSource 是测试用的插件实现：先用一个 POST 换取上传票据，
// 再用 PUT 直传文件，借此证明插件可以自由发起任意方法与任意次数的请求。
const pluginSource = `package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Mr-losfer/s3bridge/sdk"
)

type plugin struct{}

// Upload 演示真实的「两步上传」：先换取凭据，再 PUT 直传。
func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
	f, err := os.Open(in.Path)
	if err != nil {
		return nil, fmt.Errorf("打开文件: %w", err)
	}
	defer f.Close()

	c := sdk.NewClient(20 * time.Second)
	var ticket map[string]string
	if _, err := c.JSON(ctx, http.MethodPost, cfg["upload_url"]+"/ticket",
		map[string]string{"Authorization": "Bearer " + cfg["token"]},
		map[string]string{"name": in.FileName}, &ticket); err != nil {
		return nil, fmt.Errorf("换取上传凭据: %w", err)
	}

	body, contentType := sdk.MultipartStream(cfg["file_field"], in.FileName, in.MimeType, f,
		map[string]string{"trace": traceID()})
	req, err := http.NewRequestWithContext(ctx, http.MethodPut, cfg["upload_url"], body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("X-Ticket", ticket["ticket"])
	req.Header.Set("X-Sign", sdk.HMACSHA256Hex(cfg["token"], ticket["ticket"]+in.FileName))

	resp, err := c.Do(req)
	if err != nil {
		return nil, fmt.Errorf("PUT 直传: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("上游返回 %d", resp.StatusCode)
	}
	return &sdk.Output{
		URL: "https://cdn.example.com/" + in.FileName, ObjectKey: in.ObjectKey,
		FileName: in.FileName, MimeType: in.MimeType, FileSize: in.Size,
	}, nil
}

func (plugin) Check(ctx context.Context, cfg sdk.Config) error {
	c := sdk.NewClient(10 * time.Second)
	_, err := c.Bytes(ctx, http.MethodGet, cfg["upload_url"]+"/health", nil, nil)
	return err
}

func main() { sdk.Serve(plugin{}) }
`

const helperSource = `package main

import "github.com/Mr-losfer/s3bridge/sdk"

// traceID 生成上游侧的一次性追踪号。
func traceID() string { return "v2-" + sdk.RandHex(4) }
`

// brokenHelper 故意漏掉右大括号：用来验证编译失败不会覆盖已就绪的产物，源码也不会被丢弃。
const brokenHelper = `package main

func traceID() string { return "broken"
`

// TestPluginDevelopmentViaMCP 证明 Agent 只通过 MCP 即可完成 Go 插件的完整闭环：
// 看内置插件文件 → 建插件（真编译）→ 多文件改写 → 编译失败与修复 → 改清单
// → 配参数 → 调试看请求追踪（密钥脱敏）→ 真实上传 → 归档 → 导出 → 删除 → 审计。
func TestPluginDevelopmentViaMCP(t *testing.T) {
	var (
		gotAuth   string
		gotSign   string
		gotTicket string
		gotTrace  string
		putCalls  int
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/upload/health":
			w.WriteHeader(http.StatusOK)
		case r.Method == http.MethodPost && r.URL.Path == "/upload/ticket":
			gotAuth = r.Header.Get("Authorization")
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ticket":"tk-1"}`)
		case r.Method == http.MethodPut && r.URL.Path == "/upload":
			putCalls++
			gotTicket = r.Header.Get("X-Ticket")
			gotSign = r.Header.Get("X-Sign")
			if err := r.ParseMultipartForm(1 << 20); err != nil {
				t.Errorf("上游解析 multipart 失败: %v", err)
			}
			gotTrace = r.FormValue("trace")
			file, _, err := r.FormFile("file")
			if err != nil {
				t.Errorf("上游未收到文件字段: %v", err)
			} else {
				_, _ = io.Copy(io.Discard, file)
				_ = file.Close()
			}
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"ok":true}`)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
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

	// ---- 0. 工具清单覆盖 Go 插件的完整开发链路 ----
	status, listBody := doJSON(t, client, http.MethodPost, srv.URL+"/mcp", adminToken,
		map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	if status != http.StatusOK {
		t.Fatalf("tools/list 失败: %d %s", status, listBody)
	}
	for _, tool := range []string{
		"plugin_create", "plugin_files", "plugin_file_read", "plugin_file_write",
		"plugin_file_delete", "plugin_file_rename", "plugin_build", "plugin_validate",
		"plugin_manifest_update", "plugin_export", "plugin_delete",
	} {
		if !strings.Contains(listBody, tool) {
			t.Fatalf("tools/list 缺少 %s", tool)
		}
	}

	// ---- 1. 内置插件就是一个普通文件夹：源码可读、系统文件只读 ----
	_, out := mustOK("plugin_files", map[string]any{"id": "workbuddy"})
	files := obj(out)["files"].([]any)
	names := map[string]map[string]any{}
	for _, f := range files {
		m := obj(f)
		names[m["name"].(string)] = m
	}
	for _, want := range []string{"main.go", "plugin.json", "go.mod"} {
		if names[want] == nil {
			t.Fatalf("内置插件缺少文件 %s: %+v", want, names)
		}
	}
	if names["main.go"]["entry"] != true || names["main.go"]["editable"] != true {
		t.Fatalf("main.go 应为可编辑入口: %+v", names["main.go"])
	}
	if names["plugin.json"]["system"] != true || names["plugin.json"]["editable"] != false {
		t.Fatalf("plugin.json 应为只读系统文件: %+v", names["plugin.json"])
	}

	_, out = mustOK("plugin_file_read", map[string]any{"id": "workbuddy", "name": "main.go"})
	builtinMain, _ := obj(out)["content"].(string)
	if !strings.Contains(builtinMain, "sdk.Serve") {
		t.Fatalf("内置插件源码不是 Go 实现: %s", builtinMain[:min(200, len(builtinMain))])
	}
	_, out = mustOK("plugin_file_read", map[string]any{"id": "workbuddy", "name": "plugin.json"})
	if obj(out)["editable"] != false {
		t.Fatal("plugin.json 不应可编辑")
	}

	// ---- 2. 创建插件（多文件，立即编译）----
	_, out = mustOK("plugin_create", map[string]any{
		"id": "agent-up", "name": "Agent 上传器", "version": "1.0.0",
		"template": "http-multipart", "enabled": true, "priority": 260,
		"fileTypes": []any{"image", "document"},
		"files":     map[string]any{"main.go": pluginSource, "helper.go": helperSource},
		"params": map[string]any{
			"upload_url": upstream.URL + "/upload", "token": "agent-token",
			"file_field": "file", "token_expires_at": "2099-01-01T00:00:00Z",
		},
	})
	if obj(out)["created"] != "agent-up" {
		t.Fatalf("创建失败: %+v", out)
	}
	created := obj(obj(out)["plugin"])
	if created["priority"] != float64(260) || created["custom"] != true {
		t.Fatalf("创建结果异常: %+v", created)
	}
	if types, ok := created["fileTypes"].([]any); !ok || len(types) != 2 || types[0] != "image" {
		t.Fatalf("创建时文件类型未生效: %+v", created)
	}
	// 创建即编译：产物就绪后 built/buildSha 才有值。
	_, out = mustOK("plugin_get", map[string]any{"id": "agent-up"})
	detail := obj(out)
	if detail["built"] != true || detail["buildError"] != nil && detail["buildError"] != "" {
		t.Fatalf("插件创建后未编译成功: %+v", detail)
	}
	if detail["editable"] != true || detail["toolchain"] != true {
		t.Fatalf("自定义插件应可编辑且工具链可用: %+v", detail)
	}
	if guarded, ok := detail["params"].(map[string]any); ok {
		if guarded["token"] == "agent-token" {
			t.Fatal("敏感参数应脱敏")
		}
	}

	// ---- 3. 多文件编译：把 helper.go 写坏 → 编译失败但源码保留 ----
	_, out = mustOK("plugin_file_write", map[string]any{
		"id": "agent-up", "name": "helper.go", "content": brokenHelper,
	})
	if br := obj(out)["build"].(map[string]any); br["ok"] != false || strings.TrimSpace(str(br["output"])) == "" {
		t.Fatalf("语法错误应导致编译失败并给出编译器输出: %+v", br)
	}
	_, out = mustOK("plugin_file_read", map[string]any{"id": "agent-up", "name": "helper.go"})
	if !strings.Contains(str(obj(out)["content"]), "broken") {
		t.Fatal("编译失败后源码不应被丢弃")
	}

	// 修复后立即重新编译成功，说明旧产物可用性不受影响。
	_, out = mustOK("plugin_file_write", map[string]any{
		"id": "agent-up", "name": "helper.go", "content": helperSource,
	})
	if br := obj(out)["build"].(map[string]any); br["ok"] != true {
		t.Fatalf("修复后应编译成功: %+v", br)
	}

	// ---- 4. 新增第三个文件并用 file_rename 改成入口不相关文件 ----
	_, out = mustOK("plugin_file_write", map[string]any{
		"id": "agent-up", "name": "notes.md", "content": "# Agent 上传器\n两步上传：ticket + PUT。\n",
	})
	_, out = mustOK("plugin_file_rename", map[string]any{
		"id": "agent-up", "from": "notes.md", "to": "README.md",
	})
	_, out = mustOK("plugin_files", map[string]any{"id": "agent-up"})
	var hasReadme bool
	for _, f := range obj(out)["files"].([]any) {
		if obj(f)["name"] == "README.md" {
			hasReadme = true
		}
	}
	if !hasReadme {
		t.Fatalf("重命名后文件缺失: %+v", out)
	}

	// ---- 5. 清单更新：有效期覆盖 + 展示名 ----
	_, out = mustOK("plugin_get", map[string]any{"id": "agent-up"})
	mf := obj(obj(out)["manifest"])
	mf["name"] = "Agent 上传器 v2"
	mf["validity"] = map[string]any{"enabled": true, "intervalHours": 48}
	_, out = mustOK("plugin_manifest_update", map[string]any{"id": "agent-up", "manifest": mf})
	if obj(out)["validityHours"] != float64(48) {
		t.Fatalf("清单里的有效期周期未落库: %+v", out)
	}

	// ---- 6. 静态校验：一份合法源码应通过，一份语法错误应失败 ----
	_, out = mustOK("plugin_validate", map[string]any{
		"id":    "agent-up",
		"files": map[string]any{"main.go": pluginSource, "helper.go": helperSource},
	})
	if br := obj(out)["build"].(map[string]any); br["ok"] != true {
		t.Fatalf("合法源码未通过校验: %+v", br)
	}
	_, out = mustOK("plugin_validate", map[string]any{
		"files": map[string]any{"main.go": "package main\nfunc main() { oops }\n"},
	})
	if br := obj(out)["build"].(map[string]any); br["ok"] != false {
		t.Fatalf("非法源码应校验失败: %+v", br)
	}

	// ---- 7. 调试：真实跑一次 Check，带回请求追踪且密钥脱敏 ----
	debugText, out := mustOK("plugin_debug", map[string]any{"id": "agent-up"})
	report := obj(out)
	if report["healthy"] != true {
		t.Fatalf("调试未通过: %s", debugText)
	}
	var sawParam, sawBuild, sawProbe bool
	for _, s := range report["steps"].([]any) {
		step := obj(s)
		switch step["name"] {
		case "参数校验":
			sawParam = step["status"] == "pass"
		case "编译产物":
			sawBuild = step["status"] == "pass"
		case "插件自检":
			sawProbe = step["status"] == "pass"
		}
	}
	if !sawParam || !sawBuild || !sawProbe {
		t.Fatalf("调试步骤不完整: %s", debugText)
	}
	traces, _ := report["traces"].([]any)
	if len(traces) == 0 {
		t.Fatalf("调试应带回出站请求追踪: %s", debugText)
	}
	for _, tr := range traces {
		if h, ok := obj(tr)["reqHeaders"].(map[string]any); ok {
			if v, ok := h["Authorization"].(string); ok && v != "***" {
				t.Fatalf("Authorization 未脱敏: %v", h)
			}
		}
	}

	// ---- 8. 真实上传：两步请求（POST 换票 → PUT 直传）全部生效 ----
	content := base64.StdEncoding.EncodeToString([]byte("PNGDATA"))
	uploadText, out := mustOK("upload_test", map[string]any{
		"fileName": "x.png", "contentType": "image/png", "contentBase64": content,
		"bucket": "default", "pluginId": "agent-up",
	})
	uploaded := obj(out)
	if uploaded["provider"] != "agent-up" || uploaded["fileName"] != "x.png" {
		t.Fatalf("上传未走自定义插件: %s", uploadText)
	}
	if gotAuth != "Bearer agent-token" {
		t.Fatalf("票据请求的鉴权头未生效: %q", gotAuth)
	}
	if putCalls != 1 || gotTicket != "tk-1" {
		t.Fatalf("PUT 直传请求异常: calls=%d ticket=%q", putCalls, gotTicket)
	}
	if want := sdk.HMACSHA256Hex("agent-token", "tk-1x.png"); gotSign != want {
		t.Fatalf("HMAC 签名不一致: got=%q want=%q", gotSign, want)
	}
	if !strings.HasPrefix(gotTrace, "v2-") {
		t.Fatalf("helper.go 的改动未进入编译产物: trace=%q", gotTrace)
	}

	// ---- 9. 归档可按插件过滤 ----
	listText, _ := mustOK("uploads_list", map[string]any{"pluginId": "agent-up"})
	if !strings.Contains(listText, "x.png") {
		t.Fatalf("上传归档未落库: %s", listText)
	}

	// ---- 10. 导出全部源码，可在另一实例复刻 ----
	_, out = mustOK("plugin_export", map[string]any{"id": "agent-up"})
	exported := obj(out)["files"].(map[string]any)
	for _, want := range []string{"main.go", "helper.go", "plugin.json", "README.md"} {
		if _, ok := exported[want]; !ok {
			t.Fatalf("导出缺少 %s: %+v", want, exported)
		}
	}

	// ---- 11. 内置插件不可删，自定义插件可删 ----
	_, _, isErr := call("plugin_delete", map[string]any{"id": "workbuddy"}, adminToken)
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

	// ---- 12. 变更审计完整 ----
	changesText, _ := mustOK("plugin_changes", map[string]any{"id": "agent-up"})
	for _, action := range []string{"create", "file-write", "file-rename", "build", "manifest", "delete"} {
		if !strings.Contains(changesText, action) {
			t.Fatalf("审计记录缺少 %s: %s", action, changesText)
		}
	}
}

// str 把 JSON 解出的任意值转成字符串，便于断言。
func str(v any) string {
	s, _ := v.(string)
	return s
}
