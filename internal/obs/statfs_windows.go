//go:build windows

package obs

// statfs 在 Windows 上返回空值：磁盘容量由前端显示为「不可用」，
// 其余监控能力不受影响。避免引入 syscall 平台差异带来的维护成本。
func statfs(string) (total, free uint64, ok bool) { return 0, 0, false }
