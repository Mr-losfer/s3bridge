package updater

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
)

const maxAssetBytes = 200 << 20

// runApply 是自动更新的完整状态机：下载 → 校验 → 解压 → 备份 → 替换 → 重启。
// 任何一步失败都会尝试从备份恢复，保证服务不会停在半更新状态。
func (u *Updater) runApply(ctx context.Context, rel *Release) {
	if err := u.doApply(ctx, rel); err != nil {
		u.logApply("更新失败：%v", err)
		u.finishApply(err)
		return
	}
	u.finishApply(nil)
}

func (u *Updater) doApply(ctx context.Context, rel *Release) error {
	u.logApply("开始更新 %s → %s（资产 %s）", u.current, rel.Tag, rel.AssetName)
	u.setApply(PhaseDownloading, 3, "正在下载新版本…")

	lastPct := -1
	asset, err := u.client.Download(ctx, rel.AssetURL, maxAssetBytes, func(done, total int64) {
		pct := 5
		if total > 0 {
			pct = 5 + int(done*55/total)
		}
		if pct != lastPct && (pct-lastPct >= 2 || lastPct < 0) {
			lastPct = pct
			u.setApply(PhaseDownloading, pct,
				fmt.Sprintf("下载 %s（%s / %s）", rel.AssetName, humanBytes(done), humanBytes(total)))
		}
	})
	if err != nil {
		return fmt.Errorf("下载发布资产: %w", err)
	}
	u.logApply("资产下载完成（%s），正在获取 checksums.txt", humanBytes(int64(len(asset))))

	sums, err := u.client.FetchChecksums(ctx, rel.ChecksumURL)
	if err != nil {
		return fmt.Errorf("获取校验文件: %w", err)
	}
	want, ok := sums[rel.AssetName]
	if !ok {
		return fmt.Errorf("checksums.txt 中没有 %s 的校验值", rel.AssetName)
	}
	u.setApply(PhaseVerifying, 65, "正在校验 SHA256…")
	sum := sha256.Sum256(asset)
	got := hex.EncodeToString(sum[:])
	if got != strings.ToLower(want) {
		return fmt.Errorf("SHA256 校验不匹配（期望 %s，实际 %s），已拒绝安装", want, got)
	}
	u.logApply("SHA256 校验通过 %s", got[:16]+"…")

	u.setApply(PhaseVerifying, 72, "正在解包新版本…")
	isZip := strings.HasSuffix(strings.ToLower(rel.AssetName), ".zip")
	newBin, err := extractBinary(asset, binaryName(), isZip)
	if err != nil {
		return err
	}
	if !validBinary(newBin) {
		return errors.New("解包得到的文件不是有效的可执行程序，已拒绝安装")
	}
	u.logApply("解包完成，主程序 %s", humanBytes(int64(len(newBin))))

	// 可选：同时更新 stdio MCP 命令行工具（老版本资产可能不含该文件）。
	companion := "s3bridge-mcp"
	if runtime.GOOS == "windows" {
		companion += ".exe"
	}
	companionBin, companionErr := extractBinary(asset, companion, isZip)
	companionPath := u.companionPath()
	// 二进制部署不凭空创建文件；容器部署则写入数据卷供 entrypoint 优先加载。
	if companionErr != nil || !validBinary(companionBin) ||
		(companionPath != "" && !fileExists(companionPath) && u.strategy != "container") {
		companionPath = ""
		companionErr = errors.New("跳过 MCP 命令行工具")
	}

	backup, err := u.backupCurrent(u.targetPath)
	if err != nil {
		return fmt.Errorf("备份当前版本失败，出于安全考虑已中止: %w", err)
	}
	companionBackup := ""
	if companionPath != "" && companionErr == nil {
		companionBackup, _ = u.backupCurrent(companionPath)
	}
	if backup != "" {
		u.mu.Lock()
		u.status.Apply.BackupPath = backup
		u.mu.Unlock()
		u.logApply("已备份当前版本 → %s", backup)
	}

	u.setApply(PhaseReplacing, 85, "正在原子替换可执行文件…")
	if err := atomicReplace(u.targetPath, newBin, 0o755); err != nil {
		u.restoreBackup(backup, u.targetPath)
		return fmt.Errorf("替换主程序失败，已回滚: %w", err)
	}
	if companionPath != "" && companionErr == nil {
		if err := atomicReplace(companionPath, companionBin, 0o755); err != nil {
			u.logApply("MCP 命令行更新失败（不影响主服务）：%v", err)
			u.restoreBackup(companionBackup, companionPath)
		} else {
			u.logApply("已同步更新 MCP 命令行工具")
		}
	}
	u.logApply("新版本已写入 %s", u.targetPath)

	u.setApply(PhaseRestarting, 98, "新版本已就绪，正在重启服务…")
	u.mu.Lock()
	u.status.Apply.Restarting = true
	u.mu.Unlock()
	// 留出时间让前端轮询到 restarting 状态；重启期间连接中断由前端重试覆盖。
	if !sleepCtx(ctx, 900*time.Millisecond) {
		return errors.New("更新被取消，已保留新版本文件，重启服务后生效")
	}

	u.mu.RLock()
	restartFn := u.restartFn
	u.mu.RUnlock()
	if restartFn == nil {
		return errors.New("未配置自重启回调")
	}
	if err := restartFn(u.targetPath); err != nil {
		u.restoreBackup(backup, u.targetPath)
		u.restoreBackup(companionBackup, companionPath)
		return fmt.Errorf("自重启失败，已回滚: %w", err)
	}
	// syscall.Exec 成功时不会返回；能返回说明是测试注入或平台降级路径。
	u.logApply("自重启调用完成")
	return nil
}

