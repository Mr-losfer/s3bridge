package plugin

import (
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const encryptedPrefix = "enc:"

// ValidateParams 按 Manifest 校验参数：必填、类型、枚举、URL、正则、临时参数过期时间。
// 参数来自后台表单或 MCP，必须在此统一收口，避免脏配置进入第三方调用。
func ValidateParams(m Manifest, params map[string]string) error {
	var problems []string
	for _, f := range m.Params {
		v, ok := params[f.Key]
		if !ok || strings.TrimSpace(v) == "" {
			if f.Required {
				problems = append(problems, fmt.Sprintf("%s(%s) 为必填", f.Label, f.Key))
			}
			continue
		}
		switch f.Type {
		case "number", "duration":
			if _, err := strconv.ParseFloat(v, 64); err != nil {
				problems = append(problems, fmt.Sprintf("%s 必须是数字", f.Label))
			}
		case "bool":
			if v != "true" && v != "false" {
				problems = append(problems, fmt.Sprintf("%s 必须是 true/false", f.Label))
			}
		case "url":
			u, err := url.Parse(v)
			if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
				problems = append(problems, fmt.Sprintf("%s 必须是合法的 http(s) URL", f.Label))
			}
		case "select":
			valid := false
			for _, o := range f.Options {
				if o.Value == v {
					valid = true
					break
				}
			}
			if !valid {
				problems = append(problems, fmt.Sprintf("%s 的取值不在允许范围内", f.Label))
			}
		case "datetime":
			if _, err := parseFlexibleTime(v); err != nil {
				problems = append(problems, fmt.Sprintf("%s 时间格式无法识别", f.Label))
			}
		}
		if f.Pattern != "" {
			re, err := regexp.Compile(f.Pattern)
			if err == nil && !re.MatchString(v) {
				problems = append(problems, fmt.Sprintf("%s 格式不符合要求", f.Label))
			}
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("%w: %s", ErrInvalidParams, strings.Join(problems, "; "))
	}
	return nil
}

// VolatileWarning 检查临时参数（如 Bearer Token）是否已过期或即将过期，
// 返回给前端与告警中心的提示文本。
func VolatileWarning(m Manifest, params map[string]string) string {
	for _, f := range m.Params {
		if !f.Volatile || f.ExpiresField == "" {
			continue
		}
		raw := strings.TrimSpace(params[f.ExpiresField])
		if raw == "" {
			return fmt.Sprintf("%s 未设置过期时间，无法判断临时凭据有效期", f.Label)
		}
		t, err := parseFlexibleTime(raw)
		if err != nil {
			return fmt.Sprintf("%s 的过期时间无法解析", f.Label)
		}
		left := time.Until(t)
		switch {
		case left <= 0:
			return fmt.Sprintf("%s 已于 %s 过期，请立即更新", f.Label, t.Format(time.RFC3339))
		case left < 24*time.Hour:
			return fmt.Sprintf("%s 将在 %s 过期（剩余约 %s）", f.Label, t.Format(time.RFC3339), left.Round(time.Hour))
		}
	}
	return ""
}

func parseFlexibleTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if n, err := strconv.ParseInt(v, 10, 64); err == nil {
		// 兼容秒级与毫秒级 Unix 时间戳。
		if n > 1e12 {
			return time.UnixMilli(n), nil
		}
		return time.Unix(n, 0), nil
	}
	for _, layout := range []string{time.RFC3339, "2006-01-02 15:04:05", "2006-01-02T15:04", "2006-01-02"} {
		if t, err := time.Parse(layout, v); err == nil {
			return t, nil
		}
	}
	return time.Time{}, fmt.Errorf("无法解析时间 %q", v)
}

// SealParams 把标记为 sensitive 的参数值加密，其余原样保留。
func SealParams(m Manifest, params map[string]string, seal func(string) (string, error)) (map[string]string, error) {
	out := make(map[string]string, len(params))
	for k, v := range params {
		f, known := FieldByKey(m, k)
		if known && f.Sensitive && v != "" && !strings.HasPrefix(v, encryptedPrefix) {
			enc, err := seal(v)
			if err != nil {
				return nil, fmt.Errorf("加密参数 %s: %w", k, err)
			}
			out[k] = encryptedPrefix + enc
		} else {
			out[k] = v
		}
	}
	return out, nil
}

// OpenParams 解密敏感参数，得到可直接传给 Uploader 的明文配置。
func OpenParams(params map[string]string, open func(string) (string, error)) (map[string]string, error) {
	out := make(map[string]string, len(params))
	for k, v := range params {
		if strings.HasPrefix(v, encryptedPrefix) {
			plain, err := open(strings.TrimPrefix(v, encryptedPrefix))
			if err != nil {
				return nil, fmt.Errorf("解密参数 %s: %w", k, err)
			}
			out[k] = plain
		} else {
			out[k] = v
		}
	}
	return out, nil
}

// MaskParams 生成后台列表/详情展示用的脱敏视图。
func MaskParams(m Manifest, params map[string]string) map[string]any {
	out := make(map[string]any, len(params))
	for k, v := range params {
		f, _ := FieldByKey(m, k)
		switch {
		case v == "":
			out[k] = ""
		case strings.HasPrefix(v, encryptedPrefix), f.Sensitive:
			out[k] = mask(v)
		case f.Type == "bool":
			out[k] = v == "true"
		case f.Type == "number", f.Type == "duration":
			if n, err := strconv.ParseFloat(v, 64); err == nil {
				out[k] = n
			} else {
				out[k] = v
			}
		default:
			out[k] = v
		}
	}
	return out
}

func mask(v string) string {
	v = strings.TrimPrefix(v, encryptedPrefix)
	if len(v) <= 8 {
		return "••••••••"
	}
	return v[:4] + "••••••••" + v[len(v)-4:]
}
