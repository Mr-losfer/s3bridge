package runtime

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin/sdk"
)

// testPlugin 是一个最小可用插件：读文件、回显输出，并支持「慢调用」分支用于验证超时杀进程。
const testPlugin = `package main

import (
	"context"
	"fmt"
	"os"
	"time"

	"github.com/Mr-losfer/s3bridge/sdk"
)

type plugin struct{}

func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
	if cfg["fail"] == "1" {
		return nil, fmt.Errorf("按参数失败")
	}
	if cfg["mode"] == "slow" {
		time.Sleep(5 * time.Second) // 故意忽略 ctx，验证运行时能强杀进程
	}
	data, err := os.ReadFile(in.Path)
	if err != nil {
		return nil, err
	}
	return &sdk.Output{
		URL: "mem://" + in.FileName, FileName: in.FileName, FileSize: int64(len(data)),
		Extra: map[string]any{"pid": os.Getpid()},
	}, nil
}

func (plugin) Check(ctx context.Context, cfg sdk.Config) error { return nil }

func main() { sdk.Serve(plugin{}) }
`

// 每次编译都很贵：整个测试包共用一个运行时与 GOCACHE，靠不同插件 ID 隔离。
var (
	sharedOnce sync.Once
	sharedRt   *Runtime
	sharedRoot string
	sharedErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if sharedRt != nil {
		_ = sharedRt.Close()
	}
	if sharedRoot != "" {
		_ = os.RemoveAll(sharedRoot)
	}
	os.Exit(code)
}

func newTestRuntime(t *testing.T) *Runtime {
	t.Helper()
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("未安装 Go 工具链，跳过运行时测试")
	}
	if testing.Short() {
		t.Skip("编译插件较慢，-short 模式下跳过")
	}
	sharedOnce.Do(func() {
		root, err := os.MkdirTemp("", "s3bridge-runtime-")
		if err != nil {
			sharedErr = err
			return
		}
		sharedRoot = root
		sharedRt, sharedErr = New(Options{
			PluginsDir:   filepath.Join(root, "plugins"),
			CacheDir:     filepath.Join(root, "cache"),
			BuildTimeout: 90 * time.Second,
			IdleTimeout:  time.Minute,
		})
	})
	if sharedErr != nil {
		t.Fatal(sharedErr)
	}
	return sharedRt
}

// writePlugin 落盘一个可编译的插件目录（go.mod 指向运行时解出的 SDK）。
func writePlugin(t *testing.T, rt *Runtime, id, source string) string {
	t.Helper()
	dir := filepath.Join(rt.Options().PluginsDir, id)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	gomod := "module s3bridge.plugin/" + id + "\n\ngo 1.21\n\n" +
		"require github.com/Mr-losfer/s3bridge/sdk v0.0.0\n\n" +
		"replace github.com/Mr-losfer/s3bridge/sdk => " + filepath.ToSlash(rt.SDKPath()) + "\n"
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(gomod), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(source), 0o600); err != nil {
		t.Fatal(err)
	}
	return dir
}

