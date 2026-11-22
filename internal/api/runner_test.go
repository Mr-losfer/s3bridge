package api_test

import (
	"os"
	"testing"

	"github.com/Mr-losfer/s3bridge/internal/plugin/engine"
)

// TestMain 让测试二进制充当插件 runner（生产环境里 runner 是主程序本身）。
// 任何直接启动 Core 的测试包都需要这一段，否则子进程会去跑测试用例。
func TestMain(m *testing.M) {
	if len(os.Args) >= 3 && os.Args[1] == "__plugin_runner__" {
		if err := engine.Run(os.Args[2], os.Stdin, os.Stdout); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}
