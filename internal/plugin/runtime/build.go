// Package runtime 负责把插件源码编译成可执行文件，并以子进程方式调用。
//
// 设计取舍：
//   - 编译而不是解释：插件是真正的 Go 程序，可用全部标准库与任意第三方库；
//   - 独立进程而不是 plugin 包：跨平台、崩溃隔离、不受 Go 版本 ABI 约束；
//   - 源码目录内自带的 go.mod 通过 replace 指向主服务落盘的 SDK，离线可编译。
package runtime

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

type Options struct {
	PluginsDir   string        // {DataDir}/plugins
	SDKDir       string        // {DataDir}/sdk/s3bridge-sdk
	CacheDir     string        // {DataDir}/cache
	GoBinary     string        // 可覆盖，默认从 PATH 查找
	BuildTimeout time.Duration // 默认 120s
	IdleTimeout  time.Duration // 子进程空闲回收，默认 10m
	CallTimeout  time.Duration // 单次调用默认超时，默认 60s
}

type Runtime struct {
	opts  Options
	goBin string

	buildMu sync.Mutex // 构建串行：共享 GOCACHE，并行收益有限且更容易打满内存
	mu      sync.Mutex
	procs   map[string]*process

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
}

type BuildResult struct {
	Binary     string `json:"binary"`
	Output     string `json:"output"`
	DurationMs int64  `json:"durationMs"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Cached     bool   `json:"cached"`
}

// New 创建运行时：落盘 SDK、探测 Go 工具链、启动空闲回收协程。
func New(opts Options) (*Runtime, error) {
	if opts.PluginsDir == "" {
		return nil, fmt.Errorf("runtime: 缺少插件目录")
	}
	if opts.SDKDir == "" {
		opts.SDKDir = filepath.Join(filepath.Dir(opts.PluginsDir), "sdk", "s3bridge-sdk")
	}
	if opts.CacheDir == "" {
		opts.CacheDir = filepath.Join(filepath.Dir(opts.PluginsDir), "cache")
	}
	if opts.BuildTimeout <= 0 {
		opts.BuildTimeout = 120 * time.Second
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 10 * time.Minute
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = 60 * time.Second
	}
	for _, dir := range []string{opts.PluginsDir, opts.SDKDir, opts.CacheDir} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, fmt.Errorf("创建目录 %s: %w", dir, err)
		}
	}
	if err := extractSDK(opts.SDKDir); err != nil {
		return nil, err
	}
	r := &Runtime{opts: opts, procs: map[string]*process{}, stop: make(chan struct{})}
	if opts.GoBinary != "" {
		r.goBin = opts.GoBinary
	} else if p, err := exec.LookPath("go"); err == nil {
		r.goBin = p
	}
	r.wg.Add(1)
	go r.reap()
	return r, nil
}

// Toolchain 返回可用的 Go 工具链路径；未安装时为空。
func (r *Runtime) Toolchain() string { return r.goBin }

// SDKPath 返回落盘的 SDK 目录，供插件 go.mod 的 replace 使用。
func (r *Runtime) SDKPath() string { return r.opts.SDKDir }

// Options 返回只读配置。
func (r *Runtime) Options() Options { return r.opts }

// Close 停止回收协程并杀掉所有子进程。
func (r *Runtime) Close() error {
	r.stopOnce.Do(func() { close(r.stop) })
	r.wg.Wait()
	r.mu.Lock()
	procs := make([]*process, 0, len(r.procs))
	for _, p := range r.procs {
		procs = append(procs, p)
	}
	r.procs = map[string]*process{}
	r.mu.Unlock()
	for _, p := range procs {
		p.kill()
	}
	return nil
}

// Build 编译插件目录。目录必须已包含 go.mod（由 plugin.EnsureGoMod 生成）。
func (r *Runtime) Build(ctx context.Context, id, dir string) (*BuildResult, error) {
	if r.goBin == "" {
		return nil, fmt.Errorf("未找到 go 命令：请安装 Go 工具链，或把已编译的插件二进制放到 %s", binaryPath(dir))
	}
	r.buildMu.Lock()
	defer r.buildMu.Unlock()

	out := binaryPath(dir)
	if err := os.MkdirAll(filepath.Dir(out), 0o700); err != nil {
		return nil, err
	}
	start := time.Now()
	buildCtx, cancel := context.WithTimeout(ctx, r.opts.BuildTimeout)
	defer cancel()

	logOut, err := r.runGo(buildCtx, dir, "build", "-trimpath", "-buildvcs=false", "-o", out, ".")
	if err != nil && needsTidy(logOut) {
		// 插件引入了新的第三方依赖：先补齐 go.mod/go.sum 再编译一次。
		tidyOut, tidyErr := r.runGo(buildCtx, dir, "mod", "tidy")
		logOut = strings.TrimSpace(logOut + "\n" + tidyOut)
		if tidyErr == nil {
			logOut2, err2 := r.runGo(buildCtx, dir, "build", "-trimpath", "-buildvcs=false", "-o", out, ".")
			logOut = strings.TrimSpace(logOut + "\n" + logOut2)
			err = err2
		}
	}
	if buildCtx.Err() == context.DeadlineExceeded {
		return &BuildResult{Output: trimLog(logOut), DurationMs: time.Since(start).Milliseconds()},
			fmt.Errorf("编译超时（>%s）", r.opts.BuildTimeout)
	}
	if err != nil {
		return &BuildResult{Output: trimLog(logOut), DurationMs: time.Since(start).Milliseconds()},
			fmt.Errorf("%w: %s", ErrBuild, firstLines(trimLog(logOut), 6))
	}
	info, err := os.Stat(out)
	if err != nil {
		return nil, fmt.Errorf("编译产物缺失: %w", err)
	}
	sum, err := hashFile(out)
	if err != nil {
		return nil, err
	}
	// 编译成功后重启旧进程，确保运行的是新代码。
	r.Drop(id)
	return &BuildResult{
		Binary: out, Output: trimLog(logOut), DurationMs: time.Since(start).Milliseconds(),
		SHA256: sum, Size: info.Size(),
	}, nil
}

// EnsureBuilt 在二进制缺失时补编译，用于重启后首次调用。
func (r *Runtime) EnsureBuilt(ctx context.Context, id, dir string) error {
	if fi, err := os.Stat(binaryPath(dir)); err == nil && fi.Size() > 0 {
		return nil
	}
	_, err := r.Build(ctx, id, dir)
	return err
}

func (r *Runtime) runGo(ctx context.Context, dir string, args ...string) (string, error) {
	cmd := exec.CommandContext(ctx, r.goBin, args...)
	cmd.Dir = dir
	cmd.Env = r.buildEnv()
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func (r *Runtime) buildEnv() []string {
	env := os.Environ()
	// 过滤掉可能干扰交叉/缓存的环境变量，再追加固定值。
	drop := map[string]bool{
		"GOOS": true, "GOARCH": true, "GOFLAGS": true, "GOTOOLCHAIN": true,
		"GOCACHE": true, "GOMODCACHE": true, "GOPATH": true, "GOWORK": true, "CGO_ENABLED": true,
	}
	out := make([]string, 0, len(env)+10)
	for _, kv := range env {
		k := kv
		if i := strings.IndexByte(kv, '='); i > 0 {
			k = kv[:i]
		}
		if drop[k] {
			continue
		}
		out = append(out, kv)
	}
	out = append(out,
		"CGO_ENABLED=0",
		"GO111MODULE=on",
		"GOTOOLCHAIN=local",
		"GOFLAGS=-mod=mod",
		"GOWORK=off",
		"GOCACHE="+filepath.Join(r.opts.CacheDir, "go-build"),
		"GOMODCACHE="+filepath.Join(r.opts.CacheDir, "gomod"),
		"GOPATH="+filepath.Join(r.opts.CacheDir, "gopath"),
	)
	if os.Getenv("GOPROXY") == "" {
		out = append(out, "GOPROXY=https://proxy.golang.org,direct")
	}
	return out
}

// BinaryPath 返回插件编译产物的绝对路径（Windows 带 .exe）。
func BinaryPath(dir string) string { return binaryPath(dir) }

func binaryPath(dir string) string {
	name := "plugin"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, ".build", name)
}

func hashFile(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func needsTidy(out string) bool {
	for _, s := range []string{
		"missing go.sum entry", "updates to go.mod needed",
		"no required module provides", "cannot find module providing",
	} {
		if strings.Contains(out, s) {
			return true
		}
	}
	return false
}

func trimLog(s string) string {
	s = strings.TrimSpace(s)
	const max = 32 << 10
	if len(s) > max {
		s = s[len(s)-max:]
	}
	return s
}

func firstLines(s string, n int) string {
	lines := strings.Split(s, "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	return strings.Join(lines, " | ")
}
