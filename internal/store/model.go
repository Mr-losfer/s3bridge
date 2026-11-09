package store

import "time"

// ---------- 角色与权限 ----------

type Role string

const (
	RoleAdmin  Role = "admin"  // 全部权限，含用户与系统设置
	RoleEditor Role = "editor" // 插件配置、上传、bucket 管理
	RoleViewer Role = "viewer" // 只读
)

type Permission string

const (
	PermPluginRead   Permission = "plugin:read"
	PermPluginWrite  Permission = "plugin:write"
	PermPluginDebug  Permission = "plugin:debug"
	PermUploadRead   Permission = "upload:read"
	PermUploadWrite  Permission = "upload:write"
	PermBucketRead   Permission = "bucket:read"
	PermBucketWrite  Permission = "bucket:write"
	PermUserManage   Permission = "user:manage"
	PermSystemManage Permission = "system:manage"
	PermS3Access     Permission = "s3:access"
)

// AllPermissions 定义全部可分配权限，供前端渲染复选框。
var AllPermissions = []Permission{
	PermPluginRead, PermPluginWrite, PermPluginDebug,
	PermUploadRead, PermUploadWrite,
	PermBucketRead, PermBucketWrite,
	PermUserManage, PermSystemManage, PermS3Access,
}

// RolePermissions 返回角色默认权限集合。
func RolePermissions(r Role) map[Permission]bool {
	base := map[Permission]bool{
		PermPluginRead: true, PermUploadRead: true,
		PermBucketRead: true, PermS3Access: true,
	}
	switch r {
	case RoleAdmin:
		for _, p := range AllPermissions {
			base[p] = true
		}
	case RoleEditor:
		base[PermPluginWrite] = true
		base[PermPluginDebug] = true
		base[PermUploadWrite] = true
		base[PermBucketWrite] = true
	}
	return base
}

// User 是多用户体系中的账号。
type User struct {
	ID           string       `json:"id"`
	Username     string       `json:"username"`
	DisplayName  string       `json:"displayName"`
	PasswordHash string       `json:"passwordHash"`
	Role         Role         `json:"role"`
	ExtraPerms   []Permission `json:"extraPerms,omitempty"`
	RevokedPerms []Permission `json:"revokedPerms,omitempty"`
	Disabled     bool         `json:"disabled"`
	CreatedAt    time.Time    `json:"createdAt"`
	UpdatedAt    time.Time    `json:"updatedAt"`
	LastLoginAt  *time.Time   `json:"lastLoginAt,omitempty"`
}

// Can 计算最终权限（角色默认 + 额外 - 撤销）。
func (u *User) Can(p Permission) bool {
	if u.Disabled {
		return false
	}
	if has(u.RevokedPerms, p) {
		return false
	}
	return RolePermissions(u.Role)[p] || has(u.ExtraPerms, p)
}

func has(list []Permission, p Permission) bool {
	for _, x := range list {
		if x == p {
			return true
		}
	}
	return false
}

// PublicUser 是脱敏后的对外视图，绝不携带密码哈希。
type PublicUser struct {
	ID           string       `json:"id"`
	Username     string       `json:"username"`
	DisplayName  string       `json:"displayName"`
	Role         Role         `json:"role"`
	ExtraPerms   []Permission `json:"extraPerms"`
	RevokedPerms []Permission `json:"revokedPerms"`
	Permissions  []string     `json:"permissions"`
	Disabled     bool         `json:"disabled"`
	CreatedAt    time.Time    `json:"createdAt"`
	LastLoginAt  *time.Time   `json:"lastLoginAt,omitempty"`
}

func (u *User) Public() PublicUser {
	perms := make([]string, 0, len(AllPermissions))
	for _, p := range AllPermissions {
		if u.Can(p) {
			perms = append(perms, string(p))
		}
	}
	return PublicUser{
		ID: u.ID, Username: u.Username, DisplayName: u.DisplayName,
		Role: u.Role, ExtraPerms: u.ExtraPerms, RevokedPerms: u.RevokedPerms,
		Permissions: perms, Disabled: u.Disabled,
		CreatedAt: u.CreatedAt, LastLoginAt: u.LastLoginAt,
	}
}

