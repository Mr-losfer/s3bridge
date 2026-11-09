// Package app 负责依赖装配：把配置、存储、认证、插件、上传、S3、健康调度
// 组合成一个可复用的 Core。HTTP 服务与 stdio MCP 服务共享同一套装配逻辑，
// 避免两处各写一遍初始化而漂移。
package app

import (
	"fmt"
	"log/slog"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/config"
	"github.com/Mr-losfer/s3bridge/internal/health"
	"github.com/Mr-losfer/s3bridge/internal/plugin"
	"github.com/Mr-losfer/s3bridge/internal/plugins/workbuddy"
	"github.com/Mr-losfer/s3bridge/internal/s3gateway"
	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/updater"
	"github.com/Mr-losfer/s3bridge/internal/upload"
)

type Core struct {
	Cfg       *config.Config
	Store     store.Store
	Auth      *auth.Service
	Registry  *plugin.Registry
	Plugins   *plugin.Manager
	Uploads   *upload.Service
	S3        *s3gateway.Gateway
	Health    *health.Scheduler
	Updater   *updater.Updater
	Version   string
	StartedAt time.Time
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

	reg := plugin.NewRegistry()
	if err := reg.Register(workbuddy.Descriptor()); err != nil {
		return nil, fmt.Errorf("注册插件: %w", err)
	}
	mgr := plugin.NewManager(st, reg,
		func(v string) (string, error) { return auth.Encrypt(cfg.MasterKey, v) },
		func(v string) (string, error) { return auth.Decrypt(cfg.MasterKey, v) },
		cfg.DefaultHealthEvery,
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

	return &Core{
		Cfg: cfg, Store: st, Auth: authSvc, Registry: reg, Plugins: mgr,
		Uploads: uploads, S3: s3gw, Health: health.New(mgr), Updater: upd,
		Version: version, StartedAt: time.Now().UTC(),
	}, nil
}

// Close 停止后台 goroutine 并释放资源。
func (c *Core) Close() error {
	if c.Health != nil {
		c.Health.Stop()
	}
	if c.Updater != nil {
		c.Updater.Stop()
	}
	if c.Store != nil {
		return c.Store.Close()
	}
	return nil
}
