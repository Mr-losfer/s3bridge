package plugin

import (
	"errors"
	"strings"
	"testing"
	"time"
)

// testManifest 构造一个带多种参数类型的清单，供本文件复用。
func testManifest() Manifest {
	return Manifest{
		Schema: ManifestSchema,
		ID:     "acme",
		Name:   "ACME 图床",
		Params: []ParamField{
			{Key: "endpoint", Label: "上传地址", Type: "url", Required: true},
			{Key: "bucket", Label: "存储桶", Type: "text", Required: true, Pattern: `^[a-z0-9-]+$`},
			{Key: "retries", Label: "重试次数", Type: "number"},
			{Key: "secure", Label: "使用 HTTPS", Type: "bool"},
			{Key: "region", Label: "区域", Type: "select", Options: []ParamOption{
				{Label: "华东", Value: "cn-east"},
				{Label: "华北", Value: "cn-north"},
			}},
			{Key: "token", Label: "Bearer Token", Type: "secret", Sensitive: true},
			{Key: "token_expires_at", Label: "Token 过期时间", Type: "datetime"},
		},
	}
}

func TestValidateParams(t *testing.T) {
	m := testManifest()
	soon := time.Now().Add(6 * time.Hour).Format(time.RFC3339)

	cases := []struct {
		name    string
		params  map[string]string
		wantErr string // 期望错误信息包含的片段；空表示期望通过
	}{
		{
			name:   "全部合法",
			params: map[string]string{"endpoint": "https://api.acme.dev/put", "bucket": "img-01"},
		},
		{
			name:    "必填缺失",
			params:  map[string]string{"bucket": "img-01"},
			wantErr: "上传地址(endpoint) 为必填",
		},
		{
			name:    "必填为空白字符",
			params:  map[string]string{"endpoint": "   ", "bucket": "img-01"},
			wantErr: "上传地址(endpoint) 为必填",
		},
		{
			name:    "URL 协议不合法",
			params:  map[string]string{"endpoint": "ftp://api.acme.dev", "bucket": "img-01"},
			wantErr: "必须是合法的 http(s) URL",
		},
		{
			name:    "URL 缺少主机",
			params:  map[string]string{"endpoint": "https://", "bucket": "img-01"},
			wantErr: "必须是合法的 http(s) URL",
		},
		{
			name:    "数字类型不合法",
			params:  map[string]string{"endpoint": "https://a.dev", "bucket": "img-01", "retries": "三次"},
			wantErr: "重试次数 必须是数字",
		},
		{
			name:    "布尔类型不合法",
			params:  map[string]string{"endpoint": "https://a.dev", "bucket": "img-01", "secure": "yes"},
			wantErr: "必须是 true/false",
		},
		{
			name:    "枚举越界",
			params:  map[string]string{"endpoint": "https://a.dev", "bucket": "img-01", "region": "us-west"},
			wantErr: "取值不在允许范围内",
		},
		{
			name:    "正则不匹配",
			params:  map[string]string{"endpoint": "https://a.dev", "bucket": "IMG_01"},
			wantErr: "格式不符合要求",
		},
		{
			name:   "时间戳与日期格式都接受",
			params: map[string]string{"endpoint": "https://a.dev", "bucket": "img-01", "token_expires_at": soon},
		},
		{
			name:   "毫秒时间戳接受",
			params: map[string]string{"endpoint": "https://a.dev", "bucket": "img-01", "token_expires_at": "1893456000000"},
		},
		{
			name:    "时间格式无法识别",
			params:  map[string]string{"endpoint": "https://a.dev", "bucket": "img-01", "token_expires_at": "明天"},
			wantErr: "时间格式无法识别",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateParams(m, tc.params)
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("期望通过，实际报错: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("期望报错 %q，实际通过", tc.wantErr)
			}
			if !errors.Is(err, ErrInvalidParams) {
				t.Errorf("错误应可被 ErrInvalidParams 识别，实际: %v", err)
			}
			if !strings.Contains(err.Error(), tc.wantErr) {
				t.Errorf("错误信息 %q 不含 %q", err.Error(), tc.wantErr)
			}
		})
	}
}