// ---------- API Key / S3 凭据 ----------

// APIKey 一条密钥同时用于 REST Bearer 鉴权与 S3 SigV4 签名。
// SecretEnc 是 AES-GCM 密文；明文只在创建时返回一次。
type APIKey struct {
	ID         string       `json:"id"`
	UserID     string       `json:"userId"`
	Name       string       `json:"name"`
	AccessKey  string       `json:"accessKey"`  // S3 AccessKeyId，形如 AK_xxx
	SecretEnc  string       `json:"secretEnc"`  // S3 SecretAccessKey 密文（AES-GCM）
	SecretHash string       `json:"secretHash"` // 明文 SHA-256，用于鉴权快速比对
	SecretHint string       `json:"secretHint"`
	Scopes     []Permission `json:"scopes"`
	Revoked    bool         `json:"revoked"`
	ExpiresAt  *time.Time   `json:"expiresAt,omitempty"`
	LastUsedAt *time.Time   `json:"lastUsedAt,omitempty"`
	CreatedAt  time.Time    `json:"createdAt"`
}

func (k *APIKey) Can(p Permission) bool {
	if k.Revoked || (k.ExpiresAt != nil && time.Now().After(*k.ExpiresAt)) {
		return false
	}
	if len(k.Scopes) == 0 {
		return true
	}
	return has(k.Scopes, p)
}

// ---------- Bucket 与对象 ----------

type Bucket struct {
	ID          string    `json:"id"`
	Name        string    `json:"name"`
	OwnerID     string    `json:"ownerId"`
	Description string    `json:"description"`
	PublicRead  bool      `json:"publicRead"`
	CreatedAt   time.Time `json:"createdAt"`
}

// FileCategory 是按文件类型归档的维度。
type FileCategory string

const (
	CategoryImage   FileCategory = "image"
	CategoryVideo   FileCategory = "video"
	CategoryAudio   FileCategory = "audio"
	CategoryDoc     FileCategory = "document"
	CategoryArchive FileCategory = "archive"
	CategoryOther   FileCategory = "other"
)

var AllCategories = []FileCategory{
	CategoryImage, CategoryVideo, CategoryAudio,
	CategoryDoc, CategoryArchive, CategoryOther,
}

// Object 是统一标准输出落库后的对象元数据。
type Object struct {
	ID          string       `json:"id"`
	Bucket      string       `json:"bucket"`
	ObjectKey   string       `json:"objectKey"`
	FileName    string       `json:"fileName"`
	MimeType    string       `json:"mimeType"`
	Category    FileCategory `json:"category"`
	Size        int64        `json:"size"`
	SHA256      string       `json:"sha256"`
	Provider    string       `json:"provider"` // 来源插件 ID
	PluginID    string       `json:"pluginId"`
	RemoteURL   string       `json:"remoteUrl"` // 第三方返回的 URL（可能为临时签名）
	RemoteKey   string       `json:"remoteKey"`
	URLExpireAt *time.Time   `json:"urlExpireAt,omitempty"`
	LocalPath   string       `json:"localPath"` // 相对 ObjectDir 的安全路径
	OwnerID     string       `json:"ownerId"`
	CreatedAt   time.Time    `json:"createdAt"`
}

// ---------- 插件 ----------

type PluginStatus string

const (
	StatusUnknown   PluginStatus = "unknown"
	StatusHealthy   PluginStatus = "healthy"
	StatusDegraded  PluginStatus = "degraded"
	StatusUnhealthy PluginStatus = "unhealthy" // 前端显示为红色
	StatusDisabled  PluginStatus = "disabled"
)

