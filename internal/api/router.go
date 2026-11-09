// Package api 提供后台 REST API。
//
// Go 1.21 的 net/http.ServeMux 尚不支持方法路由与路径参数，因此这里实现一个
// 约 150 行的轻量路由器：零依赖、支持 {param} / {rest...}、按路由声明权限，
// 认证与权限检查统一在分发前完成，handler 不再散落鉴权代码。
package api

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

type APIError struct {
	Status  int
	Code    string
	Message string
}

func (e *APIError) Error() string { return e.Message }

func errf(status int, code, format string, args ...any) *APIError {
	return &APIError{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

type Ctx struct {
	W         http.ResponseWriter
	R         *http.Request
	Params    map[string]string
	Principal *auth.Principal
	RequestID string
}

func (c *Ctx) Param(key string) string { return c.Params[key] }

func (c *Ctx) Query(key, def string) string {
	if v := strings.TrimSpace(c.R.URL.Query().Get(key)); v != "" {
		return v
	}
	return def
}

func (c *Ctx) IntQuery(key string, def int) int {
	if n, err := parseInt(c.Query(key, "")); err == nil {
		return n
	}
	return def
}

func (c *Ctx) JSON(status int, v any) {
	c.W.Header().Set("Content-Type", "application/json; charset=utf-8")
	c.W.WriteHeader(status)
	_ = json.NewEncoder(c.W).Encode(v)
}

func (c *Ctx) Bind(v any) error {
	dec := json.NewDecoder(io.LimitReader(c.R.Body, 2<<20))
	if err := dec.Decode(v); err != nil {
		return errf(http.StatusBadRequest, "invalid_json", "请求体不是合法 JSON: %v", err)
	}
	return nil
}

func (c *Ctx) Actor() string {
	if c.Principal == nil {
		return "anonymous"
	}
	return c.Principal.Username
}

func (c *Ctx) UserID() string {
	if c.Principal == nil {
		return ""
	}
	return c.Principal.UserID
}

func (c *Ctx) IsAdmin() bool { return c.Principal != nil && c.Principal.IsAdmin() }

func (c *Ctx) Require(perm store.Permission) error {
	if c.Principal == nil || !c.Principal.Can(perm) {
		return errf(http.StatusForbidden, "forbidden", "权限不足：需要 %s", perm)
	}
	return nil
}

type Handler func(*Ctx) error

type route struct {
	method   string
	pattern  string
	segments []string
	catchAll string
	handler  Handler
	perms    []store.Permission
	public   bool
}

type Router struct {
	mu      sync.RWMutex
	routes  []route
	authSvc *auth.Service
	guard   *loginGuard
}

func NewRouter(authSvc *auth.Service) *Router {
	return &Router{authSvc: authSvc, guard: newLoginGuard()}
}

// Handle 注册需要登录与指定权限的路由。
func (r *Router) Handle(method, pattern string, h Handler, perms ...store.Permission) {
	r.add(route{method: method, pattern: pattern, segments: split(pattern), handler: h, perms: perms})
}

// Public 注册无需登录的路由。
func (r *Router) Public(method, pattern string, h Handler) {
	r.add(route{method: method, pattern: pattern, segments: split(pattern), handler: h, public: true})
}

func (r *Router) add(rt route) {
	for _, seg := range rt.segments {
		if strings.HasSuffix(seg, "...}") && strings.HasPrefix(seg, "{") {
			rt.catchAll = seg[1 : len(seg)-4]
		}
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.routes = append(r.routes, rt)
}

func (r *Router) LoginGuard() *loginGuard { return r.guard }

func (r *Router) ServeHTTP(w http.ResponseWriter, req *http.Request) {
	requestID := "req_" + fmt.Sprintf("%x", time.Now().UnixNano())
	w.Header().Set("X-Request-Id", requestID)
	w.Header().Set("X-Content-Type-Options", "nosniff")
	if origin := req.Header.Get("Origin"); origin != "" {
		w.Header().Set("Access-Control-Allow-Origin", origin)
		w.Header().Set("Access-Control-Allow-Credentials", "true")
		w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Requested-With")
		w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, PATCH, DELETE, OPTIONS")
		w.Header().Set("Vary", "Origin")
	}
	if req.Method == http.MethodOptions {
		w.WriteHeader(http.StatusNoContent)
		return
	}

	defer func() {
		if rec := recover(); rec != nil {
			slog.Error("API panic", "requestId", requestID, "panic", rec)
			writeError(w, errf(http.StatusInternalServerError, "internal_error", "服务内部错误"))
		}
	}()

	r.mu.RLock()
	routes := append([]route(nil), r.routes...)
	r.mu.RUnlock()

	var matched *route
	params := map[string]string{}
	for i := range routes {
		rt := &routes[i]
		if rt.method != req.Method {
			continue
		}
		if p, ok := match(rt, req.URL.Path); ok {
			matched, params = rt, p
			break
		}
	}
	if matched == nil {
		writeError(w, errf(http.StatusNotFound, "not_found", "接口不存在: %s %s", req.Method, req.URL.Path))
		return
	}

	ctx := &Ctx{W: w, R: req, Params: params, RequestID: requestID}
	if !matched.public {
		if r.authSvc == nil {
			writeError(w, errf(http.StatusInternalServerError, "internal_error", "认证服务未初始化"))
			return
		}
		p, err := r.authSvc.Authenticate(req.Header.Get("Authorization"))
		if err != nil {
			writeError(w, errf(http.StatusUnauthorized, "unauthorized", "请先登录或提供有效 API Key"))
			return
		}
		ctx.Principal = p
		for _, perm := range matched.perms {
			if !p.Can(perm) {
				writeError(w, errf(http.StatusForbidden, "forbidden", "权限不足：需要 %s", perm))
				return
			}
		}
	}

	if err := matched.handler(ctx); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) {
			writeError(w, apiErr)
			return
		}
		slog.Error("API 处理失败", "requestId", requestID, "err", err)
		writeError(w, errf(http.StatusInternalServerError, "internal_error", err.Error()))
	}
}

func writeError(w http.ResponseWriter, e *APIError) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(e.Status)
	_ = json.NewEncoder(w).Encode(map[string]any{
		"error": e.Message, "code": e.Code, "status": e.Status,
	})
}

