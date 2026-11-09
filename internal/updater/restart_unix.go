//go:build !windows

package updater

import (
	"os"
	"syscall"
)

// RestartSelf 用新二进制替换当前进程映像，保持 PID 与监听端口不变。
// 调用前 main 已优雅关闭 HTTP 服务；成功时本函数不会返回。
func RestartSelf(path string) error {
	args := append([]string{path}, os.Args[1:]...)
	return syscall.Exec(path, args, os.Environ())
}
