package workbuddy

import (
	"context"
	"encoding/base64"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin"
)

func fakeJWT(exp int64, sub string) string {
	h := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"RS256","typ":"JWT"}`))
	p := base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d,"sub":%q}`, exp, sub)))
	return "Bearer " + h + "." + p + ".signature"
}

func testConfig(base string) map[string]string {
	return map[string]string{
		"base_url":          base,
		"upload_path":       "/v2/backgroundagent/localProxy/upload",
		"file_field":        "file",
		"timeout_secs":      "5",
		"authorization":     fakeJWT(time.Now().Add(24*time.Hour).Unix(), "user-123"),
		"token_expires_at":  fmt.Sprintf("%d", time.Now().Add(24*time.Hour).Unix()),
		"x_user_id":         "user-123",
		"x_client_version":  "2.8.0",
		"x_client_product":  "workbuddy-app",
		"x_client_platform": "android",
		"user_agent":        "WorkBuddyApp/2.8.0",
	}
}

func TestUploadConvertsUpstreamResponse(t *testing.T) {
	var gotMethod, gotPath, gotAuth, gotUserID, gotPlatform, gotContentType, gotFileName, gotBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotMethod, gotPath = r.Method, r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		gotUserID = r.Header.Get("X-User-Id")
		gotPlatform = r.Header.Get("X-Client-Platform")
		gotContentType = r.Header.Get("Content-Type")
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			t.Errorf("解析 multipart: %v", err)
		}
		f, hdr, err := r.FormFile("file")
		if err != nil {
			t.Errorf("读取 file 字段: %v", err)
		} else {
			gotFileName = hdr.Filename
			b, _ := io.ReadAll(f)
			gotBody = string(b)
			_ = f.Close()
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"code":0,"msg":"OK","requestId":"req-xyz","data":{"success":true,
			"url":"https://cos.example.com/a.png?q-sign-algorithm=sha1&q-sign-time=1794182312%3B1801958312&q-signature=abc",
			"objectKey":"attachments/u/a.png","fileInfo":{"fileName":"a.png","fileSize":4,"mimeType":"image/png"}}}`)
	}))
	defer srv.Close()

	cfg := testConfig(srv.URL)
	u := New()
	res, err := u.Upload(context.Background(), plugin.UploadRequest{
		FileName: "a.png", MimeType: "image/png", Size: 4,
		Reader: strings.NewReader("data"), Bucket: "default", ObjectKey: "a.png",
	}, cfg)
	if err != nil {
		t.Fatalf("Upload: %v", err)
	}
	if gotMethod != http.MethodPost || gotPath != "/v2/backgroundagent/localProxy/upload" {
		t.Fatalf("请求行错误: %s %s", gotMethod, gotPath)
	}
	if !strings.HasPrefix(gotAuth, "Bearer ") || gotUserID != "user-123" || gotPlatform != "android" {
		t.Fatalf("请求头错误: auth=%q user=%q platform=%q", gotAuth, gotUserID, gotPlatform)
	}
	if !strings.HasPrefix(gotContentType, "multipart/form-data; boundary=") {
		t.Fatalf("Content-Type 错误: %q", gotContentType)
	}
	if gotFileName != "a.png" || gotBody != "data" {
		t.Fatalf("文件内容错误: name=%q body=%q", gotFileName, gotBody)
	}
	if res.Provider != Kind || res.ObjectKey != "attachments/u/a.png" || res.FileSize != 4 {
		t.Fatalf("标准输出映射错误: %+v", res)
	}
	if res.MimeType != "image/png" || res.FileName != "a.png" {
		t.Fatalf("fileInfo 映射错误: %+v", res)
	}
	if res.URLExpireAt == nil || res.URLExpireAt.Unix() != 1801958312 {
		t.Fatalf("签名过期时间解析错误: %+v", res.URLExpireAt)
	}
	if res.Extra["requestId"] != "req-xyz" {
		t.Fatalf("Extra 缺失: %+v", res.Extra)
	}
}

func TestUploadErrors(t *testing.T) {
	tests := []struct {
		name   string
		status int
		body   string
	}{
		{"业务失败", 200, `{"code":40001,"msg":"token expired","data":{"success":false}}`},
		{"HTTP 错误", 502, `bad gateway`},
		{"非法 JSON", 200, `not-json`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
				_, _ = io.WriteString(w, tt.body)
			}))
			defer srv.Close()
			_, err := New().Upload(context.Background(), plugin.UploadRequest{
				FileName: "a.png", Reader: strings.NewReader("x"), Size: 1,
			}, testConfig(srv.URL))
			if err == nil {
				t.Fatal("上游异常时必须返回错误")
			}
		})
	}
}

func TestUploadRejectsOversizeAndBadBase(t *testing.T) {
	cfg := testConfig("http://127.0.0.1:1")
	cfg["max_file_bytes"] = "2"
	_, err := New().Upload(context.Background(), plugin.UploadRequest{
		FileName: "a.png", Size: 10, Reader: strings.NewReader("0123456789"),
	}, cfg)
	if err == nil || !strings.Contains(err.Error(), "超过插件上限") {
		t.Fatalf("应拒绝超限文件: %v", err)
	}
	cfg["base_url"] = "javascript:alert(1)"
	if _, err := New().Upload(context.Background(), plugin.UploadRequest{
		Reader: strings.NewReader("x"),
	}, cfg); err == nil {
		t.Fatal("非法域名必须被拒绝（SSRF 防护）")
	}
}

func TestCheck(t *testing.T) {
	tests := []struct {
		name    string
		status  int
		wantErr bool
	}{
		{"服务可达", 404, false}, // 4xx 说明网关是通的
		{"服务异常", 503, true},
		{"服务正常", 200, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tt.status)
			}))
			defer srv.Close()
			err := New().Check(context.Background(), testConfig(srv.URL))
			if (err != nil) != tt.wantErr {
				t.Fatalf("Check err = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestCheckRejectsExpiredToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(200)
	}))
	defer srv.Close()
	cfg := testConfig(srv.URL)
	cfg["token_expires_at"] = fmt.Sprintf("%d", time.Now().Add(-time.Hour).Unix())
	if err := New().Check(context.Background(), cfg); err == nil {
		t.Fatal("过期临时凭据必须被拒绝")
	}
}

func TestParseTokenExpiryAndSubject(t *testing.T) {
	exp := time.Now().Add(48 * time.Hour).Unix()
	jwt := fakeJWT(exp, "31d95b4d-cf30-47d9-aae8-ffca2051be28")
	got, err := ParseTokenExpiry(jwt)
	if err != nil {
		t.Fatal(err)
	}
	if got.Unix() != exp {
		t.Fatalf("exp = %d, want %d", got.Unix(), exp)
	}
	sub, err := ParseTokenSubject(jwt)
	if err != nil || sub != "31d95b4d-cf30-47d9-aae8-ffca2051be28" {
		t.Fatalf("sub = %q, err = %v", sub, err)
	}
	if _, err := ParseTokenExpiry("not-a-jwt"); err == nil {
		t.Fatal("非法 JWT 必须报错")
	}
}

func TestParseSignExpiry(t *testing.T) {
	if got := parseSignExpiry("https://x/a.png?q-sign-time=100%3B200"); got == nil || got.Unix() != 200 {
		t.Fatalf("解析失败: %+v", got)
	}
	if got := parseSignExpiry("https://x/a.png"); got != nil {
		t.Fatalf("无签名参数应返回 nil: %+v", got)
	}
}

func TestJoinURLRejectsAbsolutePath(t *testing.T) {
	if _, err := joinURL("https://a.com", "https://evil.com/x"); err == nil {
		t.Fatal("绝对 URL 必须被拒绝")
	}
	got, err := joinURL("https://a.com/", "/v2/upload")
	if err != nil || got != "https://a.com/v2/upload" {
		t.Fatalf("拼接结果异常: %q, %v", got, err)
	}
}
