// Package auth 提供用户认证、授权与密钥保护。
//
// 本项目刻意保持 Go 后端零第三方依赖：PBKDF2、JWT(HMAC-SHA256)、AES-GCM
// 均基于标准库实现，减少供应链风险并让 `go build ./...` 在任何环境都可复现。
package auth

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

const (
	pbkdf2Iterations = 120000
	pbkdf2KeyLen     = 32
	pbkdf2SaltLen    = 16
)

var (
	ErrInvalidHash   = errors.New("密码哈希格式非法")
	ErrInvalidToken  = errors.New("令牌无效或已过期")
	ErrBadCiphertext = errors.New("密文损坏或主密钥不匹配")
)

// ---------- 密码哈希：PBKDF2-HMAC-SHA256 ----------

// HashPassword 返回形如 pbkdf2$120000$<salt>$<hash> 的自描述哈希。
func HashPassword(password string) (string, error) {
	salt := make([]byte, pbkdf2SaltLen)
	if _, err := rand.Read(salt); err != nil {
		return "", fmt.Errorf("生成盐: %w", err)
	}
	sum := pbkdf2([]byte(password), salt, pbkdf2Iterations, pbkdf2KeyLen)
	return fmt.Sprintf("pbkdf2$%d$%s$%s", pbkdf2Iterations,
		base64.RawStdEncoding.EncodeToString(salt),
		base64.RawStdEncoding.EncodeToString(sum)), nil
}

// VerifyPassword 以常量时间比对密码。
func VerifyPassword(encoded, password string) bool {
	parts := strings.Split(encoded, "$")
	if len(parts) != 4 || parts[0] != "pbkdf2" {
		return false
	}
	iters, err := strconv.Atoi(parts[1])
	if err != nil || iters <= 0 || iters > 1_000_000 {
		return false
	}
	salt, err := base64.RawStdEncoding.DecodeString(parts[2])
	if err != nil {
		return false
	}
	want, err := base64.RawStdEncoding.DecodeString(parts[3])
	if err != nil {
		return false
	}
	got := pbkdf2([]byte(password), salt, iters, len(want))
	return subtle.ConstantTimeCompare(got, want) == 1
}

// pbkdf2 是 RFC 8018 PBKDF2 的 HMAC-SHA256 实现。
func pbkdf2(password, salt []byte, iter, keyLen int) []byte {
	mac := hmac.New(sha256.New, password)
	hashLen := mac.Size()
	blocks := (keyLen + hashLen - 1) / hashLen
	out := make([]byte, 0, blocks*hashLen)
	buf := make([]byte, 4)
	for i := 1; i <= blocks; i++ {
		mac.Reset()
		mac.Write(salt)
		buf[0], buf[1], buf[2], buf[3] = byte(i>>24), byte(i>>16), byte(i>>8), byte(i)
		mac.Write(buf)
		u := mac.Sum(nil)
		t := append([]byte(nil), u...)
		for j := 1; j < iter; j++ {
			mac.Reset()
			mac.Write(u)
			u = mac.Sum(nil)
			for k := range t {
				t[k] ^= u[k]
			}
		}
		out = append(out, t...)
	}
	return out[:keyLen]
}

// ---------- 对称加密：用于保存 API Key 明文（SigV4 校验需要） ----------

// Encrypt 用 32 字节主密钥做 AES-256-GCM 加密，返回 base64(nonce||ciphertext)。
func Encrypt(key []byte, plaintext string) (string, error) {
	block, err := aes.NewCipher(normalizeKey(key))
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", err
	}
	ct := aead.Seal(nonce, nonce, []byte(plaintext), nil)
	return base64.RawStdEncoding.EncodeToString(ct), nil
}

// Decrypt 解密 Encrypt 产生的密文。
func Decrypt(key []byte, encoded string) (string, error) {
	raw, err := base64.RawStdEncoding.DecodeString(encoded)
	if err != nil {
		return "", ErrBadCiphertext
	}
	block, err := aes.NewCipher(normalizeKey(key))
	if err != nil {
		return "", err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(raw) < aead.NonceSize() {
		return "", ErrBadCiphertext
	}
	nonce, ct := raw[:aead.NonceSize()], raw[aead.NonceSize():]
	plain, err := aead.Open(nil, nonce, ct, nil)
	if err != nil {
		return "", ErrBadCiphertext
	}
	return string(plain), nil
}

func normalizeKey(key []byte) []byte {
	if len(key) == 32 {
		return key
	}
	sum := sha256.Sum256(key)
	return sum[:]
}

// ---------- 随机令牌 ----------

// RandomToken 生成带业务前缀的 URL 安全随机串。
func RandomToken(prefix string, n int) (string, error) {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("生成随机令牌: %w", err)
	}
	return prefix + base64.RawURLEncoding.EncodeToString(b), nil
}

// ---------- 会话令牌：JWT / HS256 ----------

// Claims 是会话令牌的载荷。
type Claims struct {
	Subject   string `json:"sub"`
	Username  string `json:"usr"`
	Role      string `json:"role"`
	IssuedAt  int64  `json:"iat"`
	ExpiresAt int64  `json:"exp"`
	TokenID   string `json:"jti"`
}

// SignToken 签发会话令牌。
func SignToken(secret []byte, c Claims, ttl time.Duration) (string, error) {
	now := time.Now()
	c.IssuedAt = now.Unix()
	c.ExpiresAt = now.Add(ttl).Unix()
	if c.TokenID == "" {
		id, err := RandomToken("", 12)
		if err != nil {
			return "", err
		}
		c.TokenID = id
	}
	header := base64.RawURLEncoding.EncodeToString([]byte(`{"alg":"HS256","typ":"JWT"}`))
	payload, err := json.Marshal(c)
	if err != nil {
		return "", err
	}
	unsigned := header + "." + base64.RawURLEncoding.EncodeToString(payload)
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(unsigned))
	return unsigned + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil)), nil
}

// ParseToken 校验签名与有效期。
func ParseToken(secret []byte, token string) (*Claims, error) {
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return nil, ErrInvalidToken
	}
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(parts[0] + "." + parts[1]))
	want := mac.Sum(nil)
	got, err := base64.RawURLEncoding.DecodeString(parts[2])
	if err != nil || subtle.ConstantTimeCompare(got, want) != 1 {
		return nil, ErrInvalidToken
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, ErrInvalidToken
	}
	var c Claims
	if err := json.Unmarshal(payload, &c); err != nil {
		return nil, ErrInvalidToken
	}
	if c.ExpiresAt <= time.Now().Unix() || c.Subject == "" {
		return nil, ErrInvalidToken
	}
	return &c, nil
}