func split(pattern string) []string {
	trimmed := strings.Trim(pattern, "/")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "/")
}

func match(rt *route, path string) (map[string]string, bool) {
	parts := split(path)
	out := map[string]string{}
	for i, seg := range rt.segments {
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "...}") {
			key := seg[1 : len(seg)-4]
			if i <= len(parts) {
				out[key] = strings.Join(parts[i:], "/")
				return out, true
			}
			return nil, false
		}
		if i >= len(parts) {
			return nil, false
		}
		if strings.HasPrefix(seg, "{") && strings.HasSuffix(seg, "}") {
			if parts[i] == "" {
				return nil, false
			}
			out[seg[1:len(seg)-1]] = parts[i]
			continue
		}
		if seg != parts[i] {
			return nil, false
		}
	}
	if len(parts) != len(rt.segments) {
		return nil, false
	}
	return out, true
}

func parseInt(s string) (int, error) { return strconv.Atoi(strings.TrimSpace(s)) }

// ---------- 登录限流：防止口令爆破 ----------

type bucket struct {
	count   int
	resetAt time.Time
}

type loginGuard struct {
	mu      sync.Mutex
	buckets map[string]*bucket
}

func newLoginGuard() *loginGuard {
	return &loginGuard{buckets: map[string]*bucket{}}
}

const loginLimit = 8

func (g *loginGuard) Allow(key string) bool {
	g.mu.Lock()
	defer g.mu.Unlock()
	now := time.Now()
	// 懒清理：无后台 goroutine，避免限流器本身成为泄漏源。
	for k, b := range g.buckets {
		if now.After(b.resetAt) {
			delete(g.buckets, k)
		}
	}
	b, ok := g.buckets[key]
	if !ok || now.After(b.resetAt) {
		g.buckets[key] = &bucket{count: 1, resetAt: now.Add(time.Minute)}
		return true
	}
	if b.count >= loginLimit {
		return false
	}
	b.count++
	return true
}

func clientIP(r *http.Request) string {
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err != nil {
		return r.RemoteAddr
	}
	return host
}
