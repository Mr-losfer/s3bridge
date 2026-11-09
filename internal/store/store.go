// Package store 提供领域模型与持久化。
//
// 采用「Store 接口 + JSON 原子落盘」实现：单实例部署零外部依赖，
// Update 在副本上执行，回调失败或落盘失败均不会污染内存状态。
// 若未来需要多实例部署，只需实现同一个 Store 接口替换为 PostgreSQL/SQLite。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const maxObjects = 50000
const maxChanges = 5000
const maxAlerts = 2000

type Store interface {
	// View 以只读方式访问状态。回调中禁止修改 State 或其任意切片元素。
	View(fn func(*State) error) error
	// Update 在状态副本上执行写操作，成功后原子落盘。
	Update(fn func(*State) error) error
	Path() string
	Close() error
}

type JSONStore struct {
	mu    sync.RWMutex
	path  string
	state State
}

// Open 打开（或初始化）一个 JSON 存储。
func Open(path string) (*JSONStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("创建存储目录: %w", err)
	}
	s := &JSONStore{path: path, state: State{Settings: DefaultSettings()}}
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		if err := s.persist(&s.state); err != nil {
			return nil, err
		}
		return s, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取状态文件: %w", err)
	}
	if len(b) > 0 {
		if err := json.Unmarshal(b, &s.state); err != nil {
			return nil, fmt.Errorf("解析状态文件 %s: %w", path, err)
		}
	}
	// 兼容老版本文件里缺失的默认设置。
	if s.state.Settings.SiteName == "" {
		s.state.Settings = DefaultSettings()
	}
	return s, nil
}

func (s *JSONStore) Path() string { return s.path }

func (s *JSONStore) View(fn func(*State) error) error {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return fn(&s.state)
}

func (s *JSONStore) Update(fn func(*State) error) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	draft, err := clone(&s.state)
	if err != nil {
		return err
	}
	draft.Revision++
	if err := fn(&draft); err != nil {
		return err
	}
	trim(&draft)
	if err := s.persist(&draft); err != nil {
		return err
	}
	s.state = draft
	return nil
}

func (s *JSONStore) Close() error { return nil }

// clone 用 JSON 做深拷贝，保证写事务失败时可整体回滚。
func clone(in *State) (State, error) {
	b, err := json.Marshal(in)
	if err != nil {
		return State{}, fmt.Errorf("序列化状态: %w", err)
	}
	var out State
	if err := json.Unmarshal(b, &out); err != nil {
		return State{}, fmt.Errorf("反序列化状态: %w", err)
	}
	return out, nil
}

func trim(s *State) {
	if len(s.Objects) > maxObjects {
		s.Objects = append([]Object(nil), s.Objects[len(s.Objects)-maxObjects:]...)
	}
	if len(s.Changes) > maxChanges {
		s.Changes = append([]ChangeLog(nil), s.Changes[len(s.Changes)-maxChanges:]...)
	}
	if len(s.Alerts) > maxAlerts {
		s.Alerts = append([]Alert(nil), s.Alerts[len(s.Alerts)-maxAlerts:]...)
	}
}

// persist 先写临时文件再 rename，避免进程中断留下半截 JSON。
func (s *JSONStore) persist(st *State) error {
	b, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化状态: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, b, 0o600); err != nil {
		return fmt.Errorf("写临时状态文件: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("提交状态文件: %w", err)
	}
	return nil
}

// NowUTC 返回统一时区的当前时间，便于测试与日志比对。
func NowUTC() time.Time { return time.Now().UTC() }
