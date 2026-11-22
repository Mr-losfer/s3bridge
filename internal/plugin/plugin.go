// Package plugin 管理 Go 插件：文件夹布局、加载、子进程调用与实例生命周期。
//
// 一个插件 = 一个文件夹：
//
//	{DataDir}/plugins/{id}/
//	├── plugin.json   # 元信息、参数 Schema、调度与健康配置
//	├── main.go       # 入口：sdk.Serve(plugin{}.Upload, plugin{}.Check)
//	└── *.go          # 任意多个源文件，可 import 整个标准库
//
// 源码由服务端内嵌的 Go 解释器直接执行（见 internal/plugin/engine），
// 不需要安装 Go 工具链，保存即生效；执行体是主程序自身派生的 runner 子进程。
// 插件拥有完整标准库能力：任意次 HTTP 请求、任意方法、任意加解密。
package plugin

import (
	"context"
	"errors"
	"io"
	"sort"
	"time"
)

var (
	ErrPluginNotFound = errors.New("插件实例不存在")
	ErrPluginDisabled = errors.New("插件已停用")
	ErrInvalidParams  = errors.New("插件参数校验失败")
	ErrUpstream       = errors.New("第三方接口返回异常")
	// ErrFileTypeUnsupported 表示显式指定的插件不承接当前文件类型。
	ErrFileTypeUnsupported = errors.New("插件不支持该文件类型")
	ErrSourceInvalid       = errors.New("插件源码不合法")
)

// ParamField 描述一个插件配置项的渲染与校验规则。
// 它仍然是 JSON 元数据：Go 代码负责逻辑，Schema 负责后台表单与校验。
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

// ValidityHint 控制该插件产出的图片参与有效性巡检的频率。
// IntervalHours 为 0 表示跟随全局设置（默认 72 小时）。
type ValidityHint struct {
	Enabled       *bool `json:"enabled,omitempty"`
	IntervalHours int   `json:"intervalHours,omitempty"`
}

// Manifest 是插件文件夹内 plugin.json 的内容。
type Manifest struct {
	Schema      int          `json:"schema"` // 固定为 2
	ID          string       `json:"id"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Description string       `json:"description"`
	Author      string       `json:"author"`
	Entry       string       `json:"entry,omitempty"` // 入口文件，默认 main.go
	FileTypes   []string     `json:"fileTypes"`       // 可承接的文件类型；空表示全部
	Priority    int          `json:"priority"`        // 默认调度权重
	TimeoutSecs int          `json:"timeoutSecs"`     // 单次调用超时，默认 60
	Health      HealthHint   `json:"health"`
	Validity    ValidityHint `json:"validity"`
	Params      []ParamField `json:"params"`
	Docs        string       `json:"docs,omitempty"`
}

// UploadRequest 是统一后的上传输入。
type UploadRequest struct {
	FileName  string
	MimeType  string
	Size      int64
	Reader    io.Reader
	Path      string // 已落盘的文件路径，插件直接打开，避免进程间传大文件
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

// Trace 是插件一次出站请求的记录（调试台展示用）。
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

// Template 是可从后台/MCP 派生的插件模板。
type Template struct {
	Kind        string            `json:"kind"`
	Name        string            `json:"name"`
	Description string            `json:"description"`
	FileTypes   []string          `json:"fileTypes"`
	Params      []ParamField      `json:"params"`
	Files       map[string]string `json:"files"` // 文件名 → 源码
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

// SortedGroups 按出现顺序对参数分组，供前端渲染。
func SortedGroups(m Manifest) []ParamGroup {
	var order []string
	idx := map[string]*ParamGroup{}
	for _, f := range m.Params {
		g := f.Group
		if g == "" {
			g = "通用"
		}
		grp := idx[g]
		if grp == nil {
			grp = &ParamGroup{Name: g}
			idx[g] = grp
			order = append(order, g)
		}
		grp.Fields = append(grp.Fields, f)
	}
	out := make([]ParamGroup, 0, len(order))
	for _, g := range order {
		out = append(out, *idx[g])
	}
	return out
}

type ParamGroup struct {
	Name   string       `json:"name"`
	Fields []ParamField `json:"fields"`
}

// SortManifests 按 ID 排序，保证列表稳定。
func SortManifests(list []Manifest) {
	sort.Slice(list, func(i, j int) bool { return list[i].ID < list[j].ID })
}

// EnsureContext 在缺少 deadline 时补一个默认超时，避免子进程调用悬挂。
func EnsureContext(ctx context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		d = 60 * time.Second
	}
	if _, ok := ctx.Deadline(); ok {
		return context.WithCancel(ctx)
	}
	return context.WithTimeout(ctx, d)
}
