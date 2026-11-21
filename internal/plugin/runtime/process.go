package runtime

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin/sdk"
)

var (
	// ErrBuild 表示编译失败，原始日志在 BuildResult.Output 中。
	ErrBuild = errors.New("插件编译失败")
	// ErrTimeout 表示调用超过插件超时时间，进程已被杀掉。
	ErrTimeout = errors.New("插件调用超时")
	// ErrCrashed 表示插件进程异常退出。
	ErrCrashed = errors.New("插件进程异常退出")
)

type process struct {
	id   string
	cmd  *exec.Cmd
	in   *bufio.Writer
	out  *bufio.Reader
	errs *ringBuf
	done chan struct{}

	mu       sync.Mutex // 串行化同一插件的调用
	lastUsed atomic.Int64
	dead     atomic.Bool
}

// Call 向插件进程发送一次调用。同一插件的调用串行执行（插件无需自己加锁），
// 超时会杀掉进程并在下次调用时自动重启。
func (r *Runtime) Call(ctx context.Context, id, dir string, req sdk.Request) (sdk.Response, error) {
	p, err := r.proc(id, dir)
	if err != nil {
		return sdk.Response{}, err
	}
	timeout := time.Duration(req.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = r.opts.CallTimeout
	}
	if dl, ok := ctx.Deadline(); ok {
		if d := time.Until(dl); d < timeout {
			timeout = d
		}
	}

	p.mu.Lock()
	defer p.mu.Unlock()
	if p.dead.Load() {
		return sdk.Response{}, ErrCrashed
	}
	p.lastUsed.Store(time.Now().UnixNano())
	return r.callLocked(ctx, p, req, timeout)
}

func (r *Runtime) callLocked(ctx context.Context, p *process, req sdk.Request, timeout time.Duration) (sdk.Response, error) {
	raw, err := json.Marshal(req)
	if err != nil {
		return sdk.Response{}, err
	}
	if _, err := p.in.Write(append(raw, '\n')); err != nil {
		p.kill()
		return sdk.Response{}, fmt.Errorf("%w: %v", ErrCrashed, err)
	}
	if err := p.in.Flush(); err != nil {
		p.kill()
		return sdk.Response{}, fmt.Errorf("%w: %v", ErrCrashed, err)
	}

	type readResult struct {
		line []byte
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		line, err := p.out.ReadBytes('\n')
		ch <- readResult{line: line, err: err}
	}()

	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		p.kill()
		return sdk.Response{}, ctx.Err()
	case <-timer.C:
		p.kill()
		return sdk.Response{}, fmt.Errorf("%w（>%s）", ErrTimeout, timeout)
	case res := <-ch:
		if res.err != nil {
			msg := strings.TrimSpace(p.errs.String())
			p.kill()
			if msg != "" {
				return sdk.Response{}, fmt.Errorf("%w: %v；stderr: %s", ErrCrashed, res.err, truncate(msg, 400))
			}
			return sdk.Response{}, fmt.Errorf("%w: %v", ErrCrashed, res.err)
		}
		var resp sdk.Response
		if err := json.Unmarshal(res.line, &resp); err != nil {
			p.kill()
			return sdk.Response{}, fmt.Errorf("%w: 响应解析失败: %v", ErrCrashed, err)
		}
		if resp.ID != req.ID {
			return sdk.Response{}, fmt.Errorf("%w: 响应 ID 不匹配", ErrCrashed)
		}
		if !resp.OK {
			msg := resp.Error
			if msg == "" {
				msg = "插件返回未知错误"
			}
			return resp, errors.New(msg)
		}
		return resp, nil
	}
}

func (r *Runtime) proc(id, dir string) (*process, error) {
	r.mu.Lock()
	if p := r.procs[id]; p != nil && !p.dead.Load() {
		r.mu.Unlock()
		return p, nil
	}
	delete(r.procs, id)
	r.mu.Unlock()

	bin := binaryPath(dir)
	if fi, err := os.Stat(bin); err != nil || fi.Size() == 0 {
		return nil, fmt.Errorf("插件二进制不存在，请先构建")
	}
	p := &process{id: id, errs: &ringBuf{max: 8 << 10}, done: make(chan struct{})}
	p.lastUsed.Store(time.Now().UnixNano())
	cmd := exec.Command(bin)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "S3BRIDGE_PLUGIN_ID="+id)
	inPipe, err := cmd.StdinPipe()
	if err != nil {
		return nil, err
	}
	outPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	cmd.Stderr = p.errs
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动插件进程: %w", err)
	}
	p.cmd = cmd
	p.in = bufio.NewWriterSize(inPipe, 64<<10)
	p.out = bufio.NewReaderSize(outPipe, 1<<20)
	go func() {
		_ = cmd.Wait()
		p.dead.Store(true)
		close(p.done)
	}()

	// 握手：确认进程能正常响应协议，避免把编译产物损坏当成业务错误。
	pingCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := r.callLocked(pingCtx, p, sdk.Request{ID: "ping-0", Method: "ping", TimeoutSecs: 5}, 5*time.Second); err != nil {
		p.kill()
		return nil, fmt.Errorf("插件启动失败: %w", err)
	}

	r.mu.Lock()
	r.procs[id] = p
	r.mu.Unlock()
	return p, nil
}

func (p *process) kill() {
	if p.dead.Swap(true) {
		return
	}
	if p.cmd != nil && p.cmd.Process != nil {
		_ = p.cmd.Process.Kill()
	}
	select {
	case <-p.done:
	case <-time.After(3 * time.Second):
	}
}

// Drop 杀掉指定插件的进程（编译成功后强制下次调用加载新二进制）。
func (r *Runtime) Drop(id string) {
	r.mu.Lock()
	p := r.procs[id]
	delete(r.procs, id)
	r.mu.Unlock()
	if p != nil {
		p.kill()
	}
}

func (r *Runtime) reap() {
	defer r.wg.Done()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	for {
		select {
		case <-r.stop:
			return
		case <-t.C:
			now := time.Now()
			var idle []*process
			r.mu.Lock()
			for id, p := range r.procs {
				last := time.Unix(0, p.lastUsed.Load())
				if now.Sub(last) > r.opts.IdleTimeout || p.dead.Load() {
					idle = append(idle, p)
					delete(r.procs, id)
				}
			}
			r.mu.Unlock()
			for _, p := range idle {
				p.kill()
			}
		}
	}
}

// ringBuf 保存 stderr 末尾内容，进程崩溃时用于诊断。
type ringBuf struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (b *ringBuf) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf = append(b.buf, p...)
	if len(b.buf) > b.max {
		b.buf = b.buf[len(b.buf)-b.max:]
	}
	return len(p), nil
}

func (b *ringBuf) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return string(b.buf)
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
