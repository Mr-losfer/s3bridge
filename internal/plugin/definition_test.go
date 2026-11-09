package plugin

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func testDefinition(baseURL string) *Definition {
	return &Definition{
		Kind: "mockup", Name: "Mock 上传", Version: "1.0.0",
		Params: []ParamField{
			{Key: "base_url", Label: "服务地址", Type: "url", Group: "连接", Required: true},
			{Key: "token", Label: "Token", Type: "secret", Group: "鉴权", Sensitive: true},
		},
		Request: RequestSpec{
			Method: "POST", URL: "{{base_url}}/upload", Mode: "multipart",
			FileField: "file", TimeoutSecs: 10,
			Headers: map[string]string{"X-Token": "{{token}}"},
			Fields:  map[string]string{"note": "{{file_name}}"},
		},
		Response: ResponseSpec{
			Format: "json", CodePath: "code", SuccessCode: "0", MessagePath: "msg",
			URLPath: "data.url", ObjectKeyPath: "data.objectKey",
			FileNamePath: "data.fileInfo.fileName", SizePath: "data.fileInfo.fileSize",
			MimeTypePath: "data.fileInfo.mimeType", ExpirePath: "data.expireAt",
		},
		Health: HealthHint{Enabled: true, IntervalSecs: 300, TimeoutSecs: 8, FailThreshold: 2, ProbeMode: "ping"},
	}
}

func TestDefinitionValidate(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*Definition)
		wantErr string
	}{
		{"合法定义", func(*Definition) {}, ""},
		{"kind 非法", func(d *Definition) { d.Kind = "Bad_Kind" }, "kind"},
		{"缺少 urlPath", func(d *Definition) { d.Response.URLPath = "" }, "urlPath"},
		{"未知模板变量", func(d *Definition) { d.Request.URL = "{{base_url}}/{{undefined_key}}" }, "未在 params 中声明"},
		{"头部注入", func(d *Definition) { d.Request.Headers["X-Bad"] = "a\r\nX-Evil: 1" }, "换行"},
		{"重复参数 key", func(d *Definition) { d.Params = append(d.Params, d.Params[0]) }, "重复"},
		{"非法 mode", func(d *Definition) { d.Request.Mode = "ftp" }, "mode"},
		{"multipart 缺 fileField", func(d *Definition) { d.Request.FileField = "" }, "fileField"},
		{"expiresField 不存在", func(d *Definition) { d.Params[1].ExpiresField = "nope" }, "expiresField"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			d := testDefinition("https://api.example.com")
			tc.mutate(d)
			err := d.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过校验，实际: %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("期望错误包含 %q，实际: %v", tc.wantErr, err)
			}
		})
	}
}

func TestSkeletonAndExport(t *testing.T) {
	d := SkeletonDefinition("agent-up", "Agent 上传器")
	if err := d.Validate(); err != nil {
		t.Fatalf("骨架定义应可直接运行: %v", err)
	}
	raw, err := MarshalDefinition(d)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := ParseDefinition(raw)
	if err != nil {
		t.Fatalf("骨架定义往返失败: %v", err)
	}
	if parsed.Kind != "agent-up" || parsed.Request.Mode != "multipart" {
		t.Fatalf("往返字段丢失: %+v", parsed)
	}
	goSrc, err := ExportGo(parsed)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"package agentup", "func Descriptor()", "sourceJSON", "plugin.ParseDefinition"} {
		if !strings.Contains(goSrc, want) {
			t.Fatalf("导出的 Go 源码缺少 %q", want)
		}
	}
}

