package engine

import (
	"bytes"
	"fmt"
	"go/ast"
	"go/parser"
	"go/printer"
	"go/token"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// SDKPath 是插件导入 SDK 的路径，与 go.mod 的模块路径一致。
const SDKPath = "github.com/Mr-losfer/s3bridge/sdk"

// Span 记录合并后源码里某个文件占据的行区间，用于把解释器报的行号还原成文件名。
type Span struct {
	File      string
	StartLine int // 合并源码中的起始行（含）
	EndLine   int // 结束行（含）
}

// Source 是合并后的可执行源码。
type Source struct {
	Code  string
	Files []string
	Spans []Span
}

// ReadFiles 读取目录下参与执行的 .go 文件（按文件名排序，跳过测试与隐藏文件）。
func ReadFiles(dir string) (map[string]string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读取插件目录: %w", err)
	}
	out := map[string]string{}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") || strings.HasPrefix(name, ".") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			return nil, fmt.Errorf("读取 %s: %w", name, err)
		}
		out[name] = string(raw)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("目录内没有 .go 源码")
	}
	return out, nil
}

// Merge 把同一 package 的多个文件合并成一份源码，供解释器一次执行。
//
// 解释器按「一个包一份源码」执行，多文件必须先合并：提取 package 名、去重 import、
// 按文件名顺序拼接声明。合并前的单文件解析负责给出带文件名与行号的语法错误。
func Merge(files map[string]string) (*Source, error) {
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)

	fset := token.NewFileSet()
	pkg := ""
	type spec struct{ alias, path string }
	var imports []spec
	seen := map[string]bool{}

	type block struct {
		name  string
		decls []string
	}
	var blocks []block

	for _, name := range names {
		file, err := parser.ParseFile(fset, name, files[name], parser.ParseComments)
		if err != nil {
			return nil, err
		}
		if pkg == "" {
			pkg = file.Name.Name
		} else if file.Name.Name != pkg {
			return nil, fmt.Errorf("%s: package %s 与 %s 不一致", name, file.Name.Name, pkg)
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil {
				return nil, fmt.Errorf("%s: import 路径无法解析", name)
			}
			alias := ""
			if imp.Name != nil {
				alias = imp.Name.Name
			}
			key := alias + " " + path
			if seen[key] {
				continue
			}
			seen[key] = true
			imports = append(imports, spec{alias: alias, path: path})
		}
		blk := block{name: name}
		for _, d := range file.Decls {
			if gd, ok := d.(*ast.GenDecl); ok && gd.Tok == token.IMPORT {
				continue
			}
			var buf bytes.Buffer
			if err := printer.Fprint(&buf, fset, d); err != nil {
				return nil, fmt.Errorf("%s: 输出声明失败: %w", name, err)
			}
			blk.decls = append(blk.decls, buf.String())
		}
		blocks = append(blocks, blk)
	}

	// 同一个包被两种方式导入（"fmt" 与 f "fmt"）会让合并后的代码含义不明，直接拒绝。
	byPath := map[string]string{}
	for _, imp := range imports {
		if prev, ok := byPath[imp.path]; ok && prev != imp.alias {
			return nil, fmt.Errorf("包 %s 被重复导入且别名不一致（%q 与 %q）", imp.path, prev, imp.alias)
		}
		byPath[imp.path] = imp.alias
	}

	var b strings.Builder
	line := 1
	write := func(s string) {
		b.WriteString(s)
		line += strings.Count(s, "\n")
	}
	write("package " + pkg + "\n\n")
	if len(imports) > 0 {
		write("import (\n")
		for _, imp := range imports {
			if imp.alias != "" {
				write("\t" + imp.alias + " " + strconv.Quote(imp.path) + "\n")
			} else {
				write("\t" + strconv.Quote(imp.path) + "\n")
			}
		}
		write(")\n\n")
	}

	var spans []Span
	for _, blk := range blocks {
		start := line
		for _, d := range blk.decls {
			write(d)
			write("\n\n")
		}
		spans = append(spans, Span{File: blk.name, StartLine: start, EndLine: line})
	}

	return &Source{Code: b.String(), Files: names, Spans: spans}, nil
}

// Load 读取并合并目录下的源码，同时检查 import 是否在标准库范围内。
func Load(dir string) (*Source, error) {
	files, err := ReadFiles(dir)
	if err != nil {
		return nil, err
	}
	if err := checkImports(files); err != nil {
		return nil, err
	}
	return Merge(files)
}

// checkImports 提前拦下第三方依赖：解释器只有标准库符号，导入第三方包会得到
// 「could not import」这类难以理解的报错。
func checkImports(files map[string]string) error {
	std := stdlibPaths()
	fset := token.NewFileSet()
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		file, err := parser.ParseFile(fset, name, files[name], parser.ImportsOnly)
		if err != nil {
			return err
		}
		for _, imp := range file.Imports {
			path, err := strconv.Unquote(imp.Path.Value)
			if err != nil || path == SDKPath || std[path] {
				continue
			}
			return fmt.Errorf("%s: 不支持导入 %q —— 内嵌解释器只提供标准库，第三方包请改用标准库实现", name, path)
		}
	}
	return nil
}
