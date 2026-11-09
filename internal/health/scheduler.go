// Package health 实现「每个插件独立的定时健康检测」调度器。
//
// 并发约束（可退出 / 可等待 / 有所有者）：
//   - 每个插件一个 goroutine，由 Scheduler 独占持有 CancelFunc；
//   - Stop 取消父 ctx 与所有子 ctx，并通过 WaitGroup 等待全部退出；
//   - Reload 在配置变更时重建受影响的 worker，不会出现两个同 ID 的 worker。
package health

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

type Scheduler struct {
	mgr *plugin.Manager

	mu      sync.Mutex
	parent  context.Context
	cancel  context.CancelFunc
	workers map[string]worker
	wg      sync.WaitGroup
	onceWG  sync.WaitGroup // 独立管理一次性检测，避免与 wg.Wait 竞态
	running bool
}

type worker struct {
	cancel   context.CancelFunc
	interval time.Duration
	enabled  bool
}

func New(mgr *plugin.Manager) *Scheduler {
	return &Scheduler{mgr: mgr, workers: make(map[string]worker)}
}

// Start 建立父上下文并同步一次 worker 集合；重复调用安全。
func (s *Scheduler) Start(parent context.Context) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return nil
	}
	s.parent, s.cancel = context.WithCancel(parent)
	s.running = true
	s.mu.Unlock()

	if err := s.Reload(); err != nil {
		return err
	}
	// 启动后立刻跑一次检测，让后台状态在数秒内可见。
	s.RunAllOnce()
	return nil
}

// Reload 在插件启用状态、检测间隔或系统开关变化后调用。
func (s *Scheduler) Reload() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.running {
		return nil
	}
	return s.reloadLocked()
}

// desired 读取当前应运行的 worker 集合。
func (s *Scheduler) desired() (map[string]worker, error) {
	out := make(map[string]worker)
	err := s.mgr.Store().View(func(st *store.State) error {
		if !st.Settings.HealthEnabled {
			return nil
		}
		for i := range st.Plugins {
			p := &st.Plugins[i]
			if !p.Enabled || !p.Health.Enabled {
				continue
			}
			secs := p.Health.IntervalSecs
			if secs < 10 {
				secs = 10
			}
			out[p.ID] = worker{interval: time.Duration(secs) * time.Second, enabled: true}
		}
		return nil
	})
	return out, err
}

func (s *Scheduler) reloadLocked() error {
	want, err := s.desired()
	if err != nil {
		return err
	}
	// 停止不再需要或间隔已变化的 worker。
	for id, w := range s.workers {
		nw, keep := want[id]
		if !keep || nw.interval != w.interval {
			w.cancel()
			delete(s.workers, id)
		} else {
			delete(want, id) // 已在运行，无需重建
		}
	}
	// 启动新的 worker。
	for id, w := range want {
		ctx, cancel := context.WithCancel(s.parent)
		s.workers[id] = worker{cancel: cancel, interval: w.interval, enabled: true}
		s.wg.Add(1)
		go s.run(ctx, id, w.interval)
		slog.Info("已启动插件定时检测", "plugin", id, "interval", w.interval.String())
	}
	return nil
}

func (s *Scheduler) run(ctx context.Context, id string, interval time.Duration) {
	defer s.wg.Done()
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := s.mgr.Check(ctx, id); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("定时检测执行失败", "plugin", id, "err", err)
			}
		}
	}
}

// RunAllOnce 对所有已启用插件立即执行一次检测（并发、带超时），不阻塞调用方。
func (s *Scheduler) RunAllOnce() {
	var ids []string
	parent := context.Background()
	_ = s.mgr.Store().View(func(st *store.State) error {
		for i := range st.Plugins {
			if st.Plugins[i].Enabled {
				ids = append(ids, st.Plugins[i].ID)
			}
		}
		return nil
	})
	s.mu.Lock()
	if s.parent != nil {
		parent = s.parent
	}
	s.mu.Unlock()
	for _, id := range ids {
		pluginID := id
		s.onceWG.Add(1)
		go func() {
			defer s.onceWG.Done()
			ctx, cancel := context.WithTimeout(parent, 15*time.Second)
			defer cancel()
			if err := s.mgr.Check(ctx, pluginID); err != nil {
				slog.Debug("启动检测失败", "plugin", pluginID, "err", err)
			}
		}()
	}
}

// RunNow 立即检测指定插件，供后台按钮与 MCP 工具调用。
func (s *Scheduler) RunNow(ctx context.Context, id string) error {
	return s.mgr.Check(ctx, id)
}

// Stop 取消所有 worker 并等待退出，保证进程干净关闭。
func (s *Scheduler) Stop() {
	s.mu.Lock()
	if !s.running {
		s.mu.Unlock()
		s.onceWG.Wait()
		return
	}
	if s.cancel != nil {
		s.cancel()
	}
	for id, w := range s.workers {
		w.cancel()
		delete(s.workers, id)
	}
	s.running = false
	s.mu.Unlock()
	s.wg.Wait()
	s.onceWG.Wait()
	slog.Info("定时检测调度器已停止")
}

// Status 返回调度器当前管理的 worker 摘要，供站点信息接口展示。
func (s *Scheduler) Status() map[string]int {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]int{}
	for id, w := range s.workers {
		out[id] = int(w.interval.Seconds())
	}
	return out
}