func (u *Updater) companionPath() string {
	if u.strategy == "container" {
		name := "s3bridge-mcp"
		if runtime.GOOS == "windows" {
			name += ".exe"
		}
		return filepath.Join(u.dataDir, "bin", name)
	}
	dir := filepath.Dir(u.targetPath)
	name := "s3bridge-mcp"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(dir, name)
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ---------- 归档解包 ----------

func extractBinary(data []byte, name string, isZip bool) ([]byte, error) {
	if isZip {
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return nil, fmt.Errorf("打开 zip 归档: %w", err)
		}
		for _, f := range zr.File {
			if path.Base(f.Name) != name || f.FileInfo().IsDir() {
				continue
			}
			if f.UncompressedSize64 > maxAssetBytes {
				return nil, errors.New("归档内文件体积异常")
			}
			rc, err := f.Open()
			if err != nil {
				return nil, err
			}
			b, err := io.ReadAll(io.LimitReader(rc, maxAssetBytes+1))
			_ = rc.Close()
			if err != nil {
				return nil, err
			}
			return b, nil
		}
		return nil, fmt.Errorf("归档中找不到 %s", name)
	}
	gz, err := gzip.NewReader(bytes.NewReader(data))
	if err != nil {
		return nil, fmt.Errorf("打开 gzip 归档: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("读取 tar 归档: %w", err)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			continue
		}
		if path.Base(h.Name) != name {
			continue
		}
		b, err := io.ReadAll(io.LimitReader(tr, maxAssetBytes+1))
		if err != nil {
			return nil, err
		}
		return b, nil
	}
	return nil, fmt.Errorf("归档中找不到 %s", name)
}

// validBinary 做最低限度的可执行格式检查，防止把错误页/HTML 当作程序安装。
func validBinary(b []byte) bool {
	if len(b) < 512<<10 {
		return false
	}
	switch {
	case bytes.HasPrefix(b, []byte("\x7fELF")):
		return true
	case bytes.HasPrefix(b, []byte("MZ")):
		return true
	case bytes.HasPrefix(b, []byte{0xfe, 0xed, 0xfa, 0xce}),
		bytes.HasPrefix(b, []byte{0xce, 0xfa, 0xed, 0xfe}),
		bytes.HasPrefix(b, []byte{0xcf, 0xfa, 0xed, 0xfe}),
		bytes.HasPrefix(b, []byte{0xca, 0xfe, 0xba, 0xbe}):
		return true // Mach-O / fat binary
	default:
		return false
	}
}

// ---------- 备份、原子替换与回滚 ----------

func (u *Updater) backupCurrent(target string) (string, error) {
	if target == "" {
		return "", nil
	}
	info, err := os.Stat(target)
	if err != nil {
		return "", nil // 容器策略下首次更新时数据卷内还没有文件
	}
	if err := os.MkdirAll(u.backupDir, 0o700); err != nil {
		return "", err
	}
	b, err := os.ReadFile(target)
	if err != nil {
		return "", err
	}
	dst := filepath.Join(u.backupDir,
		fmt.Sprintf("%s.%s.%d", filepath.Base(target), sanitize(u.current), time.Now().UnixNano()))
	if err := os.WriteFile(dst, b, info.Mode().Perm()); err != nil {
		return "", err
	}
	u.pruneBackups()
	return dst, nil
}

func (u *Updater) pruneBackups() {
	entries, err := os.ReadDir(u.backupDir)
	if err != nil || len(entries) <= 3 {
		return
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-3] {
		_ = os.Remove(filepath.Join(u.backupDir, n))
	}
}

func (u *Updater) restoreBackup(backup, target string) {
	if backup == "" || target == "" {
		return
	}
	b, err := os.ReadFile(backup)
	if err != nil {
		u.logApply("回滚失败（读取备份）：%v", err)
		return
	}
	if err := atomicReplace(target, b, 0o755); err != nil {
		u.logApply("回滚失败：%v", err)
		return
	}
	u.logApply("已从备份恢复 %s", target)
}

func atomicReplace(target string, data []byte, mode os.FileMode) error {
	dir := filepath.Dir(target)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(dir, ".s3bridge-new-*")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }()
	if _, err := tmp.Write(data); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Chmod(mode); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Sync(); err != nil {
		_ = tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	// Windows 不允许 rename 覆盖已存在文件；先移除，回滚由备份保证。
	if runtime.GOOS == "windows" {
		_ = os.Remove(target)
	}
	return os.Rename(tmpName, target)
}

func sanitize(s string) string {
	var b strings.Builder
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9') || r == '.' || r == '-' {
			b.WriteRune(r)
		} else {
			b.WriteByte('_')
		}
	}
	return b.String()
}

func humanBytes(n int64) string {
	if n < 1024 {
		return fmt.Sprintf("%d B", n)
	}
	units := []string{"KB", "MB", "GB"}
	f := float64(n) / 1024
	for _, u := range units {
		if f < 1024 {
			return fmt.Sprintf("%.1f %s", f, u)
		}
		f /= 1024
	}
	return fmt.Sprintf("%.1f TB", f)
}
