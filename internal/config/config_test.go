package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestLoadNormalizesPaths 验证数据目录及其派生子目录一律为绝对路径。
// 插件以子进程运行（工作目录是插件文件夹），相对路径在那边会解析到错误位置，
// 因此这条不变量必须被守住：它同时影响上传临时文件与 go.mod 的 replace 目标。
func TestLoadNormalizesPaths(t *testing.T) {
	work := t.TempDir()
	old, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(old) })

	t.Setenv(EnvDataDir, "rel-data")
	t.Setenv(EnvMasterKey, strings.Repeat("k", 32))
	t.Setenv(EnvAddr, "127.0.0.1:0")

	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	for name, path := range map[string]string{
		"DataDir": cfg.DataDir, "ObjectDir": cfg.ObjectDir, "ObsDir": cfg.ObsDir,
		"PluginsDir": cfg.PluginsDir, "PluginCacheDir": cfg.PluginCacheDir,
		"ThumbDir": cfg.ThumbDir,
	} {
		if !filepath.IsAbs(path) {
			t.Fatalf("%s 不是绝对路径: %q", name, path)
		}
	}
	if !strings.HasPrefix(cfg.PluginsDir, cfg.DataDir) || !strings.HasPrefix(cfg.ObjectDir, cfg.DataDir) {
		t.Fatalf("子目录不在数据目录下: %+v", cfg)
	}
	// 相对路径解析到当前工作目录，而不是二进制所在目录。
	if want := filepath.Join(work, "rel-data"); cfg.DataDir != want {
		t.Fatalf("DataDir=%q want=%q", cfg.DataDir, want)
	}
}
