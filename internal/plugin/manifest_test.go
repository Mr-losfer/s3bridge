package plugin

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestManifestValidate 表驱动覆盖清单校验的每条规则。
func TestManifestValidate(t *testing.T) {
	valid := func() Manifest {
		return Manifest{Schema: ManifestSchema, ID: "my-up", Name: "我的上传器"}
	}
	cases := []struct {
		name    string
		mutate  func(*Manifest)
		wantErr string
	}{
		{name: "最小清单通过"},
		{
			name:    "ID 大写非法",
			mutate:  func(m *Manifest) { m.ID = "MyUp" },
			wantErr: "插件 ID",
		},
		{
			name:    "ID 单个字符非法",
			mutate:  func(m *Manifest) { m.ID = "a" },
			wantErr: "插件 ID",
		},
		{
			name:    "ID 含路径分隔符非法",
			mutate:  func(m *Manifest) { m.ID = "a/b" },
			wantErr: "插件 ID",
		},
		{
			name:    "名称为空非法",
			mutate:  func(m *Manifest) { m.Name = "  " },
			wantErr: "名称不能为空",
		},
		{
			name:    "schema 不匹配非法",
			mutate:  func(m *Manifest) { m.Schema = 1 },
			wantErr: "不支持的 schema",
		},
		{
			name:    "timeout 超范围非法",
			mutate:  func(m *Manifest) { m.TimeoutSecs = 601 },
			wantErr: "timeoutSecs",
		},
		{
			name:    "有效期周期超范围非法",
			mutate:  func(m *Manifest) { m.Validity.IntervalHours = 721 },
			wantErr: "validity.intervalHours",
		},
		{
			name: "参数缺 key 非法",
			mutate: func(m *Manifest) {
				m.Params = []ParamField{{Label: "无 key"}}
			},
			wantErr: "缺少 key",
		},
		{
			name: "参数 key 重复非法",
			mutate: func(m *Manifest) {
				m.Params = []ParamField{{Key: "a"}, {Key: "a"}}
			},
			wantErr: "key 重复",
		},
		{
			name: "参数类型不支持非法",
			mutate: func(m *Manifest) {
				m.Params = []ParamField{{Key: "a", Type: "color"}}
			},
			wantErr: "不支持",
		},
		{
			name: "select 缺 options 非法",
			mutate: func(m *Manifest) {
				m.Params = []ParamField{{Key: "a", Type: "select"}}
			},
			wantErr: "未提供 options",
		},
		{
			name: "正则非法",
			mutate: func(m *Manifest) {
				m.Params = []ParamField{{Key: "a", Pattern: "(["}}
			},
			wantErr: "正则无效",
		},
		{
			name: "合法参数",
			mutate: func(m *Manifest) {
				m.Params = []ParamField{
					{Key: "url", Type: "url"},
					{Key: "mode", Type: "select", Options: []ParamOption{{Value: "a", Label: "A"}}},
					{Key: "note"}, // 省略 type 时按 text 处理
				}
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := valid()
			if tc.mutate != nil {
				tc.mutate(&m)
			}
			err := m.Validate()
			if tc.wantErr == "" {
				if err != nil {
					t.Fatalf("应通过，实际 %v", err)
				}
				return
			}
			if err == nil || !strings.Contains(err.Error(), tc.wantErr) {
				t.Fatalf("错误信息应包含 %q，实际 %v", tc.wantErr, err)
			}
			if !errors.Is(err, ErrSourceInvalid) {
				t.Fatalf("错误应可用 ErrSourceInvalid 判定: %v", err)
			}
		})
	}
}

