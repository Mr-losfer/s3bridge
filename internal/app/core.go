// Package app 负责依赖装配：把配置、存储、认证、插件、上传、S3、健康调度
// 组合成一个可复用的 Core。HTTP 服务与 stdio MCP 服务共享同一套装配逻辑，
// 避免两处各写一遍初始化而漂移。
package app

import (
	"context"
	"crypto/sha256"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/config"
	"github.com/Mr-losfer/s3bridge/internal/health"
	"github.com/Mr-losfer/s3bridge/internal/obs"
	"github.com/Mr-losfer/s3bridge/internal/plugin"
	pluginrt "github.com/Mr-losfer/s3bridge/internal/plugin/runtime"
	"github.com/Mr-losfer/s3bridge/internal/plugins/workbuddy"
	"github.com/Mr-losfer/s3bridge/internal/s3gateway"
	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/updater"
	"github.com/Mr-losfer/s3bridge/internal/upload"
	"github.com/Mr-losfer/s3bridge/internal/validity"
)

type Core struct {
	Cfg      *config.Config
	Store    store.Store
	Auth     *auth.Service
	Runtime  *pluginrt.Runtime
	Plugins  *plugin.Manager
	Uploads  *upload.Service
	S3       *s3gateway.Gateway
	Health   *health.Scheduler
	Updater  *updater.Updater
	Obs      *obs.Collector
	Thumbs   *obs.ThumbCache
	Auditor  *obs.Auditor
	Validity *validity.Checker
	// ProbeSecret 供图片巡检向本机回环发 HEAD 时签名，避免探测接口被外部滥用。
	ProbeSecret []byte
	Version     string
	StartedAt   time.Time

	warm sync.WaitGroup // 插件预编译协程，Close 时等待，避免退出时留下半截产物
}

// New 构建核心依赖，并执行首次启动的引导（管理员、默认 bucket、内置插件）。
func New(cfg *config.Config, version string) (*Core, error) {
	st, err := store.Open(cfg.DataDir + "/state.json")
	if err != nil {
		return nil, fmt.Errorf("打开存储: %w", err)
	}
	authSvc := auth.NewService(st, cfg.MasterKey, cfg.SessionSecret, cfg.SessionTTL)
	if err := authSvc.Bootstrap(cfg.BootstrapUser, cfg.BootstrapPassword); err != nil {
		return nil, fmt.Errorf("初始化管理员: %w", err)
	}

	// 插件运行时：内嵌解释器，不依赖外部 Go 工具链。
	rt, err := pluginrt.New(pluginrt.Options{
		PluginsDir:  cfg.PluginsDir,
		LoadTimeout: 30 * time.Second, IdleTimeout: 10 * time.Minute, CallTimeout: 60 * time.Second,
	})
	if err != nil {
		return nil, fmt.Errorf("初始化插件运行时: %w", err)
	}
	mgr := plugin.NewManager(st, rt, cfg.PluginsDir,
		func(v string) (string, error) { return auth.Encrypt(cfg.MasterKey, v) },
		func(v string) (string, error) { return auth.Decrypt(cfg.MasterKey, v) },
		cfg.DefaultHealthEvery,
		[]plugin.Builtin{workbuddy.Builtin()},
	)
	if err := mgr.EnsureBuiltin(); err != nil {
		return nil, fmt.Errorf("初始化插件: %w", err)
	}

	uploads := upload.New(st, mgr, cfg.ObjectDir, cfg.MaxUploadBytes)
	s3gw := s3gateway.New(st, authSvc, uploads, cfg.ObjectDir, cfg.MaxUploadBytes, "us-east-1")

	upd, err := updater.New(updater.Options{
		Current: version, Repo: cfg.UpdateRepo, APIBaseURL: cfg.UpdateAPI,
		Proxy: cfg.UpdateProxy,
		Token: cfg.UpdateToken, AutoCheck: cfg.UpdateAutoCheck,
		Interval: cfg.UpdateInterval, Strategy: cfg.UpdateStrategy, DataDir: cfg.DataDir,
		Audit: func(action, detail string) {
			slog.Info("系统更新审计", "action", action, "detail", detail)
		},
	})
	if err != nil {
		_ = st.Close()
		return nil, fmt.Errorf("初始化版本更新器: %w", err)
	}

	// 探测密钥由主密钥派生，不落盘、不随状态文件导出。
	probeSeed := sha256.Sum256(append([]byte("object-probe:"), cfg.MasterKey...))
	core := &Core{
		Cfg: cfg, Store: st, Auth: authSvc, Runtime: rt, Plugins: mgr,
		Uploads: uploads, S3: s3gw, Health: health.New(mgr), Updater: upd,
		Auditor: obs.NewAuditor(), ProbeSecret: probeSeed[:],
		Version: version, StartedAt: time.Now().UTC(),
	}
	var vSettings store.Settings
	_ = st.View(func(state *store.State) error { vSettings = state.Settings; return nil })
	core.Validity = validity.New(st, validity.Options{
		Enabled:       vSettings.ImageValidityEnabled,
		Interval:      time.Duration(vSettings.ImageValidityIntervalHours) * time.Hour,
		FailThreshold: vSettings.ImageValidityFailThreshold,
		BaseURL:       validity.LoopbackBase(cfg.Addr), Secret: probeSeed[:],
	})
	// 监控是增强能力：初始化失败只降级，不影响上传与 S3 主链路。
	if col, err := obs.New(obs.Options{Dir: cfg.ObsDir}); err == nil {
		core.Obs = col
	} else {
		slog.Warn("监控采集初始化失败，已降级", "err", err)
	}
	if thumbs, err := obs.NewThumbCache(cfg.ThumbDir, 2, 40<<20); err == nil {
		core.Thumbs = thumbs
	} else {
		slog.Warn("缩略图缓存初始化失败，相册将回退原图", "err", err)
	}
	return core, nil
}

// WarmPlugins 在后台预编译缺失产物的插件。
// 失败只记日志：预编译是优化，不是启动的前置条件。
func (c *Core) WarmPlugins(ctx context.Context) {
	c.warm.Add(1)
	go func() {
		defer c.warm.Done()
		c.Plugins.Warmup(ctx)
	}()
}

// Close 停止后台 goroutine 并释放资源。
func (c *Core) Close() error {
	if c.Validity != nil {
		c.Validity.Stop()
	}
	if c.Health != nil {
		c.Health.Stop()
	}
	if c.Updater != nil {
		c.Updater.Stop()
	}
	// 先等预编译结束（最多 20 秒），再关运行时，否则编译子进程会被强杀并留下临时文件。
	waited := make(chan struct{})
	go func() { c.warm.Wait(); close(waited) }()
	select {
	case <-waited:
	case <-time.After(20 * time.Second):
		slog.Warn("插件预编译未在 20 秒内结束，强制继续关闭")
	}
	if c.Runtime != nil {
		_ = c.Runtime.Close()
	}
	if c.Obs != nil {
		_ = c.Obs.Close()
	}
	if c.Store != nil {
		return c.Store.Close()
	}
	return nil
}
