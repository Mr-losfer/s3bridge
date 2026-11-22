package sdk

import (
	"context"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestServeAndHandlers 验证注册式入口：Serve 存、Handlers 取、Reset 清空。
func TestServeAndHandlers(t *testing.T) {
	Reset()
	if up, ck := Handlers(); up != nil || ck != nil {
		t.Fatal("初始状态应为空")
	}

	var got string
	up := func(ctx context.Context, in Input, cfg Config) (*Output, error) {
		got = in.FileName
		return &Output{URL: "https://cdn/x"}, nil
	}
	ck := func(ctx context.Context, cfg Config) error { return nil }
	Serve(up, ck)

	gotUp, gotCk := Handlers()
	if gotUp == nil || gotCk == nil {
		t.Fatal("Serve 后应能取回两个函数")
	}
	if _, err := gotUp(context.Background(), Input{FileName: "a.png"}, nil); err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if got != "a.png" {
		t.Fatalf("取回的函数不是注册的那个: %q", got)
	}

	Reset()
	if up, ck := Handlers(); up != nil || ck != nil {
		t.Fatal("Reset 后应清空")
	}
}

func TestCollectorLimit(t *testing.T) {
	c := NewCollector()
	for i := 0; i < MaxTraces+10; i++ {
		c.Add(Trace{Method: http.MethodGet, URL: "https://x"})
	}
	if n := len(c.Traces()); n != MaxTraces {
		t.Fatalf("追踪应截断到 %d，实际 %d", MaxTraces, n)
	}
	var nilCol *Collector
	nilCol.Add(Trace{}) // 不应 panic
	if nilCol.Traces() != nil {
		t.Fatal("nil 收集器应返回 nil")
	}
	if CollectorFrom(context.Background()) != nil {
		t.Fatal("没有收集器的 context 应返回 nil")
	}
}

// TestTraceCollectedAndRedacted 验证插件发起的每个请求都会进入追踪，且凭据被脱敏。
func TestTraceCollectedAndRedacted(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.Method+" "+r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer srv.Close()

	col := NewCollector()
	ctx := WithCollector(context.Background(), col)
	c := NewClient(5 * time.Second)

	// 第一次：带敏感头与敏感 query
	if _, err := c.Bytes(ctx, http.MethodPut, srv.URL+"/put?token=abc&key=k", map[string]string{
		"Authorization": "Bearer secret-token", "Content-Type": "text/plain",
	}, []byte("hi")); err != nil {
		t.Fatal(err)
	}
	// 第二次：JSON
	var out map[string]bool
	if _, err := c.JSON(ctx, http.MethodPost, srv.URL+"/json", nil, map[string]string{"a": "b"}, &out); err != nil {
		t.Fatal(err)
	}

	traces := col.Traces()
	if len(traces) != 2 {
		t.Fatalf("应记录 2 次请求，实际 %d: %+v", len(traces), traces)
	}
	first := traces[0]
	if first.Method != http.MethodPut || first.Status != http.StatusOK || first.ReqBytes != 2 {
		t.Fatalf("追踪字段不符: %+v", first)
	}
	if got := first.ReqHeaders["Authorization"]; got != "***" {
		t.Fatalf("Authorization 未脱敏: %q", got)
	}
	if strings.Contains(first.URL, "abc") || strings.Contains(first.URL, "key=k") {
		t.Fatalf("敏感 query 未脱敏: %s", first.URL)
	}
	if !strings.Contains(first.URL, "token=") {
		t.Fatalf("脱敏后应保留参数名: %s", first.URL)
	}
	if traces[1].Method != http.MethodPost {
		t.Fatalf("第二次请求未记录: %+v", traces[1])
	}
	if len(paths) != 2 {
		t.Fatalf("上游收到的请求数不符: %v", paths)
	}
}

// TestClientErrors 覆盖非 2xx、非法 URL 与 JSON 解析失败的错误信息。
func TestClientErrors(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/ok" {
			_, _ = io.WriteString(w, "not-json")
			return
		}
		w.WriteHeader(http.StatusTeapot)
		_, _ = io.WriteString(w, "nope")
	}))
	defer srv.Close()

	c := NewClient(time.Second)
	if _, err := c.JSON(context.Background(), http.MethodGet, srv.URL, nil, nil, nil); err == nil ||
		!strings.Contains(err.Error(), "418") {
		t.Fatalf("非 2xx 应报错并带状态码，实际 %v", err)
	}
	if _, err := c.Bytes(context.Background(), http.MethodGet, "://bad", nil, nil); err == nil {
		t.Fatal("非法 URL 应报错")
	}
	if _, err := c.JSON(context.Background(), http.MethodGet, srv.URL+"/ok", nil, nil, &struct{}{}); err == nil {
		t.Fatal("非 JSON 响应应解析失败")
	}
	// 未安装 collector 的 context 也必须安全工作。
	if _, err := NewClient(time.Second).Bytes(context.Background(), http.MethodGet, srv.URL, nil, nil); err != nil {
		t.Fatalf("无 collector 时不应报错: %v", err)
	}
}

// TestMultipartStream 验证流式表单：文件与普通字段都能被标准库解析。
func TestMultipartStream(t *testing.T) {
	body, contentType := MultipartStream("file", "照片 1.png", "image/png",
		strings.NewReader("PNGDATA"), map[string]string{"trace": "t-1"})

	mt, params, err := mime.ParseMediaType(contentType)
	if err != nil || mt != "multipart/form-data" {
		t.Fatalf("Content-Type 异常: %q %v", contentType, err)
	}
	mr := multipart.NewReader(body, params["boundary"])
	form, err := mr.ReadForm(1 << 20)
	if err != nil {
		t.Fatal(err)
	}
	defer form.RemoveAll()
	if got := form.Value["trace"]; len(got) != 1 || got[0] != "t-1" {
		t.Fatalf("普通字段丢失: %v", form.Value)
	}
	file, err := form.File["file"][0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	if header := form.File["file"][0]; header.Filename != "照片 1.png" {
		t.Fatalf("文件名未保留: %q", header.Filename)
	}
	raw, _ := io.ReadAll(file)
	if string(raw) != "PNGDATA" {
		t.Fatalf("文件内容不符: %q", raw)
	}
}