func TestValidateParamsCollectsAllProblems(t *testing.T) {
	m := testManifest()
	err := ValidateParams(m, map[string]string{"retries": "x", "secure": "maybe"})
	if err == nil {
		t.Fatal("期望报错")
	}
	msg := err.Error()
	for _, want := range []string{"上传地址(endpoint) 为必填", "存储桶(bucket) 为必填", "重试次数 必须是数字", "必须是 true/false"} {
		if !strings.Contains(msg, want) {
			t.Errorf("错误信息缺少 %q，实际: %s", want, msg)
		}
	}
}

// TestVolatileWarning 覆盖临时凭据提示；其中「参数为空时不提示」是线上真实踩过的缺陷：
// http-multipart 模板自带可选的 token 字段，未配置时曾被判为「未设置过期时间」，
// 导致插件调试直接报不健康。
func TestVolatileWarning(t *testing.T) {
	base := Manifest{
		Schema: ManifestSchema,
		ID:     "acme",
		Name:   "ACME",
		Params: []ParamField{
			{Key: "token", Label: "访问令牌", Type: "secret", Volatile: true, ExpiresField: "token_expires_at"},
			{Key: "token_expires_at", Label: "过期时间", Type: "datetime"},
			{Key: "cookie", Label: "Cookie", Type: "secret"},
		},
	}

	cases := []struct {
		name   string
		params map[string]string
		want   string
	}{
		{
			name:   "令牌与过期时间都未配置",
			params: map[string]string{},
		},
		{
			name:   "令牌为空白字符也视为未配置",
			params: map[string]string{"token": "   ", "token_expires_at": "明天"},
		},
		{
			name:   "已配置但缺过期时间",
			params: map[string]string{"token": "tk-123"},
			want:   "未设置过期时间",
		},
		{
			name:   "过期时间无法解析",
			params: map[string]string{"token": "tk-123", "token_expires_at": "下周"},
			want:   "过期时间无法解析",
		},
		{
			name: "已经过期",
			params: map[string]string{
				"token":            "tk-123",
				"token_expires_at": time.Now().Add(-time.Hour).Format(time.RFC3339),
			},
			want: "请立即更新",
		},
		{
			name: "24 小时内过期会提醒",
			params: map[string]string{
				"token":            "tk-123",
				"token_expires_at": time.Now().Add(3 * time.Hour).Format(time.RFC3339),
			},
			want: "将在",
		},
		{
			name: "充裕有效期不提示",
			params: map[string]string{
				"token":            "tk-123",
				"token_expires_at": time.Now().Add(30 * 24 * time.Hour).Format(time.RFC3339),
			},
		},
		{
			name:   "非临时参数不参与检查",
			params: map[string]string{"cookie": "a=b"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := VolatileWarning(base, tc.params)
			if tc.want == "" {
				if got != "" {
					t.Fatalf("期望无提示，实际: %s", got)
				}
				return
			}
			if !strings.Contains(got, tc.want) {
				t.Fatalf("提示 %q 不含 %q", got, tc.want)
			}
			if !strings.Contains(got, "访问令牌") {
				t.Errorf("提示应带上字段名，实际: %s", got)
			}
		})
	}
}

