// Package docs 把仓库 docs/ 的 Markdown 同步副本嵌入二进制。
//
// 单一事实来源：根目录 docs/*.md 是源，internal/docs/files/*.md 是同步副本
// （由 make docs-sync 生成）。MCP 的文档工具与后端 /api/docs 都从这里读取，
// 保证「运行时看到的文档」与「仓库里的文档」永远一致。
package docs

import (
	"embed"
	"sort"
	"strings"
)

//go:embed files/*.md
var files embed.FS

type Meta struct {
	Name  string `json:"name"`
	Title string `json:"title"`
	Size  int    `json:"size"`
}

var titles = map[string]string{
	"plugin-development": "插件开发文档",
	"s3-api":             "S3 开放接口文档",
	"mcp":                "MCP 能力与工具清单",
	"installation":       "安装与部署",
}

// Get 按名称返回 Markdown 正文。
func Get(name string) (string, bool) {
	if !known(name) {
		return "", false
	}
	b, err := files.ReadFile("files/" + name + ".md")
	if err != nil {
		return "", false
	}
	return string(b), true
}

// List 返回按名称排序的文档索引。
func List() []Meta {
	out := make([]Meta, 0, len(titles))
	for name, title := range titles {
		if body, ok := Get(name); ok {
			out = append(out, Meta{Name: name, Title: title, Size: len(body)})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func known(name string) bool {
	if strings.ContainsAny(name, "/\\.") {
		return false // 防目录穿越
	}
	_, ok := titles[name]
	return ok
}
