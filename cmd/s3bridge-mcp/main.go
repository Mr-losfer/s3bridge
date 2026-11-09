// s3bridge-mcp 是 MCP 的 stdio 入口。
//
// 两种运行模式（由环境变量选择）：
//  1. 本地模式（默认）：直接打开 S3BRIDGE_DATA_DIR 的数据，无需启动 HTTP 服务；
//  2. HTTP 桥模式：设置 S3BRIDGE_URL 后，把 stdin 的 JSON-RPC 转发到运行中的
//     <URL>/mcp，适合主服务已在运行时使用（避免两个进程同时写状态文件）。
//
// 协议日志一律写 stderr，stdout 只输出 JSON-RPC 消息。
package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/config"
	"github.com/Mr-losfer/s3bridge/internal/mcp"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

var version = "dev"

type handlerFunc func(context.Context, []byte) []byte

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	handler, cleanup, err := buildHandler()
	if err != nil {
		fmt.Fprintln(os.Stderr, "初始化 MCP 服务失败:", err)
		os.Exit(1)
	}
	defer cleanup()

	scanner := bufio.NewScanner(os.Stdin)
	scanner.Buffer(make([]byte, 64*1024), 32<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	for scanner.Scan() {
		line := bytes.TrimSpace(scanner.Bytes())
		if len(line) == 0 {
			continue
		}
		res := handler(context.Background(), line)
		if len(res) == 0 {
			continue // notification，无响应
		}
		_, _ = out.Write(res)
		_ = out.WriteByte('\n')
		_ = out.Flush()
	}
	if err := scanner.Err(); err != nil {
		fmt.Fprintln(os.Stderr, "读取 stdin 失败:", err)
		os.Exit(1)
	}
}

func buildHandler() (handlerFunc, func(), error) {
	if base := strings.TrimRight(os.Getenv("S3BRIDGE_URL"), "/"); base != "" {
		token := os.Getenv("S3BRIDGE_TOKEN")
		if token == "" {
			return nil, nil, errors.New("HTTP 桥模式需要设置 S3BRIDGE_TOKEN（管理员 JWT 或 sk_ 开头的 API Key）")
		}
		client := &http.Client{Timeout: 90 * time.Second}
		h := func(ctx context.Context, line []byte) []byte {
			req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/mcp", bytes.NewReader(line))
			if err != nil {
				return rpcError(-32603, err.Error())
			}
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+token)
			resp, err := client.Do(req)
			if err != nil {
				return rpcError(-32603, "连接 S3Bridge 失败: "+err.Error())
			}
			defer resp.Body.Close()
			body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			if err != nil {
				return rpcError(-32603, err.Error())
			}
			if resp.StatusCode >= 400 {
				return rpcError(-32001, fmt.Sprintf("S3Bridge 返回 HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body))))
			}
			return body
		}
		slog.Warn("MCP stdio 已启动（HTTP 桥模式）", "url", base)
		return h, func() {}, nil
	}

	cfg, err := config.Load()
	if err != nil {
		return nil, nil, err
	}
	core, err := app.New(cfg, version)
	if err != nil {
		return nil, nil, err
	}
	server := mcp.New(core)
	// 本地模式不启动健康调度器：避免与可能在运行的 HTTP 服务争抢状态文件。
	p := &auth.Principal{
		UserID: "usr_admin", Username: "admin", Role: store.RoleAdmin,
		Via: "stdio", Perms: map[store.Permission]bool{},
	}
	for _, perm := range store.AllPermissions {
		p.Perms[perm] = true
	}
	h := func(ctx context.Context, line []byte) []byte {
		return server.Handle(ctx, line, p)
	}
	slog.Warn("MCP stdio 已启动（本地模式）", "dataDir", cfg.DataDir)
	return h, func() { _ = core.Close() }, nil
}

func rpcError(code int, message string) []byte {
	b, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0", "id": nil,
		"error": map[string]any{"code": code, "message": message},
	})
	return b
}