func TestSealAndOpenParams(t *testing.T) {
	m := testManifest()
	params := map[string]string{
		"endpoint": "https://api.acme.dev/put",
		"token":    "tk-secret",
	}

	sealed, err := SealParams(m, params, func(s string) (string, error) { return "sealed(" + s + ")", nil })
	if err != nil {
		t.Fatalf("加密失败: %v", err)
	}
	if !strings.HasPrefix(sealed["token"], encryptedPrefix) {
		t.Fatalf("敏感参数应带 %q 前缀，实际: %s", encryptedPrefix, sealed["token"])
	}
	if sealed["endpoint"] != params["endpoint"] {
		t.Errorf("非敏感参数不应被改动: %s", sealed["endpoint"])
	}

	// 重复加密不应叠加前缀。
	again, err := SealParams(m, sealed, func(s string) (string, error) { return "sealed(" + s + ")", nil })
	if err != nil {
		t.Fatalf("二次加密失败: %v", err)
	}
	if again["token"] != sealed["token"] {
		t.Errorf("已加密的值不应被二次加密: %s", again["token"])
	}

	opened, err := OpenParams(sealed, func(s string) (string, error) {
		return strings.TrimSuffix(strings.TrimPrefix(s, "sealed("), ")"), nil
	})
	if err != nil {
		t.Fatalf("解密失败: %v", err)
	}
	if opened["token"] != "tk-secret" {
		t.Errorf("解密结果不对: %s", opened["token"])
	}

	if _, err := SealParams(m, params, func(string) (string, error) {
		return "", errors.New("kms down")
	}); err == nil || !strings.Contains(err.Error(), "加密参数 token") {
		t.Errorf("加密失败应带参数名，实际: %v", err)
	}

	if _, err := OpenParams(sealed, func(string) (string, error) {
		return "", errors.New("bad key")
	}); err == nil || !strings.Contains(err.Error(), "解密参数 token") {
		t.Errorf("解密失败应带参数名，实际: %v", err)
	}
}

func TestMaskParams(t *testing.T) {
	m := testManifest()
	got := MaskParams(m, map[string]string{
		"endpoint":         "https://api.acme.dev/put",
		"token":            "tk-0123456789",
		"retries":          "3",
		"secure":           "true",
		"bucket":           "",
		"region":           "cn-east",
		"token_expires_at": "2026-01-01T00:00:00Z",
	})

	if got["endpoint"] != "https://api.acme.dev/put" {
		t.Errorf("普通文本应原样返回: %v", got["endpoint"])
	}
	if s, _ := got["token"].(string); strings.Contains(s, "23456789") {
		t.Errorf("敏感参数应脱敏，实际: %v", got["token"])
	}
	if got["retries"] != 3.0 {
		t.Errorf("number 应转成数字，实际: %T %v", got["retries"], got["retries"])
	}
	if got["secure"] != true {
		t.Errorf("bool 应转成布尔，实际: %T %v", got["secure"], got["secure"])
	}
	if got["bucket"] != "" {
		t.Errorf("空值应保持空字符串，实际: %v", got["bucket"])
	}
}

func TestMaskShortSecretFullyHidden(t *testing.T) {
	if s := mask("short"); s != "••••••••" {
		t.Errorf("短密文应完全隐藏，实际: %s", s)
	}
	if s := mask("enc:" + "abcdefghijklmnop"); !strings.HasPrefix(s, "abcd") || !strings.HasSuffix(s, "mnop") {
		t.Errorf("长密文应保留首尾各 4 位，实际: %s", s)
	}
}

func TestParseFlexibleTime(t *testing.T) {
	cases := []struct {
		in   string
		want string // 期望的 UTC 时间，便于比对
	}{
		{"1700000000", "2023-11-14T22:13:20Z"},
		{"1700000000000", "2023-11-14T22:13:20Z"},
		{"2023-11-14T22:13:20Z", "2023-11-14T22:13:20Z"},
		{"2023-11-14 22:13:20", "2023-11-14T22:13:20Z"},
		{"2023-11-14T22:13", "2023-11-14T22:13:00Z"},
		{"2023-11-14", "2023-11-14T00:00:00Z"},
		{"  2023-11-14  ", "2023-11-14T00:00:00Z"},
	}
	for _, tc := range cases {
		got, err := parseFlexibleTime(tc.in)
		if err != nil {
			t.Errorf("%q 应能解析，实际报错: %v", tc.in, err)
			continue
		}
		if s := got.UTC().Format(time.RFC3339); s != tc.want {
			t.Errorf("%q 解析为 %s，期望 %s", tc.in, s, tc.want)
		}
	}

	if _, err := parseFlexibleTime("next week"); err == nil {
		t.Error("无法识别的时间应报错")
	}
	if _, err := parseFlexibleTime(""); err == nil {
		t.Error("空字符串应报错")
	}
}
