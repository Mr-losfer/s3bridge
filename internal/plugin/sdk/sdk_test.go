package sdk

import (
	"context"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type fakeUploader struct {
	out  *Output
	err  error
	call func(ctx context.Context, cfg Config) error
	boom bool
}

func (f fakeUploader) Upload(ctx context.Context, in Input, cfg Config) (*Output, error) {
	if f.boom {
		panic("插件内部炸了")
	}
	if f.err != nil {
		return nil, f.err
	}
	if f.call != nil {
		if err := f.call(ctx, cfg); err != nil {
			return nil, err
		}
	}
	return f.out, nil
}

func (f fakeUploader) Check(ctx context.Context, cfg Config) error {
	if f.call != nil {
		return f.call(ctx, cfg)
	}
	return f.err
}

// TestHandleProtocol 覆盖子进程协议：上传、自检、未知方法、panic 兜底与缺参。
func TestHandleProtocol(t *testing.T) {
	out := &Output{URL: "https://cdn.example.com/a.png", FileName: "a.png", FileSize: 7}

	cases := []struct {
		name     string
		req      Request
		up       Uploader
		wantOK   bool
		wantErr  string
		wantURL  string
		wantPong bool
	}{
		{
			name:    "上传成功",
			req:     Request{ID: "1", Method: "upload", Input: &Input{FileName: "a.png", Size: 7}},
			up:      fakeUploader{out: out},
			wantOK:  true,
			wantURL: out.URL,
		},
		{
			name:    "上传缺 input",
			req:     Request{ID: "2", Method: "upload"},
			up:      fakeUploader{out: out},
			wantErr: "缺少 input",
		},
		{
			name:    "上传报错透出原因",
			req:     Request{ID: "3", Method: "upload", Input: &Input{}},
			up:      fakeUploader{err: errors.New("上游 401")},
			wantErr: "上游 401",
		},
		{
			name:    "插件 panic 不拖垮进程",
			req:     Request{ID: "4", Method: "upload", Input: &Input{}},
			up:      fakeUploader{boom: true},
			wantErr: "panic",
		},
		{
			name:   "自检成功",
			req:    Request{ID: "5", Method: "check"},
			up:     fakeUploader{},
			wantOK: true,
		},
		{
			name:    "自检失败",
			req:     Request{ID: "6", Method: "check"},
			up:      fakeUploader{err: errors.New("探活失败")},
			wantErr: "探活失败",
		},
		{
			name:   "ping 握手",
			req:    Request{ID: "7", Method: "ping"},
			up:     fakeUploader{},
			wantOK: true,
			// ping 不需要业务实现，只回 pong
			wantPong: true,
		},
		{
			name:    "未知方法",
			req:     Request{ID: "8", Method: "drop-table"},
			up:      fakeUploader{},
			wantErr: "未知方法",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := handle(tc.up, tc.req)
			if resp.ID != tc.req.ID || resp.Version != Version {
				t.Fatalf("响应头不符: %+v", resp)
			}
			if resp.OK != tc.wantOK {
				t.Fatalf("OK=%v want=%v (%s)", resp.OK, tc.wantOK, resp.Error)
			}
			if tc.wantErr != "" && !strings.Contains(resp.Error, tc.wantErr) {
				t.Fatalf("错误信息 %q 未包含 %q", resp.Error, tc.wantErr)
			}
			if tc.wantURL != "" && (resp.Result == nil || resp.Result.URL != tc.wantURL) {
				t.Fatalf("结果不符: %+v", resp.Result)
			}
			if resp.Pong != tc.wantPong {
				t.Fatalf("pong=%v want=%v", resp.Pong, tc.wantPong)
			}
		})
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

	up := fakeUploader{out: &Output{URL: "u"}, call: func(ctx context.Context, cfg Config) error {
		c := NewClient(5 * time.Second)
		// 第一次：带敏感头
		if _, err := c.Bytes(ctx, http.MethodPut, srv.URL+"/put?token=abc&key=k", map[string]string{
			"Authorization": "Bearer secret-token", "Content-Type": "text/plain",
		}, []byte("hi")); err != nil {
			return err
		}
		// 第二次：JSON
		var out map[string]bool
		_, err := c.JSON(ctx, http.MethodPost, srv.URL+"/json", nil, map[string]string{"a": "b"}, &out)
		return err
	}}
	resp := handle(up, Request{ID: "t", Method: "upload", Input: &Input{FileName: "a.png"}, TimeoutSecs: 5})
	if !resp.OK {
		t.Fatalf("调用失败: %s", resp.Error)
	}
	if len(resp.Traces) != 2 {
		t.Fatalf("应记录 2 次请求，实际 %d: %+v", len(resp.Traces), resp.Traces)
	}
	first := resp.Traces[0]
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
	if resp.Traces[1].Method != http.MethodPost {
		t.Fatalf("第二次请求未记录: %+v", resp.Traces[1])
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
