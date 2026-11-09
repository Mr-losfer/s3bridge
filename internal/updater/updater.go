package updater

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"time"
)

const (
	PhaseIdle        = "idle"
	PhaseDownloading = "downloading"
	PhaseVerifying   = "verifying"
	PhaseReplacing   = "replacing"
	PhaseRestarting  = "restarting"
	PhaseDone        = "done"
	PhaseFailed      = "failed"
)

// Status 是前端与 MCP 读取的完整更新状态快照。
type Status struct {
	Current           string      `json:"current"`
	Latest            *Release    `json:"latest"`
	UpdateAvailable   bool        `json:"updateAvailable"`
	CheckedAt         string      `json:"checkedAt"`
	CheckError        string      `json:"checkError"`
	AutoCheck         bool        `json:"autoCheck"`
	CheckIntervalSecs int         `json:"checkIntervalSecs"`
	Repo              string      `json:"repo"`
	Proxy             string      `json:"proxy"`
	Strategy          string      `json:"strategy"`
	StrategyNote      string      `json:"strategyNote"`
	PersistCommand    string      `json:"persistCommand"`
	Dismissed         bool        `json:"dismissed"`
	RestartConfigured bool        `json:"restartConfigured"`
	Apply             ApplyStatus `json:"apply"`
}

type ApplyStatus struct {
	Phase      string   `json:"phase"`
	Percent    int      `json:"percent"`
	Message    string   `json:"message"`
	From       string   `json:"from"`
	To         string   `json:"to"`
	StartedAt  string   `json:"startedAt"`
	FinishedAt string   `json:"finishedAt"`
	Error      string   `json:"error"`
	Log        []string `json:"log"`
	Restarting bool     `json:"restarting"`
	Actor      string   `json:"actor"`
	BackupPath string   `json:"backupPath,omitempty"`
	TargetPath string   `json:"targetPath,omitempty"`
}

type Options struct {
	Current    string
	Repo       string
	APIBaseURL string
	Proxy      string // GitHub 加速代理，如 https://gh-proxy.com；空表示直连
	Token      string
	AutoCheck  bool
	Interval   time.Duration
	Strategy   string
	DataDir    string
	// 测试注入：覆盖替换目标与备份目录，不会触碰真实可执行文件。
	TargetPath string
	BackupDir  string
	GOOS       string
	GOARCH     string
	Audit      func(action, detail string)
}

type Updater struct {
	mu      sync.RWMutex
	status  Status
	client  *Client
	current string
	goos    string
	goarch  string
	dataDir string

	interval  time.Duration
	autoCheck bool
	lastCheck time.Time
	checkMu   sync.Mutex
	applying  atomic.Bool

	targetPath string
	backupDir  string
	strategy   string
	restartFn  func(target string) error
	audit      func(action, detail string)
	dismissed  string

	parent  context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	running bool
}

// New 构造更新器；失败只发生在配置非法时，网络错误留到 Check 阶段。
func New(opts Options) (*Updater, error) {
	if opts.Current == "" {
		opts.Current = "dev"
	}
	if opts.Repo == "" {
		opts.Repo = "Mr-losfer/s3bridge"
	}
	if opts.APIBaseURL == "" {
		opts.APIBaseURL = "https://api.github.com"
	}
	if opts.Interval <= 0 {
		opts.Interval = 6 * time.Hour
	}
	if opts.Interval < time.Minute {
		opts.Interval = time.Minute
	}
	if opts.GOOS == "" {
		opts.GOOS = runtime.GOOS
	}
	if opts.GOARCH == "" {
		opts.GOARCH = runtime.GOARCH
	}
	if opts.DataDir == "" {
		opts.DataDir = "./data"
	}
	client, err := NewClient(opts.APIBaseURL, opts.Repo, opts.Token, opts.Proxy)
	if err != nil {
		return nil, err
	}
	strategy, target, note, persist := resolveStrategy(opts.Strategy, opts.DataDir)
	if opts.TargetPath != "" {
		target = opts.TargetPath
	}
	backupDir := opts.BackupDir
	if backupDir == "" {
		backupDir = filepath.Join(opts.DataDir, "updates")
	}

	u := &Updater{
		current: opts.Current, goos: opts.GOOS, goarch: opts.GOARCH,
		dataDir: opts.DataDir, client: client, interval: opts.Interval,
		autoCheck: opts.AutoCheck, targetPath: target, backupDir: backupDir,
		strategy: strategy, audit: opts.Audit,
	}
	u.status = Status{
		Current: opts.Current, Repo: opts.Repo, Proxy: opts.Proxy,
		AutoCheck:         opts.AutoCheck,
		CheckIntervalSecs: int(opts.Interval.Seconds()), Strategy: strategy,
		StrategyNote: note, PersistCommand: persist,
		Apply: ApplyStatus{Phase: PhaseIdle, Message: "尚未执行更新", Log: []string{}},
	}
	return u, nil
}

