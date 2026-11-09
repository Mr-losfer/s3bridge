// Package updater 实现「从 GitHub Releases 检测新版本 → 展示更新要点 → 一键自动更新」。
//
// 设计原则：
//   - 零外部依赖，只用标准库，避免为一个管理功能引入供应链风险；
//   - 所有网络请求的 host 都做白名单校验，防止 Release JSON 被篡改后造成 SSRF；
//   - 下载物必须校验 checksums.txt 中的 SHA256，替换前备份、失败可回滚；
//   - 所有后台 goroutine 都有明确的取消与等待机制（Start/Stop + WaitGroup）。
package updater

import (
	"strconv"
	"strings"
)

// Version 是语义化版本的一个够用子集：主/次/修订号 + 预发布标识。
type Version struct {
	Major int64
	Minor int64
	Patch int64
	Pre   string
	Raw   string
}

// ParseVersion 解析 "v1.2.3"、"1.2"、"1.2.3-rc.1+build.5" 等常见形态。
// 拒绝非数字段，避免把 "dev"、commit hash 误判成版本。
func ParseVersion(s string) (Version, bool) {
	raw := strings.TrimSpace(s)
	v := strings.TrimPrefix(strings.TrimPrefix(raw, "v"), "V")
	if v == "" {
		return Version{}, false
	}
	if i := strings.IndexByte(v, '+'); i >= 0 { // 丢弃 build metadata
		v = v[:i]
	}
	pre := ""
	if i := strings.IndexByte(v, '-'); i >= 0 {
		pre = v[i+1:]
		v = v[:i]
	}
	parts := strings.Split(v, ".")
	if len(parts) == 0 || len(parts) > 3 {
		return Version{}, false
	}
	nums := [3]int64{}
	for i, p := range parts {
		if p == "" {
			return Version{}, false
		}
		n, err := strconv.ParseInt(p, 10, 64)
		if err != nil || n < 0 {
			return Version{}, false
		}
		nums[i] = n
	}
	return Version{Major: nums[0], Minor: nums[1], Patch: nums[2], Pre: pre, Raw: raw}, true
}

// Compare 返回 -1/0/1。预发布版本低于同号正式版；预发布之间按字典序比较。
func Compare(a, b Version) int {
	for _, pair := range [][2]int64{{a.Major, b.Major}, {a.Minor, b.Minor}, {a.Patch, b.Patch}} {
		if pair[0] < pair[1] {
			return -1
		}
		if pair[0] > pair[1] {
			return 1
		}
	}
	switch {
	case a.Pre == "" && b.Pre == "":
		return 0
	case a.Pre == "":
		return 1
	case b.Pre == "":
		return -1
	default:
		return strings.Compare(a.Pre, b.Pre)
	}
}

// IsNewer 判断 latest 是否比 current 更新。
// 当前是开发构建（dev / 无法解析）时视为落后，方便本地开发也能看到更新提示。
func IsNewer(latest, current string) bool {
	lv, ok := ParseVersion(latest)
	if !ok {
		return false
	}
	cv, ok := ParseVersion(current)
	if !ok {
		return true
	}
	return Compare(lv, cv) > 0
}
