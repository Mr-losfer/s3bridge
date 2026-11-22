package runtime

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin/engine"
	"github.com/Mr-losfer/s3bridge/internal/plugin/sdk"
)

// TestMain 让测试二进制自己充当 runner：生产环境里 runner 是主程序，
// 这样测试不必先构建一个可执行文件，也不依赖 Go 工具链。
func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "__plugin_runner__" {
		if err := engine.Run(os.Args[2], os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

const rtPlugin = `package main

import (
	"context"
	"fmt"

	"github.com/Mr-losfer/s3bridge/sdk"
)

type plugin struct{}

func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
	if cfg["fail"] == "1" {
		return nil, fmt.Errorf("上游拒绝")
	}
	return &sdk.Output{URL: "https://cdn.example.com/" + in.FileName, FileSize: in.Size}, nil
}

func (plugin) Check(ctx context.Context, cfg sdk.Config) error { return nil }

func main() { sdk.Serve(plugin{}.Upload, plugin{}.Check) }
`

const slowPlugin = `package main

import (
	"context"
	"time"

	"github.com/Mr-losfer/s3bridge/sdk"
)

type plugin struct{}

func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
	time.Sleep(10 * time.Second)
	return &sdk.Output{URL: "https://slow"}, nil
}

func (plugin) Check(ctx context.Context, cfg sdk.Config) error { return nil }

func main() { sdk.Serve(plugin{}.Upload, plugin{}.Check) }
`

func newRuntime(t *testing.T) *Runtime {
	t.Helper()
	dir := t.TempDir()
	rt, err := New(Options{
		PluginsDir:  filepath.Join(dir, "plugins"),
		LoadTimeout: 10 * time.Second,
		CallTimeout: 5 * time.Second,
		IdleTimeout: time.Minute,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = rt.Close() })
	return rt
}

func writeSource(t *testing.T, rt *Runtime, id, src string) string {
	t.Helper()
	dir := filepath.Join(rt.Options().PluginsDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(src), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func TestLoadAndCall(t *testing.T) {
	rt := newRuntime(t)
	dir := writeSource(t, rt, "demo", rtPlugin)

	info, err := rt.Load(context.Background(), "demo", dir)
	if err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	if info.SHA256 == "" || info.Files != 1 || info.Entry != "main.go" {
		t.Fatalf("加载结果不完整: %+v", info)
	}
	if info.DurationMs > 5000 {
		t.Fatalf("解释器加载不应这么慢: %dms", info.DurationMs)
	}

	resp, err := rt.Call(context.Background(), "demo", dir, sdk.Request{
		ID: "c1", Method: "upload", Input: &sdk.Input{FileName: "a.png", Size: 9},
	})
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if resp.Result == nil || resp.Result.URL != "https://cdn.example.com/a.png" {
		t.Fatalf("返回值不对: %+v", resp.Result)
	}

	// 业务错误不应杀进程：拿到的是错误消息而不是 ErrCrashed。
	_, err = rt.Call(context.Background(), "demo", dir, sdk.Request{
		ID: "c2", Method: "upload", Config: sdk.Config{"fail": "1"},
		Input: &sdk.Input{FileName: "b.png"},
	})
	if err == nil || !strings.Contains(err.Error(), "上游拒绝") {
		t.Fatalf("业务错误应原样返回，实际: %v", err)
	}
	if _, err := rt.Call(context.Background(), "demo", dir, sdk.Request{
		ID: "c3", Method: "ping",
	}); err != nil {
		t.Fatalf("业务错误后进程应仍然可用: %v", err)
	}
}

func TestLoadRejectsBrokenSource(t *testing.T) {
	rt := newRuntime(t)
	dir := writeSource(t, rt, "broken", "package main\n\nfunc main() {\n")

	_, err := rt.Load(context.Background(), "broken", dir)
	if err == nil {
		t.Fatal("语法错误应导致加载失败")
	}
	if !errors.Is(err, ErrLoad) {
		t.Fatalf("错误应可被 ErrLoad 识别，实际: %v", err)
	}
	if !strings.Contains(err.Error(), "main.go:") {
		t.Fatalf("错误应带文件名与行号，实际: %v", err)
	}
}

func TestCallTimeoutKillsProcess(t *testing.T) {
	rt := newRuntime(t)
	dir := writeSource(t, rt, "slow", slowPlugin)
	if _, err := rt.Load(context.Background(), "slow", dir); err != nil {
		t.Fatal(err)
	}

	start := time.Now()
	_, err := rt.Call(context.Background(), "slow", dir, sdk.Request{
		ID: "t1", Method: "upload", Input: &sdk.Input{FileName: "a.png"}, TimeoutSecs: 1,
	})
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("应超时，实际: %v", err)
	}
	if d := time.Since(start); d > 3*time.Second {
		t.Fatalf("超时后应立即返回，实际等了 %s", d)
	}
}

func TestEnsureLoadedCaches(t *testing.T) {
	rt := newRuntime(t)
	dir := writeSource(t, rt, "demo", rtPlugin)

	if err := rt.EnsureLoaded(context.Background(), "demo", dir); err != nil {
		t.Fatal(err)
	}
	sha := rt.LoadSHA("demo")
	if sha == "" {
		t.Fatal("加载后应能读到源码 SHA")
	}
	// 再调用一次不应重启进程（SHA 不变，进程存活）。
	if err := rt.EnsureLoaded(context.Background(), "demo", dir); err != nil {
		t.Fatal(err)
	}
	if rt.LoadSHA("demo") != sha {
		t.Fatal("重复 EnsureLoaded 不应重新加载")
	}
}

func TestLoadHotSwap(t *testing.T) {
	rt := newRuntime(t)
	dir := writeSource(t, rt, "demo", rtPlugin)
	if _, err := rt.Load(context.Background(), "demo", dir); err != nil {
		t.Fatal(err)
	}
	first := rt.LoadSHA("demo")

	updated := strings.Replace(rtPlugin, "https://cdn.example.com/", "https://new.example.com/", 1)
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(updated), 0o600); err != nil {
		t.Fatal(err)
	}
	info, err := rt.Load(context.Background(), "demo", dir)
	if err != nil {
		t.Fatal(err)
	}
	if info.SHA256 == first {
		t.Fatal("改源码后 SHA 应变化")
	}
	resp, err := rt.Call(context.Background(), "demo", dir, sdk.Request{
		ID: "c1", Method: "upload", Input: &sdk.Input{FileName: "a.png"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if resp.Result.URL != "https://new.example.com/a.png" {
		t.Fatalf("应运行新代码，实际: %s", resp.Result.URL)
	}
}

func TestLoadUpgradesLegacyEntry(t *testing.T) {
	rt := newRuntime(t)
	legacy := strings.Replace(rtPlugin, "sdk.Serve(plugin{}.Upload, plugin{}.Check)", "sdk.Serve(plugin{})", 1)
	dir := writeSource(t, rt, "legacy", legacy)

	if _, err := rt.Load(context.Background(), "legacy", dir); err != nil {
		t.Fatalf("旧入口应自动升级并加载成功: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(raw), "(plugin{}).Upload") {
		t.Fatalf("旧入口未被改写:\n%s", raw)
	}
}

func TestEnsureLoadedUpgradesLegacyEntry(t *testing.T) {
	// 回归：启动预加载走 EnsureLoaded → start，曾绕过 NormalizeEntry，
	// 升级后的旧插件会在预加载阶段直接报
	// main.go:237:25: cannot use type main.plugin as type sdk.UploadFunc。
	rt := newRuntime(t)
	legacy := strings.Replace(rtPlugin, "sdk.Serve(plugin{}.Upload, plugin{}.Check)", "sdk.Serve(plugin{})", 1)
	dir := writeSource(t, rt, "legacy-ensure", legacy)

	if err := rt.EnsureLoaded(context.Background(), "legacy-ensure", dir); err != nil {
		t.Fatalf("预加载路径也应自动升级旧入口: %v", err)
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(raw), "(plugin{}).Upload") {
		t.Fatalf("预加载路径未改写旧入口:\n%s", raw)
	}
}

func TestDropRestartsProcess(t *testing.T) {
	rt := newRuntime(t)
	dir := writeSource(t, rt, "demo", rtPlugin)
	if _, err := rt.Load(context.Background(), "demo", dir); err != nil {
		t.Fatal(err)
	}
	rt.Drop("demo")
	if rt.LoadSHA("demo") != "" {
		t.Fatal("Drop 后不应再报告已加载")
	}
	if err := rt.EnsureLoaded(context.Background(), "demo", dir); err != nil {
		t.Fatalf("Drop 后应能重新加载: %v", err)
	}
}

func TestCallWithoutSource(t *testing.T) {
	rt := newRuntime(t)
	dir := filepath.Join(rt.Options().PluginsDir, "empty")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	_, err := rt.Call(context.Background(), "empty", dir, sdk.Request{ID: "c", Method: "ping"})
	if !errors.Is(err, ErrLoad) {
		t.Fatalf("没有源码时应报加载错误，实际: %v", err)
	}
}
