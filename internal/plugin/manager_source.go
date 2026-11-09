package plugin

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// 本文件实现「Agent 通过 MCP 完全掌控插件开发」的核心能力：
// 创建插件实例、读取/上传/差量修改声明式源码、静态校验、导出、删除。
// 所有写操作都写入 ChangeLog，与后台按钮走同一套审计。

// CreateSpec 是创建插件的输入。
type CreateSpec struct {
	Kind        string
	Name        string
	Version     string
	Description string
	Author      string
	Definition  *Definition // 为空时生成可直接运行的骨架定义
	Params      map[string]string
	Enabled     bool
	Priority    *int     // 调度权重，留空默认 100
	FileTypes   []string // 可承接的文件类型，留空表示全部
}

// ValidateKind 校验自定义插件的类型标识。
func ValidateKind(kind string) error {
	if !kindRe.MatchString(kind) {
		return errors.New("kind 必须匹配 ^[a-z][a-z0-9_-]{1,63}$（小写字母开头，仅含小写字母/数字/下划线/连字符）")
	}
	return nil
}

// SkeletonDefinition 生成一份可直接运行的骨架源码，Agent 随后用
// plugin_code_upload / plugin_code_patch 把 URL、请求头与响应映射改成真实接口。
func SkeletonDefinition(kind, name string) *Definition {
	return &Definition{
		Kind:        kind,
		Name:        name,
		Version:     "1.0.0",
		Description: "由 MCP 创建的声明式插件，请用 plugin_code_upload 编写请求与响应映射",
		Author:      "S3Bridge MCP",
		Params: []ParamField{
			{Key: "base_url", Label: "服务地址", Type: "url", Group: "连接", Required: true, Default: "https://api.example.com"},
			{Key: "authorization", Label: "Authorization", Type: "secret", Group: "鉴权", Sensitive: true},
		},
		Request: RequestSpec{
			Method: "POST", URL: "{{base_url}}/upload", Mode: "multipart",
			FileField: "file", TimeoutSecs: 30,
			Headers: map[string]string{"Authorization": "{{authorization}}"},
		},
		Response: ResponseSpec{
			Format: "json", CodePath: "code", SuccessCode: "0", MessagePath: "msg",
			URLPath: "data.url", ObjectKeyPath: "data.objectKey",
			FileNamePath: "data.fileName", SizePath: "data.size", MimeTypePath: "data.mimeType",
		},
		Health: HealthHint{Enabled: true, IntervalSecs: 300, TimeoutSecs: 8, FailThreshold: 2, ProbeMode: "ping"},
	}
}

