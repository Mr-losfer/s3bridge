package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin/engine"
	"github.com/Mr-losfer/s3bridge/internal/plugin/runtime"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

// TestMain 让测试二进制充当插件 runner：生产环境里 runner 是主程序本身。
func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "__plugin_runner__" {
		if err := engine.Run(os.Args[2], os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// identity 是最简单的「加密」实现，测试里不需要真加密，但要能验证 encode/decode 闭环。
func identity(v string) (string, error) { return v, nil }

func newManager(t *testing.T, builtins []Builtin) (*Manager, store.Store) {
	t.Helper()
	root := t.TempDir()
	st, err := store.Open(filepath.Join(root, "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	rt, err := runtime.New(runtime.Options{
		PluginsDir: filepath.Join(root, "plugins"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return NewManager(st, rt, filepath.Join(root, "plugins"), identity, identity, 5*time.Minute, builtins), st
}

// TestMigrateLegacyDeclarative 验证 v1.x 的声明式插件被迁成可编辑的 Go 骨架，
// 而不是直接消失：源码留档、参数与文件类型保留、停用等待重写。
func TestMigrateLegacyDeclarative(t *testing.T) {
	m, st := newManager(t, nil)
	legacyDef, _ := json.Marshal(map[string]any{"kind": "custom-up", "request": map[string]any{"method": "POST"}})
	if err := st.Update(func(s *store.State) error {
		s.Plugins = append(s.Plugins, store.Plugin{
			ID: "old-up", Kind: "old-up", Name: "旧插件", Enabled: true,
			Definition: string(legacyDef), Custom: true,
			FileTypes: []store.FileCategory{store.CategoryImage},
			Params:    map[string]string{"api_key": "enc:ZmFrZQ==", "album": "blog"},
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.EnsureBuiltin(); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(m.pluginsDir, "old-up")
	for _, name := range []string{"plugin.json", "main.go", "legacy-definition.json"} {
		if _, err := os.Stat(filepath.Join(dir, name)); err != nil {
			t.Fatalf("迁移后缺少 %s: %v", name, err)
		}
	}
	mf, err := LoadManifest(dir)
	if err != nil {
		t.Fatalf("迁移生成的清单不合法: %v", err)
	}
	if mf.ID != "old-up" || mf.Docs == "" {
		t.Fatalf("迁移清单缺少说明: %+v", mf)
	}
	if len(mf.FileTypes) != 1 || mf.FileTypes[0] != "image" {
		t.Fatalf("旧插件的文件类型应保留，实际 %v", mf.FileTypes)
	}
	keyField, ok := FieldByKey(mf, "api_key")
	if !ok || !keyField.Sensitive || keyField.Type != "secret" {
		t.Fatalf("api_key 应迁移为敏感字段，实际 %+v", keyField)
	}
	albumField, ok := FieldByKey(mf, "album")
	if !ok || albumField.Sensitive {
		t.Fatalf("album 应迁移为普通文本字段，实际 %+v", albumField)
	}

	_ = st.View(func(s *store.State) error {
		p := store.PluginByID(s, "old-up")
		if p == nil {
			t.Fatal("迁移后插件记录丢失")
		}
		if p.Definition != "" {
			t.Fatal("旧的声明式定义应被清空")
		}
		if p.Dir != dir {
			t.Fatalf("插件目录未回填: %q", p.Dir)
		}
		// 骨架的 Upload 只会返回「尚未实现」，留着启用必然被调度到然后失败。
		if p.Enabled {
			t.Fatal("迁移后的骨架插件应停用，等待用户重写后手动启用")
		}
		if p.Status != store.StatusDisabled {
			t.Fatalf("迁移后应标记为已停用，实际状态 %s", p.Status)
		}
		if p.StatusMessage == "" {
			t.Fatal("迁移后应说明停用原因")
		}
		var found bool
		for _, c := range s.Changes {
			if c.PluginID == "old-up" && c.Action == "migrate" {
				found = true
			}
		}
		if !found {
			t.Fatal("迁移应留下审计记录")
		}
		return nil
	})

	// 重复迁移必须幂等：不会覆盖用户改过的 main.go。
	if err := WriteSourceFile(dir, "main.go", "package main\n// 用户重写\n"); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureBuiltin(); err != nil {
		t.Fatal(err)
	}
	content, err := ReadSourceFile(dir, "main.go")
	if err != nil {
		t.Fatal(err)
	}
	if content != "package main\n// 用户重写\n" {
		t.Fatalf("重复迁移覆盖了用户改动: %q", content)
	}
}

// TestEnsureBuiltinKeepsUserEdits 验证内置插件只在文件缺失时落盘，本地修改不会被覆盖。
func TestEnsureBuiltinKeepsUserEdits(t *testing.T) {
	b := Builtin{ID: "demo", Files: map[string]string{
		"plugin.json": `{"schema":2,"id":"demo","name":"Demo","version":"1.0.0"}`,
		"main.go":     "package main\n// 官方版本\n",
	}}
	m, st := newManager(t, []Builtin{b})
	if err := m.EnsureBuiltin(); err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(m.pluginsDir, "demo")
	if _, err := os.Stat(filepath.Join(dir, "plugin.json")); err != nil {
		t.Fatalf("内置插件应落盘 plugin.json: %v", err)
	}
	_ = st.View(func(s *store.State) error {
		p := store.PluginByID(s, "demo")
		if p == nil || !p.Enabled || p.Custom {
			t.Fatalf("内置插件记录异常: %+v", p)
		}
		if p.Dir != dir || p.ValidityHours != 0 {
			t.Fatalf("内置插件字段异常: %+v", p)
		}
		return nil
	})

	// 用户改一行 → 再次 EnsureBuiltin 不应覆盖。
	if err := WriteSourceFile(dir, "main.go", "package main\n// 我改过的\n"); err != nil {
		t.Fatal(err)
	}
	if err := m.EnsureBuiltin(); err != nil {
		t.Fatal(err)
	}
	content, _ := ReadSourceFile(dir, "main.go")
	if content != "package main\n// 我改过的\n" {
		t.Fatalf("内置插件源码被覆盖: %q", content)
	}
}

// TestCreateRejectsDuplicate 验证 ID 冲突与非法清单会被拒绝，且不留下半个目录。
func TestCreateRejectsDuplicate(t *testing.T) {
	m, _ := newManager(t, nil)
	spec := CreateSpec{ID: "dup", Name: "重复", Template: "blank"}
	if _, err := m.Create(context.Background(), spec, "tester"); err != nil {
		t.Fatalf("首次创建失败: %v", err)
	}
	if _, err := m.Create(context.Background(), spec, "tester"); err == nil {
		t.Fatal("重复 ID 应被拒绝")
	}
	if _, err := m.Create(context.Background(), CreateSpec{ID: "Bad ID", Name: "x"}, "tester"); err == nil {
		t.Fatal("非法 ID 应被拒绝")
	}
	if _, err := m.Create(context.Background(), CreateSpec{ID: "ok-id", Name: "x", Template: "nope"}, "tester"); err == nil {
		t.Fatal("未知模板应被拒绝")
	}
}

// TestViewKeepsParamsWhenDisabled 盯住一个数据丢失陷阱：
// 插件停用后视图曾把参数清空，用户再打开表单看到一片空白，随手保存就把配置抹了。
func TestViewKeepsParamsWhenDisabled(t *testing.T) {
	m, st := newManager(t, nil)
	dir := filepath.Join(m.pluginsDir, "off-up")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	mf := Manifest{
		Schema: ManifestSchema, ID: "off-up", Name: "停用的插件",
		Params: []ParamField{
			{Key: "endpoint", Label: "上传地址", Type: "url"},
			{Key: "token", Label: "令牌", Type: "secret", Sensitive: true},
		},
	}
	if err := SaveManifest(dir, mf); err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(s *store.State) error {
		s.Plugins = append(s.Plugins, store.Plugin{
			ID: "off-up", Kind: "off-up", Name: "停用的插件", Enabled: false, Custom: true, Dir: dir,
			Params: map[string]string{"endpoint": "https://api.acme.dev/put", "token": "enc:dGstc2VjcmV0"},
			Status: store.StatusDisabled, StatusMessage: "已停用",
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	v, err := m.Get("off-up")
	if err != nil {
		t.Fatal(err)
	}
	if got := v.Params["endpoint"]; got != "https://api.acme.dev/put" {
		t.Errorf("停用插件也应回显普通参数，实际 %v", got)
	}
	if s, _ := v.Params["token"].(string); s == "" || strings.Contains(s, "c2VjcmV0") {
		t.Errorf("敏感参数应脱敏回显，实际 %v", v.Params["token"])
	}
	if v.Warning != "" {
		t.Errorf("停用插件不该弹出临时凭据告警，实际: %s", v.Warning)
	}
}
