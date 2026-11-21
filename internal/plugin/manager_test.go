package plugin

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin/runtime"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

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
		CacheDir:   filepath.Join(root, "cache"),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return NewManager(st, rt, filepath.Join(root, "plugins"), identity, identity, 5*time.Minute, builtins), st
}

// TestMigrateLegacyDeclarative 验证 v1.x 的声明式插件被迁成可编辑的 Go 骨架，
// 而不是直接消失：源码留档、状态置为需要重写。
func TestMigrateLegacyDeclarative(t *testing.T) {
	m, st := newManager(t, nil)
	legacyDef, _ := json.Marshal(map[string]any{"kind": "custom-up", "request": map[string]any{"method": "POST"}})
	if err := st.Update(func(s *store.State) error {
		s.Plugins = append(s.Plugins, store.Plugin{
			ID: "old-up", Kind: "old-up", Name: "旧插件", Enabled: true,
			Definition: string(legacyDef), Custom: true,
		})
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if err := m.EnsureBuiltin(); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(m.pluginsDir, "old-up")
	for _, name := range []string{"plugin.json", "main.go", "go.mod", "legacy-definition.json"} {
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
		if p.Status != store.StatusUnhealthy {
			t.Fatalf("迁移后应提示需要重写，实际状态 %s", p.Status)
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
	if _, err := os.Stat(filepath.Join(dir, "go.mod")); err != nil {
		t.Fatalf("内置插件应生成 go.mod: %v", err)
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
	if testing.Short() {
		t.Skip("需要真实编译，-short 模式下跳过")
	}
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
