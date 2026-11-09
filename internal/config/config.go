// Package config 负责从环境变量加载服务配置。
//
// 设计取舍：所有敏感信息（主密钥、初始管理员密码）都不写入代码或配置文件，
// 默认走环境变量；主密钥缺失时自动在数据目录生成一个 0600 权限的随机密钥，
// 保证单容器零配置即可启动，同时不在仓库中留下任何凭据。
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

const (
	EnvAddr           = "S3BRIDGE_ADDR"
	EnvDataDir        = "S3BRIDGE_DATA_DIR"
	EnvMasterKey      = "S3BRIDGE_MASTER_KEY"
	EnvBootstrapUser  = "S3BRIDGE_BOOTSTRAP_USER"
	EnvBootstrapPass  = "S3BRIDGE_BOOTSTRAP_PASSWORD"
	EnvPublicBaseURL  = "S3BRIDGE_PUBLIC_URL"
	EnvLogLevel       = "S3BRIDGE_LOG_LEVEL"
	EnvMaxUploadBytes = "S3BRIDGE_MAX_UPLOAD_BYTES"
	EnvHealthInterval = "S3BRIDGE_HEALTH_INTERVAL"
	EnvSessionTTL     = "S3BRIDGE_SESSION_TTL"
	EnvReadTimeout    = "S3BRIDGE_READ_TIMEOUT"
)

// Config 是不可变的运行时配置快照。
type Config struct {
	Addr               string
	DataDir            string
	ObjectDir          string
	MasterKey          []byte
	SessionSecret      []byte
	BootstrapUser      string
	BootstrapPassword  string
	PublicBaseURL      string
	LogLevel           string
	MaxUploadBytes     int64
	DefaultHealthEvery time.Duration
	SessionTTL         time.Duration
	ReadTimeout        time.Duration
	WriteTimeout       time.Duration
}

// Load 读取环境变量并做合法性校验。
func Load() (*Config, error) {
	c := &Config{
		Addr:               env(EnvAddr, ":8080"),
		DataDir:            env(EnvDataDir, "./data"),
		BootstrapUser:      env(EnvBootstrapUser, "admin"),
		BootstrapPassword:  env(EnvBootstrapPass, ""),
		PublicBaseURL:      strings.TrimRight(env(EnvPublicBaseURL, ""), "/"),
		LogLevel:           env(EnvLogLevel, "info"),
		MaxUploadBytes:     envInt64(EnvMaxUploadBytes, 64<<20),
		DefaultHealthEvery: envDuration(EnvHealthInterval, 5*time.Minute),
		SessionTTL:         envDuration(EnvSessionTTL, 24*time.Hour),
		ReadTimeout:        envDuration(EnvReadTimeout, 60*time.Second),
		WriteTimeout:       60 * time.Second,
	}
	if c.MaxUploadBytes <= 0 {
		return nil, fmt.Errorf("%s 必须为正数", EnvMaxUploadBytes)
	}
	if c.DefaultHealthEvery < 10*time.Second {
		return nil, fmt.Errorf("%s 不得小于 10s，避免对第三方造成压力", EnvHealthInterval)
	}
	if err := os.MkdirAll(c.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建数据目录: %w", err)
	}
	c.ObjectDir = filepath.Join(c.DataDir, "objects")
	if err := os.MkdirAll(c.ObjectDir, 0o700); err != nil {
		return nil, fmt.Errorf("创建对象目录: %w", err)
	}

	key, err := loadMasterKey(c.DataDir)
	if err != nil {
		return nil, err
	}
	c.MasterKey = key

	// 会话密钥由主密钥派生，避免再引入一份需要管理的秘密。
	s := sha256.Sum256(append([]byte("session:"), key...))
	c.SessionSecret = s[:]
	return c, nil
}

// loadMasterKey 优先使用环境变量，否则生成并持久化一个随机主密钥。
func loadMasterKey(dataDir string) ([]byte, error) {
	if raw := strings.TrimSpace(os.Getenv(EnvMasterKey)); raw != "" {
		// 支持 base64 与原始字符串两种形式。
		if dec, err := base64.StdEncoding.DecodeString(raw); err == nil && len(dec) == 32 {
			return dec, nil
		}
		h := sha256.Sum256([]byte(raw))
		return h[:], nil
	}
	path := filepath.Join(dataDir, "master.key")
	if b, err := os.ReadFile(path); err == nil {
		if len(b) == 32 {
			return b, nil
		}
		return nil, errors.New("master.key 长度异常，请检查数据目录")
	} else if !errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("读取 master.key: %w", err)
	}
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("生成主密钥: %w", err)
	}
	if err := os.WriteFile(path, key, 0o600); err != nil {
		return nil, fmt.Errorf("写入 master.key: %w", err)
	}
	return key, nil
}

func env(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

func envInt64(k string, def int64) int64 {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			return n
		}
	}
	return def
}

func envDuration(k string, def time.Duration) time.Duration {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		if d, err := time.ParseDuration(v); err == nil {
			return d
		}
	}
	return def
}
