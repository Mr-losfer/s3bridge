// Package runtime 负责以子进程方式运行插件，并管理进程生命周期。
//
// 设计取舍：
//   - 解释执行而不是编译：服务端内嵌 Go 解释器，用户保存即生效，不需要安装 Go 工具链；
//   - 独立进程而不是同进程：解释执行的死循环/栈溢出无法安全回收，独立进程可以强杀；
//   - 进程由主程序自身启动（__plugin_runner__ 子命令），所以部署只有一个二进制。
package runtime

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin/engine"
)

type Options struct {
	PluginsDir  string        // {DataDir}/plugins
	IdleTimeout time.Duration // 子进程空闲回收，默认 10m
	CallTimeout time.Duration // 单次调用默认超时，默认 60s
	LoadTimeout time.Duration // 加载并握手的超时，默认 30s
	SelfBinary  string        // 覆盖 runner 可执行文件，默认取当前进程
}

type Runtime struct {
	opts Options
	self string

	mu    sync.Mutex
	procs map[string]*process

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
}

// LoadResult 描述一次加载的结果，字段名沿用「构建」时代的语义以便前端平滑升级。
type LoadResult struct {
	Output     string `json:"output"`
	DurationMs int64  `json:"durationMs"`
	SHA256     string `json:"sha256"`
	Size       int64  `json:"size"`
	Files      int    `json:"files"`
	Entry      string `json:"entry"`
}

// New 创建运行时并启动空闲回收协程。
func New(opts Options) (*Runtime, error) {
	if opts.PluginsDir == "" {
		return nil, fmt.Errorf("runtime: 缺少插件目录")
	}
	if opts.IdleTimeout <= 0 {
		opts.IdleTimeout = 10 * time.Minute
	}
	if opts.CallTimeout <= 0 {
		opts.CallTimeout = 60 * time.Second
	}
	if opts.LoadTimeout <= 0 {
		opts.LoadTimeout = 30 * time.Second
	}
	if err := os.MkdirAll(opts.PluginsDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建目录 %s: %w", opts.PluginsDir, err)
	}
	self := opts.SelfBinary
	if self == "" {
		exe, err := os.Executable()
		if err != nil {
			return nil, fmt.Errorf("定位自身可执行文件: %w", err)
		}
		self = exe
	}
	r := &Runtime{opts: opts, self: self, procs: map[string]*process{}, stop: make(chan struct{})}
	r.wg.Add(1)
	go r.reap()
	return r, nil
}

// Toolchain 返回执行引擎标识。解释执行不需要外部工具链，恒为 embedded。
func (r *Runtime) Toolchain() string { return "embedded" }

// Options 返回只读配置。
func (r *Runtime) Options() Options { return r.opts }

// SelfBinary 返回 runner 使用的可执行文件路径。
func (r *Runtime) SelfBinary() string { return r.self }

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

// Load 重新加载插件源码：改写旧入口 → 启动新的 runner 进程 → ping 握手校验。
//
// 加载成功后旧进程已被替换，下一次调用直接跑新代码，实现保存即生效。
// 加载失败不会影响旧进程之外的任何状态，错误信息直接来自解释器，带 文件:行:列。
func (r *Runtime) Load(ctx context.Context, id, dir string) (*LoadResult, error) {
	start := time.Now()
	if changed, err := engine.NormalizeEntry(dir); err != nil {
		wrapped := fmt.Errorf("%w: %v", engine.ErrLoad, err)
		return &LoadResult{Output: wrapped.Error(), DurationMs: time.Since(start).Milliseconds()}, wrapped
	} else if changed {
		slog.Info("已自动升级插件入口为注册式", "plugin", id)
	}

	r.Drop(id)
	p, err := r.start(ctx, id, dir)
	if err != nil {
		return &LoadResult{Output: err.Error(), DurationMs: time.Since(start).Milliseconds()}, err
	}
	return &LoadResult{
		Output:     p.log.String(),
		DurationMs: time.Since(start).Milliseconds(),
		SHA256:     p.sha,
		Size:       int64(p.sourceSize),
		Files:      p.files,
		Entry:      p.entry,
	}, nil
}

// EnsureLoaded 确保插件进程已就绪；进程已在运行则直接返回。
func (r *Runtime) EnsureLoaded(ctx context.Context, id, dir string) error {
	r.mu.Lock()
	p := r.procs[id]
	r.mu.Unlock()
	if p != nil && !p.dead.Load() {
		return nil
	}
	_, err := r.start(ctx, id, dir)
	return err
}

// dirs 便于日志与诊断。
func (r *Runtime) runnerArgs(dir string) []string {
	return []string{"__plugin_runner__", dir}
}

func absDir(dir string) string {
	if abs, err := filepath.Abs(dir); err == nil {
		return abs
	}
	return dir
}

func trimOutput(s string) string {
	s = strings.TrimSpace(s)
	const max = 32 << 10
	if len(s) > max {
		s = s[len(s)-max:]
	}
	return s
}
