//go:build windows

package updater

import "os"

// RestartSelf 在 Windows 上启动新进程后退出当前进程。
// 调用前 main 已优雅关闭 HTTP 服务，端口已释放，避免新旧进程抢占。
func RestartSelf(path string) error {
	args := append([]string{path}, os.Args[1:]...)
	proc, err := os.StartProcess(path, args, &os.ProcAttr{
		Dir:   "",
		Env:   os.Environ(),
		Files: []*os.File{os.Stdin, os.Stdout, os.Stderr},
	})
	if err != nil {
		return err
	}
	_ = proc.Release()
	os.Exit(0)
	return nil
}
