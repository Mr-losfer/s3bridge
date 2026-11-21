package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strings"
	"time"
)

// Client 是带追踪的 HTTP 客户端。插件的每一次出站请求都会记录到本次调用的
// 追踪列表，后台调试台与 plugin_debug 因此能看到「插件到底发了什么」。
//
// 直接用 http.DefaultClient 也能工作，但不会产生追踪。
type Client struct {
	hc *http.Client
}

// NewClient 创建带超时的客户端。
func NewClient(timeout time.Duration) *Client {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	return &Client{hc: &http.Client{Timeout: timeout}}
}

// NewClientFrom 复用已有的 http.Client（例如自定义 Transport）。
func NewClientFrom(hc *http.Client) *Client {
	if hc == nil {
		hc = http.DefaultClient
	}
	return &Client{hc: hc}
}

// HTTP 暴露底层客户端，便于需要完全控制（流式下载、重定向策略）的场景。
func (c *Client) HTTP() *http.Client { return c.hc }

// Do 执行请求并记录追踪。响应的 Body 被包装为计数读取器，
// 读完或关闭时补齐本次追踪的响应字节数与错误片段。
func (c *Client) Do(req *http.Request) (*http.Response, error) {
	col := collectorFrom(req.Context())
	info := traceInfo{
		at:     time.Now(),
		method: req.Method,
		url:    redactURL(req.URL),
		header: redactHeaders(req.Header),
		reqLen: req.ContentLength,
	}
	start := time.Now()
	resp, err := c.hc.Do(req)
	info.duration = time.Since(start)
	if err != nil {
		info.err = err.Error()
		col.add(info.trace(nil, nil))
		return nil, err
	}
	info.status = resp.StatusCode
	info.respHeader = redactHeaders(resp.Header)
	wrapped := &countingBody{
		ReadCloser: resp.Body,
		maxSnippet: 512,
		onDone: func(n int64, snippet string) {
			info.respLen = n
			info.snippet = snippet
			col.add(info.trace(nil, nil))
		},
	}
	resp.Body = wrapped
	return resp, nil
}

// Result 是便捷调用的返回值。
type Result struct {
	Status  int
	Header  http.Header
	Body    []byte
	Elapsed time.Duration
}

// OK 判断状态码是否 2xx。
func (r *Result) OK() bool { return r.Status >= 200 && r.Status < 300 }

// JSON 发送并解析 JSON。in 为 nil 表示无请求体；out 为 nil 表示不解析响应。
// 非 2xx 返回错误（错误信息包含状态码与响应片段），便于插件直接透出上游原因。
func (c *Client) JSON(ctx context.Context, method, rawURL string, headers map[string]string, in any, out any) (*Result, error) {
	var body []byte
	if in != nil {
		var err error
		body, err = json.Marshal(in)
		if err != nil {
			return nil, fmt.Errorf("序列化请求体: %w", err)
		}
	}
	res, err := c.Bytes(ctx, method, rawURL, headers, body)
	if err != nil {
		return res, err
	}
	if !res.OK() {
		return res, fmt.Errorf("上游返回 %d: %s", res.Status, snippet(res.Body, 300))
	}
	if out != nil && len(res.Body) > 0 {
		if err := json.Unmarshal(res.Body, out); err != nil {
			return res, fmt.Errorf("解析上游 JSON: %w (body=%s)", err, snippet(res.Body, 300))
		}
	}
	return res, nil
}

// Bytes 发送原始字节请求，读取完整响应（上限 32 MiB）。
func (c *Client) Bytes(ctx context.Context, method, rawURL string, headers map[string]string, body []byte) (*Result, error) {
	var rdr io.Reader
	if body != nil {
		rdr = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, rawURL, rdr)
	if err != nil {
		return nil, fmt.Errorf("构造请求: %w", err)
	}
	for k, v := range headers {
		req.Header.Set(k, v)
	}
	if body != nil && req.Header.Get("Content-Type") == "" {
		req.Header.Set("Content-Type", "application/json")
	}
	start := time.Now()
	resp, err := c.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, 32<<20))
	if err != nil {
		return nil, fmt.Errorf("读取响应: %w", err)
	}
	return &Result{Status: resp.StatusCode, Header: resp.Header, Body: data, Elapsed: time.Since(start)}, nil
}

