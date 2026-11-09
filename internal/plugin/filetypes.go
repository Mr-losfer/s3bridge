package plugin

import (
	"fmt"
	"strings"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// 文件类型是插件调度的第二维度：插件声明自己能承接哪些类型（图片/视频/文档/
// 压缩包…），上传时先判定文件类型，再在同类型插件中按权重从高到低选择。
// 空列表表示「全部类型」，兼容旧数据与内置插件。

// ParseFileTypes 归一化并校验文件类型列表。
// 传入空值、["*"] 或 ["all"] 表示支持全部类型，返回 nil。
func ParseFileTypes(in []string) ([]store.FileCategory, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := map[store.FileCategory]bool{}
	for _, raw := range in {
		v := store.FileCategory(strings.ToLower(strings.TrimSpace(raw)))
		switch v {
		case "", "*", "all":
			return nil, nil
		}
		if !store.ValidCategory(v) {
			return nil, fmt.Errorf("未知文件类型 %q（可选：image/video/audio/document/archive/other）", raw)
		}
		seen[v] = true
	}
	out := make([]store.FileCategory, 0, len(seen))
	for _, c := range store.AllCategories {
		if seen[c] {
			out = append(out, c)
		}
	}
	return out, nil
}

// FileTypeStrings 把类型枚举转成字符串，供 Manifest 与 MCP 响应展示。
func FileTypeStrings(in []store.FileCategory) []string {
	if len(in) == 0 {
		return []string{"all"}
	}
	out := make([]string, 0, len(in))
	for _, c := range in {
		out = append(out, string(c))
	}
	return out
}