// TestNormalizeDefaults 验证旧清单缺字段时被补齐，且默认值足够安全。
func TestNormalizeDefaults(t *testing.T) {
	m := Manifest{ID: "x1", Name: "X"}
	if err := m.Validate(); err != nil {
		t.Fatal(err)
	}
	if m.Schema != ManifestSchema || m.Entry != "main.go" || m.TimeoutSecs != 60 || m.Priority != 100 {
		t.Fatalf("默认值未补齐: %+v", m)
	}
	if m.Health.TimeoutSecs != 8 || m.Health.FailThreshold != 2 || m.Health.ProbeMode != "ping" {
		t.Fatalf("健康默认值未补齐: %+v", m.Health)
	}
	// 已经设置的值不会被覆盖。
	m2 := Manifest{ID: "x2", Name: "X", TimeoutSecs: 5, Priority: 300, Entry: "plugin.go"}
	if err := m2.Validate(); err != nil {
		t.Fatal(err)
	}
	if m2.TimeoutSecs != 5 || m2.Priority != 300 || m2.Entry != "plugin.go" {
		t.Fatalf("显式值被覆盖: %+v", m2)
	}
}

// TestManifestRoundTrip 验证落盘再读回完全一致，且要求 id 与目录名一致。
func TestManifestRoundTrip(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "my-up")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	m := Manifest{
		Schema: ManifestSchema, ID: "my-up", Name: "我的上传器", Version: "1.2.3",
		Description: "说明", Author: "作者", Entry: "main.go",
		FileTypes: []string{"image"}, Priority: 200, TimeoutSecs: 30,
		Validity: ValidityHint{IntervalHours: 48},
		Params:   []ParamField{{Key: "url", Type: "url", Required: true}},
	}
	if err := SaveManifest(dir, m); err != nil {
		t.Fatal(err)
	}
	got, err := LoadManifest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got.ID != m.ID || got.Name != m.Name || got.Version != m.Version ||
		got.Validity.IntervalHours != 48 || len(got.Params) != 1 {
		t.Fatalf("读回不一致: %+v", got)
	}

	// 目录名与 id 不一致时必须拒绝，防止加载到别人的源码。
	other := filepath.Join(t.TempDir(), "wrong-name")
	_ = os.MkdirAll(other, 0o700)
	if err := SaveManifest(other, m); err != nil {
		t.Fatal(err)
	}
	if _, err := LoadManifest(other); err == nil || !strings.Contains(err.Error(), "不一致") {
		t.Fatalf("目录名与 id 不一致应报错，实际 %v", err)
	}

	if _, err := LoadManifest(t.TempDir()); err == nil {
		t.Fatal("缺少 plugin.json 应报错")
	}
}

// TestParamHelpers 覆盖参数模板相关的纯函数。
func TestParamHelpers(t *testing.T) {
	m := Manifest{Params: []ParamField{
		{Key: "b", Label: "B", Type: "text", Group: "第二组"},
		{Key: "a", Label: "A", Type: "url", Group: "第一组", Default: "https://x"},
		{Key: "c", Type: "text", Default: "有默认"},
	}}
	if f, ok := FieldByKey(m, "a"); !ok || f.Label != "A" {
		t.Fatalf("FieldByKey 失败: %+v %v", f, ok)
	}
	if _, ok := FieldByKey(m, "zzz"); ok {
		t.Fatal("不存在的 key 应返回 false")
	}
	def := DefaultParams(m)
	if def["a"] != "https://x" || def["c"] != "有默认" {
		t.Fatalf("默认参数提取错误: %+v", def)
	}
	if _, ok := def["b"]; ok {
		t.Fatal("无默认值的参数不应出现在默认集合里")
	}
	groups := SortedGroups(m)
	if len(groups) != 3 { // 按出现顺序：第二组、第一组、未分组
		t.Fatalf("参数分组数量不符: %+v", groups)
	}
	var names []string
	for _, g := range groups {
		names = append(names, g.Name)
	}
	if strings.Join(names, ",") != "第二组,第一组,通用" {
		t.Fatalf("分组未按声明顺序排列: %v", names)
	}
	if len(groups[0].Fields) != 1 || groups[0].Fields[0].Key != "b" {
		t.Fatalf("分组内字段错误: %+v", groups[0])
	}
}
