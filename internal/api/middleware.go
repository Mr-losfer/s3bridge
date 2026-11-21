package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/obs"
)

// recorder 包装 ResponseWriter，记录状态码、响应字节数与错误响应体片段。
//
// 实现 Unwrap 让 http.NewResponseController 能找到原始 ResponseWriter，
// 从而不影响 Flush/Hijack/Deadline 等扩展能力。
type recorder struct {
	http.ResponseWriter
	status int
	bytes  int64
	body   []byte // 仅记录 4xx/5xx 的前 1KB，用于异常日志的可读消息
}

func (r *recorder) WriteHeader(code int) {
	if r.status == 0 {
		r.status = code
	}
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Write(b []byte) (int, error) {
	if r.status == 0 {
		r.status = http.StatusOK
	}
	n, err := r.ResponseWriter.Write(b)
	r.bytes += int64(n)
	if r.status >= 400 && len(r.body) < 1024 && n > 0 {
		room := 1024 - len(r.body)
		if n < room {
			room = n
		}
		r.body = append(r.body, b[:room]...)
	}
	return n, err
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }

func (r *recorder) Flush() {
	_ = http.NewResponseController(r.ResponseWriter).Flush()
}

// slowRequest 是判定「慢请求」的阈值：超过即写入异常日志，便于事后定位。
const slowRequest = 800 * time.Millisecond

// observe 是全站请求采集中间件。
//
// 只观测 /api、/s3、/mcp 三类业务流量，静态资源不计入，避免噪音淹没真实指标。
// 写路径只做原子累加，异常事件才加锁，因此对吞吐影响可忽略。
func (s *Server) observe(next http.Handler) http.Handler {
	collector := s.core.Obs
	if collector == nil {
		return next
	}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !shouldObserve(r.URL.Path) {
			next.ServeHTTP(w, r)
			return
		}
		start := time.Now()
		rec := &recorder{ResponseWriter: w}
		next.ServeHTTP(rec, r)
		dur := time.Since(start)
		status := rec.status
		if status == 0 {
			status = http.StatusOK
		}
		route := rec.Header().Get("X-Route-Pattern")
		if route == "" {
			route = normalizeRoute(r.URL.Path)
		}
		in := r.ContentLength
		if in < 0 {
			in = 0
		}
		actor := ""
		if v := r.Context().Value(ctxActorKey{}); v != nil {
			actor, _ = v.(string)
		}
		collector.Record(obs.Record{
			Method: r.Method, Path: r.URL.Path, Route: route,
			Actor: actor, IP: clientIP(r), Status: status, Latency: dur,
			BytesIn: in, BytesOut: rec.bytes,
			Upload: strings.HasPrefix(r.URL.Path, "/api/upload"),
		})

		if status >= 400 || dur >= slowRequest {
			collector.Event(buildEvent(r, rec, route, status, dur, actor))
		}
	})
}

// buildEvent 把一次失败/慢请求转成结构化事件。
func buildEvent(r *http.Request, rec *recorder, route string, status int, dur time.Duration, actor string) obs.Event {
	level := obs.LevelWarn
	if status >= 500 {
		level = obs.LevelError
	}
	cat := classify(r.URL.Path, status)
	msg := strings.TrimSpace(string(rec.body))
	if msg != "" {
		msg = parseErrorMessage(rec.body)
	}
	if msg == "" {
		if dur >= slowRequest && status < 400 {
			msg = "慢请求：" + route
		} else {
			msg = http.StatusText(status)
		}
	}
	fields := map[string]string{"route": route}
	if q := r.URL.RawQuery; q != "" && len(q) <= 200 {
		fields["query"] = q
	}
	if ua := r.UserAgent(); ua != "" && len(ua) <= 120 {
		fields["ua"] = ua
	}
	return obs.Event{
		Time: time.Now().UTC(), Level: level, Category: cat,
		Status: status, Method: r.Method, Route: route, Path: r.URL.Path,
		Actor: actor, IP: clientIP(r), LatencyMs: float64(dur.Microseconds()) / 1000,
		Message: msg, Fields: fields,
	}
}

