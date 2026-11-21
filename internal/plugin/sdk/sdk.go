// Package sdk 是 S3Bridge Go 插件的开发包。
//
// 插件是独立的 Go 程序：一个文件夹内有 plugin.json（元信息与参数 Schema）
// 和任意多个 .go 文件，编译后由主服务以子进程方式调用。插件拥有完整 Go 能力：
// 任意次 HTTP 请求、任意方法（PUT/DELETE/multipart）、任意加解密与编码。
//
// 最小插件：
//
//	package main
//
//	import "github.com/Mr-losfer/s3bridge/sdk"
//
//	type plugin struct{}
//
//	func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
//		return &sdk.Output{URL: "https://example.com/a.png"}, nil
//	}
//
//	func (plugin) Check(ctx context.Context, cfg sdk.Config) error { return nil }
//
//	func main() { sdk.Serve(plugin{}) }
package sdk

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sync"
	"time"
)

// Version 是协议版本。主服务与 SDK 的大版本必须一致。
const Version = "2"

// Config 是后台配置的参数集合（敏感值已由主服务解密后传入）。
type Config map[string]string

// Input 是一次上传的输入。文件已落盘，插件直接按 Path 打开即可，
// 避免大文件在进程间以 base64 传输。
type Input struct {
	FileName  string `json:"fileName"`
	MimeType  string `json:"mimeType"`
	Size      int64  `json:"size"`
	Path      string `json:"path"`
	Bucket    string `json:"bucket"`
	ObjectKey string `json:"objectKey"`
	UserID    string `json:"userId"`
}

// Output 是统一标准输出。URLExpireAt 用于「临时签名链接」的到期提醒。
type Output struct {
	URL         string         `json:"url"`
	ObjectKey   string         `json:"objectKey"`
	FileName    string         `json:"fileName"`
	MimeType    string         `json:"mimeType"`
	FileSize    int64          `json:"fileSize"`
	URLExpireAt *time.Time     `json:"urlExpireAt,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// Uploader 是插件必须实现的接口。
type Uploader interface {
	// Upload 把文件转发到第三方并返回统一结果。
	Upload(ctx context.Context, in Input, cfg Config) (*Output, error)
	// Check 做无副作用探活（不得上传真实业务文件）。
	Check(ctx context.Context, cfg Config) error
}

// Request / Response 是子进程通信协议（每行一条 JSON）。
type Request struct {
	ID          string            `json:"id"`
	Method      string            `json:"method"`
	Config      Config            `json:"config,omitempty"`
	Input       *Input            `json:"input,omitempty"`
	TimeoutSecs int               `json:"timeoutSecs,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`
}

type Response struct {
	ID      string   `json:"id"`
	OK      bool     `json:"ok"`
	Result  *Output  `json:"result,omitempty"`
	Error   string   `json:"error,omitempty"`
	Traces  []Trace  `json:"traces,omitempty"`
	Pong    bool     `json:"pong,omitempty"`
	Version string   `json:"version,omitempty"`
	Notes   []string `json:"notes,omitempty"`
}

// Trace 记录一次出站请求，供后台调试台与 plugin_debug 展示。
// 敏感请求头（Authorization / Cookie / X-Api-Key 等）会被脱敏。
type Trace struct {
	At          time.Time         `json:"at"`
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Status      int               `json:"status"`
	DurationMs  float64           `json:"durationMs"`
	ReqBytes    int64             `json:"reqBytes"`
	RespBytes   int64             `json:"respBytes"`
	ReqHeaders  map[string]string `json:"reqHeaders,omitempty"`
	RespHeaders map[string]string `json:"respHeaders,omitempty"`
	Snippet     string            `json:"snippet,omitempty"`
	Error       string            `json:"error,omitempty"`
}

// MaxTraces 限制单次调用的追踪条数，防止死循环插件撑爆响应。
const MaxTraces = 64

// Serve 启动插件主循环：从 stdin 读请求、向 stdout 写响应。
// 日志务必写 stderr，stdout 只能输出协议消息，否则会破坏通信。
func Serve(u Uploader) {
	dec := json.NewDecoder(bufio.NewReaderSize(os.Stdin, 1<<20))
	enc := json.NewEncoder(os.Stdout)
	for {
		var req Request
		if err := dec.Decode(&req); err != nil {
			return // EOF 或协议错误：主服务会重启进程
		}
		resp := handle(u, req)
		if err := enc.Encode(resp); err != nil {
			return
		}
	}
}

func handle(u Uploader, req Request) (resp Response) {
	resp = Response{ID: req.ID, Version: Version}
	defer func() {
		if r := recover(); r != nil {
			resp.OK = false
			resp.Error = fmt.Sprintf("插件 panic: %v", r)
		}
	}()

	timeout := time.Duration(req.TimeoutSecs) * time.Second
	if timeout <= 0 || timeout > 10*time.Minute {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	col := &collector{}
	ctx = withCollector(ctx, col)

	switch req.Method {
	case "ping":
		resp.OK = true
		resp.Pong = true
		return resp
	case "upload":
		if req.Input == nil {
			resp.Error = "缺少 input"
			return resp
		}
		out, err := u.Upload(ctx, *req.Input, req.Config)
		if err != nil {
			resp.Error = err.Error()
			return resp
		}
		resp.OK = true
		resp.Result = out
	case "check":
		if err := u.Check(ctx, req.Config); err != nil {
			resp.Error = err.Error()
			return resp
		}
		resp.OK = true
	default:
		resp.Error = "未知方法: " + req.Method
	}
	resp.Traces = col.snapshot()
	return resp
}

// collector 收集单次调用内的出站请求追踪。
type collector struct {
	mu     sync.Mutex
	traces []Trace
}

func (c *collector) add(t Trace) {
	if c == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.traces) >= MaxTraces {
		return
	}
	c.traces = append(c.traces, t)
}

func (c *collector) snapshot() []Trace {
	if c == nil {
		return nil
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.traces) == 0 {
		return nil
	}
	out := make([]Trace, len(c.traces))
	copy(out, c.traces)
	return out
}

type collectorKey struct{}

func withCollector(ctx context.Context, c *collector) context.Context {
	return context.WithValue(ctx, collectorKey{}, c)
}

func collectorFrom(ctx context.Context) *collector {
	c, _ := ctx.Value(collectorKey{}).(*collector)
	return c
}
