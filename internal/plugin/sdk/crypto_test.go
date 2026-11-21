package sdk

import (
	"strings"
	"testing"
)

// TestCryptoSymmetric 验证 AES-GCM 加解密闭环，以及密钥/密文出错时必须失败。
func TestCryptoSymmetric(t *testing.T) {
	key := DeriveKey("后台填的那段密钥")
	plain := []byte(`{"file":"照片.png","size":1024}`)
	aad := []byte("v1")

	token, err := AESGCMSeal(key, plain, aad)
	if err != nil {
		t.Fatal(err)
	}
	if token == "" || strings.Contains(token, "照片") {
		t.Fatalf("密文不应包含明文: %q", token)
	}
	// 同一明文两次加密必须不同（随机 nonce）。
	token2, _ := AESGCMSeal(key, plain, aad)
	if token == token2 {
		t.Fatal("nonce 未随机化")
	}

	got, err := AESGCMOpen(key, token, aad)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(plain) {
		t.Fatalf("解密结果不符: %q", got)
	}

	cases := []struct {
		name  string
		key   []byte
		token string
		aad   []byte
	}{
		{"密钥不匹配", DeriveKey("另一段密钥"), token, aad},
		{"AAD 不匹配", key, token, []byte("v2")},
		{"密文被篡改", key, flipLast(token), aad},
		{"非 base64", key, "!!!not-base64!!!", aad},
		{"密文过短", key, "AAAA", aad},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := AESGCMOpen(tc.key, tc.token, tc.aad); err == nil {
				t.Fatal("应当解密失败")
			}
		})
	}

	if _, err := AESGCMSeal([]byte("short"), plain, nil); err == nil {
		t.Fatal("非法密钥长度应报错")
	}
}

// TestDigests 用已知向量核对摘要/HMAC 实现，避免自行改实现后悄悄不兼容。
func TestDigests(t *testing.T) {
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"SHA256(abc)", SHA256Hex([]byte("abc")), "ba7816bf8f01cfea414140de5dae2223b00361a396177a9cb410ff61f20015ad"},
		{"MD5(abc)", MD5Hex([]byte("abc")), "900150983cd24fb0d6963f7d28e17f72"},
		{
			"HMAC-SHA256(Jefe)",
			HMACSHA256Hex("Jefe", "what do ya want for nothing?"),
			"5bdcc146bf60754e6a042426089575c75a003f089d2739839dec58b964ec3843",
		},
	}
	for _, tc := range cases {
		if tc.got != tc.want {
			t.Fatalf("%s: got=%s want=%s", tc.name, tc.got, tc.want)
		}
	}
}

func TestRandHexAndBase64(t *testing.T) {
	a, b := RandHex(8), RandHex(8)
	if len(a) != 16 || a == b {
		t.Fatalf("随机串异常: %q %q", a, b)
	}
	if len(RandHex(0)) != 16 {
		t.Fatal("非法长度应回退到默认 8 字节")
	}
	enc := Base64([]byte("hello"))
	if enc != "aGVsbG8=" {
		t.Fatalf("base64 编码错误: %s", enc)
	}
	raw, err := UnBase64(enc)
	if err != nil || string(raw) != "hello" {
		t.Fatalf("base64 解码错误: %q %v", raw, err)
	}
}

// TestJWTPayload 验证只解析 payload、不校验签名，非法输入必须报错。
func TestJWTPayload(t *testing.T) {
	// {"exp":4102444800,"sub":"user-1"}，签名段随意。
	token := "eyJhbGciOiJIUzI1NiJ9.eyJleHAiOjQxMDI0NDQ4MDAsInN1YiI6InVzZXItMSJ9.sig"
	got, err := JWTPayload(token)
	if err != nil {
		t.Fatal(err)
	}
	if got["sub"] != "user-1" || got["exp"] != float64(4102444800) {
		t.Fatalf("payload 解析错误: %+v", got)
	}
	for _, bad := range []string{"", "a.b", "a.b.c", "header.!!!.sig"} {
		if _, err := JWTPayload(bad); err == nil {
			t.Fatalf("%q 应解析失败", bad)
		}
	}
}

// flipLast 改动 base64 串的一个字符，模拟密文被截断/篡改。
func flipLast(s string) string {
	if s == "" {
		return s
	}
	last := s[len(s)-1]
	rep := byte('A')
	if last == 'A' {
		rep = 'B'
	}
	return s[:len(s)-1] + string(rep)
}
