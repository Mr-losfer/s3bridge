// Package s3gateway 提供 S3 兼容的开放接口。
//
// 凭据模型：每个用户在后台创建自己的 API Key，
//
//	AccessKeyId     = APIKey.AccessKey
//	SecretAccessKey = APIKey.Secret（服务端 AES-GCM 加密保存，登录后按需解密查看）
//
// 因此不同用户天然隔离到各自的 bucket，且可用标准 AWS SDK / aws-cli / s3cmd 直连。
package s3gateway

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

const (
	algorithm     = "AWS4-HMAC-SHA256"
	terminator    = "aws4_request"
	emptyBodyHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"
	unsignedPL    = "UNSIGNED-PAYLOAD"
)

var (
	ErrNoSignature  = errors.New("缺少签名信息，需 SigV4 Authorization 头或预签名 URL 参数")
	ErrBadSignature = errors.New("签名校验失败")
	ErrExpired      = errors.New("签名已过期或时间偏差过大")
	ErrUnknownKey   = errors.New("AccessKeyId 无效")
)

type verifier struct {
	st      store.Store
	authSvc *auth.Service
	region  string
	service string
	maxSkew time.Duration
}

// VerifyResult 携带校验后的身份与密钥记录。
type VerifyResult struct {
	Principal *auth.Principal
	Key       *store.APIKey
	Secret    string
}

// verify 支持 header 签名与 presigned URL，并对 payload hash 做多候选兼容。
func (v *verifier) verify(r *http.Request, payloadHashes []string) (*VerifyResult, error) {
	var cred, signedHeaders, signature, amzDate, scope string
	var presigned bool
	q := r.URL.Query()

	if authHeader := r.Header.Get("Authorization"); strings.HasPrefix(authHeader, algorithm) {
		parsed, err := parseAuthorization(authHeader)
		if err != nil {
			return nil, err
		}
		cred, signedHeaders, signature, scope = parsed.credential, parsed.signedHeaders, parsed.signature, parsed.scope
		amzDate = firstNonEmpty(r.Header.Get("X-Amz-Date"), r.Header.Get("x-amz-date"), q.Get("X-Amz-Date"))
	} else if q.Get("X-Amz-Algorithm") == algorithm {
		presigned = true
		cred = q.Get("X-Amz-Credential")
		signedHeaders = q.Get("X-Amz-SignedHeaders")
		signature = q.Get("X-Amz-Signature")
		scope = scopeFromCredential(cred)
		amzDate = q.Get("X-Amz-Date")
	} else if strings.HasPrefix(authHeader, "Bearer ") {
		return v.verifyBearer(r, strings.TrimPrefix(authHeader, "Bearer "))
	} else {
		return nil, ErrNoSignature
	}
	if cred == "" || signature == "" || amzDate == "" {
		return nil, ErrNoSignature
	}

	accessKey := strings.Split(cred, "/")[0]
	ts, err := time.Parse("20060102T150405Z", amzDate)
	if err != nil {
		return nil, fmt.Errorf("%w: X-Amz-Date 格式错误", ErrBadSignature)
	}
	if skew := time.Since(ts); skew > v.maxSkew || skew < -v.maxSkew {
		return nil, ErrExpired
	}
	if presigned {
		expires := 900
		if n := parseInt(q.Get("X-Amz-Expires"), 0); n > 0 {
			expires = n
		}
		if time.Since(ts) > time.Duration(expires)*time.Second {
			return nil, ErrExpired
		}
	}
	// scope 中的日期、region、service 必须与服务端一致。
	wantScope := ts.Format("20060102") + "/" + v.region + "/" + v.service + "/" + terminator
	if scope != "" && scope != wantScope {
		return nil, fmt.Errorf("%w: scope 不匹配，期望 %s", ErrBadSignature, wantScope)
	}
	scope = wantScope

	var key *store.APIKey
	var secret string
	err = v.st.View(func(st *store.State) error {
		k := store.APIKeyByAccess(st, accessKey)
		if k == nil || k.Revoked {
			return ErrUnknownKey
		}
		copyK := *k
		key = &copyK
		return nil
	})
	if err != nil {
		return nil, err
	}
	secret, err = v.authSvc.Secret(key)
	if err != nil {
		return nil, fmt.Errorf("读取密钥: %w", err)
	}

	canonicalQuery := canonicalQueryString(r.URL.Query(), presigned)
	for _, ph := range dedup(append(payloadHashes, emptyBodyHash, unsignedPL)) {
		canonical := canonicalRequest(r, signedHeaders, canonicalQuery, ph)
		sts := stringToSign(ts, scope, canonical)
		want := hex.EncodeToString(hmacSHA256(signingKey(secret, ts.Format("20060102"), v.region, v.service), sts))
		if subtle.ConstantTimeCompare([]byte(want), []byte(strings.ToLower(signature))) == 1 {
			principal := &auth.Principal{
				UserID: key.UserID, Via: "s3", KeyID: key.ID,
				Perms: map[store.Permission]bool{},
			}
			var user *store.User
			_ = v.st.View(func(st *store.State) error {
				if u := store.UserByID(st, key.UserID); u != nil {
					user = u
				}
				return nil
			})
			if user != nil {
				principal.Username = user.Username
				principal.Role = user.Role
				for _, p := range store.AllPermissions {
					if user.Can(p) && (len(key.Scopes) == 0 || containsPerm(key.Scopes, p)) {
						principal.Perms[p] = true
					}
				}
			}
			return &VerifyResult{Principal: principal, Key: key, Secret: secret}, nil
		}
	}
	return nil, ErrBadSignature
}

