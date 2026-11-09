// Package workbuddy 实现 WorkBuddy 开放上传接口插件。
//
// 上游协议：POST {base_url}/v2/backgroundagent/localProxy/upload
//
//	multipart/form-data，携带 Authorization / x-user-id / x-client-* 等头；
//	返回 {"code":0,"data":{"success":true,"url":...,"objectKey":...,"fileInfo":{...}}}
//
// 本插件把上游返回的 COS 临时签名 URL 与 objectKey 转换成 S3Bridge 的
// 标准 UploadResult；同时从 q-sign-time 解析签名过期时间，用于提前告警。
package workbuddy

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/textproto"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin"
)

const Kind = "workbuddy"

var Manifest = plugin.Manifest{
	Kind:         Kind,
	Name:         "WorkBuddy 开放上传",
	Version:      "1.0.0",
	Description:  "对接 WorkBuddy App 的 localProxy/upload 接口，把第三方临时签名 COS 链接转换为统一标准输出。需要按有效期轮换 Bearer Token。",
	Author:       "S3Bridge Core",
	Endpoint:     "POST https://copilot.tencent.com/v2/backgroundagent/localProxy/upload",
	Capabilities: []string{"upload", "image", "temp-url", "health-probe"},
	Health: plugin.HealthHint{
		Enabled: true, IntervalSecs: 300, TimeoutSecs: 8,
		FailThreshold: 2, ProbeMode: "ping",
	},
	Params: []plugin.ParamField{
		{Key: "base_url", Label: "服务域名", Type: "url", Group: "连接", Required: true,
			Default: "https://copilot.tencent.com", Help: "WorkBuddy 开放接口的服务根地址，不含路径。"},
		{Key: "upload_path", Label: "上传路径", Type: "text", Group: "连接", Required: true,
			Default: "/v2/backgroundagent/localProxy/upload"},
		{Key: "file_field", Label: "文件字段名", Type: "text", Group: "连接", Required: true,
			Default: "file", Help: "multipart 表单中文件字段的名称。"},
		{Key: "timeout_secs", Label: "请求超时（秒）", Type: "number", Group: "连接", Required: true, Default: "30"},
		{Key: "max_file_bytes", Label: "单文件上限（字节）", Type: "number", Group: "连接", Default: "52428800"},

		{Key: "authorization", Label: "Bearer Token / Authorization", Type: "secret", Group: "身份凭据",
			Required: true, Sensitive: true, Volatile: true, ExpiresField: "token_expires_at",
			Placeholder: "Bearer eyJhbGciOiJSUzI1NiIs...",
			Help:        "填完整 Authorization 头值；只粘贴 token 时系统会自动补 Bearer 前缀。这是会过期的临时参数。"},
		{Key: "token_expires_at", Label: "Token 过期时间", Type: "datetime", Group: "身份凭据",
			Volatile: true, Placeholder: "2026-12-08 12:00:00 / 1796601498",
			Help: "支持 RFC3339、Unix 秒/毫秒。可从 Bearer Token 自动解析，过期前 24 小时开始提醒。"},
		{Key: "x_user_id", Label: "X-User-Id", Type: "text", Group: "身份凭据", Required: true,
			Help: "WorkBuddy 用户 UUID，需与 Bearer Token 中的 sub 一致。"},
		{Key: "cookie", Label: "Cookie（可选）", Type: "secret", Group: "身份凭据", Sensitive: true,
			Placeholder: "tgw_l7_route=...", Help: "网关路由 Cookie，某些网络环境需要。"},

		{Key: "x_client_version", Label: "客户端版本", Type: "text", Group: "客户端标识", Default: "2.8.0"},
		{Key: "x_client_product", Label: "客户端产品", Type: "text", Group: "客户端标识", Default: "workbuddy-app"},
		{Key: "x_client_platform", Label: "客户端平台", Type: "select", Group: "客户端标识", Default: "android",
			Options: []plugin.ParamOption{
				{Label: "Android", Value: "android"},
				{Label: "iOS", Value: "ios"},
				{Label: "Web", Value: "web"},
				{Label: "Desktop", Value: "desktop"},
			}},
		{Key: "user_agent", Label: "User-Agent", Type: "text", Group: "客户端标识", Default: "WorkBuddyApp/2.8.0"},
	},
	Docs: "### 接入说明\n\n" +
		"WorkBuddy 的上传接口需要一组**临时参数**：Bearer Token 有有效期（JWT `exp`），" +
		"`x-user-id` 必须与 Token 中的 `sub` 一致，部分网络环境还需要 `cookie` 中的网关路由值。\n\n" +
		"在插件详情页把上述参数填入即可，无需改代码。Token 可以填完整 `Authorization` 头，" +
		"也可以只粘贴 token 本体；`Token 过期时间` 支持从 Bearer Token 一键解析。\n\n" +
		"**响应转换**：上游 `data.url` → 标准 `url`，`data.objectKey` → 标准 `objectKey`，" +
		"`data.fileInfo` → 标准 `fileName` / `mimeType` / `fileSize`；" +
		"签名 URL 中的 `q-sign-time` 结束时间会被解析为 `urlExpireAt`。\n\n" +
		"> 上游返回的是**临时签名链接**，S3Bridge 会同时把文件本体归档到本地存储桶，" +
		"因此即使第三方链接过期，通过 S3 标准接口取回的对象依然有效。\n",
	SampleCode: "func (u *Uploader) Upload(ctx context.Context, req plugin.UploadRequest, cfg map[string]string) (*plugin.UploadResult, error) {\n" +
		"    var body bytes.Buffer\n" +
		"    w := multipart.NewWriter(&body)\n" +
		"    part, _ := w.CreateFormFile(cfg[\"file_field\"], req.FileName)\n" +
		"    io.Copy(part, req.Reader)\n" +
		"    w.Close()\n" +
		"    r, _ := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)\n" +
		"    r.Header.Set(\"Authorization\", cfg[\"authorization\"])\n" +
		"    r.Header.Set(\"Content-Type\", w.FormDataContentType())\n" +
		"    resp, err := u.client.Do(r)\n" +
		"    // ...解析上游 JSON，映射到 plugin.UploadResult\n" +
		"    return &plugin.UploadResult{URL: data.URL, ObjectKey: data.ObjectKey, Provider: \"workbuddy\"}, err\n" +
		"}\n",
}

