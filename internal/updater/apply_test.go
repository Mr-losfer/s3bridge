package updater

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func fakeBinary(marker string) []byte {
	b := make([]byte, 600<<10)
	copy(b, []byte("\x7fELF"))
	for i := 8; i < len(b); i++ {
		b[i] = byte((i*31 + 7) % 251)
	}
	copy(b[16:], []byte(marker))
	return b
}

func makeTarGz(t *testing.T, bin []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, name := range []string{"s3bridge", "s3bridge-mcp"} {
		if err := tw.WriteHeader(&tar.Header{
			Name: name, Mode: 0o755, Size: int64(len(bin)), Typeflag: tar.TypeReg,
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(bin); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func checksumLine(archive []byte) string {
	sum := sha256.Sum256(archive)
	return fmt.Sprintf("%s  %s\n", hex.EncodeToString(sum[:]), AssetName(runtime.GOOS, runtime.GOARCH))
}

type testEnv struct {
	u       *Updater
	target  string
	oldBin  []byte
	newBin  []byte
	tmp     string
	backups string
}

func newTestEnv(t *testing.T, tag string, corruptChecksum bool, onRequest func()) *testEnv {
	t.Helper()
	tmp := t.TempDir()
	target := filepath.Join(tmp, "s3bridge")
	oldBin, newBin := fakeBinary("OLD-BUILD"), fakeBinary("NEW-BUILD")
	if err := os.WriteFile(target, oldBin, 0o755); err != nil {
		t.Fatal(err)
	}
	archive := makeTarGz(t, newBin)
	sums := checksumLine(archive)
	if corruptChecksum {
		sums = strings.Repeat("0", 64) + "  " + AssetName(runtime.GOOS, runtime.GOARCH) + "\n"
	}
	srv := newMockReleaseServer(t, mockReleaseOpts{
		Tag: tag, Body: sampleChangelog, Archive: archive, Checksums: sums, OnRequest: onRequest,
	})
	u, err := New(Options{
		Current: "v1.0.0", Repo: "owner/repo", APIBaseURL: srv.URL,
		AutoCheck: false, Interval: time.Hour, Strategy: "binary", DataDir: tmp,
		TargetPath: target, BackupDir: filepath.Join(tmp, "backups"),
	})
	if err != nil {
		t.Fatal(err)
	}
	u.SetRestartFunc(func(string) error { return nil }) // 测试中不真的替换进程
	t.Cleanup(u.Stop)
	return &testEnv{u: u, target: target, oldBin: oldBin, newBin: newBin,
		tmp: tmp, backups: filepath.Join(tmp, "backups")}
}

func waitApply(t *testing.T, u *Updater, timeout time.Duration) ApplyStatus {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		a := u.Status().Apply
		if a.Phase == PhaseDone || a.Phase == PhaseFailed {
			return a
		}
		time.Sleep(40 * time.Millisecond)
	}
	t.Fatalf("更新任务超时，最后状态：%+v", u.Status().Apply)
	return ApplyStatus{}
}

func TestApplySuccess(t *testing.T) {
	env := newTestEnv(t, "v1.1.0", false, nil)
	st, err := env.u.Check(context.Background(), true)
	if err != nil {
		t.Fatal(err)
	}
	if !st.UpdateAvailable || st.Latest == nil || st.Latest.Tag != "v1.1.0" {
		t.Fatalf("应检测到新版本：%+v", st)
	}
	if !st.RestartConfigured {
		t.Fatal("自重启能力未标记")
	}
	if _, err := env.u.Apply("tester"); err != nil {
		t.Fatalf("Apply: %v", err)
	}
	a := waitApply(t, env.u, 8*time.Second)
	if a.Phase != PhaseDone {
		t.Fatalf("更新未成功：%+v", a)
	}
	if a.Percent != 100 || a.From != "v1.0.0" || a.To != "v1.1.0" || a.Actor != "tester" {
		t.Fatalf("更新状态字段不符：%+v", a)
	}
	got, err := os.ReadFile(env.target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, env.newBin) {
		t.Fatal("目标文件未被替换为新版本")
	}
	if a.BackupPath == "" || !fileExists(a.BackupPath) {
		t.Fatal("未生成旧版本备份")
	}
	backup, err := os.ReadFile(a.BackupPath)
	if err != nil || !bytes.Equal(backup, env.oldBin) {
		t.Fatal("备份内容不是旧版本")
	}
	joined := strings.Join(a.Log, "\n")
	for _, want := range []string{"SHA256 校验通过", "新版本已写入", "自重启"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("更新日志缺少 %q：\n%s", want, joined)
		}
	}
}

func TestApplyRollbackOnChecksumMismatch(t *testing.T) {
	env := newTestEnv(t, "v1.1.0", true, nil)
	if _, err := env.u.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := env.u.Apply("tester"); err != nil {
		t.Fatal(err)
	}
	a := waitApply(t, env.u, 8*time.Second)
	if a.Phase != PhaseFailed || !strings.Contains(a.Error, "SHA256") {
		t.Fatalf("应因校验失败而终止：%+v", a)
	}
	got, err := os.ReadFile(env.target)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, env.oldBin) {
		t.Fatal("校验失败后原文件被改动")
	}
}

func TestApplyRequiresUpdateAndRestart(t *testing.T) {
	env := newTestEnv(t, "v1.1.0", false, nil)
	if _, err := env.u.Apply("tester"); err == nil {
		t.Fatal("未检测更新时应拒绝 Apply")
	}
	env.u.mu.Lock()
	env.u.status.RestartConfigured = false
	env.u.restartFn = nil
	env.u.mu.Unlock()
	if _, err := env.u.Check(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	if _, err := env.u.Apply("tester"); err == nil || !strings.Contains(err.Error(), "自重启") {
		t.Fatalf("未配置自重启时应拒绝 Apply，实际 %v", err)
	}
}

func TestCheckThrottleAndDismiss(t *testing.T) {
	var calls atomic.Int32
	env := newTestEnv(t, "v1.1.0", false, func() { calls.Add(1) })
	_, _ = env.u.Check(context.Background(), false)
	if got := calls.Load(); got != 1 {
		t.Fatalf("首次检查应请求一次 API，实际 %d", got)
	}
	_, _ = env.u.Check(context.Background(), false)
	if got := calls.Load(); got != 1 {
		t.Fatalf("30 秒内的重复检查应命中节流，实际请求 %d 次", got)
	}
	_, _ = env.u.Check(context.Background(), true)
	if got := calls.Load(); got != 2 {
		t.Fatalf("force=true 应强制刷新，实际请求 %d 次", got)
	}
	st := env.u.Status()
	if !st.UpdateAvailable || st.Dismissed {
		t.Fatalf("初始状态异常：%+v", st)
	}
	env.u.Dismiss("v1.1.0")
	if !env.u.Status().Dismissed {
		t.Fatal("Dismiss 未生效")
	}
}

func TestStartStopIsIdempotent(t *testing.T) {
	env := newTestEnv(t, "v1.1.0", false, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if err := env.u.Start(ctx); err != nil {
		t.Fatal(err)
	}
	if err := env.u.Start(ctx); err != nil { // 重复调用安全
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		env.u.Stop()
		env.u.Stop()
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Stop 阻塞，后台 goroutine 未正确退出")
	}
}
