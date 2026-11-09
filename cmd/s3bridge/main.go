package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/api"
	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/config"
	"github.com/Mr-losfer/s3bridge/internal/updater"
)

// version 由 -ldflags "-X main.version=..." 注入。
var version = "dev"

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "s3bridge:", err)
		os.Exit(1)
	}
}

func run() error {
	cfg, err := config.Load()
	if err != nil {
		return err
	}
	setupLogger(cfg.LogLevel)

	core, err := app.New(cfg, version)
	if err != nil {
		return err
	}
	defer core.Close()

	rootCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := core.Health.Start(rootCtx); err != nil {
		return fmt.Errorf("启动健康调度: %w", err)
	}
	if err := core.Updater.Start(rootCtx); err != nil {
		return fmt.Errorf("启动版本检测: %w", err)
	}

	// 自动更新的最后一步由主流程执行：先优雅关闭监听，再用新二进制替换进程映像。
	// 用带缓冲 channel 解耦，更新协程不会因 HTTP 请求结束被取消。
	restartCh := make(chan string, 1)
	core.Updater.SetRestartFunc(func(target string) error {
		select {
		case restartCh <- target:
		default:
		}
		return nil
	})

	handler := api.New(core)
	srv := &http.Server{
		Addr: cfg.Addr, Handler: handler,
		ReadTimeout: cfg.ReadTimeout, WriteTimeout: cfg.WriteTimeout,
		IdleTimeout:    120 * time.Second,
		MaxHeaderBytes: 1 << 20,
	}

	serveErr := make(chan error, 1)
	go func() {
		slog.Info("S3Bridge 已启动",
			"version", version, "addr", cfg.Addr,
			"dataDir", cfg.DataDir, "s3Endpoint", "/s3", "mcpEndpoint", "/mcp")
		fmt.Fprintf(os.Stdout, "\n  S3Bridge %s\n  管理后台   http://%s/\n  S3 接口    http://%s/s3\n  MCP 端点   POST http://%s/mcp\n  数据目录   %s\n\n",
			version, displayAddr(cfg.Addr), displayAddr(cfg.Addr), displayAddr(cfg.Addr), cfg.DataDir)
		serveErr <- srv.ListenAndServe()
	}()

	select {
	case err := <-serveErr:
		if err == nil || errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return fmt.Errorf("HTTP 服务退出: %w", err)
	case <-rootCtx.Done():
		slog.Info("收到退出信号，开始优雅关闭")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			return fmt.Errorf("优雅关闭失败: %w", err)
		}
		return nil
	case target := <-restartCh:
		slog.Info("自动更新：替换完成，准备重启", "target", target)
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutdownCtx); err != nil {
			slog.Warn("重启前关闭服务", "err", err)
		}
		// 成功时进程映像被直接替换，以下代码不会执行；失败才返回错误让运维介入。
		if err := updater.RestartSelf(target); err != nil {
			return fmt.Errorf("自重启失败，请手动启动 %s: %w", target, err)
		}
		return nil
	}
}

func displayAddr(addr string) string {
	if len(addr) > 0 && addr[0] == ':' {
		return "localhost" + addr
	}
	return addr
}

func setupLogger(level string) {
	var lv slog.Level
	switch level {
	case "debug":
		lv = slog.LevelDebug
	case "warn":
		lv = slog.LevelWarn
	case "error":
		lv = slog.LevelError
	default:
		lv = slog.LevelInfo
	}
	h := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: lv})
	slog.SetDefault(slog.New(h))
}