// Create 创建一个自定义插件实例并立即写入可运行源码。
func (m *Manager) Create(spec CreateSpec, actor string) (View, error) {
	kind := strings.ToLower(strings.TrimSpace(spec.Kind))
	if err := ValidateKind(kind); err != nil {
		return View{}, err
	}
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = kind
	}
	def := spec.Definition
	if def == nil {
		def = SkeletonDefinition(kind, name)
	}
	def.Kind = kind // 定义中的 kind 必须与实例 ID 一致，防止篡改
	if strings.TrimSpace(spec.Name) != "" {
		def.Name = name
	}
	if strings.TrimSpace(spec.Version) != "" {
		def.Version = spec.Version
	}
	if strings.TrimSpace(spec.Description) != "" {
		def.Description = spec.Description
	}
	if strings.TrimSpace(spec.Author) != "" {
		def.Author = spec.Author
	}
	if err := def.Validate(); err != nil {
		return View{}, err
	}
	canonical, err := MarshalDefinition(def)
	if err != nil {
		return View{}, err
	}
	mf := def.ToManifest()
	params := DefaultParams(mf)
	for k, v := range spec.Params {
		params[k] = v
	}
	sealed, err := SealParams(mf, params, m.seal)
	if err != nil {
		return View{}, err
	}
	priority := 100
	if spec.Priority != nil {
		priority = *spec.Priority
	}
	types, err := ParseFileTypes(spec.FileTypes)
	if err != nil {
		return View{}, err
	}
	if spec.FileTypes == nil && len(def.FileTypes) > 0 {
		types, err = ParseFileTypes(def.FileTypes)
		if err != nil {
			return View{}, err
		}
	}
	// 新建插件时必填参数尚未配置（随后用 plugin_params_set 补齐），
	// 因此这里不做全量必填校验，参数正确性由 plugin_debug / 真实上传时收口。

	now := store.NowUTC()
	err = m.st.Update(func(st *store.State) error {
		if store.PluginByID(st, kind) != nil {
			return fmt.Errorf("插件 %s 已存在", kind)
		}
		if _, err := m.registry.Get(kind); err == nil {
			return fmt.Errorf("kind %s 与内置插件类型冲突，请换一个 kind（可用 plugin_export 派生）", kind)
		}
		st.Plugins = append(st.Plugins, store.Plugin{
			ID: kind, Kind: kind, Name: def.Name, Description: def.Description,
			Version: def.Version, Author: def.Author,
			Enabled: spec.Enabled, Priority: priority, FileTypes: types, Params: sealed,
			Status: store.StatusUnknown, StatusMessage: "新插件已创建，等待健康检测",
			Health: healthConfig(def.Health), Revision: 1,
			Definition: string(canonical), Custom: true,
			CreatedAt: now, UpdatedAt: now,
		})
		st.Changes = append(st.Changes, newChange(kind, "create", actor, map[string]string{
			"kind": kind, "name": def.Name, "mode": def.Request.Mode,
			"url": def.Request.URL, "urlPath": def.Response.URLPath,
		}, "创建声明式插件"))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return m.Get(kind)
}

// SourceJSON 返回规范化后的插件源码。内置插件返回其等价声明式模板（只读参考）。
// 第二个返回值表示源码是否可编辑。
func (m *Manager) SourceJSON(id string) (string, bool, error) {
	var raw string
	custom := false
	err := m.st.View(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		custom = p.Custom
		if p.Custom {
			raw = p.Definition
			return nil
		}
		desc, err := m.registry.Get(p.Kind)
		if err != nil {
			return err
		}
		raw = desc.Manifest.Source
		return nil
	})
	if err != nil {
		return "", false, err
	}
	if strings.TrimSpace(raw) == "" {
		return "", custom, errors.New("该内置插件未提供等价声明式源码")
	}
	d, err := ParseDefinition([]byte(raw))
	if err != nil {
		return "", custom, err
	}
	b, err := MarshalDefinition(d)
	if err != nil {
		return "", custom, err
	}
	return string(b), custom, nil
}

// SetDefinition 用一份完整源码替换自定义插件的源码（不做差量）。
func (m *Manager) SetDefinition(id string, raw []byte, expected *int64, actor string) (View, error) {
	return m.applyDefinition(id, raw, expected, actor, "code")
}

// PatchDefinition 对源码做字段级差量修改，支持点号路径与 params 数组按 key 合并。
func (m *Manager) PatchDefinition(id string, patch map[string]any, expected *int64, actor string) (View, error) {
	src, custom, err := m.SourceJSON(id)
	if err != nil {
		return View{}, err
	}
	if !custom {
		return View{}, errors.New("内置插件源码只读；请用 plugin_export 导出后经 plugin_create 派生自定义插件再修改")
	}
	var base map[string]any
	if err := json.Unmarshal([]byte(src), &base); err != nil {
		return View{}, fmt.Errorf("解析现有源码: %w", err)
	}
	paths := []string{}
	mergeDefinitionMap(base, expandDotted(patch), "", &paths)
	if len(paths) == 0 {
		return View{}, errors.New("patch 为空，没有需要修改的字段")
	}
	merged, err := json.Marshal(base)
	if err != nil {
		return View{}, err
	}
	v, err := m.applyDefinition(id, merged, expected, actor, "code_patch")
	if err != nil {
		return View{}, err
	}
	return v, nil
}

