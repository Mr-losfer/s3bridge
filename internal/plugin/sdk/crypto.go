package sdk

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/hmac"
	"crypto/md5"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// DeriveKey 把任意口令派生为 32 字节 AES-256 密钥。
// 适合「后台填一段密钥，插件据此加密请求体」的场景。
func DeriveKey(secret string) []byte {
	sum := sha256.Sum256([]byte(secret))
	return sum[:]
}

// AESGCMSeal 用 AES-256-GCM 加密并返回 base64（含 nonce）。
// key 长度必须是 16/24/32 字节，可直接用 DeriveKey 生成。
func AESGCMSeal(key, plaintext, aad []byte) (string, error) {
	gcm, err := newGCM(key)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return "", fmt.Errorf("生成 nonce: %w", err)
	}
	out := gcm.Seal(nonce, nonce, plaintext, aad)
	return base64.StdEncoding.EncodeToString(out), nil
}

// AESGCMOpen 解密 AESGCMSeal 的产物。
func AESGCMOpen(key []byte, token string, aad []byte) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(token))
	if err != nil {
		return nil, fmt.Errorf("base64 解码: %w", err)
	}
	gcm, err := newGCM(key)
	if err != nil {
		return nil, err
	}
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("密文长度不足")
	}
	nonce, body := raw[:gcm.NonceSize()], raw[gcm.NonceSize():]
	out, err := gcm.Open(nil, nonce, body, aad)
	if err != nil {
		return nil, errors.New("解密失败：密钥不匹配或密文被篡改")
	}
	return out, nil
}

func newGCM(key []byte) (cipher.AEAD, error) {
	switch len(key) {
	case 16, 24, 32:
	default:
		return nil, fmt.Errorf("AES 密钥长度必须是 16/24/32 字节，当前 %d", len(key))
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// HMACSHA256Hex 返回十六进制 HMAC-SHA256，常用于接口签名。
func HMACSHA256Hex(secret, message string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(message))
	return hex.EncodeToString(mac.Sum(nil))
}

// SHA256Hex 返回十六进制 SHA-256。
func SHA256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// MD5Hex 返回十六进制 MD5（兼容只接受 MD5 的上游）。
func MD5Hex(data []byte) string {
	sum := md5.Sum(data)
	return hex.EncodeToString(sum[:])
}

// RandHex 返回 n 字节随机数的十六进制串（长度 2n）。
func RandHex(n int) string {
	if n <= 0 {
		n = 8
	}
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprint(time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// Base64 标准 base64 编码。
func Base64(b []byte) string { return base64.StdEncoding.EncodeToString(b) }

// UnBase64 标准 base64 解码。
func UnBase64(s string) ([]byte, error) { return base64.StdEncoding.DecodeString(s) }

// JWTPayload 解析 JWT 的 payload（不校验签名），常用于读取 exp/sub。
func JWTPayload(token string) (map[string]any, error) {
	parts := strings.Split(strings.TrimSpace(token), ".")
	if len(parts) != 3 {
		return nil, errors.New("不是合法的 JWT")
	}
	raw, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		return nil, fmt.Errorf("解码 payload: %w", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析 payload: %w", err)
	}
	return out, nil
}
