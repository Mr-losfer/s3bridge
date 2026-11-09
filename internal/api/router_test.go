package api

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

func TestMatch(t *testing.T) {
	tests := []struct {
		name    string
		pattern string
		path    string
		wantOK  bool
		wantID  string
	}{
		{"静态路径", "/api/plugins", "/api/plugins", true, ""},
		{"单参数", "/api/plugins/{id}", "/api/plugins/workbuddy", true, "workbuddy"},
		{"带子路径的参数", "/api/plugins/{id}/changes", "/api/plugins/workbuddy/changes", true, "workbuddy"},
		{"路径段数不符", "/api/plugins/{id}", "/api/plugins/a/b", false, ""},
		{"尾斜杠", "/api/plugins/{id}", "/api/plugins/", false, ""},
		{"通配", "/api/files/{path...}", "/api/files/a/b/c.png", true, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			rt := route{method: http.MethodGet, pattern: tt.pattern, segments: split(tt.pattern)}
			params, ok := match(&rt, tt.path)
			if ok != tt.wantOK {
				t.Fatalf("match = %v, want %v (params=%v)", ok, tt.wantOK, params)
			}
			if tt.wantID != "" && params["id"] != tt.wantID {
				t.Fatalf("id = %q, want %q", params["id"], tt.wantID)
			}
			if tt.pattern == "/api/files/{path...}" && ok && params["path"] != "a/b/c.png" {
				t.Fatalf("catchAll = %q", params["path"])
			}
		})
	}
}

func TestRouterAuthenticationAndPermissions(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	master := []byte("0123456789abcdef0123456789abcdef")
	svc := auth.NewService(st, master, []byte("sess"), time.Hour)
	if err := svc.Bootstrap("admin", "password123"); err != nil {
		t.Fatal(err)
	}
	hash, _ := auth.HashPassword("viewerpass")
	_ = st.Update(func(s *store.State) error {
		s.Users = append(s.Users, store.User{
			ID: "usr_viewer", Username: "viewer", PasswordHash: hash, Role: store.RoleViewer,
		})
		return nil
	})
	adminToken, _, err := svc.Login("admin", "password123")
	if err != nil {
		t.Fatal(err)
	}
	viewerToken, _, err := svc.Login("viewer", "viewerpass")
	if err != nil {
		t.Fatal(err)
	}

	r := NewRouter(svc)
	r.Handle(http.MethodGet, "/api/secret", func(c *Ctx) error {
		c.JSON(http.StatusOK, map[string]any{"ok": true})
		return nil
	}, store.PermUploadWrite)

	tests := []struct {
		name   string
		token  string
		status int
	}{
		{"未登录", "", http.StatusUnauthorized},
		{"无效令牌", "garbage", http.StatusUnauthorized},
		{"权限不足", viewerToken, http.StatusForbidden},
		{"管理员", adminToken, http.StatusOK},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "/api/secret", nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			rec := httptest.NewRecorder()
			r.ServeHTTP(rec, req)
			if rec.Code != tt.status {
				t.Fatalf("status = %d, want %d, body=%s", rec.Code, tt.status, rec.Body.String())
			}
		})
	}
}

func TestRouterPublicAndNotFound(t *testing.T) {
	r := NewRouter(nil)
	r.Public(http.MethodGet, "/api/health", func(c *Ctx) error {
		c.JSON(http.StatusOK, map[string]any{"ok": true})
		return nil
	})
	rec := httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/health", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("public status = %d", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body["ok"] != true {
		t.Fatalf("body 异常: %s", rec.Body.String())
	}
	rec = httptest.NewRecorder()
	r.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/api/missing", nil))
	if rec.Code != http.StatusNotFound {
		t.Fatalf("missing status = %d", rec.Code)
	}
}

func TestLoginGuardRateLimit(t *testing.T) {
	g := newLoginGuard()
	for i := 0; i < loginLimit; i++ {
		if !g.Allow("1.2.3.4:admin") {
			t.Fatalf("第 %d 次不应被限流", i+1)
		}
	}
	if g.Allow("1.2.3.4:admin") {
		t.Fatal("超过阈值应被限流")
	}
	if !g.Allow("5.6.7.8:admin") {
		t.Fatal("不同来源不应互相影响")
	}
}
