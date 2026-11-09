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