func TestDynamicUploaderMultipart(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("方法错误: %s", r.Method)
		}
		if got := r.Header.Get("X-Token"); got != "secret-123" {
			t.Errorf("模板头部未渲染: %q", got)
		}
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("解析 multipart 失败: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if got := r.FormValue("note"); got != "a.png" {
			t.Errorf("内置变量未渲染: %q", got)
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			t.Errorf("缺少文件字段: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		defer f.Close()
		content, _ := io.ReadAll(f)
		if string(content) != "PNGDATA" {
			t.Errorf("文件内容不符: %q", content)
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"OK","data":{"url":"https://cdn.example.com/a.png","objectKey":"attachments/a.png","fileInfo":{"fileName":"a.png","fileSize":7,"mimeType":"image/png"},"expireAt":"2026-12-01T00:00:00Z"}}`)
	}))
	defer upstream.Close()

	d := testDefinition(upstream.URL)
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	res, err := NewDynamicUploader(d).Upload(context.Background(), UploadRequest{
		FileName: "a.png", MimeType: "image/png", Size: 7,
		Reader: strings.NewReader("PNGDATA"), Bucket: "default", ObjectKey: "images/a.png",
	}, map[string]string{"base_url": upstream.URL, "token": "secret-123"})
	if err != nil {
		t.Fatalf("动态插件上传失败: %v", err)
	}
	if res.URL != "https://cdn.example.com/a.png" || res.ObjectKey != "attachments/a.png" {
		t.Fatalf("响应映射错误: %+v", res)
	}
	if res.FileSize != 7 || res.FileName != "a.png" || res.MimeType != "image/png" {
		t.Fatalf("文件信息映射错误: %+v", res)
	}
	if res.Provider != "mockup" || res.URLExpireAt == nil {
		t.Fatalf("Provider / 过期时间缺失: %+v", res)
	}
	if res.Extra["upstreamCode"] != "0" {
		t.Fatalf("业务码未回传: %+v", res.Extra)
	}
}

func TestDynamicUploaderErrors(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		status  int
		wantErr string
	}{
		{"业务失败", `{"code":1,"msg":"token expired"}`, 200, "token expired"},
		{"缺少 url", `{"code":0,"data":{}}`, 200, "缺少"},
		{"HTTP 错误", `{"oops":1}`, 500, "HTTP 500"},
		{"非法 JSON", `not-json`, 200, "不是合法 JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = io.WriteString(w, tc.payload)
			}))
			defer upstream.Close()
			d := testDefinition(upstream.URL)
			_, err := NewDynamicUploader(d).Upload(context.Background(), UploadRequest{
				FileName: "a.png", MimeType: "image/png", Size: 3,
				Reader: strings.NewReader("abc"),
			}, map[string]string{"base_url": upstream.URL, "token": "t"})
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("期望错误包含 %q，实际: %v", tc.wantErr, err)
			}
		})
	}
}

func TestDynamicUploaderCheck(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.WriteHeader(http.StatusNotFound)
	}))
	defer upstream.Close()

	d := testDefinition(upstream.URL)
	d.Request.CheckURL = "{{base_url}}/health"
	cfg := map[string]string{"base_url": upstream.URL, "token": "t"}
	if err := NewDynamicUploader(d).Check(context.Background(), cfg); err != nil {
		t.Fatalf("探活应成功: %v", err)
	}

	// 无 checkUrl 时退化为 TCP 连通性探测，不产生上传副作用。
	d.Request.CheckURL = ""
	if err := NewDynamicUploader(d).Check(context.Background(), cfg); err != nil {
		t.Fatalf("TCP 探活应成功: %v", err)
	}
	d.Request.URL = "http://127.0.0.1:1/upload"
	if err := NewDynamicUploader(d).Check(context.Background(), cfg); err == nil {
		t.Fatal("不可达地址应探活失败")
	}
}

func TestExtractPathAndToString(t *testing.T) {
	var payload any
	dec := json.NewDecoder(strings.NewReader(`{"data":{"items":[{"url":"u1"}],"n":2033070,"f":1.5}}`))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		path string
		want string
	}{
		{"data.items[0].url", "u1"},
		{"data.n", "2033070"}, // json.Number 不能用科学计数法输出
		{"data.f", "1.5"},
	}
	for _, tc := range cases {
		v, ok := extractPath(payload, tc.path)
		if !ok || toString(v) != tc.want {
			t.Errorf("路径 %s: got=%v ok=%v want=%s", tc.path, v, ok, tc.want)
		}
	}
	if _, ok := extractPath(payload, "data.items[9].url"); ok {
		t.Error("越界下标应返回 false")
	}
	if _, ok := extractPath(payload, "data.missing.deep"); ok {
		t.Error("缺失路径应返回 false")
	}
	if got := fmt.Sprint(toString(json.Number("2033070"))); got != "2033070" {
		t.Errorf("数字被科学计数法污染: %s", got)
	}
}

func TestValidateDefinitionPreview(t *testing.T) {
	d := testDefinition("https://api.example.com")
	raw, err := MarshalDefinition(d)
	if err != nil {
		t.Fatal(err)
	}
	result, err := ValidateDefinition(raw, map[string]string{
		"base_url": "https://api.example.com", "token": "super-secret",
	})
	if err != nil {
		t.Fatal(err)
	}
	if result["valid"] != true {
		t.Fatalf("应校验通过: %+v", result)
	}
	preview := result["requestPreview"].(map[string]any)
	headers := preview["headers"].(map[string]string)
	if headers["X-Token"] != "****" {
		t.Fatalf("敏感值未脱敏: %+v", headers)
	}
	if preview["url"] != "https://api.example.com/upload" {
		t.Fatalf("URL 预览错误: %+v", preview)
	}

	// 缺少必填参数应被标记但不应 panic
	result, err = ValidateDefinition(raw, map[string]string{"token": "t"})
	if err != nil {
		t.Fatal(err)
	}
	if result["valid"] != false {
		t.Fatalf("缺少必填参数应判定为无效: %+v", result)
	}
}
