package plugin

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// ParseBearerToken 解析 Authorization 头里的 JWT payload（不校验签名），
// 返回过期时间与 sub。用于后台“一键填好 Token 过期时间与用户 ID”。
func ParseBearerToken(raw string) (exp time.Time, subject string, err error) {
	token := strings.TrimSpace(raw)
	if i := strings.IndexByte(token, ' '); i > 0 { // 去掉 "Bearer " 前缀
		token = strings.TrimSpace(token[i+1:])
	}
	parts := strings.Split(token, ".")
	if len(parts) != 3 {
		return time.Time{}, "", errors.New("不是合法的 JWT（应为三段式）")
	}
	payload, err := base64.RawURLEncoding.DecodeString(parts[1])
	if err != nil {
		// 兼容带 padding 的实现
		payload, err = base64.URLEncoding.DecodeString(parts[1])
		if err != nil {
			return time.Time{}, "", fmt.Errorf("解码 payload: %w", err)
		}
	}
	var claims struct {
		Exp any    `json:"exp"`
		Sub string `json:"sub"`
	}
	if err := json.Unmarshal(payload, &claims); err != nil {
		return time.Time{}, "", fmt.Errorf("解析 payload: %w", err)
	}
	switch v := claims.Exp.(type) {
	case float64:
		exp = time.Unix(int64(v), 0).UTC()
	case string:
		if n, e := parseInt64(v); e == nil {
			exp = time.Unix(n, 0).UTC()
		}
	}
	if exp.IsZero() {
		return time.Time{}, claims.Sub, errors.New("Token 中没有 exp 字段")
	}
	return exp, claims.Sub, nil
}

func parseInt64(s string) (int64, error) {
	var n int64
	_, err := fmt.Sscanf(strings.TrimSpace(s), "%d", &n)
	return n, err
}