func writeFile(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte("DATA"), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestBuildAndCall 覆盖「编译 → 子进程调用 → 复用进程」的主链路。
func TestBuildAndCall(t *testing.T) {
	rt := newTestRuntime(t)
	dir := writePlugin(t, rt, "t1", testPlugin)

	res, err := rt.Build(context.Background(), "t1", dir)
	if err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	if res.Size <= 0 || len(res.SHA256) != 64 {
		t.Fatalf("编译结果异常: %+v", res)
	}
	if res.Binary != BinaryPath(dir) {
		t.Fatalf("产物路径不符: %s vs %s", res.Binary, BinaryPath(dir))
	}
	if runtime.GOOS == "windows" && !strings.HasSuffix(res.Binary, ".exe") {
		t.Fatalf("Windows 产物应带 .exe: %s", res.Binary)
	}

	call := func(cfg sdk.Config, timeout int) (sdk.Response, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return rt.Call(ctx, "t1", dir, sdk.Request{
			ID: "c1", Method: "upload", Config: cfg, TimeoutSecs: timeout,
			Input: &sdk.Input{FileName: "a.png", MimeType: "image/png", Size: 4, Path: writeFile(t, "a.png")},
		})
	}

	resp, err := call(sdk.Config{}, 5)
	if err != nil {
		t.Fatalf("调用失败: %v", err)
	}
	if resp.Result == nil || resp.Result.FileName != "a.png" || resp.Result.FileSize != 4 {
		t.Fatalf("返回结果不符: %+v", resp.Result)
	}
	firstPID := resp.Result.Extra["pid"]

	// 第二次调用应复用同一个进程（不会因为重启导致 pid 变化）。
	resp2, err := call(sdk.Config{}, 5)
	if err != nil {
		t.Fatalf("第二次调用失败: %v", err)
	}
	if resp2.Result.Extra["pid"] != firstPID {
		t.Fatalf("进程未复用: %v → %v", firstPID, resp2.Result.Extra["pid"])
	}

	// 业务错误必须原样透出，且不杀进程。
	if _, err := call(sdk.Config{"fail": "1"}, 5); err == nil || !strings.Contains(err.Error(), "按参数失败") {
		t.Fatalf("业务错误未透出: %v", err)
	}
	if resp3, err := call(sdk.Config{}, 5); err != nil || resp3.Result.Extra["pid"] != firstPID {
		t.Fatalf("业务错误后进程不应重启: %v %+v", err, resp3.Result)
	}
}

// TestCallTimeoutKillsProcess 验证超时会杀掉进程，下次调用自动重启并正常工作。
func TestCallTimeoutKillsProcess(t *testing.T) {
	rt := newTestRuntime(t)
	dir := writePlugin(t, rt, "t2", testPlugin)
	if _, err := rt.Build(context.Background(), "t2", dir); err != nil {
		t.Fatalf("编译失败: %v", err)
	}
	input := &sdk.Input{FileName: "a.png", Size: 4, Path: writeFile(t, "a.png")}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	start := time.Now()
	_, err := rt.Call(ctx, "t2", dir, sdk.Request{
		ID: "slow", Method: "upload", Config: sdk.Config{"mode": "slow"},
		TimeoutSecs: 1, Input: input,
	})
	if err == nil {
		t.Fatal("慢调用应超时")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("错误应为 ErrTimeout: %v", err)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("超时控制未生效，耗时 %s", elapsed)
	}

	// 进程已被杀掉，下一次调用要能自动重启。
	resp, err := rt.Call(context.Background(), "t2", dir, sdk.Request{
		ID: "after", Method: "upload", TimeoutSecs: 5, Input: input,
	})
	if err != nil {
		t.Fatalf("超时后重启失败: %v", err)
	}
	if resp.Result == nil || resp.Result.FileName != "a.png" {
		t.Fatalf("重启后结果异常: %+v", resp.Result)
	}
}

// TestBuildFailureKeepsOutput 验证编译错误会带回编译器原始输出，便于 Agent 自我修复。
func TestBuildFailureKeepsOutput(t *testing.T) {
	rt := newTestRuntime(t)
	dir := writePlugin(t, rt, "t3", "package main\n\nfunc main() { oops() }\n")

	res, err := rt.Build(context.Background(), "t3", dir)
	if err == nil {
		t.Fatal("源码有误应编译失败")
	}
	if !errors.Is(err, ErrBuild) {
		t.Fatalf("错误应可用 ErrBuild 判定: %v", err)
	}
	if res == nil || !strings.Contains(res.Output, "oops") {
		t.Fatalf("应带回编译器输出: %+v", res)
	}
	// 源码必须保留，方便下一轮修复。
	if _, statErr := os.Stat(filepath.Join(dir, "main.go")); statErr != nil {
		t.Fatalf("编译失败不应删除源码: %v", statErr)
	}
}

// TestEnsureBuiltCaches 验证已就绪的产物不会重复编译。
func TestEnsureBuiltCaches(t *testing.T) {
	rt := newTestRuntime(t)
	dir := writePlugin(t, rt, "t4", testPlugin)
	if err := rt.EnsureBuilt(context.Background(), "t4", dir); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(BinaryPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(10 * time.Millisecond)
	if err := rt.EnsureBuilt(context.Background(), "t4", dir); err != nil {
		t.Fatal(err)
	}
	after, err := os.Stat(BinaryPath(dir))
	if err != nil {
		t.Fatal(err)
	}
	if !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("产物已就绪时不应重新编译")
	}
}

func TestBuildWithoutToolchain(t *testing.T) {
	root := t.TempDir()
	rt, err := New(Options{PluginsDir: filepath.Join(root, "p"), CacheDir: filepath.Join(root, "c")})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()
	rt.goBin = "" // 模拟未安装 Go
	dir := writePlugin(t, rt, "t5", testPlugin)
	if _, err := rt.Build(context.Background(), "t5", dir); err == nil ||
		!strings.Contains(err.Error(), "未找到 go 命令") {
		t.Fatalf("缺少工具链时应给出明确提示: %v", err)
	}
	if _, err := rt.Call(context.Background(), "t5", dir, sdk.Request{Method: "ping"}); err == nil {
		t.Fatal("没有产物时调用应失败")
	}
}

// TestBuildEnvIsolation 验证编译环境不会被外部 GOFLAGS/GOWORK 等污染。
func TestBuildEnvIsolation(t *testing.T) {
	root := t.TempDir()
	rt, err := New(Options{PluginsDir: filepath.Join(root, "p"), CacheDir: filepath.Join(root, "c")})
	if err != nil {
		t.Fatal(err)
	}
	defer rt.Close()

	t.Setenv("GOFLAGS", "-mod=vendor")
	t.Setenv("GOWORK", "/tmp/whatever")
	t.Setenv("CGO_ENABLED", "1")
	t.Setenv("GOCACHE", "/tmp/evil-cache")

	env := map[string]string{}
	for _, kv := range rt.buildEnv() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			env[kv[:i]] = kv[i+1:]
		}
	}
	want := map[string]string{
		"CGO_ENABLED": "0",
		"GOFLAGS":     "-mod=mod",
		"GOWORK":      "off",
		"GOTOOLCHAIN": "local",
		"GOCACHE":     filepath.Join(rt.Options().CacheDir, "go-build"),
		"GOMODCACHE":  filepath.Join(rt.Options().CacheDir, "gomod"),
		"GOPATH":      filepath.Join(rt.Options().CacheDir, "gopath"),
	}
	for k, v := range want {
		if env[k] != v {
			t.Fatalf("%s=%q，期望 %q", k, env[k], v)
		}
	}
}
