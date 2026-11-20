package api

import (
	"net/http"
	"testing"

	"github.com/Mr-losfer/s3bridge/internal/obs"
)

// TestClassify 锁定异常分类的优先级：S3 → 401/403 → 具体业务前缀 → 兜底。
// 这张表是「异常日志分类筛选」的契约，改动归类顺序必须先改这里。
func TestClassify(t *testing.T) {
	tests := []struct {
		name   string
		path   string
		status int
		want   string
	}{
		{"S3 签名错误优先归 S3", "/s3/default/a.png", http.StatusForbidden, obs.CatS3},
		{"未登录任意接口归鉴权", "/api/plugins", http.StatusUnauthorized, obs.CatAuth},
		{"权限不足归鉴权", "/api/objects/download", http.StatusForbidden, obs.CatAuth},
		{"登录频率限制归鉴权", "/api/auth/login", http.StatusTooManyRequests, obs.CatAuth},
		{"上传失败归上传", "/api/upload", http.StatusBadRequest, obs.CatUpload},
		{"上传列表错误也归上传", "/api/uploads", http.StatusNotFound, obs.CatUpload},
		{"对象缺失归存储", "/api/objects/download", http.StatusNotFound, obs.CatStorage},
		{"桶非空归存储", "/api/buckets/default", http.StatusConflict, obs.CatStorage},
		{"对象接口 5xx 归系统", "/api/objects/download", http.StatusInternalServerError, obs.CatSystem},
		{"桶接口 5xx 归系统", "/api/buckets", http.StatusBadGateway, obs.CatSystem},
		{"插件错误归插件", "/api/plugins/workbuddy", http.StatusBadRequest, obs.CatPlugin},
		{"插件 5xx 归系统", "/api/plugins/workbuddy", http.StatusInternalServerError, obs.CatSystem},
		{"MCP 错误归插件", "/mcp", http.StatusBadRequest, obs.CatPlugin},
		{"密钥错误归鉴权", "/api/keys", http.StatusNotFound, obs.CatAuth},
		{"其余 4xx 归请求异常", "/api/admin/stats", http.StatusBadRequest, obs.CatRequest},
		{"其余 5xx 归系统", "/api/admin/stats", http.StatusInternalServerError, obs.CatSystem},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := classify(tt.path, tt.status); got != tt.want {
				t.Fatalf("classify(%q, %d) = %q, want %q", tt.path, tt.status, got, tt.want)
			}
		})
	}
}

// TestShouldObserve 确保只采集业务流量，静态资源不进指标。
func TestShouldObserve(t *testing.T) {
	tests := []struct {
		path string
		want bool
	}{
		{"/api/gallery", true},
		{"/api/", true},
		{"/s3", true},
		{"/s3/default/a.png", true},
		{"/mcp", true},
		{"/mcp/", true},
		{"/", false},
		{"/assets/index-abc.js", false},
		{"/favicon.ico", false},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := shouldObserve(tt.path); got != tt.want {
				t.Fatalf("shouldObserve(%q) = %v, want %v", tt.path, got, tt.want)
			}
		})
	}
}

// TestNormalizeRoute 保证同一接口不会因为路径里的 ID 不同而被拆成多行排行。
func TestNormalizeRoute(t *testing.T) {
	tests := []struct {
		path string
		want string
	}{
		{"/s3", "/s3"},
		{"/s3/default", "/s3/{bucket}"},
		{"/s3/default/image/202611/a.png", "/s3/{bucket}/{key...}"},
		{"/api/plugins/plg_1234567890abcdef", "/api/plugins/{id}"},
		{"/api/plugins", "/api/plugins"},
	}
	for _, tt := range tests {
		t.Run(tt.path, func(t *testing.T) {
			if got := normalizeRoute(tt.path); got != tt.want {
				t.Fatalf("normalizeRoute(%q) = %q, want %q", tt.path, got, tt.want)
			}
		})
	}
}
