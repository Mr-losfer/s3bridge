// Package engine 用内嵌 Go 解释器执行插件源码，不需要安装 Go 工具链。
//
// 执行流程：读取目录内全部 .go → 合并成一个包 → 语法与 import 校验 →
// yaegi 解释执行 → 从 sdk.Serve 取回上传/探活函数 → 走 JSON-RPC 提供服务。
//
// 为什么仍然放在子进程里：解释执行的插件 panic 或死循环无法被宿主直接回收，
// 独立进程可以用强杀兜底；进程由主程序自身启动（s3bridge __plugin_runner__），
// 所以不需要外部工具链。
package engine

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin/sdk"
	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

// ErrLoad 表示源码无法加载（语法错误、入口缺失、导入了不支持的包等）。
var ErrLoad = errors.New("插件加载失败")

// Info 描述一次成功加载的结果。
type Info struct {
	Entry      string `json:"entry"`
	Files      int    `json:"files"`
	SourceSize int    `json:"sourceSize"`
	SHA256     string `json:"sha256"`
	DurationMs int64  `json:"durationMs"`
}

// Engine 持有一个已加载的插件实例。
type Engine struct {
	interp *interp.Interpreter
	upload sdk.UploadFunc
	check  sdk.CheckFunc
	info   Info
}

// loadMu 串行化加载：sdk 的注册表是包级变量，并发加载会互相覆盖。
var loadMu sync.Mutex

// Load 加载目录中的插件源码。成功返回后引擎即可处理调用。
func (e *Engine) Load(dir string) (*Info, error) {
	loadMu.Lock()
	defer loadMu.Unlock()
	start := time.Now()
	sdk.Reset()
	src, err := Load(dir)
	if err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoad, err)
	}
	it := interp.New(interp.Options{})
	it.Use(stdlib.Symbols)
	it.Use(exports)
	if _, err := it.Eval(src.Code); err != nil {
		return nil, fmt.Errorf("%w: %v", ErrLoad, mapError(src, err))
	}
	upload, check := sdk.Handlers()
	if upload == nil {
		return nil, fmt.Errorf("%w: 没有找到入口。请在 main 中调用 sdk.Serve(plugin{}.Upload, plugin{}.Check)", ErrLoad)
	}
	sum := sha256.Sum256([]byte(src.Code))
	e.interp = it
	e.upload = upload
	e.check = check
	e.info = Info{
		Entry:      entryName(src),
		Files:      len(src.Files),
		SourceSize: len(src.Code),
		SHA256:     hex.EncodeToString(sum[:]),
		DurationMs: time.Since(start).Milliseconds(),
	}
	return &e.info, nil
}

// Info 返回最近一次加载的信息。
func (e *Engine) Info() Info { return e.info }

// Handle 处理一条 RPC 请求。插件 panic 会被兜住并转成错误响应。
func (e *Engine) Handle(req sdk.Request) (resp sdk.Response) {
	resp = sdk.Response{ID: req.ID, Version: sdk.Version}
	defer func() {
		if r := recover(); r != nil {
			resp.OK = false
			resp.Error = fmt.Sprintf("插件 panic: %v", r)
		}
	}()

	timeout := time.Duration(req.TimeoutSecs) * time.Second
	if timeout <= 0 || timeout > 10*time.Minute {
		timeout = 60 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	col := sdk.NewCollector()
	ctx = sdk.WithCollector(ctx, col)

	switch req.Method {
	case "ping":
		resp.OK, resp.Pong = true, true
		resp.Meta = map[string]string{
			"sha256": e.info.SHA256,
			"entry":  e.info.Entry,
			"files":  strconv.Itoa(e.info.Files),
			"size":   strconv.Itoa(e.info.SourceSize),
		}
		return resp
	case "upload":
		if req.Input == nil {
			resp.Error = "缺少 input"
			return resp
		}
		out, err := e.upload(ctx, *req.Input, req.Config)
		if err != nil {
			resp.Error = err.Error()
			return resp
		}
		resp.OK, resp.Result = true, out
	case "check":
		if err := e.check(ctx, req.Config); err != nil {
			resp.Error = err.Error()
			return resp
		}
		resp.OK = true
	case "reload":
		// 由宿主在源码变更后调用：同一进程内重新解释执行，省掉一次进程启动。
		info, err := e.Load(req.Extra["dir"])
		if err != nil {
			resp.Error = err.Error()
			return resp
		}
		resp.OK = true
		resp.Meta = map[string]string{"sha256": info.SHA256}
	default:
		resp.Error = "未知方法: " + req.Method
	}
	resp.Traces = col.Traces()
	return resp
}

// Run 是 runner 子进程的入口：加载插件后循环处理 stdin 上的请求。
func Run(dir string, in io.Reader, out io.Writer) error {
	e := &Engine{}
	info, err := e.Load(dir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return err
	}
	fmt.Fprintf(os.Stderr, "[engine] 已加载 %s（%d 个文件 · %d 字节 · %s · %dms）\n",
		dir, info.Files, info.SourceSize, info.SHA256[:8], info.DurationMs)

	dec := json.NewDecoder(bufio.NewReaderSize(in, 1<<20))
	enc := json.NewEncoder(out)
	for {
		var req sdk.Request
		if err := dec.Decode(&req); err != nil {
			return nil // EOF：宿主要么关闭了，要么准备重启进程
		}
		if err := enc.Encode(e.Handle(req)); err != nil {
			return nil
		}
	}
}

// --- 错误定位 ---

var errLine = regexp.MustCompile(`^(\d+):(\d+):\s*(.*)$`)

// mapError 把合并源码里的行号还原成「文件:行:列」，否则用户看到的行号对不上。
func mapError(src *Source, err error) error {
	if err == nil {
		return nil
	}
	lines := strings.Split(err.Error(), "\n")
	out := make([]string, 0, len(lines))
	for _, ln := range lines {
		m := errLine.FindStringSubmatch(strings.TrimSpace(ln))
		if m == nil {
			out = append(out, ln)
			continue
		}
		no, convErr := strconv.Atoi(m[1])
		if convErr != nil {
			out = append(out, ln)
			continue
		}
		if file := fileAt(src, no); file != "" {
			out = append(out, fmt.Sprintf("%s:%s:%s: %s", file, m[1], m[2], m[3]))
			continue
		}
		out = append(out, ln)
	}
	return errors.New(strings.Join(out, "\n"))
}

func fileAt(src *Source, line int) string {
	for _, sp := range src.Spans {
		if line >= sp.StartLine && line <= sp.EndLine {
			return sp.File
		}
	}
	return ""
}

// entryName 取入口文件名，优先 main.go，否则用排序后的第一个文件。
func entryName(src *Source) string {
	for _, name := range src.Files {
		if name == "main.go" {
			return name
		}
	}
	if len(src.Files) > 0 {
		return src.Files[0]
	}
	return ""
}
