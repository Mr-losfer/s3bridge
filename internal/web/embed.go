// Package web 把前端构建产物嵌入 Go 二进制。
//
// 前端源码在 web/，Vite 的 outDir 指向 internal/web/dist/，
// 构建产物随仓库提交，因此 clone 后只需 `go build` 即可获得完整可用的单文件服务，
// 部署机无需安装 Node.js。
package web

import (
	"embed"
	"io/fs"
	"net/http"
	"strings"
)

//go:embed all:dist
var distFS embed.FS

func FS() (fs.FS, error) { return fs.Sub(distFS, "dist") }

// Handler 提供静态资源与 SPA history fallback。
func Handler() http.Handler {
	sub, err := FS()
	if err != nil {
		return http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "前端资源未构建", http.StatusServiceUnavailable)
		})
	}
	fileServer := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		path := strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/"), "/")
		if path == "" {
			path = "index.html"
		}
		if _, err := fs.Stat(sub, path); err != nil {
			// 前端路由（如 /plugins/workbuddy）统一回落到 index.html。
			w.Header().Set("Cache-Control", "no-cache")
			serveIndex(w, r, sub)
			return
		}
		if path == "index.html" {
			w.Header().Set("Cache-Control", "no-cache")
		} else if strings.HasPrefix(path, "assets/") {
			w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
		}
		fileServer.ServeHTTP(w, r)
	})
}

func serveIndex(w http.ResponseWriter, r *http.Request, sub fs.FS) {
	b, err := fs.ReadFile(sub, "index.html")
	if err != nil {
		http.Error(w, "前端资源未构建", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(b)
}
