package auth

import (
	"strings"
	"testing"
	"time"
)

func TestHashPassword(t *testing.T) {
	tests := []struct {
		name     string
		password string
		other    string
		wantOK   bool
	}{
		{"正确密码", "s3bridge-strong-pass", "s3bridge-strong-pass", true},
		{"错误密码", "s3bridge-strong-pass", "wrong", false},
		{"空密码也能哈希", "", "", true},
		{"中文密码", "桥接存储密码", "桥接存储密码", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash, err := HashPassword(tt.password)
			if err != nil {
				t.Fatalf("HashPassword: %v", err)
			}
			if !strings.HasPrefix(hash, "pbkdf2$120000$") {
				t.Fatalf("哈希格式异常: %s", hash)
			}
			if got := VerifyPassword(hash, tt.other); got != tt.wantOK {
				t.Fatalf("VerifyPassword = %v, want %v", got, tt.wantOK)
			}
			if VerifyPassword(hash, tt.password) != true {
				t.Fatal("原始密码必须校验通过")
			}
		})
	}
}

func TestHashPasswordSaltedDifferently(t *testing.T) {
	a, _ := HashPassword("same")
	b, _ := HashPassword("same")
	if a == b {
		t.Fatal("相同密码两次哈希必须不同（盐必须随机）")
	}
}

func TestVerifyPasswordMalformed(t *testing.T) {
	for _, bad := range []string{"", "plain", "pbkdf2$abc$x$y", "pbkdf2$999999999$!!$!!", "other$1$a$b"} {
		if VerifyPassword(bad, "x") {
			t.Fatalf("非法哈希 %q 不应通过", bad)
		}
	}
}

func TestEncryptDecrypt(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	tests := []struct {
		name  string
		plain string
	}{
		{"普通文本", "sk_live_abcdefghijklmnop"},
		{"中文", "临时令牌-包含中文"},
		{"空串", ""},
		{"长文本", strings.Repeat("A", 4096)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ct, err := Encrypt(key, tt.plain)
			if err != nil {
				t.Fatalf("Encrypt: %v", err)
			}
			if ct != "" && strings.Contains(ct, tt.plain) && tt.plain != "" {
				t.Fatal("密文不得包含明文")
			}
			got, err := Decrypt(key, ct)
			if err != nil {
				t.Fatalf("Decrypt: %v", err)
			}
			if got != tt.plain {
				t.Fatalf("roundtrip = %q, want %q", got, tt.plain)
			}
		})
	}
}

func TestDecryptRejectsTamperAndWrongKey(t *testing.T) {
	key := []byte("0123456789abcdef0123456789abcdef")
	ct, _ := Encrypt(key, "secret")
	if _, err := Decrypt([]byte("another-32-byte-key-00000000000"), ct); err == nil {
		t.Fatal("错误主密钥必须解密失败")
	}
	if _, err := Decrypt(key, ct+"x"); err == nil {
		t.Fatal("篡改密文必须解密失败")
	}
}

func TestSignParseToken(t *testing.T) {
	secret := []byte("session-secret-for-test")
	claims := Claims{Subject: "usr_1", Username: "alice", Role: "admin"}
	token, err := SignToken(secret, claims, time.Hour)
	if err != nil {
		t.Fatalf("SignToken: %v", err)
	}
	parsed, err := ParseToken(secret, token)
	if err != nil {
		t.Fatalf("ParseToken: %v", err)
	}
	if parsed.Subject != "usr_1" || parsed.Username != "alice" || parsed.Role != "admin" {
		t.Fatalf("claims 不匹配: %+v", parsed)
	}
	if parsed.ExpiresAt <= time.Now().Unix() {
		t.Fatal("过期时间必须在未来")
	}
}

func TestParseTokenRejectsInvalid(t *testing.T) {
	secret := []byte("session-secret-for-test")
	good, _ := SignToken(secret, Claims{Subject: "u"}, time.Hour)
	expired, _ := SignToken(secret, Claims{Subject: "u"}, -time.Minute)
	tests := []struct {
		name  string
		token string
	}{
		{"空", ""},
		{"乱串", "abc.def.ghi"},
		{"篡改签名", good + "x"},
		{"错误密钥", mustSign([]byte("other"), Claims{Subject: "u"})},
		{"已过期", expired},
		{"段数不足", "abc.def"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := ParseToken(secret, tt.token); err == nil {
				t.Fatal("非法令牌必须被拒绝")
			}
		})
	}
}

func mustSign(secret []byte, c Claims) string {
	s, _ := SignToken(secret, c, time.Hour)
	return s
}

func TestRandomTokenPrefix(t *testing.T) {
	a, err := RandomToken("sk_", 24)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := RandomToken("sk_", 24)
	if !strings.HasPrefix(a, "sk_") || a == b || len(a) < 20 {
		t.Fatalf("随机令牌异常: %q / %q", a, b)
	}
}
