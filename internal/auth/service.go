package auth

import (
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

var (
	ErrBadCredentials = errors.New("用户名或密码错误")
	ErrUnauthorized   = errors.New("未提供有效凭据")
	ErrUserDisabled   = errors.New("账号已被禁用")
)

// Principal 是一次请求的身份上下文。
type Principal struct {
	UserID   string
	Username string
	Role     store.Role
	Via      string // session | apikey
	KeyID    string
	Perms    map[store.Permission]bool
}

func (p *Principal) Can(perm store.Permission) bool { return p != nil && p.Perms[perm] }

func (p *Principal) IsAdmin() bool { return p != nil && p.Role == store.RoleAdmin }

// Service 承载登录、令牌签发与 API Key 管理。
type Service struct {
	st            store.Store
	masterKey     []byte
	sessionSecret []byte
	ttl           time.Duration
}

func NewService(st store.Store, masterKey, sessionSecret []byte, ttl time.Duration) *Service {
	return &Service{st: st, masterKey: masterKey, sessionSecret: sessionSecret, ttl: ttl}
}

// Bootstrap 在空库时创建初始管理员与默认 bucket。
// 未设置 S3BRIDGE_BOOTSTRAP_PASSWORD 时生成随机密码并仅打印到 stdout，不落盘。
func (s *Service) Bootstrap(username, password string) error {
	return s.st.Update(func(st *store.State) error {
		if len(st.Users) > 0 {
			return nil
		}
		generated := password == ""
		if generated {
			var err error
			password, err = RandomToken("", 12)
			if err != nil {
				return err
			}
		}
		hash, err := HashPassword(password)
		if err != nil {
			return err
		}
		now := store.NowUTC()
		st.Users = append(st.Users, store.User{
			ID: "usr_admin", Username: username, DisplayName: "系统管理员",
			PasswordHash: hash, Role: store.RoleAdmin,
			CreatedAt: now, UpdatedAt: now,
		})
		st.Buckets = append(st.Buckets, store.Bucket{
			ID: "bkt_default", Name: "default", OwnerID: "usr_admin",
			Description: "默认存储桶", CreatedAt: now,
		})
		if generated {
			slog.Warn("已生成初始管理员密码（仅显示一次，请立即保存并修改）",
				"username", username, "password", password)
		}
		return nil
	})
}

// Login 校验密码并签发会话令牌。
func (s *Service) Login(username, password string) (string, *store.User, error) {
	var out *store.User
	err := s.st.Update(func(st *store.State) error {
		u := store.UserByName(st, username)
		if u == nil || !VerifyPassword(u.PasswordHash, password) {
			return ErrBadCredentials
		}
		if u.Disabled {
			return ErrUserDisabled
		}
		now := store.NowUTC()
		u.LastLoginAt = &now
		out = &store.User{
			ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
			Role: u.Role, ExtraPerms: u.ExtraPerms, RevokedPerms: u.RevokedPerms,
			Disabled: u.Disabled, CreatedAt: u.CreatedAt, LastLoginAt: &now,
		}
		return nil
	})
	if err != nil {
		return "", nil, err
	}
	token, err := SignToken(s.sessionSecret, Claims{
		Subject: out.ID, Username: out.Username, Role: string(out.Role),
	}, s.ttl)
	if err != nil {
		return "", nil, fmt.Errorf("签发会话令牌: %w", err)
	}
	return token, out, nil
}

// Authenticate 解析 Authorization 头，支持 JWT 会话与 API Key 两种凭据。
func (s *Service) Authenticate(header string) (*Principal, error) {
	if header == "" {
		return nil, ErrUnauthorized
	}
	token := strings.TrimSpace(header)
	if len(token) >= 7 && strings.EqualFold(token[:7], "Bearer ") {
		token = strings.TrimSpace(token[7:])
	}
	if token == "" {
		return nil, ErrUnauthorized
	}
	if strings.HasPrefix(token, "sk_") {
		return s.authenticateAPIKey(token)
	}
	claims, err := ParseToken(s.sessionSecret, token)
	if err != nil {
		return nil, ErrUnauthorized
	}
	var p *Principal
	err = s.st.View(func(st *store.State) error {
		u := store.UserByID(st, claims.Subject)
		if u == nil || u.Disabled {
			return ErrUnauthorized
		}
		p = &Principal{
			UserID: u.ID, Username: u.Username, Role: u.Role,
			Via: "session", Perms: effectivePerms(u, nil),
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	return p, nil
}

func (s *Service) authenticateAPIKey(token string) (*Principal, error) {
	digest := sha256.Sum256([]byte(token))
	hash := hex.EncodeToString(digest[:])
	var p *Principal
	var touch func()
	err := s.st.View(func(st *store.State) error {
		for i := range st.APIKeys {
			k := &st.APIKeys[i]
			if subtle.ConstantTimeCompare([]byte(k.SecretHash), []byte(hash)) != 1 {
				continue
			}
			if k.Revoked || (k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt)) {
				return ErrUnauthorized
			}
			u := store.UserByID(st, k.UserID)
			if u == nil || u.Disabled {
				return ErrUnauthorized
			}
			p = &Principal{
				UserID: u.ID, Username: u.Username, Role: u.Role,
				Via: "apikey", KeyID: k.ID, Perms: effectivePerms(u, k.Scopes),
			}
			id := k.ID
			touch = func() {
				_ = s.st.Update(func(s2 *store.State) error {
					if kk := store.APIKeyByID(s2, id); kk != nil {
						now := store.NowUTC()
						kk.LastUsedAt = &now
					}
					return nil
				})
			}
			return nil
		}
		return ErrUnauthorized
	})
	if err != nil {
		return nil, err
	}
	// 更新最后使用时间放在读锁外，避免锁升级死锁；失败不影响鉴权结果。
	if touch != nil {
		go touch()
	}
	return p, nil
}

// effectivePerms 计算用户权限与密钥 scopes 的交集。
func effectivePerms(u *store.User, scopes []store.Permission) map[store.Permission]bool {
	out := make(map[store.Permission]bool, len(store.AllPermissions))
	for _, perm := range store.AllPermissions {
		if !u.Can(perm) {
			continue
		}
		if len(scopes) > 0 && !hasPerm(scopes, perm) {
			continue
		}
		out[perm] = true
	}
	return out
}

func hasPerm(list []store.Permission, p store.Permission) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}

// CreateAPIKey 生成一对 S3 凭据。明文 secret 只在返回值中出现一次。
func (s *Service) CreateAPIKey(userID, name string, scopes []store.Permission, expiresAt *time.Time) (*store.APIKey, string, error) {
	secret, err := RandomToken("sk_", 24)
	if err != nil {
		return nil, "", err
	}
	access, err := RandomToken("AK_", 9)
	if err != nil {
		return nil, "", err
	}
	enc, err := Encrypt(s.masterKey, secret)
	if err != nil {
		return nil, "", fmt.Errorf("加密密钥: %w", err)
	}
	digest := sha256.Sum256([]byte(secret))
	key := &store.APIKey{
		ID:     "key_" + hex.EncodeToString(digest[:6]),
		UserID: userID, Name: name,
		AccessKey: access, SecretEnc: enc,
		SecretHash: hex.EncodeToString(digest[:]),
		SecretHint: secret[:10] + "…",
		Scopes:     scopes, ExpiresAt: expiresAt,
		CreatedAt: store.NowUTC(),
	}
	if err := s.st.Update(func(st *store.State) error {
		if store.UserByID(st, userID) == nil {
			return errors.New("用户不存在")
		}
		st.APIKeys = append(st.APIKeys, *key)
		return nil
	}); err != nil {
		return nil, "", err
	}
	return key, secret, nil
}

// Secret 解密出 API Key 明文，供 S3 SigV4 签名校验使用。
func (s *Service) Secret(k *store.APIKey) (string, error) {
	return Decrypt(s.masterKey, k.SecretEnc)
}

// SecretHashOf 供测试与工具函数使用。
func SecretHashOf(secret string) string {
	d := sha256.Sum256([]byte(secret))
	return hex.EncodeToString(d[:])
}
