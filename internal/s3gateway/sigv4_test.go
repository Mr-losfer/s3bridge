package s3gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

type testEnv struct {
	v      *verifier
	access string
	secret string
	date   string
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	master := []byte("0123456789abcdef0123456789abcdef")
	svc := auth.NewService(st, master, []byte("session-secret"), time.Hour)
	if err := svc.Bootstrap("admin", "password123"); err != nil {
		t.Fatal(err)
	}
	key, secret, err := svc.CreateAPIKey("usr_admin", "test", nil, nil)
	if err != nil {
		t.Fatal(err)
	}
	return &testEnv{
		v:      &verifier{st: st, authSvc: svc, region: "us-east-1", service: "s3", maxSkew: 15 * time.Minute},
		access: key.AccessKey, secret: secret, date: time.Now().UTC().Format("20060102"),
	}
}

// 以下签名逻辑是独立于被测代码的 AWS SigV4 客户端实现，用于交叉验证。
func awsEncode(s string) string {
	const hexChars = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') ||
			c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexChars[c>>4])
			b.WriteByte(hexChars[c&0x0f])
		}
	}
	return b.String()
}

func clientSigningKey(secret, date, region, service string) []byte {
	h := func(key []byte, data string) []byte {
		m := hmac.New(sha256.New, key)
		m.Write([]byte(data))
		return m.Sum(nil)
	}
	return h(h(h(h([]byte("AWS4"+secret), date), region), service), "aws4_request")
}

func clientSignature(secret, date, region, service, stringToSign string) string {
	m := hmac.New(sha256.New, clientSigningKey(secret, date, region, service))
	m.Write([]byte(stringToSign))
	return hex.EncodeToString(m.Sum(nil))
}

func canonicalQuery(q url.Values) string {
	type pair struct{ k, v string }
	var pairs []pair
	for k, vals := range q {
		for _, v := range vals {
			pairs = append(pairs, pair{awsEncode(k), awsEncode(v)})
		}
	}
	sort.Slice(pairs, func(i, j int) bool {
		if pairs[i].k != pairs[j].k {
			return pairs[i].k < pairs[j].k
		}
		return pairs[i].v < pairs[j].v
	})
	out := make([]string, 0, len(pairs))
	for _, p := range pairs {
		out = append(out, p.k+"="+p.v)
	}
	return strings.Join(out, "&")
}

func signRequest(t *testing.T, env *testEnv, req *http.Request, payloadHash string, skew time.Duration) {
	t.Helper()
	now := time.Now().UTC().Add(skew)
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	scope := date + "/us-east-1/s3/aws4_request"
	req.Header.Set("X-Amz-Date", amzDate)
	req.Header.Set("X-Amz-Content-Sha256", payloadHash)
	signedHeaders := "host;x-amz-content-sha256;x-amz-date"
	canonicalHeaders := "host:" + req.Host + "\n" +
		"x-amz-content-sha256:" + payloadHash + "\n" +
		"x-amz-date:" + amzDate + "\n"
	canonical := strings.Join([]string{
		req.Method, req.URL.EscapedPath(), canonicalQuery(req.URL.Query()),
		canonicalHeaders, signedHeaders, payloadHash,
	}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	sts := strings.Join([]string{algorithm, amzDate, scope, hex.EncodeToString(sum[:])}, "\n")
	sig := clientSignature(env.secret, date, "us-east-1", "s3", sts)
	req.Header.Set("Authorization", algorithm+" Credential="+env.access+"/"+scope+
		", SignedHeaders="+signedHeaders+", Signature="+sig)
}

func TestVerifyValidSignature(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/s3/default/a.png", nil)
	signRequest(t, env, req, emptyBodyHash, 0)
	res, err := env.v.verify(req, []string{emptyBodyHash})
	if err != nil {
		t.Fatalf("合法签名应通过: %v", err)
	}
	if res.Principal == nil || res.Principal.UserID != "usr_admin" || !res.Principal.Can(store.PermS3Access) {
		t.Fatalf("身份解析异常: %+v", res.Principal)
	}
	if res.Secret != env.secret {
		t.Fatal("解密出的 SecretAccessKey 不一致")
	}
}

func TestVerifyInvalidCases(t *testing.T) {
	env := newTestEnv(t)
	tests := []struct {
		name    string
		mutate  func(*http.Request)
		wantErr error
	}{
		{"篡改签名", func(r *http.Request) {
			r.Header.Set("Authorization", strings.TrimSuffix(r.Header.Get("Authorization"), "1")+"0")
		}, ErrBadSignature},
		{"无签名", func(r *http.Request) {
			r.Header.Del("Authorization")
		}, ErrNoSignature},
		{"时间偏差过大", func(r *http.Request) {
			r.Header.Set("X-Amz-Date", time.Now().UTC().Add(-time.Hour).Format("20060102T150405Z"))
		}, ErrExpired},
		{"未知 AccessKey", func(r *http.Request) {
			r.Header.Set("Authorization", strings.Replace(r.Header.Get("Authorization"),
				"Credential="+env.access, "Credential=AK_unknown", 1))
		}, ErrUnknownKey},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req := httptest.NewRequest(http.MethodGet, "http://example.com/s3/default/a.png", nil)
			signRequest(t, env, req, emptyBodyHash, 0)
			tt.mutate(req)
			_, err := env.v.verify(req, []string{emptyBodyHash})
			if !errors.Is(err, tt.wantErr) {
				t.Fatalf("err = %v, want %v", err, tt.wantErr)
			}
		})
	}
}