func (m *Manager) applyDefinition(id string, raw []byte, expected *int64, actor, action string) (View, error) {
	d, err := ParseDefinition(raw)
	if err != nil {
		return View{}, err
	}
	if d.Kind != id {
		return View{}, fmt.Errorf("定义中的 kind %q 必须与插件 ID %q 一致", d.Kind, id)
	}
	canonical, err := MarshalDefinition(d)
	if err != nil {
		return View{}, err
	}
	mf := d.ToManifest()
	types, err := ParseFileTypes(d.FileTypes)
	if err != nil {
		return View{}, err
	}
	err = m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		if !p.Custom {
			return errors.New("内置插件由 Go 代码实现，不可直接改源码；请用 plugin_export 派生后再 plugin_create")
		}
		if err := checkRevision(p, expected); err != nil {
			return err
		}
		oldLen := len(p.Definition)
		p.Name = d.Name
		p.Version = d.Version
		p.Description = d.Description
		p.Author = d.Author
		p.FileTypes = types
		p.Health = healthConfig(d.Health)
		if p.Params == nil {
			p.Params = map[string]string{}
		}
		for k, v := range DefaultParams(mf) {
			if _, exists := p.Params[k]; !exists {
				p.Params[k] = v // 补齐新增参数默认值，保留已有配置
			}
		}
		p.Definition = string(canonical)
		p.Status = store.StatusUnknown
		p.StatusMessage = "插件源码已更新，等待健康检测"
		p.FailCount = 0
		p.Revision++
		p.UpdatedAt = store.NowUTC()
		message := "上传插件源码"
		if action == "code_patch" {
			message = "差量修改插件源码"
		}
		st.Changes = append(st.Changes, newChange(id, action, actor, map[string]string{
			"source":  fmt.Sprintf("%d → %d bytes", oldLen, len(canonical)),
			"version": d.Version, "mode": d.Request.Mode,
			"url": d.Request.URL, "urlPath": d.Response.URLPath,
		}, message))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return m.Get(id)
}

// Delete 删除自定义插件；内置插件只能停用，不可删除。
func (m *Manager) Delete(id, actor string) error {
	return m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		if !p.Custom {
			return errors.New("内置插件不能删除，可用 plugin_toggle 停用；如需改写请用 plugin_export 派生")
		}
		for i := range st.Plugins {
			if st.Plugins[i].ID == id {
				st.Plugins = append(st.Plugins[:i], st.Plugins[i+1:]...)
				break
			}
		}
		kept := st.Alerts[:0]
		for _, a := range st.Alerts {
			if a.PluginID != id {
				kept = append(kept, a)
			}
		}
		st.Alerts = kept
		st.Changes = append(st.Changes, newChange(id, "delete", actor,
			map[string]string{"name": p.Name}, "删除自定义插件"))
		return nil
	})
}

// ValidateDefinition 只做静态校验与请求预览，不落库，供 Agent 试写后自检。
func ValidateDefinition(raw []byte, params map[string]string) (map[string]any, error) {
	d, err := ParseDefinition(raw)
	if err != nil {
		return nil, err
	}
	mf := d.ToManifest()
	missing := []string{}
	for _, f := range d.Params {
		if f.Required && strings.TrimSpace(params[f.Key]) == "" {
			missing = append(missing, f.Key)
		}
	}
	var paramErr string
	if err := ValidateParams(mf, params); err != nil {
		paramErr = err.Error()
	}
	// 预览时把所有 sensitive 参数遮蔽，避免把密钥回显到 MCP 响应/日志。
	vars := templateVars(params, UploadRequest{
		FileName: "sample.png", MimeType: "image/png", Size: 1024,
		Bucket: "default", ObjectKey: "images/sample.png",
	})
	for _, f := range d.Params {
		if f.Sensitive && vars[f.Key] != "" {
			vars[f.Key] = "****"
		}
	}
	renderedURL, _ := renderTemplate(d.Request.URL, vars)
	headers := map[string]string{}
	for k, v := range d.Request.Headers {
		rendered, _ := renderTemplate(v, vars)
		headers[k] = rendered
	}
	fields := map[string]string{}
	for k, v := range d.Request.Fields {
		rendered, _ := renderTemplate(v, vars)
		fields[k] = rendered
	}
	return map[string]any{
		"valid":           paramErr == "" && len(missing) == 0,
		"kind":            d.Kind,
		"name":            d.Name,
		"version":         d.Version,
		"paramsCount":     len(d.Params),
		"missingRequired": missing,
		"paramsError":     paramErr,
		"requestPreview": map[string]any{
			"method": d.Request.Method, "url": renderedURL, "mode": d.Request.Mode,
			"fileField": d.Request.FileField, "headers": headers, "fields": fields,
		},
		"responseMap": map[string]any{
			"codePath": d.Response.CodePath, "successCode": d.Response.SuccessCode,
			"urlPath": d.Response.URLPath, "objectKeyPath": d.Response.ObjectKeyPath,
			"fileNamePath": d.Response.FileNamePath, "sizePath": d.Response.SizePath,
			"mimeTypePath": d.Response.MimeTypePath,
		},
	}, nil
}