func (v *verifier) verifyBearer(r *http.Request, token string) (*VerifyResult, error) {
	p, err := v.authSvc.Authenticate("Bearer " + token)
	if err != nil {
		return nil, err
	}
	return &VerifyResult{Principal: p}, nil
}

type authParts struct {
	credential    string
	signedHeaders string
	signature     string
	scope         string
}

func parseAuthorization(h string) (authParts, error) {
	var out authParts
	rest := strings.TrimSpace(strings.TrimPrefix(h, algorithm))
	for _, part := range strings.Split(rest, ",") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		switch strings.ToLower(strings.TrimSpace(kv[0])) {
		case "credential":
			out.credential = kv[1]
			out.scope = scopeFromCredential(kv[1])
		case "signedheaders":
			out.signedHeaders = kv[1]
		case "signature":
			out.signature = kv[1]
		}
	}
	if out.credential == "" || out.signedHeaders == "" || out.signature == "" {
		return out, fmt.Errorf("%w: Authorization 头字段不完整", ErrBadSignature)
	}
	return out, nil
}

func scopeFromCredential(cred string) string {
	if i := strings.Index(cred, "/"); i >= 0 {
		return cred[i+1:]
	}
	return ""
}

func canonicalRequest(r *http.Request, signedHeaders, canonicalQuery, payloadHash string) string {
	method := r.Method
	uri := canonicalURI(r.URL.EscapedPath())
	headers := canonicalHeaders(r, signedHeaders)
	return strings.Join([]string{method, uri, canonicalQuery, headers, signedHeaders, payloadHash}, "\n")
}

func canonicalURI(escapedPath string) string {
	if escapedPath == "" {
		return "/"
	}
	// EscapedPath 已是 URL 编码形式；按段解码再按 AWS 规则重编码，避免双重编码差异。
	raw, err := url.PathUnescape(escapedPath)
	if err != nil {
		raw = escapedPath
	}
	parts := strings.Split(raw, "/")
	for i, p := range parts {
		parts[i] = uriEncode(p, false)
	}
	return "/" + strings.TrimPrefix(strings.Join(parts, "/"), "/")
}

// uriEncode 实现 AWS SigV4 的 RFC3986 编码（保留 A-Za-z0-9-_.~）。
func uriEncode(s string, encodeSlash bool) string {
	const hexChars = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if (c >= 'A' && c <= 'Z') || (c >= 'a' && c <= 'z') ||
			(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '~' {
			b.WriteByte(c)
		} else if c == '/' && !encodeSlash {
			b.WriteByte(c)
		} else {
			b.WriteByte('%')
			b.WriteByte(hexChars[c>>4])
			b.WriteByte(hexChars[c&0x0f])
		}
	}
	return b.String()
}

func canonicalQueryString(q url.Values, presigned bool) string {
	type pair struct{ k, v string }
	var pairs []pair
	for k, values := range q {
		if presigned && strings.EqualFold(k, "X-Amz-Signature") {
			continue
		}
		for _, val := range values {
			pairs = append(pairs, pair{uriEncode(k, true), uriEncode(val, true)})
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

func canonicalHeaders(r *http.Request, signed string) string {
	var out strings.Builder
	names := strings.Split(strings.ToLower(signed), ";")
	sort.Strings(names)
	for _, name := range names {
		if name == "" {
			continue
		}
		var value string
		if name == "host" {
			value = r.Host
			if value == "" {
				value = r.URL.Host
			}
		} else {
			vals := r.Header.Values(name)
			trimmed := make([]string, 0, len(vals))
			for _, x := range vals {
				trimmed = append(trimmed, strings.Join(strings.Fields(x), " "))
			}
			value = strings.Join(trimmed, ",")
		}
		out.WriteString(name)
		out.WriteByte(':')
		out.WriteString(strings.Join(strings.Fields(value), " "))
		out.WriteByte('\n')
	}
	return out.String()
}

func stringToSign(ts time.Time, scope, canonical string) string {
	sum := sha256.Sum256([]byte(canonical))
	return strings.Join([]string{algorithm, ts.Format("20060102T150405Z"), scope, hex.EncodeToString(sum[:])}, "\n")
}

func signingKey(secret, date, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), date)
	kRegion := hmacSHA256(kDate, region)
	kService := hmacSHA256(kRegion, service)
	return hmacSHA256(kService, terminator)
}

func hmacSHA256(key []byte, data string) []byte {
	m := hmac.New(sha256.New, key)
	m.Write([]byte(data))
	return m.Sum(nil)
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func parseInt(s string, def int) int {
	var n int
	if _, err := fmt.Sscanf(s, "%d", &n); err == nil {
		return n
	}
	return def
}

func dedup(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func containsPerm(list []store.Permission, p store.Permission) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}
