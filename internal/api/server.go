package api

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/mcp"
	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/web"
)

type Server struct {
	core   *app.Core
	router *Router
	mcp    *mcp.Server
	web    http.Handler
}

func New(core *app.Core) *Server {
	s := &Server{
		core: core, router: NewRouter(core.Auth),
		mcp: mcp.New(core), web: web.Handler(),
	}
	s.routes()
	return s
}

func (s *Server) routes() {
	r := s.router

	// 公开接口
	r.Public("GET", "/api/health", s.health)
	r.Public("GET", "/api/site", s.publicSite)
	r.Public("POST", "/api/auth/login", s.login)

	// 当前身份
	r.Handle("GET", "/api/me", s.me)
	r.Handle("POST", "/api/auth/password", s.changePassword)
	r.Handle("POST", "/api/auth/logout", s.logout)

	// 站点与设置
	r.Handle("GET", "/api/admin/site", s.namedTool("site_info"))
	r.Handle("GET", "/api/admin/stats", s.namedTool("site_stats"))
	r.Handle("GET", "/api/admin/settings", s.namedTool("system_settings_get"))
	r.Handle("PUT", "/api/admin/settings", s.namedTool("system_settings_update"), store.PermSystemManage)
	r.Handle("GET", "/api/permissions", s.permissions)

	// 插件（静态路径必须注册在 {id} 之前）
	r.Handle("GET", "/api/plugins", s.namedTool("plugin_list"), store.PermPluginRead)
	r.Handle("GET", "/api/plugins/registry", s.namedTool("plugin_registry"), store.PermPluginRead)
	r.Handle("GET", "/api/plugins/docs", s.namedTool("plugin_docs"), store.PermPluginRead)
	r.Handle("GET", "/api/plugins/{id}", s.namedTool("plugin_get"), store.PermPluginRead)
	r.Handle("PATCH", "/api/plugins/{id}", s.namedTool("plugin_patch"), store.PermPluginWrite)
	r.Handle("PUT", "/api/plugins/{id}", s.namedTool("plugin_replace"), store.PermPluginWrite)
	r.Handle("POST", "/api/plugins/{id}/params", s.namedTool("plugin_params_set"), store.PermPluginWrite)
	r.Handle("POST", "/api/plugins/{id}/update", s.namedTool("plugin_update"), store.PermPluginWrite)
	r.Handle("POST", "/api/plugins/{id}/toggle", s.namedTool("plugin_toggle"), store.PermPluginWrite)
	r.Handle("POST", "/api/plugins/{id}/debug", s.namedTool("plugin_debug"), store.PermPluginDebug)
	r.Handle("POST", "/api/plugins/{id}/check", s.namedTool("plugin_health_check"), store.PermPluginWrite)
	r.Handle("PUT", "/api/plugins/{id}/health", s.namedTool("plugin_health_config"), store.PermPluginWrite)
	r.Handle("GET", "/api/plugins/{id}/changes", s.namedTool("plugin_changes"), store.PermPluginRead)
	r.Handle("POST", "/api/plugins/{id}/parse-token", s.namedTool("plugin_parse_token"), store.PermPluginWrite)

	// 告警
	r.Handle("GET", "/api/alerts", s.namedTool("alerts_list"), store.PermPluginRead)
	r.Handle("POST", "/api/alerts/{id}/ack", s.namedTool("alerts_ack"), store.PermPluginWrite)

	// 文档中心
	r.Handle("GET", "/api/docs", s.docsIndex)
	r.Handle("GET", "/api/docs/{name}", s.docsGet)

	// 上传与对象（文件流单独处理，其余复用 MCP 工具）
	r.Handle("POST", "/api/upload", s.upload, store.PermUploadWrite)
	r.Handle("GET", "/api/uploads", s.namedTool("uploads_list"), store.PermUploadRead)
	r.Handle("GET", "/api/uploads/stats", s.namedTool("site_stats"), store.PermUploadRead)
	r.Handle("GET", "/api/objects/download", s.download, store.PermUploadRead)
	r.Handle("DELETE", "/api/objects", s.deleteObject, store.PermUploadWrite)

	// 存储桶
	r.Handle("GET", "/api/buckets", s.namedTool("bucket_list"), store.PermBucketRead)
	r.Handle("POST", "/api/buckets", s.namedTool("bucket_create"), store.PermBucketWrite)
	r.Handle("PATCH", "/api/buckets/{name}", s.namedTool("bucket_update"), store.PermBucketWrite)
	r.Handle("DELETE", "/api/buckets/{name}", s.namedTool("bucket_delete"), store.PermBucketWrite)

	// 用户与权限
	r.Handle("GET", "/api/users", s.namedTool("user_list"), store.PermUserManage)
	r.Handle("POST", "/api/users", s.namedTool("user_create"), store.PermUserManage)
	r.Handle("PATCH", "/api/users/{id}", s.namedTool("user_update"), store.PermUserManage)
	r.Handle("DELETE", "/api/users/{id}", s.userDisable, store.PermUserManage)
	r.Handle("POST", "/api/users/{id}/password", s.userResetPassword, store.PermUserManage)

	// API Key / S3 凭据
	r.Handle("GET", "/api/keys", s.namedTool("apikey_list"))
	r.Handle("POST", "/api/keys", s.keyCreate)
	r.Handle("GET", "/api/keys/{id}/secret", s.keyReveal)
	r.Handle("DELETE", "/api/keys/{id}", s.keyRevoke)
	r.Handle("GET", "/api/s3/credentials", s.s3Credentials)

	// 版本更新（GitHub Releases 检测 + 一键自动更新）
	r.Handle("GET", "/api/update/status", s.updateStatus)
	r.Handle("POST", "/api/update/check", s.updateCheck)
	r.Handle("POST", "/api/update/apply", s.updateApply, store.PermSystemManage)
	r.Handle("POST", "/api/update/dismiss", s.updateDismiss)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p := r.URL.Path
	switch {
	case p == "/healthz", p == "/api/health":
		s.health(&Ctx{W: w, R: r, Params: map[string]string{}})
	case p == "/mcp", p == "/mcp/":
		s.mcpHTTP(w, r)
	case strings.HasPrefix(p, "/api/"):
		s.router.ServeHTTP(w, r)
	case strings.HasPrefix(p, "/s3"):
		s.core.S3.ServeHTTP(w, r)
	default:
		s.web.ServeHTTP(w, r)
	}
}