// ExportGo 把声明式源码导出成可内置化的 Go 插件文件源码。
func ExportGo(d *Definition) (string, error) {
	raw, err := MarshalDefinition(d)
	if err != nil {
		return "", err
	}
	pkg := strings.ReplaceAll(d.Kind, "-", "")
	return fmt.Sprintf(`// Code generated by S3Bridge plugin_export (format=go).
// 用法：把本文件保存为 internal/plugins/%s/plugin.go，
// 并在 internal/app/core.go 中注册 Descriptor()，即可把该插件内置进二进制。
package %s

import "github.com/Mr-losfer/s3bridge/internal/plugin"

const sourceJSON = %q

// Descriptor 由声明式源码即时编译得到，与 MCP 动态插件行为完全一致。
func Descriptor() plugin.Descriptor {
	def, err := plugin.ParseDefinition([]byte(sourceJSON))
	if err != nil {
		panic("invalid plugin source: " + err.Error())
	}
	return plugin.DescriptorFromDefinition(def)
}
`, pkg, pkg, string(raw)), nil
}

// ---------- helpers ----------

func healthConfig(h HealthHint) store.HealthConfig {
	every := h.IntervalSecs
	if every < 10 {
		every = 300
	}
	timeout := h.TimeoutSecs
	if timeout < 1 || timeout > 120 {
		timeout = 8
	}
	fail := h.FailThreshold
	if fail < 1 {
		fail = 2
	}
	mode := h.ProbeMode
	if mode == "" {
		mode = "ping"
	}
	return store.HealthConfig{
		Enabled: h.Enabled, IntervalSecs: every, TimeoutSecs: timeout,
		FailThreshold: fail, ProbeMode: mode,
	}
}

// expandDotted 把 {"request.headers.Authorization": "x"} 展开成嵌套对象，
// 让 Agent 可以用点号路径做精确差量修改。
func expandDotted(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for k, v := range in {
		if !strings.Contains(k, ".") {
			out[k] = v
			continue
		}
		parts := strings.Split(k, ".")
		cur := out
		for i, seg := range parts {
			if i == len(parts)-1 {
				cur[seg] = v
				break
			}
			next, ok := cur[seg].(map[string]any)
			if !ok {
				next = map[string]any{}
				cur[seg] = next
			}
			cur = next
		}
	}
	return out
}

func mergeDefinitionMap(base, patch map[string]any, prefix string, paths *[]string) {
	for k, v := range patch {
		path := k
		if prefix != "" {
			path = prefix + "." + k
		}
		if v == nil {
			delete(base, k)
			*paths = append(*paths, path+" (删除)")
			continue
		}
		if k == "params" {
			if arr, ok := v.([]any); ok {
				base[k] = mergeParamsArray(base[k], arr, path, paths)
				continue
			}
		}
		if pm, ok := v.(map[string]any); ok {
			if bm, ok := base[k].(map[string]any); ok {
				mergeDefinitionMap(bm, pm, path, paths)
				continue
			}
			fresh := map[string]any{}
			mergeDefinitionMap(fresh, pm, path, paths)
			base[k] = fresh
			continue
		}
		base[k] = v
		*paths = append(*paths, path)
	}
}

// mergeParamsArray 按 key 对参数 Schema 做 upsert；元素中带 _delete:true 表示删除。
func mergeParamsArray(base any, patch []any, prefix string, paths *[]string) []any {
	out := []any{}
	if arr, ok := base.([]any); ok {
		out = append(out, arr...)
	}
	for _, item := range patch {
		m, ok := item.(map[string]any)
		if !ok {
			continue
		}
		key, _ := m["key"].(string)
		if key == "" {
			continue
		}
		del, _ := m["_delete"].(bool)
		idx := -1
		for i, existing := range out {
			if em, ok := existing.(map[string]any); ok && em["key"] == key {
				idx = i
				break
			}
		}
		if del {
			if idx >= 0 {
				out = append(out[:idx], out[idx+1:]...)
			}
			*paths = append(*paths, prefix+"."+key+" (删除)")
			continue
		}
		if idx >= 0 {
			em := out[idx].(map[string]any)
			for kk, vv := range m {
				if kk != "_delete" {
					em[kk] = vv
				}
			}
			*paths = append(*paths, prefix+"."+key+" (更新)")
		} else {
			delete(m, "_delete")
			out = append(out, m)
			*paths = append(*paths, prefix+"."+key+" (新增)")
		}
	}
	return out
}
