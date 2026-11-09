package plugin

import (
	"errors"
	"strings"
	"testing"
	"time"
)

var testManifest = Manifest{
	Kind: "test", Name: "Test",
	Params: []ParamField{
		{Key: "url", Label: "地址", Type: "url", Required: true},
		{Key: "token", Label: "令牌", Type: "secret", Required: true, Sensitive: true, Volatile: true, ExpiresField: "expires"},
		{Key: "expires", Label: "过期", Type: "datetime", Volatile: true},
		{Key: "timeout", Label: "超时", Type: "number", Default: "30"},
		{Key: "platform", Label: "平台", Type: "select", Options: []ParamOption{{Label: "Web", Value: "web"}, {Label: "App", Value: "app"}}},
		{Key: "enabled", Label: "启用", Type: "bool"},
	},
}

func TestValidateParams(t *testing.T) {
	base := map[string]string{
		"url": "https://example.com", "token": "secret", "expires": "2030-01-01",
		"timeout": "30", "platform": "web", "enabled": "true",
	}
	tests := []struct {
		name    string
		mutate  func(map[string]string)
		wantErr bool
	}{
		{"全部合法", func(m map[string]string) {}, false},
		{"缺少必填", func(m map[string]string) { delete(m, "token") }, true},
		{"非法 URL", func(m map[string]string) { m["url"] = "ftp://x" }, true},
		{"数字字段非数字", func(m map[string]string) { m["timeout"] = "abc" }, true},
		{"布尔字段非法", func(m map[string]string) { m["enabled"] = "yes" }, true},
		{"枚举越界", func(m map[string]string) { m["platform"] = "unknown" }, true},
		{"时间格式非法", func(m map[string]string) { m["expires"] = "not-a-time" }, true},
		{"Unix 秒时间戳", func(m map[string]string) { m["expires"] = "1893456000" }, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]string{}
			for k, v := range base {
				params[k] = v
			}
			tt.mutate(params)
			err := ValidateParams(testManifest, params)
			if (err != nil) != tt.wantErr {
				t.Fatalf("ValidateParams err = %v, wantErr %v", err, tt.wantErr)
			}
			if err != nil && !errors.Is(err, ErrInvalidParams) {
				t.Fatalf("错误必须包装 ErrInvalidParams: %v", err)
			}
		})
	}
}

func TestSealOpenParamsRoundtrip(t *testing.T) {
	seal := func(v string) (string, error) { return "E(" + v + ")", nil }
	open := func(v string) (string, error) { return strings.TrimSuffix(strings.TrimPrefix(v, "E("), ")"), nil }
	params := map[string]string{"url": "https://x", "token": "very-secret", "timeout": "30"}
	sealed, err := SealParams(testManifest, params, seal)
	if err != nil {
		t.Fatal(err)
	}
	if sealed["token"] != encryptedPrefix+"E(very-secret)" {
		t.Fatalf("敏感参数未加密: %q", sealed["token"])
	}
	if sealed["url"] != "https://x" {
		t.Fatal("非敏感参数不应被改动")
	}
	plain, err := OpenParams(sealed, open)
	if err != nil {
		t.Fatal(err)
	}
	if plain["token"] != "very-secret" {
		t.Fatalf("解密结果异常: %q", plain["token"])
	}
	// 已加密的值不应被二次加密
	again, _ := SealParams(testManifest, sealed, seal)
	if again["token"] != sealed["token"] {
		t.Fatal("重复保存不应二次加密")
	}
}

func TestMaskParams(t *testing.T) {
	view := MaskParams(testManifest, map[string]string{
		"token": encryptedPrefix + "abcdefghijklmnop", "timeout": "45", "enabled": "false", "url": "https://x",
	})
	if !strings.Contains(view["token"].(string), "•") {
		t.Fatal("敏感参数必须脱敏")
	}
	if view["timeout"].(float64) != 45 {
		t.Fatal("数字字段应转为数值")
	}
	if view["enabled"].(bool) {
		t.Fatal("bool 字段应转为布尔")
	}
}

func TestVolatileWarning(t *testing.T) {
	tests := []struct {
		name     string
		expires  string
		contains string
	}{
		{"已过期", time.Now().Add(-time.Hour).UTC().Format(time.RFC3339), "过期"},
		{"即将过期", time.Now().Add(2 * time.Hour).UTC().Format(time.RFC3339), "将在"},
		{"远期有效", time.Now().Add(30 * 24 * time.Hour).UTC().Format(time.RFC3339), ""},
		{"未设置", "", "未设置"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			params := map[string]string{"token": "x", "expires": tt.expires}
			got := VolatileWarning(testManifest, params)
			if tt.contains == "" && got != "" {
				t.Fatalf("不应产生告警: %q", got)
			}
			if tt.contains != "" && !strings.Contains(got, tt.contains) {
				t.Fatalf("告警 %q 不包含 %q", got, tt.contains)
			}
		})
	}
}

func TestDefaultParamsAndFieldLookup(t *testing.T) {
	defaults := DefaultParams(testManifest)
	if defaults["timeout"] != "30" || len(defaults) != 1 {
		t.Fatalf("默认值提取异常: %+v", defaults)
	}
	if f, ok := FieldByKey(testManifest, "token"); !ok || !f.Sensitive {
		t.Fatal("字段查找失败")
	}
	if _, ok := FieldByKey(testManifest, "nope"); ok {
		t.Fatal("不存在的字段不应命中")
	}
}