// Uploader 无状态，可并发复用。每次请求用独立 http.Client 应用插件级超时。
type Uploader struct{}

func New() plugin.Uploader { return &Uploader{} }

func Descriptor() plugin.Descriptor {
	return plugin.Descriptor{Manifest: Manifest, New: New}
}

type upstreamResponse struct {
	Code      int    `json:"code"`
	Msg       string `json:"msg"`
	RequestID string `json:"requestId"`
	Data      struct {
		Success   bool   `json:"success"`
		URL       string `json:"url"`
		ObjectKey string `json:"objectKey"`
		FileInfo  struct {
			FileName string `json:"fileName"`
			FileSize int64  `json:"fileSize"`
			MimeType string `json:"mimeType"`
		} `json:"fileInfo"`
	} `json:"data"`
}

// Upload 使用 io.Pipe 流式转发文件，避免把大文件整体读入内存。
func (u *Uploader) Upload(ctx context.Context, req plugin.UploadRequest, cfg map[string]string) (*plugin.UploadResult, error) {
	endpoint, err := joinURL(cfg["base_url"], cfg["upload_path"])
	if err != nil {
		return nil, err
	}
	if max := parseInt64(cfg["max_file_bytes"], 0); max > 0 && req.Size > max {
		return nil, fmt.Errorf("文件 %d 字节超过插件上限 %d 字节", req.Size, max)
	}

	timeout := time.Duration(parseInt64(cfg["timeout_secs"], 30)) * time.Second
	client := &http.Client{Timeout: timeout}

	pr, pw := io.Pipe()
	mw := multipart.NewWriter(pw)
	// 单独 goroutine 负责写 multipart；错误通过 CloseWithError 传给请求侧。
	go func() {
		var err error
		defer func() { pw.CloseWithError(err) }()
		h := make(textproto.MIMEHeader)
		h.Set("Content-Disposition", fmt.Sprintf(`form-data; name="%s"; filename="%s"`,
			escapeQuotes(cfg["file_field"]), escapeQuotes(req.FileName)))
		contentType := req.MimeType
		if contentType == "" {
			contentType = "application/octet-stream"
		}
		h.Set("Content-Type", contentType)
		var part io.Writer
		part, err = mw.CreatePart(h)
		if err != nil {
			return
		}
		_, err = io.Copy(part, req.Reader)
		if err != nil {
			return
		}
		err = mw.Close()
	}()

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, pr)
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, fmt.Errorf("构造上游请求: %w", err)
	}
	httpReq.Header.Set("Content-Type", mw.FormDataContentType())
	applyHeaders(httpReq, cfg)

	resp, err := client.Do(httpReq)
	if err != nil {
		_ = pr.CloseWithError(err)
		return nil, fmt.Errorf("调用 WorkBuddy 上传接口: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, fmt.Errorf("读取上游响应: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%w: HTTP %d, body=%s", plugin.ErrUpstream, resp.StatusCode, truncate(string(body), 512))
	}

	var out upstreamResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("%w: 响应不是合法 JSON: %v", plugin.ErrUpstream, err)
	}
	if out.Code != 0 || !out.Data.Success || out.Data.URL == "" {
		return nil, fmt.Errorf("%w: code=%d msg=%s", plugin.ErrUpstream, out.Code, out.Msg)
	}

	fileName := out.Data.FileInfo.FileName
	if fileName == "" {
		fileName = req.FileName
	}
	mimeType := out.Data.FileInfo.MimeType
	if mimeType == "" {
		mimeType = req.MimeType
	}
	size := out.Data.FileInfo.FileSize
	if size <= 0 {
		size = req.Size
	}
	return &plugin.UploadResult{
		URL: out.Data.URL, ObjectKey: out.Data.ObjectKey,
		FileName: fileName, MimeType: mimeType, FileSize: size,
		Provider: Kind, URLExpireAt: parseSignExpiry(out.Data.URL),
		Extra: map[string]any{
			"requestId":  out.RequestID,
			"remoteKey":  out.Data.ObjectKey,
			"httpStatus": resp.StatusCode,
		},
	}, nil
}