// HealthConfig 是每个插件独立的定时检测设置。
type HealthConfig struct {
	Enabled       bool   `json:"enabled"`
	IntervalSecs  int    `json:"intervalSecs"`
	TimeoutSecs   int    `json:"timeoutSecs"`
	FailThreshold int    `json:"failThreshold"`
	ProbeMode     string `json:"probeMode"` // ping | upload-sample
}

// Plugin 是一个已注册的插件实例。
type Plugin struct {
	ID            string            `json:"id"`
	Kind          string            `json:"kind"` // 插件类型，如 workbuddy
	Name          string            `json:"name"`
	Description   string            `json:"description"`
	Version       string            `json:"version"`
	Author        string            `json:"author"`
	Enabled       bool              `json:"enabled"`
	Priority      int               `json:"priority"`
	Params        map[string]string `json:"params"` // 敏感值以 enc: 前缀加密存储
	Status        PluginStatus      `json:"status"`
	StatusMessage string            `json:"statusMessage"`
	LastCheckedAt *time.Time        `json:"lastCheckedAt,omitempty"`
	LastSuccessAt *time.Time        `json:"lastSuccessAt,omitempty"`
	FailCount     int               `json:"failCount"`
	SuccessCount  int               `json:"successCount"`
	LatencyMs     int64             `json:"latencyMs"`
	Health        HealthConfig      `json:"health"`
	Revision      int64             `json:"revision"` // 乐观锁，差量/全量更新用
	CreatedAt     time.Time         `json:"createdAt"`
	UpdatedAt     time.Time         `json:"updatedAt"`
}

// ChangeLog 记录插件的差量/全量修改与更新，供 MCP 与审计页回溯。
type ChangeLog struct {
	ID        string            `json:"id"`
	PluginID  string            `json:"pluginId"`
	Action    string            `json:"action"` // patch | replace | update | params | toggle | debug
	Actor     string            `json:"actor"`
	Diff      map[string]string `json:"diff,omitempty"`
	Message   string            `json:"message"`
	CreatedAt time.Time         `json:"createdAt"`
}

// Alert 是插件异常告警。
type Alert struct {
	ID         string     `json:"id"`
	PluginID   string     `json:"pluginId"`
	PluginName string     `json:"pluginName"`
	Level      string     `json:"level"` // warning | critical
	Title      string     `json:"title"`
	Message    string     `json:"message"`
	CreatedAt  time.Time  `json:"createdAt"`
	AckedAt    *time.Time `json:"ackedAt,omitempty"`
	AckedBy    string     `json:"ackedBy,omitempty"`
}

// Settings 是全局系统设置。
type Settings struct {
	SiteName            string `json:"siteName"`
	DefaultPlugin       string `json:"defaultPlugin"`
	HealthEnabled       bool   `json:"healthEnabled"`
	HealthIntervalSecs  int    `json:"healthIntervalSecs"`
	AlertOnFailure      bool   `json:"alertOnFailure"`
	UploadRetentionDays int    `json:"uploadRetentionDays"`
	Announcement        string `json:"announcement"`
}

func DefaultSettings() Settings {
	return Settings{
		SiteName: "S3Bridge", DefaultPlugin: "workbuddy",
		HealthEnabled: true, HealthIntervalSecs: 300,
		AlertOnFailure: true, UploadRetentionDays: 90,
	}
}

// State 是全部持久化数据。JSON 文件单实例部署，接口抽象便于替换为 SQLite/PG。
type State struct {
	Users    []User      `json:"users"`
	APIKeys  []APIKey    `json:"apiKeys"`
	Buckets  []Bucket    `json:"buckets"`
	Objects  []Object    `json:"objects"`
	Plugins  []Plugin    `json:"plugins"`
	Alerts   []Alert     `json:"alerts"`
	Changes  []ChangeLog `json:"changes"`
	Settings Settings    `json:"settings"`
	Revision int64       `json:"revision"`
}
