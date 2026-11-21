package plugin

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// ManifestSchema 是当前 plugin.json 的版本号。
const ManifestSchema = 2

// idPattern 限制插件 ID：既要能作为目录名，也要能安全地出现在 URL 与工具参数里。
var idPattern = regexp.MustCompile(`^[a-z][a-z0-9][a-z0-9_-]{0,38}$`)

var validParamTypes = map[string]bool{
	"text": true, "secret": true, "number": true, "bool": true,
	"select": true, "textarea": true, "url": true, "datetime": true, "duration": true,
}

// Normalize 填充默认值，让旧版本 manifest 也能平滑升级。
func (m *Manifest) Normalize() {
	if m.Schema == 0 {
		m.Schema = ManifestSchema
	}
	if m.Entry == "" {
		m.Entry = "main.go"
	}
	if m.TimeoutSecs <= 0 {
		m.TimeoutSecs = 60
	}
	if m.Priority <= 0 {
		m.Priority = 100
	}
	if m.Health.TimeoutSecs <= 0 {
		m.Health.TimeoutSecs = 8
	}
	if m.Health.FailThreshold <= 0 {
		m.Health.FailThreshold = 2
	}
	if m.Health.ProbeMode == "" {
		m.Health.ProbeMode = "ping"
	}
}

// Validate 校验清单本身，不检查源码。
func (m *Manifest) Validate() error {
	m.Normalize()
	if !idPattern.MatchString(m.ID) {
		return fmt.Errorf("%w: 插件 ID 必须是小写字母开头、仅含小写字母/数字/-/_，长度 2-40", ErrSourceInvalid)
	}
	if strings.TrimSpace(m.Name) == "" {
		return fmt.Errorf("%w: 插件名称不能为空", ErrSourceInvalid)
	}
	if m.Schema != ManifestSchema {
		return fmt.Errorf("%w: 不支持的 schema %d，当前仅支持 %d", ErrSourceInvalid, m.Schema, ManifestSchema)
	}
	if m.TimeoutSecs < 1 || m.TimeoutSecs > 600 {
		return fmt.Errorf("%w: timeoutSecs 必须在 1-600 之间", ErrSourceInvalid)
	}
	if m.Validity.IntervalHours < 0 || m.Validity.IntervalHours > 24*30 {
		return fmt.Errorf("%w: validity.intervalHours 必须在 0-720 之间", ErrSourceInvalid)
	}
	seen := map[string]bool{}
	for i := range m.Params {
		f := &m.Params[i]
		if strings.TrimSpace(f.Key) == "" {
			return fmt.Errorf("%w: 第 %d 个参数缺少 key", ErrSourceInvalid, i+1)
		}
		if seen[f.Key] {
			return fmt.Errorf("%w: 参数 key 重复: %s", ErrSourceInvalid, f.Key)
		}
		seen[f.Key] = true
		if f.Type == "" {
			f.Type = "text"
		}
		if !validParamTypes[f.Type] {
			return fmt.Errorf("%w: 参数 %s 的类型 %q 不支持", ErrSourceInvalid, f.Key, f.Type)
		}
		if f.Type == "select" && len(f.Options) == 0 {
			return fmt.Errorf("%w: 参数 %s 是 select 但未提供 options", ErrSourceInvalid, f.Key)
		}
		if f.Pattern != "" {
			if _, err := regexp.Compile(f.Pattern); err != nil {
				return fmt.Errorf("%w: 参数 %s 的正则无效: %v", ErrSourceInvalid, f.Key, err)
			}
		}
	}
	return nil
}

// LoadManifest 读取并校验文件夹内的 plugin.json。
func LoadManifest(dir string) (Manifest, error) {
	raw, err := os.ReadFile(filepath.Join(dir, "plugin.json"))
	if err != nil {
		return Manifest{}, fmt.Errorf("读取 plugin.json: %w", err)
	}
	var m Manifest
	if err := json.Unmarshal(raw, &m); err != nil {
		return Manifest{}, fmt.Errorf("%w: plugin.json 不是合法 JSON: %v", ErrSourceInvalid, err)
	}
	if err := m.Validate(); err != nil {
		return Manifest{}, err
	}
	if base := filepath.Base(dir); base != m.ID {
		return Manifest{}, fmt.Errorf("%w: id %q 与文件夹名 %q 不一致", ErrSourceInvalid, m.ID, base)
	}
	return m, nil
}

// SaveManifest 原子写入 plugin.json。
func SaveManifest(dir string, m Manifest) error {
	m.Normalize()
	raw, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	return writeFileAtomic(filepath.Join(dir, "plugin.json"), raw)
}

// writeFileAtomic 通过同目录临时文件 + rename 保证不会读到半截文件。
func writeFileAtomic(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	name := f.Name()
	defer func() { _ = os.Remove(name) }()
	if _, err := f.Write(data); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Chmod(name, 0o600); err != nil {
		return err
	}
	return os.Rename(name, path)
}