// Check 做无副作用探活：校验凭据有效期 + 到上游域名发一次轻量 GET。
// 刻意不上传样例文件，避免在第三方产生垃圾对象。
func (u *Uploader) Check(ctx context.Context, cfg map[string]string) error {
	if warning := plugin.VolatileWarning(Manifest, cfg); warning != "" &&
		(strings.Contains(warning, "过期") && !strings.Contains(warning, "将在")) {
		return errors.New(warning)
	}
	base := strings.TrimSpace(cfg["base_url"])
	if base == "" {
		return errors.New("未配置服务域名")
	}
	timeout := time.Duration(parseInt64(cfg["timeout_secs"], 8)) * time.Second
	if timeout <= 0 || timeout > 15*time.Second {
		timeout = 8 * time.Second
	}
	client := &http.Client{Timeout: timeout}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base, nil)
	if err != nil {
		return fmt.Errorf("构造探活请求: %w", err)
	}
	applyHeaders(req, cfg)
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("上游不可达: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	// 4xx 说明链路与网关是通的（只是该路径未授权），只有 5xx 视为服务异常。
	if resp.StatusCode >= 500 {
		return fmt.Errorf("上游返回 %d，服务异常", resp.StatusCode)
	}
	return nil
}

func applyHeaders(r *http.Request, cfg map[string]string) {
	auth := strings.TrimSpace(cfg["authorization"])
	if auth != "" && !strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		auth = "Bearer " + auth
	}
	setIf := func(k, v string) {
		if strings.TrimSpace(v) != "" {
			r.Header.Set(k, v)
		}
	}
	setIf("Authorization", auth)
	setIf("X-User-Id", cfg["x_user_id"])
	setIf("Cookie", cfg["cookie"])
	setIf("X-Client-Version", cfg["x_client_version"])
	setIf("X-Client-Product", cfg["x_client_product"])
	setIf("X-Client-Platform", cfg["x_client_platform"])
	setIf("User-Agent", cfg["user_agent"])
	setIf("X-Client-Request-Id", "c_"+randomID(12))
}

func joinURL(base, path string) (string, error) {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	if base == "" {
		return "", errors.New("服务域名为空")
	}
	u, err := url.Parse(base)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return "", fmt.Errorf("服务域名非法: %q", base)
	}
	if path == "" {
		path = "/v2/backgroundagent/localProxy/upload"
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	// 拒绝用户提供绝对路径 URL，防止 SSRF 绕过域名白名单。
	if strings.Contains(path, "://") {
		return "", errors.New("上传路径必须是相对路径")
	}
	return base + path, nil
}

// parseSignExpiry 从 COS 签名参数 q-sign-time / q-key-time 的结束时间解析过期时刻。
func parseSignExpiry(rawURL string) *time.Time {
	u, err := url.Parse(rawURL)
	if err != nil {
		return nil
	}
	for _, key := range []string{"q-sign-time", "q-key-time"} {
		v := u.Query().Get(key)
		if v == "" {
			continue
		}
		parts := strings.Split(v, ";")
		last := parts[len(parts)-1]
		if n, err := strconv.ParseInt(last, 10, 64); err == nil && n > 0 {
			t := time.Unix(n, 0).UTC()
			return &t
		}
	}
	return nil
}

// ParseTokenExpiry 从 Bearer Token（JWT）的 exp 声明解析过期时间，
// 供后台「一键解析」与 MCP 参数修改工具使用。
func ParseTokenExpiry(authorization string) (time.Time, error) {
	token := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authorization), "Bearer "))
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, errors.New("不是合法的 JWT（应为三段式）")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return time.Time{}, fmt.Errorf("JWT 载荷解码失败: %w", err)
	}
	var claims struct {
		Exp int64  `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, fmt.Errorf("JWT 载荷解析失败: %w", err)
	}
	if claims.Exp <= 0 {
		return time.Time{}, errors.New("JWT 缺少 exp 声明")
	}
	return time.Unix(claims.Exp, 0).UTC(), nil
}

// ParseTokenSubject 从 JWT 解析 sub，用于自动回填 x-user-id。
func ParseTokenSubject(authorization string) (string, error) {
	token := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(authorization), "Bearer "))
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return "", errors.New("不是合法的 JWT（应为三段式）")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return "", err
	}
	var claims struct {
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return "", err
	}
	if claims.Sub == "" {
		return "", errors.New("JWT 缺少 sub 声明")
	}
	return claims.Sub, nil
}

func escapeQuotes(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\r", "", "\n", "").Replace(s)
}

func parseInt64(v string, def int64) int64 {
	if n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64); err == nil && n > 0 {
		return n
	}
	return def
}

func randomID(n int) string {
	const alphabet = "abcdefghijklmnopqrstuvwxyz0123456789"
	b := make([]byte, n)
	if _, err := io.ReadFull(rand.Reader, b); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	for i := range b {
		b[i] = alphabet[int(b[i])%len(alphabet)]
	}
	return string(b)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