func binaryName() string {
	if runtime.GOOS == "windows" {
		return "s3bridge.exe"
	}
	return "s3bridge"
}

func resolveStrategy(requested, dataDir string) (name, target, note, persist string) {
	exe, err := os.Executable()
	if err != nil {
		exe = ""
	}
	container := requested == "container"
	if requested == "" || requested == "auto" {
		if _, statErr := os.Stat("/.dockerenv"); statErr == nil {
			container = true
		}
	}
	if container {
		return "container", filepath.Join(dataDir, "bin", binaryName()),
			"容器部署：新版本写入数据卷 /app/data/bin 并立即重启生效；容器重建后仍优先加载数据卷中的版本。",
			"docker compose pull && docker compose up -d"
	}
	return "binary", exe,
		"二进制部署：校验通过后原子替换可执行文件，并原地重启进程（systemd / nohup 均无需人工干预）。",
		""
}

// SetRestartFunc 注入重启回调，由 main 在优雅关闭 HTTP 服务后自替换进程。
func (u *Updater) SetRestartFunc(fn func(target string) error) {
	u.mu.Lock()
	u.restartFn = fn
	u.status.RestartConfigured = fn != nil
	u.mu.Unlock()
}

// Start 启动定时检查；重复调用安全。
func (u *Updater) Start(parent context.Context) error {
	u.mu.Lock()
	if u.running {
		u.mu.Unlock()
		return nil
	}
	ctx, cancel := context.WithCancel(parent)
	u.parent, u.cancel, u.running = ctx, cancel, true
	u.mu.Unlock()

	if u.autoCheck {
		u.wg.Add(1)
		go u.loop(ctx)
	}
	return nil
}

func (u *Updater) loop(ctx context.Context) {
	defer u.wg.Done()
	if !sleepCtx(ctx, 3*time.Second) {
		return
	}
	u.checkNow(ctx)
	ticker := time.NewTicker(u.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			u.checkNow(ctx)
		}
	}
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}

// Stop 取消定时检查并等待所有后台任务退出。
func (u *Updater) Stop() {
	u.mu.Lock()
	if !u.running {
		u.mu.Unlock()
		return
	}
	cancel := u.cancel
	u.running = false
	u.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	u.wg.Wait()
}

// Status 返回一致性快照（日志切片做拷贝，避免读侧看到并发追加）。
func (u *Updater) Status() Status {
	u.mu.RLock()
	defer u.mu.RUnlock()
	s := u.status
	s.Apply.Log = append([]string{}, u.status.Apply.Log...)
	return s
}

// Check 触发一次检查。force=false 时 30 秒内复用缓存，避免按钮连点打爆 GitHub API。
func (u *Updater) Check(ctx context.Context, force bool) (Status, error) {
	u.checkMu.Lock()
	defer u.checkMu.Unlock()
	u.mu.RLock()
	last := u.lastCheck
	u.mu.RUnlock()
	if !force && !last.IsZero() && time.Since(last) < 30*time.Second {
		return u.Status(), nil
	}
	return u.checkNow(ctx), nil
}

