// Package workbuddy 提供内置 WorkBuddy 插件的 Go 源码。
//
// 源码以文本资源随二进制嵌入，首次启动时落盘到 {DataDir}/plugins/workbuddy/，
// 之后就是普通插件：可以在后台直接改 main.go、点保存即编译生效。
package workbuddy

import (
	"embed"

	"github.com/Mr-losfer/s3bridge/internal/plugin"
)

//go:embed assets/*
var assets embed.FS

// Files 返回插件文件夹的初始内容。main.go.txt 落盘时改名为 main.go。
func Files() map[string]string {
	out := map[string]string{}
	entries, err := assets.ReadDir("assets")
	if err != nil {
		return out
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		raw, err := assets.ReadFile("assets/" + e.Name())
		if err != nil {
			continue
		}
		name := e.Name()
		if name == "main.go.txt" {
			name = "main.go"
		}
		out[name] = string(raw)
	}
	return out
}

// Builtin 返回随二进制发布的插件定义。
func Builtin() plugin.Builtin {
	return plugin.Builtin{ID: "workbuddy", Files: Files()}
}