func TestVerifyBearerAPIKey(t *testing.T) {
	env := newTestEnv(t)
	req := httptest.NewRequest(http.MethodGet, "http://example.com/s3/", nil)
	req.Header.Set("Authorization", "Bearer "+env.secret)
	res, err := env.v.verify(req, nil)
	if err != nil {
		t.Fatalf("Bearer 方式应通过: %v", err)
	}
	if res.Principal.Via != "apikey" {
		t.Fatalf("Via = %q, want apikey", res.Principal.Via)
	}
}

func TestVerifyPresignedURL(t *testing.T) {
	env := newTestEnv(t)
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	date := now.Format("20060102")
	scope := date + "/us-east-1/s3/aws4_request"
	q := url.Values{}
	q.Set("X-Amz-Algorithm", algorithm)
	q.Set("X-Amz-Credential", env.access+"/"+scope)
	q.Set("X-Amz-Date", amzDate)
	q.Set("X-Amz-Expires", "900")
	q.Set("X-Amz-SignedHeaders", "host")
	req := httptest.NewRequest(http.MethodGet, "http://example.com/s3/default/a.png?"+q.Encode(), nil)
	canonical := strings.Join([]string{
		http.MethodGet, "/s3/default/a.png", canonicalQuery(req.URL.Query()),
		"host:" + req.Host + "\n", "host", unsignedPL,
	}, "\n")
	sum := sha256.Sum256([]byte(canonical))
	sts := strings.Join([]string{algorithm, amzDate, scope, hex.EncodeToString(sum[:])}, "\n")
	sig := clientSignature(env.secret, date, "us-east-1", "s3", sts)
	q.Set("X-Amz-Signature", sig)
	req = httptest.NewRequest(http.MethodGet, "http://example.com/s3/default/a.png?"+q.Encode(), nil)

	if _, err := env.v.verify(req, []string{unsignedPL}); err != nil {
		t.Fatalf("预签名 URL 应通过: %v", err)
	}
	// 篡改签名必须失败
	bad := httptest.NewRequest(http.MethodGet,
		"http://example.com/s3/default/a.png?"+strings.Replace(q.Encode(), sig, strings.Repeat("0", 64), 1), nil)
	if _, err := env.v.verify(bad, []string{unsignedPL}); err == nil {
		t.Fatal("篡改预签名必须失败")
	}
}

func TestCanonicalURI(t *testing.T) {
	tests := []struct{ in, want string }{
		{"/s3/a.png", "/s3/a.png"},
		{"/s3/my%20file.png", "/s3/my%20file.png"},
		{"", "/"},
		{"/s3/中文.png", "/s3/%E4%B8%AD%E6%96%87.png"},
	}
	for _, tt := range tests {
		if got := canonicalURI(tt.in); got != tt.want {
			t.Errorf("canonicalURI(%q) = %q, want %q", tt.in, got, tt.want)
		}
	}
}
