package plugin

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func newPluginDir(t *testing.T, id string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestValidateFileName 重点覆盖路径穿越与不可编辑扩展名。
func TestValidateFileName(t *testing.T) {
	ok := []string{"main.go", "helper.go", "README.md", "plugin.json", "notes.txt", "a.yaml", "go.mod"}
	for _, name := range ok {
		if err := ValidateFileName(name); err != nil {
			t.Fatalf("%q 应被接受: %v", name, err)
		}
	}
	bad := []string{
		"", "  ", "../evil.go", "a/b.go", `a\b.go`, ".hidden.go",
		"main.sh", "page.html", "lib.so", "x.exe",
		strings.Repeat("a", 70) + ".go",
	}
	for _, name := range bad {
		if err := ValidateFileName(name); err == nil {
			t.Fatalf("%q 应被拒绝", name)
		}
	}
}

// TestWriteSourceFileLimits 覆盖系统文件保护、单文件与总量上限、文件数上限。
func TestWriteSourceFileLimits(t *testing.T) {
	dir := newPluginDir(t, "p1")
	if err := WriteSourceFile(dir, "main.go", "package main\n"); err != nil {
		t.Fatal(err)
	}
	for _, sys := range []string{"plugin.json", "go.mod", "go.sum"} {
		if err := WriteSourceFile(dir, sys, "x"); err == nil || !strings.Contains(err.Error(), "系统维护") {
			t.Fatalf("%s 应不可改写: %v", sys, err)
		}
	}
	big := strings.Repeat("a", MaxSourceFileBytes+1)
	if err := WriteSourceFile(dir, "big.go", big); err == nil {
		t.Fatal("超过单文件上限应拒绝")
	}
	// 总量上限：单文件都合规，但累积超过 2 MB 时必须拒绝。
	chunk := strings.Repeat("b", MaxSourceFileBytes)
	var writeErr error
	for i := 0; i < 8; i++ {
		if err := WriteSourceFile(dir, fmt.Sprintf("chunk%d.go", i), chunk); err != nil {
			writeErr = err
			break
		}
	}
	if writeErr == nil || !strings.Contains(writeErr.Error(), "总大小") {
		t.Fatalf("超过总量上限应拒绝，实际 %v", writeErr)
	}
}

// TestSourceFileLifecycle 覆盖读、列、改名、删除与「至少保留一个 .go」。
func TestSourceFileLifecycle(t *testing.T) {
	dir := newPluginDir(t, "p2")
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	must(WriteSourceFile(dir, "main.go", "package main\nfunc main() {}\n"))
	must(WriteSourceFile(dir, "helper.go", "package main\n"))
	must(WriteSourceFile(dir, "README.md", "# hi"))
	must(os.MkdirAll(filepath.Join(dir, ".build"), 0o700))
	must(os.WriteFile(filepath.Join(dir, ".build", "stale"), []byte("bin"), 0o600))

	mf := Manifest{Schema: ManifestSchema, ID: "p2", Name: "P2", Entry: "main.go"}
	must(mf.Validate())
	must(SaveManifest(dir, mf))

	files, err := ListFiles(dir, mf)
	must(err)
	var names []string
	for _, f := range files {
		names = append(names, f.Name)
	}
	// .build 目录被忽略，Go 文件排在前面。
	if strings.Join(names, ",") != "helper.go,main.go,README.md,plugin.json" {
		t.Fatalf("文件列表不符: %v", names)
	}

	content, err := ReadSourceFile(dir, "helper.go")
	must(err)
	if content != "package main\n" {
		t.Fatalf("读取内容不符: %q", content)
	}
	if _, err := ReadSourceFile(dir, "missing.go"); err == nil {
		t.Fatal("不存在文件应报错")
	}

	must(RenameSourceFile(dir, "helper.go", "util.go"))
	if _, err := os.Stat(filepath.Join(dir, "util.go")); err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	if err := RenameSourceFile(dir, "util.go", "README.md"); err == nil {
		t.Fatal("改名到已存在文件应失败")
	}
	if err := RenameSourceFile(dir, "README.md", "plugin.json"); err == nil {
		t.Fatal("系统文件不应参与改名")
	}

	if err := DeleteSourceFile(dir, "plugin.json"); err == nil {
		t.Fatal("系统文件不可删除")
	}
	if err := DeleteSourceFile(dir, "README.md"); err != nil {
		t.Fatalf("删除普通文件失败: %v", err)
	}
	must(DeleteSourceFile(dir, "util.go"))
	if err := DeleteSourceFile(dir, "main.go"); err == nil || !strings.Contains(err.Error(), "至少保留") {
		t.Fatalf("删掉最后一个 .go 应被拒绝: %v", err)
	}
}

// TestCheckLayout 验证编译前的最低布局要求。
func TestCheckLayout(t *testing.T) {
	dir := newPluginDir(t, "p3")
	mf := Manifest{Schema: ManifestSchema, ID: "p3", Name: "P3", Entry: "main.go"}
	_ = mf.Validate()

	if err := CheckLayout(dir, mf); err == nil || !strings.Contains(err.Error(), "至少需要一个") {
		t.Fatalf("空目录应报错: %v", err)
	}
	_ = WriteSourceFile(dir, "other.go", "package main\n")
	if err := CheckLayout(dir, mf); err == nil || !strings.Contains(err.Error(), "入口文件") {
		t.Fatalf("缺少入口应报错: %v", err)
	}
	_ = WriteSourceFile(dir, "main.go", "package main\nfunc main() {}\n")
	if err := CheckLayout(dir, mf); err != nil {
		t.Fatalf("布局合法却报错: %v", err)
	}
}

// TestBuiltinTemplates 保证模板本身可用：有入口、参数合法、能被校验。
func TestBuiltinTemplates(t *testing.T) {
	tpls := BuiltinTemplates()
	if len(tpls) < 2 {
		t.Fatalf("模板数量异常: %d", len(tpls))
	}
	for _, tpl := range tpls {
		if tpl.Kind == "" || tpl.Name == "" || tpl.Description == "" {
			t.Fatalf("模板元信息缺失: %+v", tpl)
		}
		src, ok := tpl.Files["main.go"]
		if !ok || !strings.Contains(src, "sdk.Serve") {
			t.Fatalf("模板 %s 缺少可运行的 main.go", tpl.Kind)
		}
		mf := Manifest{
			Schema: ManifestSchema, ID: "tpl-" + tpl.Kind[:min(3, len(tpl.Kind))],
			Name: tpl.Name, Params: tpl.Params,
		}
		if err := mf.Validate(); err != nil {
			t.Fatalf("模板 %s 的参数不合法: %v", tpl.Kind, err)
		}
		if _, ok := TemplateByKind(tpl.Kind); !ok {
			t.Fatalf("TemplateByKind 找不到 %s", tpl.Kind)
		}
	}
	if _, ok := TemplateByKind("nope"); ok {
		t.Fatal("未知模板不应命中")
	}
}
