// Package sdk 是 S3Bridge Go 插件的开发包。
//
// 插件是一个文件夹：plugin.json（元信息与参数 Schema）+ 任意多个 .go 文件。
// 服务端内嵌 Go 解释器直接执行源码，**不需要安装 Go 工具链**，保存即生效。
// 插件拥有完整的标准库能力：任意次 HTTP 请求、任意方法（PUT/DELETE/multipart）、
// 任意加解密与编码。
//
// 最小插件：
//
//	package main
//
//	import (
//		"context"
//
//		"github.com/Mr-losfer/s3bridge/sdk"
//	)
//
//	type plugin struct{}
//
//	func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
//		return &sdk.Output{URL: "https://example.com/a.png"}, nil
//	}
//
//	func (plugin) Check(ctx context.Context, cfg sdk.Config) error { return nil }
//
//	func main() { sdk.Serve(plugin{}.Upload, plugin{}.Check) }
//
// 入口约定：main 里调用 sdk.Serve 把两个函数交给宿主。之所以传函数而不是实例，
// 是因为解释器与宿主分属两套类型系统，函数值可以无损跨边界，接口值不行。
package sdk

import (
	"context"
	"sync"
	"time"
)

// Version 是插件入口约定版本。宿主加载时会检查，不匹配会提示迁移。
const Version = "3"

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

// UploadFunc 是上传实现的签名，插件方法可直接传入。
type UploadFunc func(ctx context.Context, in Input, cfg Config) (*Output, error)

// CheckFunc 是探活实现的签名。
type CheckFunc func(ctx context.Context, cfg Config) error

var (
	regMu     sync.RWMutex
	regUpload UploadFunc
	regCheck  CheckFunc
)

// Serve 注册插件实现。宿主在解释执行完 main 后通过 Handlers 取回。
func Serve(upload UploadFunc, check CheckFunc) {
	regMu.Lock()
	regUpload, regCheck = upload, check
	regMu.Unlock()
}

// Reset 清空已注册的实现。加载器在每次加载前调用，避免上一次加载残留的
// 函数被误当成新插件的入口（同一进程内连续校验多份源码时会遇到）。
func Reset() {
	regMu.Lock()
	regUpload, regCheck = nil, nil
	regMu.Unlock()
}

// Handlers 返回已注册的实现，供 runner 调用。
func Handlers() (UploadFunc, CheckFunc) {
	regMu.RLock()
	defer regMu.RUnlock()
	return regUpload, regCheck
}

// Request / Response 是宿主与 runner 子进程之间的通信协议（每行一条 JSON）。
type Request struct {
	ID          string            `json:"id"`
	Method      string            `json:"method"`
	Config      Config            `json:"config,omitempty"`
	Input       *Input            `json:"input,omitempty"`
	TimeoutSecs int               `json:"timeoutSecs,omitempty"`
	Extra       map[string]string `json:"extra,omitempty"`
}

type Response struct {
	ID      string            `json:"id"`
	OK      bool              `json:"ok"`
	Result  *Output           `json:"result,omitempty"`
	Error   string            `json:"error,omitempty"`
	Traces  []Trace           `json:"traces,omitempty"`
	Pong    bool              `json:"pong,omitempty"`
	Version string            `json:"version,omitempty"`
	Notes   []string          `json:"notes,omitempty"`
	Meta    map[string]string `json:"meta,omitempty"`
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

// Collector 收集单次调用内的出站请求追踪。
//
// 它通过 context 穿过解释器边界：Client 是宿主代码，读取的仍是同一个 ctx，
// 所以插件里调用 sdk 客户端也能把请求记进当前这次调用的报告。
type Collector struct {
	mu     sync.Mutex
	traces []Trace
}

// NewCollector 创建一次调用的追踪收集器。
func NewCollector() *Collector { return &Collector{} }

// Add 记录一条追踪，超过上限后丢弃。
func (c *Collector) Add(t Trace) {
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

// Traces 返回追踪快照。
func (c *Collector) Traces() []Trace {
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

// WithCollector 把收集器放进 context。
func WithCollector(ctx context.Context, c *Collector) context.Context {
	return context.WithValue(ctx, collectorKey{}, c)
}

// CollectorFrom 从 context 取出收集器，没有则返回 nil。
func CollectorFrom(ctx context.Context) *Collector {
	c, _ := ctx.Value(collectorKey{}).(*Collector)
	return c
}