// MultipartStream 构造流式 multipart 请求体，适合大文件上传：
// 文件内容不会被整体读入内存。
func MultipartStream(fileField, fileName, contentType string, file io.Reader, fields map[string]string) (io.Reader, string) {
	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	go func() {
		var err error
		defer func() { _ = pw.CloseWithError(err) }()
		for k, v := range fields {
			if err = mw.WriteField(k, v); err != nil {
				return
			}
		}
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name=%q; filename=%q`, fileField, fileName))
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		h.Set("Content-Type", contentType)
		part, err := mw.CreatePart(h)
		if err != nil {
			return
		}
		if _, err = io.Copy(part, file); err != nil {
			return
		}
		err = mw.Close()
	}()
	return pr, mw.FormDataContentType()
}

// ---------- 追踪 ----------

type traceInfo struct {
	at         time.Time
	method     string
	url        string
	status     int
	duration   time.Duration
	reqLen     int64
	respLen    int64
	header     map[string]string
	respHeader map[string]string
	snippet    string
	err        string
}

func (t traceInfo) trace(_ []byte, _ http.Header) Trace {
	return Trace{
		At: t.at, Method: t.method, URL: t.url, Status: t.status,
		DurationMs: float64(t.duration.Microseconds()) / 1000,
		ReqBytes:   max64(t.reqLen, 0), RespBytes: t.respLen,
		ReqHeaders: t.header, RespHeaders: t.respHeader,
		Snippet: t.snippet, Error: t.err,
	}
}

type countingBody struct {
	io.ReadCloser
	n          int64
	buf        []byte
	maxSnippet int
	done       bool
	onDone     func(int64, string)
}

func (b *countingBody) Read(p []byte) (int, error) {
	n, err := b.ReadCloser.Read(p)
	b.n += int64(n)
	if len(b.buf) < b.maxSnippet && n > 0 {
		room := b.maxSnippet - len(b.buf)
		if n < room {
			room = n
		}
		b.buf = append(b.buf, p[:room]...)
	}
	if err == io.EOF {
		b.finish()
	}
	return n, err
}

func (b *countingBody) Close() error {
	b.finish()
	return b.ReadCloser.Close()
}

func (b *countingBody) finish() {
	if b.done {
		return
	}
	b.done = true
	b.onDone(b.n, snippet(b.buf, b.maxSnippet))
}

var sensitiveHeaders = []string{"authorization", "cookie", "token", "secret", "api-key", "apikey", "x-api-key"}

func redactHeaders(h http.Header) map[string]string {
	if len(h) == 0 {
		return nil
	}
	out := make(map[string]string, len(h))
	for k, vs := range h {
		v := strings.Join(vs, ", ")
		if isSensitive(k) {
			v = "***"
		}
		out[k] = v
	}
	return out
}

func isSensitive(key string) bool {
	lk := strings.ToLower(key)
	for _, s := range sensitiveHeaders {
		if strings.Contains(lk, s) {
			return true
		}
	}
	return false
}

var sensitiveQuery = []string{"token", "signature", "q-sign", "x-amz-credential", "x-amz-signature", "key", "secret", "password"}

func redactURL(u *url.URL) string {
	if u == nil {
		return ""
	}
	cp := *u
	q := cp.Query()
	changed := false
	for k := range q {
		lk := strings.ToLower(k)
		for _, s := range sensitiveQuery {
			if strings.Contains(lk, s) {
				q.Set(k, "***")
				changed = true
				break
			}
		}
	}
	if changed {
		cp.RawQuery = q.Encode()
	}
	return cp.String()
}

func snippet(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		s = s[:n] + "…"
	}
	return s
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}
