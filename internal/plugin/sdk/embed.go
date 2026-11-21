package sdk

import "embed"

// Source 内嵌 SDK 源码，主服务启动时把它落盘到数据目录，
// 作为插件 go.mod 里 replace 的目标，从而在无网络环境也能编译。
//
//go:embed *.go
var Source embed.FS
