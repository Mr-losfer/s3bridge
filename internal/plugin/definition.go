package plugin

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"net/url"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

// 声明式插件源码（Plugin Source）是可运行时加载的插件定义：
// 一份 JSON 描述「参数 Schema + 第三方 HTTP 请求模板 + 响应字段映射」，
// 服务端即时编译成 Uploader，Agent 通过 MCP 创建/上传/差量修改后立即生效，
// 无需重新编译二进制，也不依赖 Go 工具链或动态链接库。
//
// 内置插件仍以 Go 代码实现，但可在 Manifest.Source 中提供等价定义，
// 供 Agent 阅读、派生和导出。

const maxDefinitionBytes = 1 << 20 // 单份插件定义上限 1 MiB

// Definition 是一份完整的插件源码。
type Definition struct {
	Kind        string       `json:"kind"`
	Name        string       `json:"name"`
	Version     string       `json:"version"`
	Description string       `json:"description,omitempty"`
	Author      string       `json:"author,omitempty"`
	Endpoint    string       `json:"endpoint,omitempty"`
	Docs        string       `json:"docs,omitempty"`
	FileTypes   []string     `json:"fileTypes,omitempty"` // 可承接的文件类型，留空表示全部
	Params      []ParamField `json:"params"`
	Request     RequestSpec  `json:"request"`
	Response    ResponseSpec `json:"response"`
	Health      HealthHint   `json:"health,omitempty"`
}

// RequestSpec 描述如何把统一上传请求转换成第三方 HTTP 请求。
// 字符串值支持 {{param_key}} 模板变量，以及 file_name / mime_type / bucket /
// object_key / file_size / timestamp / unix 等内置变量。
type RequestSpec struct {
	Method      string            `json:"method"`
	URL         string            `json:"url"`
	Headers     map[string]string `json:"headers,omitempty"`
	Mode        string            `json:"mode"` // multipart | json | form | raw
	FileField   string            `json:"fileField,omitempty"`
	Fields      map[string]string `json:"fields,omitempty"`
	RawBody     string            `json:"rawBody,omitempty"`
	CheckURL    string            `json:"checkUrl,omitempty"`
	TimeoutSecs int               `json:"timeoutSecs,omitempty"`
}

// ResponseSpec 描述如何把第三方 JSON 响应映射成 UploadResult。
// 路径支持点号与数组下标，如 data.fileInfo.fileName / data.items[0].url。
type ResponseSpec struct {
	Format        string `json:"format,omitempty"` // 目前仅支持 json
	CodePath      string `json:"codePath,omitempty"`
	SuccessCode   string `json:"successCode,omitempty"`
	MessagePath   string `json:"messagePath,omitempty"`
	URLPath       string `json:"urlPath"`
	ObjectKeyPath string `json:"objectKeyPath,omitempty"`
	FileNamePath  string `json:"fileNamePath,omitempty"`
	SizePath      string `json:"sizePath,omitempty"`
	MimeTypePath  string `json:"mimeTypePath,omitempty"`
	ExpirePath    string `json:"expirePath,omitempty"`
}