func (u *Updater) checkNow(ctx context.Context) Status {
	checkCtx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	rel, err := u.client.FetchLatest(checkCtx, u.goos, u.goarch)

	u.mu.Lock()
	u.lastCheck = time.Now()
	u.status.CheckedAt = time.Now().UTC().Format(time.RFC3339)
	if err != nil {
		u.status.CheckError = err.Error()
		slog.Warn("检测更新失败", "repo", u.status.Repo, "err", err)
	} else {
		u.status.CheckError = ""
		u.status.Latest = rel
		u.status.UpdateAvailable = IsNewer(rel.Tag, u.current)
		if !u.status.UpdateAvailable {
			u.dismissed = ""
		}
		slog.Info("检测更新完成", "current", u.current, "latest", rel.Tag,
			"available", u.status.UpdateAvailable)
	}
	u.status.Dismissed = u.status.Latest != nil && u.dismissed == u.status.Latest.Tag
	u.mu.Unlock()
	return u.Status()
}

// Dismiss 记录「忽略此版本」，仅影响弹窗，不影响首页卡片。
func (u *Updater) Dismiss(tag string) {
	u.mu.Lock()
	u.dismissed = tag
	if u.status.Latest != nil && u.status.Latest.Tag == tag {
		u.status.Dismissed = true
	}
	u.mu.Unlock()
}

func (u *Updater) parentOrBackground() context.Context {
	u.mu.RLock()
	defer u.mu.RUnlock()
	if u.parent != nil {
		return u.parent
	}
	return context.Background()
}

func (u *Updater) auditLog(action, detail string) {
	if u.audit != nil {
		u.audit(action, detail)
	}
}

// Apply 启动一次自动更新（异步），立即返回当前状态供前端轮询。
func (u *Updater) Apply(actor string) (ApplyStatus, error) {
	s := u.Status()
	if s.Latest == nil || !s.UpdateAvailable {
		return s.Apply, errors.New("当前已是最新版本，无需更新")
	}
	if !u.applying.CompareAndSwap(false, true) {
		return s.Apply, errors.New("已有更新任务正在进行中")
	}
	if s.RestartConfigured == false {
		u.applying.Store(false)
		return s.Apply, errors.New("服务未启用自重启能力，无法安全自动更新")
	}
	rel := s.Latest

	u.mu.Lock()
	now := time.Now().UTC().Format(time.RFC3339)
	u.status.Apply = ApplyStatus{
		Phase: PhaseDownloading, Message: "准备下载新版本…", From: u.current, To: rel.Tag,
		StartedAt: now, Actor: actor, Log: []string{}, TargetPath: u.targetPath,
	}
	u.mu.Unlock()
	u.auditLog("update.apply", fmt.Sprintf("%s 触发 %s → %s", actor, u.current, rel.Tag))

	u.wg.Add(1)
	go func() {
		defer u.wg.Done()
		defer u.applying.Store(false)
		ctx, cancel := context.WithTimeout(u.parentOrBackground(), 10*time.Minute)
		defer cancel()
		u.runApply(ctx, rel)
	}()
	return u.Status().Apply, nil
}

func (u *Updater) setApply(phase string, percent int, message string) {
	u.mu.Lock()
	u.status.Apply.Phase = phase
	u.status.Apply.Percent = percent
	u.status.Apply.Message = message
	u.mu.Unlock()
}

func (u *Updater) logApply(format string, args ...any) {
	line := time.Now().Format("15:04:05") + " " + fmt.Sprintf(format, args...)
	u.mu.Lock()
	u.status.Apply.Log = append(u.status.Apply.Log, line)
	if len(u.status.Apply.Log) > 80 {
		u.status.Apply.Log = append([]string(nil), u.status.Apply.Log[len(u.status.Apply.Log)-80:]...)
	}
	u.mu.Unlock()
	slog.Info("update", "phase", u.Status().Apply.Phase, "detail", line)
}

func (u *Updater) finishApply(err error) {
	u.mu.Lock()
	a := &u.status.Apply
	a.FinishedAt = time.Now().UTC().Format(time.RFC3339)
	a.Restarting = false
	if err != nil {
		a.Phase = PhaseFailed
		a.Error = err.Error()
		a.Message = "更新失败，已回滚到原版本"
	} else {
		a.Phase = PhaseDone
		a.Percent = 100
		a.Message = "更新完成，服务已使用新版本重启"
	}
	u.mu.Unlock()
	if err != nil {
		u.auditLog("update.failed", err.Error())
	} else {
		u.auditLog("update.done", a.To)
	}
}