// mcpHTTP 提供 MCP Streamable HTTP（单条 / 批量 JSON-RPC POST）。
func (s *Server) mcpHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", "POST")
		http.Error(w, "MCP 端点仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
	if err != nil {
		http.Error(w, "读取请求体失败", http.StatusBadRequest)
		return
	}
	principal, err := s.core.Auth.Authenticate(r.Header.Get("Authorization"))
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":null,"error":{"code":-32001,"message":"需要有效的 Bearer Token 或 API Key"}}`))
		return
	}
	w.Header().Set("Content-Type", "application/json")
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) > 0 && trimmed[0] == '[' {
		var batch []json.RawMessage
		if err := json.Unmarshal(trimmed, &batch); err != nil {
			http.Error(w, "批量请求格式错误", http.StatusBadRequest)
			return
		}
		out := make([]json.RawMessage, 0, len(batch))
		for _, item := range batch {
			if res := s.mcp.Handle(r.Context(), item, principal); res != nil {
				out = append(out, res)
			}
		}
		_ = json.NewEncoder(w).Encode(out)
		return
	}
	res := s.mcp.Handle(r.Context(), body, principal)
	if res == nil {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	_, _ = w.Write(res)
}

func (s *Server) health(c *Ctx) error {
	c.JSON(http.StatusOK, map[string]any{
		"status": "ok", "version": s.core.Version,
		"uptime": time.Since(s.core.StartedAt).Round(time.Second).String(),
		"time":   time.Now().UTC().Format(time.RFC3339),
	})
	return nil
}

func (s *Server) publicSite(c *Ctx) error {
	var name string
	var plugins int
	_ = s.core.Store.View(func(st *store.State) error {
		name, plugins = st.Settings.SiteName, len(st.Plugins)
		return nil
	})
	c.JSON(http.StatusOK, map[string]any{
		"name": name, "version": s.core.Version,
		"plugins": plugins, "s3Endpoint": "/s3", "region": s.core.S3.Region(),
		"mcpEndpoint": "/mcp",
	})
	return nil
}
