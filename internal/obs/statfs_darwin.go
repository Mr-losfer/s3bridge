//go:build darwin

package obs

import "syscall"

// statfs 读取文件系统容量。Darwin 下 Bsize 为 uint32，需显式转换。
func statfs(path string) (total, free uint64, ok bool) {
	var st syscall.Statfs_t
	if err := syscall.Statfs(path, &st); err != nil {
		return 0, 0, false
	}
	bs := uint64(st.Bsize)
	return st.Blocks * bs, st.Bavail * bs, true
}