func classify(path string, status int) string {
	// S3 的 401/403 属于签名问题，归到 S3 分类更利于排查。
	if strings.HasPrefix(path, "/s3") {
		return obs.CatS3
	}
	// 鉴权失败优先归类：未登录访问任意接口都应落在「鉴权」而不是「请求异常」。
	switch status {
	case http.StatusUnauthorized, http.StatusForbidden:
		return obs.CatAuth
	case http.StatusTooManyRequests:
		if strings.Contains(path, "/auth/") {
			return obs.CatAuth
		}
	}
	switch {
	case strings.HasPrefix(path, "/api/upload"):
		return obs.CatUpload
	case strings.HasPrefix(path, "/api/auth/"), strings.Contains(path, "/keys"):
		return obs.CatAuth
	case strings.HasPrefix(path, "/api/objects"), strings.HasPrefix(path, "/api/buckets"):
		// 对象与桶的 4xx 多为「文件不存在 / 桶非空 / 越权删除」，
		// 归到「存储」比笼统的「请求异常」更贴近排查动作。
		if status >= 500 {
			return obs.CatSystem
		}
		return obs.CatStorage
	case strings.HasPrefix(path, "/api/plugins"), strings.HasPrefix(path, "/mcp"):
		if status >= 500 {
			return obs.CatSystem
		}
		return obs.CatPlugin
	case status >= 500:
		return obs.CatSystem
	}
	return obs.CatRequest
}

// parseErrorMessage 从错误响应体里抽取可读消息（支持 {"error":...} 与任意文本）。
func parseErrorMessage(body []byte) string {
	const max = 300
	var payload struct {
		Error  string `json:"error"`
		Code   string `json:"code"`
		Detail string `json:"message"`
	}
	if err := json.Unmarshal(body, &payload); err == nil {
		out := firstNonEmpty(payload.Error, payload.Detail)
		if out != "" {
			return truncate(out, max)
		}
	}
	return truncate(strings.TrimSpace(string(body)), max)
}

func shouldObserve(path string) bool {
	// 内部探测每分钟一次，计入指标会稀释真实流量。
	if strings.HasPrefix(path, "/api/internal/") {
		return false
	}
	return strings.HasPrefix(path, "/api/") || path == "/mcp" || path == "/mcp/" ||
		strings.HasPrefix(path, "/s3")
}

// normalizeRoute 在缺少路由模板时按路径形态归一化，避免同一接口因 ID 不同被拆成多行。
func normalizeRoute(p string) string {
	switch {
	case strings.HasPrefix(p, "/s3"):
		rest := strings.Trim(strings.TrimPrefix(p, "/s3"), "/")
		if rest == "" {
			return "/s3"
		}
		segs := strings.Split(rest, "/")
		if len(segs) == 1 {
			return "/s3/{bucket}"
		}
		return "/s3/{bucket}/{key...}"
	case p == "/mcp" || p == "/mcp/":
		return "/mcp"
	case strings.HasPrefix(p, "/api/"):
		segs := strings.Split(strings.TrimPrefix(p, "/api/"), "/")
		out := make([]string, 0, len(segs)+1)
		out = append(out, "/api")
		for i, seg := range segs {
			if seg == "" {
				continue
			}
			if i == 1 && isResourceRoot(segs[0]) {
				out = append(out, "{id}")
				continue
			}
			if looksLikeID(seg) {
				out = append(out, "{id}")
				continue
			}
			out = append(out, seg)
		}
		return strings.Join(out, "/")
	}
	return p
}

func isResourceRoot(seg string) bool {
	switch seg {
	case "plugins", "users", "buckets", "keys", "alerts", "docs":
		return true
	}
	return false
}

func looksLikeID(seg string) bool {
	if len(seg) < 8 {
		return false
	}
	for _, p := range []string{"obj_", "key_", "u_", "pl_", "req_", "ev_", "ak_"} {
		if strings.HasPrefix(seg, p) {
			return true
		}
	}
	// 纯十六进制且长度 >= 16 的段基本是 ID。
	if len(seg) >= 16 {
		for _, c := range seg {
			if !(c >= '0' && c <= '9' || c >= 'a' && c <= 'f' || c >= 'A' && c <= 'F') {
				return false
			}
		}
		return true
	}
	return false
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// ctxActorKey 用于在中间件与路由之间传递操作者，避免重复解析 Token。
type ctxActorKey struct{}
