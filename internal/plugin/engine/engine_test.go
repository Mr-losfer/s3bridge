package engine

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Mr-losfer/s3bridge/internal/plugin/sdk"
)

var inputStub = sdk.Input{FileName: "a.png", Size: 7, MimeType: "image/png"}

func sdkRequest(method string, in *sdk.Input) sdk.Request {
	return sdk.Request{ID: "t1", Method: method, Input: in}
}

// writePlugin 把源码写进临时目录，返回目录路径。
func writePlugin(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const basicMain = `package main

import (
	"context"
	"fmt"

	"github.com/Mr-losfer/s3bridge/sdk"
)

type plugin struct{}

func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
	return &sdk.Output{URL: "https://cdn.example.com/" + in.FileName, FileSize: in.Size}, nil
}

func (plugin) Check(ctx context.Context, cfg sdk.Config) error { return nil }

func main() { sdk.Serve(plugin{}.Upload, plugin{}.Check) }
`

func TestMergeMultiFile(t *testing.T) {
	src, err := Merge(map[string]string{
		"main.go": basicMain,
		"helper.go": `package main

import "strings"

func upper(s string) string { return strings.ToUpper(s) }
`,
	})
	if err != nil {
		t.Fatalf("合并失败: %v", err)
	}
	if len(src.Files) != 2 || src.Files[0] != "helper.go" {
		t.Fatalf("文件应按名称排序: %v", src.Files)
	}
	if !strings.Contains(src.Code, "func upper(s string) string") {
		t.Fatal("合并后丢失 helper.go 的声明")
	}
	if strings.Count(src.Code, "package main") != 1 {
		t.Fatalf("合并后应只有一个 package 声明:\n%s", src.Code)
	}
	if strings.Count(src.Code, `"strings"`) != 1 {
		t.Fatalf("import 应去重:\n%s", src.Code)
	}
}

func TestMergeRejectsDifferentPackage(t *testing.T) {
	_, err := Merge(map[string]string{
		"main.go":  basicMain,
		"other.go": "package other\n\nfunc f() {}\n",
	})
	if err == nil || !strings.Contains(err.Error(), "package") {
		t.Fatalf("混用 package 应报错，实际: %v", err)
	}
}

func TestMergeReportsFileAndLine(t *testing.T) {
	_, err := Merge(map[string]string{
		"main.go":   basicMain,
		"broken.go": "package main\n\nfunc broken() {\n",
	})
	if err == nil {
		t.Fatal("语法错误应报错")
	}
	if !strings.Contains(err.Error(), "broken.go:") {
		t.Fatalf("错误应带文件名，实际: %v", err)
	}
}

func TestLoadRejectsThirdParty(t *testing.T) {
	dir := writePlugin(t, map[string]string{"main.go": `package main

import (
	"context"

	"github.com/pkg/errors"

	"github.com/Mr-losfer/s3bridge/sdk"
)

func main() { _ = errors.New("x") }
`})
	_, err := Load(dir)
	if err == nil || !strings.Contains(err.Error(), "只提供标准库") {
		t.Fatalf("第三方依赖应被拦下，实际: %v", err)
	}
}

func TestEngineCallsUpload(t *testing.T) {
	dir := writePlugin(t, map[string]string{"main.go": basicMain})
	e := &Engine{}
	if _, err := e.Load(dir); err != nil {
		t.Fatalf("加载失败: %v", err)
	}
	resp := e.Handle(sdkRequest("upload", &inputStub))
	if !resp.OK {
		t.Fatalf("调用失败: %s", resp.Error)
	}
	if resp.Result.URL != "https://cdn.example.com/a.png" {
		t.Fatalf("返回值不对: %+v", resp.Result)
	}
	if resp.Version == "" {
		t.Fatal("响应应带协议版本")
	}
}

func TestEnginePingReturnsMeta(t *testing.T) {
	dir := writePlugin(t, map[string]string{"main.go": basicMain})
	e := &Engine{}
	info, err := e.Load(dir)
	if err != nil {
		t.Fatal(err)
	}
	resp := e.Handle(sdkRequest("ping", nil))
	if !resp.OK || !resp.Pong {
		t.Fatalf("ping 失败: %+v", resp)
	}
	if resp.Meta["sha256"] != info.SHA256 || resp.Meta["entry"] != "main.go" {
		t.Fatalf("ping 元信息不完整: %+v", resp.Meta)
	}
	if resp.Meta["files"] != "1" {
		t.Fatalf("文件数不对: %s", resp.Meta["files"])
	}
}

func TestEngineMissingEntry(t *testing.T) {
	dir := writePlugin(t, map[string]string{"main.go": `package main

import "fmt"

func main() { fmt.Println("没有注册入口") }
`})
	e := &Engine{}
	_, err := e.Load(dir)
	if err == nil || !strings.Contains(err.Error(), "sdk.Serve") {
		t.Fatalf("缺少入口应给出明确提示，实际: %v", err)
	}
}

func TestEnginePanicRecovered(t *testing.T) {
	dir := writePlugin(t, map[string]string{"main.go": `package main

import (
	"context"

	"github.com/Mr-losfer/s3bridge/sdk"
)

type plugin struct{}

func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
	panic("boom")
}

func (plugin) Check(ctx context.Context, cfg sdk.Config) error { return nil }

func main() { sdk.Serve(plugin{}.Upload, plugin{}.Check) }
`})
	e := &Engine{}
	if _, err := e.Load(dir); err != nil {
		t.Fatal(err)
	}
	resp := e.Handle(sdkRequest("upload", &inputStub))
	if resp.OK || !strings.Contains(resp.Error, "panic") {
		t.Fatalf("panic 应被兜住并返回错误，实际: %+v", resp)
	}
}

func TestNormalizeEntry(t *testing.T) {
	dir := writePlugin(t, map[string]string{"main.go": strings.Replace(basicMain, "sdk.Serve(plugin{}.Upload, plugin{}.Check)", "sdk.Serve(plugin{})", 1)})
	changed, err := NormalizeEntry(dir)
	if err != nil {
		t.Fatalf("改写失败: %v", err)
	}
	if !changed {
		t.Fatal("应识别出旧入口")
	}
	raw, _ := os.ReadFile(filepath.Join(dir, "main.go"))
	if !strings.Contains(string(raw), "sdk.Serve((plugin{}).Upload, (plugin{}).Check)") {
		t.Fatalf("改写结果不对:\n%s", raw)
	}
	// 幂等：再跑一次不应改动
	changed, err = NormalizeEntry(dir)
	if err != nil || changed {
		t.Fatalf("重复改写应无改动: changed=%v err=%v", changed, err)
	}
	// 改写后的源码必须能加载
	e := &Engine{}
	if _, err := e.Load(dir); err != nil {
		t.Fatalf("改写后加载失败: %v", err)
	}
}

func TestNormalizeEntryWithAliasAndPointer(t *testing.T) {
	dir := writePlugin(t, map[string]string{"main.go": `package main

import (
	"context"

	kv "github.com/Mr-losfer/s3bridge/sdk"
)

type plugin struct{}

func (p *plugin) Upload(ctx context.Context, in kv.Input, cfg kv.Config) (*kv.Output, error) {
	return &kv.Output{URL: "https://cdn/x"}, nil
}

func (p *plugin) Check(ctx context.Context, cfg kv.Config) error { return nil }

func main() { kv.Serve(&plugin{}) }
`})
	changed, err := NormalizeEntry(dir)
	if err != nil || !changed {
		t.Fatalf("别名 / 指针入口应能改写: changed=%v err=%v", changed, err)
	}
	e := &Engine{}
	if _, err := e.Load(dir); err != nil {
		t.Fatalf("改写后加载失败: %v", err)
	}
	resp := e.Handle(sdkRequest("upload", &inputStub))
	if !resp.OK {
		t.Fatalf("调用失败: %s", resp.Error)
	}
}

func TestHandleUnknownMethod(t *testing.T) {
	dir := writePlugin(t, map[string]string{"main.go": basicMain})
	e := &Engine{}
	if _, err := e.Load(dir); err != nil {
		t.Fatal(err)
	}
	if resp := e.Handle(sdkRequest("nope", nil)); resp.OK || !strings.Contains(resp.Error, "未知方法") {
		t.Fatalf("未知方法应报错: %+v", resp)
	}
	if resp := e.Handle(sdkRequest("upload", nil)); resp.OK || !strings.Contains(resp.Error, "缺少 input") {
		t.Fatalf("缺 input 应报错: %+v", resp)
	}
}

func TestContextCancelStopsPlugin(t *testing.T) {
	dir := writePlugin(t, map[string]string{"main.go": `package main

import (
	"context"
	"time"

	"github.com/Mr-losfer/s3bridge/sdk"
)

type plugin struct{}

func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case <-time.After(10 * time.Second):
		return &sdk.Output{URL: "https://slow"}, nil
	}
}

func (plugin) Check(ctx context.Context, cfg sdk.Config) error { return nil }

func main() { sdk.Serve(plugin{}.Upload, plugin{}.Check) }
`})
	e := &Engine{}
	if _, err := e.Load(dir); err != nil {
		t.Fatal(err)
	}
	req := sdkRequest("upload", &inputStub)
	req.TimeoutSecs = 1
	resp := e.Handle(req)
	if resp.OK || !strings.Contains(resp.Error, "deadline") {
		t.Fatalf("超时应返回 context 错误，实际: %+v", resp)
	}
}
