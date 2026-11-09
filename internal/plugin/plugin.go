// Package plugin 定义插件契约：清单(Manifest)、参数 Schema、统一上传结果，
// 以及内置插件类型的注册表。
//
// 一个「插件」= 一段把第三方上传接口转换成 S3Bridge 标准输出的适配器。
// 参数 Schema 是单一事实来源：前端动态表单、参数校验、MCP 参数修改、
// 插件开发文档全部由它生成，避免三处各写一遍而漂移。
package plugin

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sort"
	"sync"
	"time"
)

var (
	ErrUnknownKind    = errors.New("未注册的插件类型")
	ErrPluginNotFound = errors.New("插件实例不存在")
	ErrPluginDisabled = errors.New("插件已停用")
	ErrInvalidParams  = errors.New("插件参数校验失败")
	ErrUpstream       = errors.New("第三方接口返回异常")
)

// ParamField 描述一个插件配置项的渲染与校验规则。
type ParamField struct {
	Key          string        `json:"key"`
	Label        string        `json:"label"`
	Type         string        `json:"type"` // text|secret|number|bool|select|textarea|url|datetime|duration
	Group        string        `json:"group"`
	Placeholder  string        `json:"placeholder,omitempty"`
	Help         string        `json:"help,omitempty"`
	Required     bool          `json:"required"`
	Sensitive    bool          `json:"sensitive"`              // 前端脱敏 + 服务端加密存储
	Volatile     bool          `json:"volatile"`               // 临时参数（如会过期的 token）
	ExpiresField string        `json:"expiresField,omitempty"` // 关联的过期时间字段 key
	Default      string        `json:"default,omitempty"`
	Options      []ParamOption `json:"options,omitempty"`
	Pattern      string        `json:"pattern,omitempty"`
}

type ParamOption struct {
	Label string `json:"label"`
	Value string `json:"value"`
}

// HealthHint 是插件作者建议的默认健康检测策略。
type HealthHint struct {
	Enabled       bool   `json:"enabled"`
	IntervalSecs  int    `json:"intervalSecs"`
	TimeoutSecs   int    `json:"timeoutSecs"`
	FailThreshold int    `json:"failThreshold"`
	ProbeMode     string `json:"probeMode"`
}

// Manifest 是插件的静态描述，由代码定义、启动即注册。
type Manifest struct {
	Kind         string       `json:"kind"`
	Name         string       `json:"name"`
	Version      string       `json:"version"`
	Description  string       `json:"description"`
	Author       string       `json:"author"`
	Endpoint     string       `json:"endpoint"`
	Capabilities []string     `json:"capabilities"`
	Params       []ParamField `json:"params"`
	Health       HealthHint   `json:"health"`
	Docs         string       `json:"docs"`             // 该插件的接入说明（Markdown）
	SampleCode   string       `json:"sampleCode"`       // 插件开发文档中的示例代码
	Source       string       `json:"source,omitempty"` // 等价的声明式插件源码（内置插件可选提供，供 Agent 派生）
}

// UploadRequest 是统一后的上传输入。
type UploadRequest struct {
	FileName  string
	MimeType  string
	Size      int64
	Reader    io.Reader
	Bucket    string
	ObjectKey string
	UserID    string
}

// UploadResult 是统一后的标准输出 —— 无论接哪家第三方，调用方拿到同一结构。
type UploadResult struct {
	URL         string         `json:"url"`
	ObjectKey   string         `json:"objectKey"`
	FileName    string         `json:"fileName"`
	MimeType    string         `json:"mimeType"`
	FileSize    int64          `json:"fileSize"`
	Provider    string         `json:"provider"`
	URLExpireAt *time.Time     `json:"urlExpireAt,omitempty"`
	Extra       map[string]any `json:"extra,omitempty"`
}

// Uploader 是插件作者必须实现的两个方法。
type Uploader interface {
	// Upload 把请求转发给第三方并转换为 UploadResult。
	Upload(ctx context.Context, req UploadRequest, cfg map[string]string) (*UploadResult, error)
	// Check 做轻量探活，不应产生真实业务副作用（不上传真实文件）。
	Check(ctx context.Context, cfg map[string]string) error
}

// Descriptor 把清单与构造器绑定。
type Descriptor struct {
	Manifest Manifest
	New      func() Uploader
}

// Registry 并发安全地保存所有已编译进二进制的插件类型。
type Registry struct {
	mu    sync.RWMutex
	items map[string]Descriptor
}

func NewRegistry() *Registry {
	return &Registry{items: make(map[string]Descriptor)}
}

func (r *Registry) Register(d Descriptor) error {
	if d.Manifest.Kind == "" || d.New == nil {
		return errors.New("插件清单缺少 kind 或构造器")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, exists := r.items[d.Manifest.Kind]; exists {
		return fmt.Errorf("插件类型 %s 重复注册", d.Manifest.Kind)
	}
	r.items[d.Manifest.Kind] = d
	return nil
}

func (r *Registry) Get(kind string) (Descriptor, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()
	d, ok := r.items[kind]
	if !ok {
		return Descriptor{}, fmt.Errorf("%w: %s", ErrUnknownKind, kind)
	}
	return d, nil
}

func (r *Registry) List() []Manifest {
	r.mu.RLock()
	defer r.mu.RUnlock()
	out := make([]Manifest, 0, len(r.items))
	for _, d := range r.items {
		out = append(out, d.Manifest)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Kind < out[j].Kind })
	return out
}

// DefaultParams 提取清单中的默认值。
func DefaultParams(m Manifest) map[string]string {
	out := make(map[string]string, len(m.Params))
	for _, f := range m.Params {
		if f.Default != "" {
			out[f.Key] = f.Default
		}
	}
	return out
}

// FieldByKey 查找参数定义，供校验与脱敏使用。
func FieldByKey(m Manifest, key string) (ParamField, bool) {
	for _, f := range m.Params {
		if f.Key == key {
			return f, true
		}
	}
	return ParamField{}, false
}