var (
	kindRe     = regexp.MustCompile(`^[a-z][a-z0-9_-]{1,63}$`)
	keyRe      = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]*$`)
	varRe      = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)
	paramTypes = map[string]bool{
		"text": true, "secret": true, "number": true, "bool": true,
		"select": true, "textarea": true, "url": true, "datetime": true, "duration": true,
	}
)

// builtinVars 是模板中无需在 params 声明即可使用的变量。
var builtinVars = map[string]bool{
	"file_name": true, "mime_type": true, "bucket": true,
	"object_key": true, "file_size": true, "timestamp": true, "unix": true,
}

// ParseDefinition 解析并校验一份插件源码。
func ParseDefinition(raw []byte) (*Definition, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, errors.New("插件定义为空")
	}
	if len(raw) > maxDefinitionBytes {
		return nil, fmt.Errorf("插件定义超过 %d 字节上限", maxDefinitionBytes)
	}
	var d Definition
	dec := json.NewDecoder(bytes.NewReader(raw))
	if err := dec.Decode(&d); err != nil {
		return nil, fmt.Errorf("解析插件定义: %w", err)
	}
	if err := d.Validate(); err != nil {
		return nil, err
	}
	return &d, nil
}

// MarshalDefinition 输出规范化的可读源码。
func MarshalDefinition(d *Definition) ([]byte, error) {
	return json.MarshalIndent(d, "", "  ")
}

// Validate 做静态校验：结构、参数 Schema、HTTP 安全约束与模板变量引用。
func (d *Definition) Validate() error {
	if !kindRe.MatchString(d.Kind) {
		return errors.New("kind 必须匹配 ^[a-z][a-z0-9_-]{1,63}$（小写字母开头，仅含小写字母/数字/下划线/连字符）")
	}
	if strings.TrimSpace(d.Name) == "" {
		return errors.New("name 不能为空")
	}
	if strings.TrimSpace(d.Version) == "" {
		d.Version = "1.0.0"
	}
	if d.Author == "" {
		d.Author = "S3Bridge MCP"
	}
	types, err := ParseFileTypes(d.FileTypes)
	if err != nil {
		return err
	}
	d.FileTypes = FileTypeStrings(types)
	if len(types) == 0 {
		d.FileTypes = nil
	}
	if err := d.validateParams(); err != nil {
		return err
	}
	if err := d.validateRequest(); err != nil {
		return err
	}
	if err := d.validateResponse(); err != nil {
		return err
	}
	return nil
}

func (d *Definition) validateParams() error {
	seen := map[string]bool{}
	for i := range d.Params {
		f := &d.Params[i]
		if !keyRe.MatchString(f.Key) {
			return fmt.Errorf("参数 key %q 非法（需字母开头，仅含字母/数字/下划线）", f.Key)
		}
		if seen[f.Key] {
			return fmt.Errorf("参数 key %q 重复", f.Key)
		}
		seen[f.Key] = true
		if !paramTypes[f.Type] {
			return fmt.Errorf("参数 %q 的 type %q 不受支持", f.Key, f.Type)
		}
		if f.Label == "" {
			f.Label = f.Key
		}
		if f.Group == "" {
			f.Group = "通用"
		}
		if f.ExpiresField != "" && !seen[f.ExpiresField] {
			// expiresField 允许引用后面才声明的参数，最后统一复核。
		}
	}
	for i := range d.Params {
		f := &d.Params[i]
		if f.ExpiresField != "" && !seen[f.ExpiresField] {
			return fmt.Errorf("参数 %q 的 expiresField %q 不存在", f.Key, f.ExpiresField)
		}
	}
	return nil
}

func (d *Definition) validateRequest() error {
	r := &d.Request
	r.Method = strings.ToUpper(strings.TrimSpace(r.Method))
	if r.Method == "" {
		r.Method = http.MethodPost
	}
	switch r.Method {
	case http.MethodGet, http.MethodPost, http.MethodPut, http.MethodPatch:
	default:
		return fmt.Errorf("request.method %q 不受支持", r.Method)
	}
	if strings.TrimSpace(r.URL) == "" {
		return errors.New("request.url 不能为空")
	}
	switch r.Mode {
	case "":
		r.Mode = "multipart"
	case "multipart", "json", "form", "raw":
	default:
		return fmt.Errorf("request.mode %q 不受支持（multipart/json/form/raw）", r.Mode)
	}
	if r.Mode == "multipart" {
		if strings.TrimSpace(r.FileField) == "" {
			return errors.New("multipart 模式必须指定 request.fileField")
		}
		if err := checkTemplateVars(r.FileField, d.Params); err != nil {
			return fmt.Errorf("request.fileField: %w", err)
		}
	}
	if r.TimeoutSecs == 0 {
		r.TimeoutSecs = 30
	}
	if r.TimeoutSecs < 1 || r.TimeoutSecs > 120 {
		return errors.New("request.timeoutSecs 需在 1~120 之间")
	}
	for k, v := range r.Headers {
		if err := validateHeader(k, v); err != nil {
			return fmt.Errorf("request.headers.%s: %w", k, err)
		}
	}
	if err := checkTemplateVars(r.URL, d.Params); err != nil {
		return fmt.Errorf("request.url: %w", err)
	}
	if err := checkTemplateVars(r.CheckURL, d.Params); err != nil {
		return fmt.Errorf("request.checkUrl: %w", err)
	}
	if err := checkTemplateVars(r.RawBody, d.Params); err != nil {
		return fmt.Errorf("request.rawBody: %w", err)
	}
	for k, v := range r.Headers {
		if err := checkTemplateVars(v, d.Params); err != nil {
			return fmt.Errorf("request.headers.%s: %w", k, err)
		}
	}
	for k, v := range r.Fields {
		if err := checkTemplateVars(v, d.Params); err != nil {
			return fmt.Errorf("request.fields.%s: %w", k, err)
		}
	}
	return nil
}

func (d *Definition) validateResponse() error {
	r := &d.Response
	if r.Format == "" {
		r.Format = "json"
	}
	if r.Format != "json" {
		return fmt.Errorf("response.format 目前仅支持 json，收到 %q", r.Format)
	}
	if strings.TrimSpace(r.URLPath) == "" {
		return errors.New("response.urlPath 不能为空（第三方返回的下载地址路径）")
	}
	return nil
}

// checkTemplateVars 确保所有 {{变量}} 都在 params 或内置变量中声明，避免运行时静默失败。
func checkTemplateVars(s string, params []ParamField) error {
	declared := map[string]bool{}
	for _, f := range params {
		declared[f.Key] = true
	}
	for _, m := range varRe.FindAllStringSubmatch(s, -1) {
		name := m[1]
		if !declared[name] && !builtinVars[name] {
			return fmt.Errorf("模板变量 {{%s}} 未在 params 中声明，也不是内置变量", name)
		}
	}
	return nil
}

func validateHeader(name, value string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("请求头名称不能为空")
	}
	if strings.ContainsAny(name, "\r\n: ") {
		return errors.New("请求头名称含非法字符")
	}
	if strings.ContainsAny(value, "\r\n") {
		return errors.New("请求头值不能包含换行（防头部注入）")
	}
	return nil
}

// ToManifest 把定义转换成驱动动态表单、参数校验与文档展示的清单。
func (d *Definition) ToManifest() Manifest {
	h := d.Health
	if h.IntervalSecs == 0 {
		h = HealthHint{Enabled: true, IntervalSecs: 300, TimeoutSecs: 8, FailThreshold: 2, ProbeMode: "ping"}
	}
	endpoint := d.Endpoint
	if endpoint == "" {
		endpoint = d.Request.Method + " " + d.Request.URL
	}
	return Manifest{
		Kind: d.Kind, Name: d.Name, Version: d.Version,
		Description: d.Description, Author: d.Author, Endpoint: endpoint,
		Capabilities: []string{"upload", "dynamic"},
		Params:       d.Params, Health: h, Docs: d.Docs, FileTypes: d.FileTypes,
		SampleCode: fmt.Sprintf("// 由声明式插件源码生成，无需 Go 代码。\n// request: %s %s (mode=%s)\n// response: %s -> UploadResult.URL\n",
			d.Request.Method, d.Request.URL, d.Request.Mode, d.Response.URLPath),
	}
}

// DescriptorFromDefinition 把定义编译成注册中心可直接使用的描述符。
func DescriptorFromDefinition(d *Definition) Descriptor {
	return Descriptor{
		Manifest: d.ToManifest(),
		New:      func() Uploader { return NewDynamicUploader(d) },
	}
}

// ---------- 动态执行器 ----------

type dynamicUploader struct{ def *Definition }

// NewDynamicUploader 由定义构造一个无状态、可并发复用的 Uploader。
func NewDynamicUploader(d *Definition) Uploader { return &dynamicUploader{def: d} }

func templateVars(cfg map[string]string, req UploadRequest) map[string]string {
	vars := make(map[string]string, len(cfg)+8)
	for k, v := range cfg {
		vars[k] = v
	}
	vars["file_name"] = req.FileName
	vars["mime_type"] = req.MimeType
	vars["bucket"] = req.Bucket
	vars["object_key"] = req.ObjectKey
	vars["file_size"] = strconv.FormatInt(req.Size, 10)
	vars["timestamp"] = time.Now().UTC().Format(time.RFC3339)
	vars["unix"] = strconv.FormatInt(time.Now().Unix(), 10)
	return vars
}

func renderTemplate(s string, vars map[string]string) (string, []string) {
	var missing []string
	out := varRe.ReplaceAllStringFunc(s, func(m string) string {
		name := varRe.FindStringSubmatch(m)[1]
		if v, ok := vars[name]; ok {
			return v
		}
		missing = append(missing, name)
		return ""
	})
	return out, missing
}

func (u *dynamicUploader) Upload(ctx context.Context, req UploadRequest, cfg map[string]string) (*UploadResult, error) {
	d := u.def
	vars := templateVars(cfg, req)
	target, missing := renderTemplate(d.Request.URL, vars)
	if len(missing) > 0 {
		return nil, fmt.Errorf("请求 URL 存在未提供的变量: %s", strings.Join(unique(missing), ", "))
	}
	if err := validateTargetURL(target); err != nil {
		return nil, err
	}

	body, contentType, err := u.buildBody(req, vars)
	if err != nil {
		return nil, err
	}
	defer body.Close()

	method := d.Request.Method
	httpReq, err := http.NewRequestWithContext(ctx, method, target, body)
	if err != nil {
		return nil, fmt.Errorf("构造上游请求: %w", err)
	}
	for _, k := range sortedKeys(d.Request.Headers) {
		v, miss := renderTemplate(d.Request.Headers[k], vars)
		if len(miss) > 0 {
			return nil, fmt.Errorf("请求头 %s 存在未提供的变量: %s", k, strings.Join(unique(miss), ", "))
		}
		if err := validateHeader(k, v); err != nil {
			return nil, fmt.Errorf("请求头 %s: %w", k, err)
		}
		httpReq.Header.Set(k, v)
	}
	if contentType != "" {
		httpReq.Header.Set("Content-Type", contentType)
	}

	client := &http.Client{Timeout: time.Duration(d.Request.TimeoutSecs) * time.Second}
	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("调用第三方上传接口: %w", err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取第三方响应: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("第三方接口返回 HTTP %d: %s", resp.StatusCode, snippet(raw, 400))
	}

	var payload any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&payload); err != nil {
		return nil, fmt.Errorf("第三方响应不是合法 JSON: %w；内容: %s", err, snippet(raw, 200))
	}
	if d.Response.CodePath != "" && d.Response.SuccessCode != "" {
		got, _ := extractPath(payload, d.Response.CodePath)
		if toString(got) != d.Response.SuccessCode {
			msg := ""
			if d.Response.MessagePath != "" {
				if m, ok := extractPath(payload, d.Response.MessagePath); ok {
					msg = toString(m)
				}
			}
			return nil, fmt.Errorf("第三方返回业务失败（%s=%s，期望 %s）%s",
				d.Response.CodePath, toString(got), d.Response.SuccessCode, msg)
		}
	}

	URL, ok := extractPath(payload, d.Response.URLPath)
	if !ok || strings.TrimSpace(toString(URL)) == "" {
		return nil, fmt.Errorf("第三方响应缺少 %s 字段: %s", d.Response.URLPath, snippet(raw, 300))
	}
	res := &UploadResult{
		URL:      toString(URL),
		FileName: req.FileName,
		MimeType: req.MimeType,
		FileSize: req.Size,
		Provider: d.Kind,
		Extra:    map[string]any{},
	}
	if v, ok := extractPath(payload, d.Response.ObjectKeyPath); ok {
		res.ObjectKey = toString(v)
	}
	if res.ObjectKey == "" {
		res.ObjectKey = req.ObjectKey
	}
	if v, ok := extractPath(payload, d.Response.FileNamePath); ok && toString(v) != "" {
		res.FileName = toString(v)
	}
	if v, ok := extractPath(payload, d.Response.MimeTypePath); ok && toString(v) != "" {
		res.MimeType = toString(v)
	}
	if v, ok := extractPath(payload, d.Response.SizePath); ok {
		if n := toInt64(v); n > 0 {
			res.FileSize = n
		}
	}
	if v, ok := extractPath(payload, d.Response.ExpirePath); ok {
		res.URLExpireAt = parseExpire(v)
	}
	if d.Response.CodePath != "" {
		if v, ok := extractPath(payload, d.Response.CodePath); ok {
			res.Extra["upstreamCode"] = toString(v)
		}
	}
	return res, nil
}

func (u *dynamicUploader) buildBody(req UploadRequest, vars map[string]string) (io.ReadCloser, string, error) {
	d := u.def
	fields := make(map[string]string, len(d.Request.Fields))
	for k, v := range d.Request.Fields {
		rendered, miss := renderTemplate(v, vars)
		if len(miss) > 0 {
			return nil, "", fmt.Errorf("表单字段 %s 存在未提供的变量: %s", k, strings.Join(unique(miss), ", "))
		}
		fields[k] = rendered
	}
	fileField, miss := renderTemplate(d.Request.FileField, vars)
	if len(miss) > 0 {
		return nil, "", fmt.Errorf("fileField 存在未提供的变量: %s", strings.Join(unique(miss), ", "))
	}
	switch d.Request.Mode {
	case "multipart":
		pr, pw := io.Pipe()
		mw := multipart.NewWriter(pw)
		go func() {
			var err error
			defer func() {
				_ = mw.Close()
				pw.CloseWithError(err)
			}()
			var part io.Writer
			part, err = mw.CreateFormFile(fileField, req.FileName)
			if err != nil {
				return
			}
			if _, err = io.Copy(part, req.Reader); err != nil {
				return
			}
			for _, k := range sortedKeys(fields) {
				if err = mw.WriteField(k, fields[k]); err != nil {
					return
				}
			}
		}()
		return pr, mw.FormDataContentType(), nil

	case "json":
		data, err := io.ReadAll(io.LimitReader(req.Reader, 32<<20))
		if err != nil {
			return nil, "", fmt.Errorf("读取上传文件: %w", err)
		}
		obj := make(map[string]any, len(fields)+1)
		for k, v := range fields {
			obj[k] = v
		}
		obj[fileField] = base64.StdEncoding.EncodeToString(data)
		encoded, err := json.Marshal(obj)
		if err != nil {
			return nil, "", err
		}
		return io.NopCloser(bytes.NewReader(encoded)), "application/json", nil

	case "form":
		form := url.Values{}
		for k, v := range fields {
			form.Set(k, v)
		}
		return io.NopCloser(strings.NewReader(form.Encode())), "application/x-www-form-urlencoded", nil

	default: // raw
		rendered, miss := renderTemplate(d.Request.RawBody, vars)
		if len(miss) > 0 {
			return nil, "", fmt.Errorf("rawBody 存在未提供的变量: %s", strings.Join(unique(miss), ", "))
		}
		return io.NopCloser(strings.NewReader(rendered)), "application/octet-stream", nil
	}
}

// Check 只做连通性探测，不上传任何文件，符合 Uploader 契约的无副作用要求。
func (u *dynamicUploader) Check(ctx context.Context, cfg map[string]string) error {
	d := u.def
	vars := templateVars(cfg, UploadRequest{})
	target := d.Request.CheckURL
	if target == "" {
		target = d.Request.URL
	}
	target, missing := renderTemplate(target, vars)
	if len(missing) > 0 {
		return fmt.Errorf("探活地址存在未提供的变量: %s", strings.Join(unique(missing), ", "))
	}
	if err := validateTargetURL(target); err != nil {
		return err
	}
	if d.Request.CheckURL != "" {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if err != nil {
			return err
		}
		for _, k := range sortedKeys(d.Request.Headers) {
			v, _ := renderTemplate(d.Request.Headers[k], vars)
			if err := validateHeader(k, v); err == nil {
				req.Header.Set(k, v)
			}
		}
		client := &http.Client{Timeout: 5 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return fmt.Errorf("探活请求失败: %w", err)
		}
		defer resp.Body.Close()
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4<<10))
		return nil // 收到任何 HTTP 响应即说明链路可达
	}
	parsed, _ := url.Parse(target)
	port := parsed.Port()
	if port == "" {
		if parsed.Scheme == "https" {
			port = "443"
		} else {
			port = "80"
		}
	}
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(parsed.Hostname(), port), 5*time.Second)
	if err != nil {
		return fmt.Errorf("上游 %s 不可达: %w", net.JoinHostPort(parsed.Hostname(), port), err)
	}
	_ = conn.Close()
	return nil
}

// ---------- helpers ----------

func validateTargetURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return fmt.Errorf("请求地址非法: %w", err)
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("请求地址必须是 http/https，收到 %q", u.Scheme)
	}
	if u.Hostname() == "" {
		return errors.New("请求地址缺少主机名")
	}
	return nil
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func unique(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}

func snippet(b []byte, n int) string {
	if len(b) > n {
		b = b[:n]
	}
	return strings.TrimSpace(strings.ReplaceAll(string(b), "\n", " "))
}

// extractPath 读取点号路径，支持数组下标，如 data.items[0].url。
func extractPath(v any, path string) (any, bool) {
	if path == "" {
		return nil, false
	}
	cur := v
	for _, seg := range strings.Split(path, ".") {
		name := seg
		indexes := []int{}
		if i := strings.IndexByte(seg, '['); i >= 0 {
			name = seg[:i]
			for _, m := range regexp.MustCompile(`\[(\d+)\]`).FindAllStringSubmatch(seg[i:], -1) {
				n, _ := strconv.Atoi(m[1])
				indexes = append(indexes, n)
			}
		}
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		cur, ok = obj[name]
		if !ok {
			return nil, false
		}
		for _, idx := range indexes {
			arr, ok := cur.([]any)
			if !ok || idx < 0 || idx >= len(arr) {
				return nil, false
			}
			cur = arr[idx]
		}
	}
	return cur, true
}

func toString(v any) string {
	switch x := v.(type) {
	case nil:
		return ""
	case string:
		return x
	case json.Number:
		return x.String()
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case bool:
		return strconv.FormatBool(x)
	default:
		return fmt.Sprint(x)
	}
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case json.Number:
		n, _ := x.Int64()
		return n
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}

func parseExpire(v any) *time.Time {
	s := strings.TrimSpace(toString(v))
	if s == "" {
		return nil
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return &t
	}
	n := toInt64(v)
	if n <= 0 {
		return nil
	}
	if n > 1<<12 {
		n = n / 1000 // 毫秒时间戳
	}
	t := time.Unix(n, 0).UTC()
	return &t
}
