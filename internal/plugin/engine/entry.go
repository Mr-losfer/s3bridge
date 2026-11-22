package engine

import (
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// NormalizeEntry 把 v2.0 的旧入口改写成注册式入口：
//
//	sdk.Serve(plugin{})  →  sdk.Serve((plugin{}).Upload, (plugin{}).Check)
//
// 解释器与宿主分属两套类型系统，接口值无法跨边界，只能传函数值；
// 旧写法无法在不改代码的情况下工作，所以加载前就地升级，用户升级后无需动手。
// 返回是否有文件被改动。
func NormalizeEntry(dir string) (bool, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return false, fmt.Errorf("读取插件目录: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, ".") {
			continue
		}
		names = append(names, name)
	}
	sort.Strings(names)

	changed := false
	for _, name := range names {
		path := filepath.Join(dir, name)
		raw, err := os.ReadFile(path)
		if err != nil {
			return changed, fmt.Errorf("读取 %s: %w", name, err)
		}
		next, ok, err := rewriteServe(name, raw)
		if err != nil {
			return changed, err
		}
		if !ok {
			continue
		}
		if err := os.WriteFile(path, next, 0o600); err != nil {
			return changed, fmt.Errorf("写回 %s: %w", name, err)
		}
		changed = true
	}
	return changed, nil
}

type textEdit struct {
	start int
	end   int
	text  string
}

func rewriteServe(name string, raw []byte) ([]byte, bool, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, name, raw, parser.ParseComments)
	if err != nil {
		return nil, false, err
	}
	local := sdkLocalName(file)
	if local == "" {
		return raw, false, nil
	}

	var edits []textEdit
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Serve" || len(call.Args) != 1 {
			return true
		}
		ident, ok := sel.X.(*ast.Ident)
		if !ok || ident.Name != local {
			return true
		}
		arg := call.Args[0]
		argText := string(raw[fset.Position(arg.Pos()).Offset:fset.Position(arg.End()).Offset])
		edits = append(edits, textEdit{
			start: fset.Position(call.Pos()).Offset,
			end:   fset.Position(call.End()).Offset,
			text:  fmt.Sprintf("%s.Serve((%s).Upload, (%s).Check)", local, argText, argText),
		})
		return true
	})
	if len(edits) == 0 {
		return raw, false, nil
	}

	out := make([]byte, len(raw))
	copy(out, raw)
	for i := len(edits) - 1; i >= 0; i-- { // 从后往前替换，前面的偏移量才不会被破坏
		if edits[i].start < 0 || edits[i].end > len(out) || edits[i].start > edits[i].end {
			return nil, false, fmt.Errorf("%s: 入口位置越界，请手动改为 sdk.Serve(plugin{}.Upload, plugin{}.Check)", name)
		}
		out = append(out[:edits[i].start], append([]byte(edits[i].text), out[edits[i].end:]...)...)
	}
	formatted, err := format.Source(out)
	if err != nil {
		return nil, false, fmt.Errorf("%s: 改写入口后格式化失败: %v", name, err)
	}
	return formatted, true, nil
}

// sdkLocalName 返回 SDK 在该文件里的本地包名；没导入则返回空。
func sdkLocalName(file *ast.File) string {
	for _, imp := range file.Imports {
		path, err := strconv.Unquote(imp.Path.Value)
		if err != nil || path != SDKPath {
			continue
		}
		if imp.Name != nil {
			return imp.Name.Name
		}
		return "sdk"
	}
	return ""
}
