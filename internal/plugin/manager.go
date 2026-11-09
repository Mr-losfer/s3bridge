package plugin

import (
	"context"
	"crypto/rand"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// Manager 负责插件实例的生命周期：注册、差量/全量修改、参数更新、
// 版本更新、调试、健康检测与异常告警。
type Manager struct {
	st           store.Store
	registry     *Registry
	seal         func(string) (string, error)
	open         func(string) (string, error)
	defaultEvery time.Duration
}

func NewManager(st store.Store, reg *Registry, seal, open func(string) (string, error), defaultEvery time.Duration) *Manager {
	return &Manager{st: st, registry: reg, seal: seal, open: open, defaultEvery: defaultEvery}
}

func (m *Manager) Registry() *Registry { return m.registry }
func (m *Manager) Store() store.Store  { return m.st }

// View 是插件列表/详情的对外视图，敏感参数已脱敏。
type View struct {
	store.Plugin
	Params      map[string]any `json:"params"` // 遮蔽内层 Params，避免密文外泄
	Manifest    Manifest       `json:"manifest"`
	Warning     string         `json:"warning"`
	Grouped     []ParamGroup   `json:"grouped"`
	HasManifest bool           `json:"hasManifest"`
}

type ParamGroup struct {
	Name   string       `json:"name"`
	Fields []ParamField `json:"fields"`
}

// EnsureBuiltin 把注册表中尚未实例化的插件类型写入存储（幂等，可反复调用）。
func (m *Manager) EnsureBuiltin() error {
	return m.st.Update(func(st *store.State) error {
		for _, mf := range m.registry.List() {
			if store.PluginByID(st, mf.Kind) != nil {
				continue
			}
			now := store.NowUTC()
			every := int(m.defaultEvery.Seconds())
			if mf.Health.IntervalSecs > 0 {
				every = mf.Health.IntervalSecs
			}
			st.Plugins = append(st.Plugins, store.Plugin{
				ID: mf.Kind, Kind: mf.Kind, Name: mf.Name,
				Description: mf.Description, Version: mf.Version, Author: mf.Author,
				Enabled: true, Priority: 100,
				Params: DefaultParams(mf),
				Status: store.StatusUnknown, StatusMessage: "尚未执行健康检测",
				Health: store.HealthConfig{
					Enabled: mf.Health.Enabled, IntervalSecs: every,
					TimeoutSecs:   mf.Health.TimeoutSecs,
					FailThreshold: mf.Health.FailThreshold,
					ProbeMode:     mf.Health.ProbeMode,
				},
				Revision: 1, CreatedAt: now, UpdatedAt: now,
			})
		}
		return nil
	})
}

// List 返回按「异常优先 → 优先级」排序的插件视图。
func (m *Manager) List() ([]View, error) {
	var out []View
	err := m.st.View(func(st *store.State) error {
		out = make([]View, 0, len(st.Plugins))
		for i := range st.Plugins {
			v, err := m.viewOf(&st.Plugins[i])
			if err != nil {
				return err
			}
			out = append(out, v)
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		ri, rj := statusRank(out[i].Status), statusRank(out[j].Status)
		if ri != rj {
			return ri < rj
		}
		return out[i].Priority > out[j].Priority
	})
	return out, nil
}

func statusRank(s store.PluginStatus) int {
	switch s {
	case store.StatusUnhealthy:
		return 0
	case store.StatusDegraded:
		return 1
	case store.StatusUnknown:
		return 2
	case store.StatusHealthy:
		return 3
	default:
		return 4
	}
}

func (m *Manager) Get(id string) (View, error) {
	var v View
	err := m.st.View(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		var err error
		v, err = m.viewOf(p)
		return err
	})
	return v, err
}

func (m *Manager) viewOf(p *store.Plugin) (View, error) {
	mf, err := m.registry.Get(p.Kind)
	view := View{Plugin: *p, Params: map[string]any{}, Grouped: []ParamGroup{}}
	if err != nil {
		view.Warning = "插件类型未编译进当前版本，无法执行上传"
		view.HasManifest = false
		return view, nil
	}
	view.HasManifest = true
	view.Manifest = mf.Manifest
	view.Params = MaskParams(mf.Manifest, p.Params)
	plain, err := OpenParams(p.Params, m.open)
	if err != nil {
		return view, err
	}
	view.Warning = VolatileWarning(mf.Manifest, plain)
	// 按 Group 保序分组，供前端分区渲染动态表单。
	seen := map[string]int{}
	for _, f := range mf.Manifest.Params {
		idx, ok := seen[f.Group]
		if !ok {
			view.Grouped = append(view.Grouped, ParamGroup{Name: f.Group})
			idx = len(view.Grouped) - 1
			seen[f.Group] = idx
		}
		view.Grouped[idx].Fields = append(view.Grouped[idx].Fields, f)
	}
	return view, nil
}

// ---------- 差量 / 全量 / 参数修改 ----------

type Patch struct {
	Name             *string             `json:"name,omitempty"`
	Description      *string             `json:"description,omitempty"`
	Enabled          *bool               `json:"enabled,omitempty"`
	Priority         *int                `json:"priority,omitempty"`
	Params           map[string]string   `json:"params,omitempty"`
	Health           *store.HealthConfig `json:"health,omitempty"`
	ExpectedRevision *int64              `json:"expectedRevision,omitempty"`
}

type Full struct {
	Name             string             `json:"name"`
	Description      string             `json:"description"`
	Enabled          bool               `json:"enabled"`
	Priority         int                `json:"priority"`
	Params           map[string]string  `json:"params"`
	Health           store.HealthConfig `json:"health"`
	ExpectedRevision *int64             `json:"expectedRevision,omitempty"`
}

// Patch 差量修改：只覆盖传入的字段，params 按键合并。
func (m *Manager) Patch(id string, patch Patch, actor string) (View, error) {
	err := m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		if err := checkRevision(p, patch.ExpectedRevision); err != nil {
			return err
		}
		mf, err := m.registry.Get(p.Kind)
		if err != nil {
			return err
		}
		diff := map[string]string{}
		if patch.Name != nil && *patch.Name != p.Name {
			diff["name"] = p.Name + " → " + *patch.Name
			p.Name = *patch.Name
		}
		if patch.Description != nil && *patch.Description != p.Description {
			diff["description"] = "已更新"
			p.Description = *patch.Description
		}
		if patch.Enabled != nil && *patch.Enabled != p.Enabled {
			diff["enabled"] = fmt.Sprintf("%v → %v", p.Enabled, *patch.Enabled)
			p.Enabled = *patch.Enabled
			if !p.Enabled {
				p.Status = store.StatusDisabled
				p.StatusMessage = "已停用"
			} else {
				p.Status = store.StatusUnknown
				p.StatusMessage = "等待下一次健康检测"
			}
		}
		if patch.Priority != nil && *patch.Priority != p.Priority {
			diff["priority"] = fmt.Sprintf("%d → %d", p.Priority, *patch.Priority)
			p.Priority = *patch.Priority
		}
		if patch.Health != nil {
			if err := validateHealth(*patch.Health); err != nil {
				return err
			}
			diff["health"] = fmt.Sprintf("每 %ds 检测", patch.Health.IntervalSecs)
			p.Health = *patch.Health
		}
		if len(patch.Params) > 0 {
			sealed, err := SealParams(mf.Manifest, patch.Params, m.seal)
			if err != nil {
				return err
			}
			plain, err := OpenParams(mergeParams(p.Params, sealed), m.open)
			if err != nil {
				return err
			}
			if err := ValidateParams(mf.Manifest, plain); err != nil {
				return err
			}
			for k := range sealed {
				label := k
				if f, ok := FieldByKey(mf.Manifest, k); ok && f.Sensitive {
					label += "(已脱敏)"
				}
				diff["param."+label] = "已更新"
			}
			p.Params = mergeParams(p.Params, sealed)
		}
		p.Revision++
		p.UpdatedAt = store.NowUTC()
		st.Changes = append(st.Changes, newChange(id, "patch", actor, diff, "差量修改"))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return m.Get(id)
}

// Replace 全量修改：未提供的字段重置为清单默认 / 零值。
func (m *Manager) Replace(id string, full Full, actor string) (View, error) {
	err := m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		if err := checkRevision(p, full.ExpectedRevision); err != nil {
			return err
		}
		mf, err := m.registry.Get(p.Kind)
		if err != nil {
			return err
		}
		if err := validateHealth(full.Health); err != nil {
			return err
		}
		params := map[string]string{}
		if full.Params != nil {
			params = full.Params
		}
		sealed, err := SealParams(mf.Manifest, params, m.seal)
		if err != nil {
			return err
		}
		plain, err := OpenParams(sealed, m.open)
		if err != nil {
			return err
		}
		if err := ValidateParams(mf.Manifest, plain); err != nil {
			return err
		}
		name := full.Name
		if name == "" {
			name = mf.Manifest.Name
		}
		p.Name = name
		p.Description = full.Description
		p.Enabled = full.Enabled
		p.Priority = full.Priority
		p.Health = full.Health
		p.Params = sealed
		p.Status = store.StatusUnknown
		p.StatusMessage = "配置已全量替换，等待健康检测"
		p.FailCount = 0
		p.Revision++
		p.UpdatedAt = store.NowUTC()
		st.Changes = append(st.Changes, newChange(id, "replace", actor,
			map[string]string{"params": "全量替换", "health": fmt.Sprintf("每 %ds", full.Health.IntervalSecs)},
			"全量修改"))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return m.Get(id)
}

// SetParams 单独修改插件参数，MCP 的 plugin_params_set 使用。
func (m *Manager) SetParams(id string, params map[string]string, actor string) (View, error) {
	return m.Patch(id, Patch{Params: params}, actor)
}

// UpdateSpec 描述一次插件版本更新。
type UpdateSpec struct {
	Version        string `json:"version"`        // 留空则同步到内置版本
	SyncParams     bool   `json:"syncParams"`     // 补齐清单中新增的参数默认值
	ResetToDefault bool   `json:"resetToDefault"` // 把所有参数重置为清单默认
	Note           string `json:"note"`
}

// Update 更新插件版本并迁移参数模板。
func (m *Manager) Update(id string, spec UpdateSpec, actor string) (View, error) {
	err := m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		mf, err := m.registry.Get(p.Kind)
		if err != nil {
			return err
		}
		target := strings.TrimSpace(spec.Version)
		if target == "" {
			target = mf.Manifest.Version
		}
		oldVersion := p.Version
		if spec.ResetToDefault {
			sealed, err := SealParams(mf.Manifest, DefaultParams(mf.Manifest), m.seal)
			if err != nil {
				return err
			}
			p.Params = sealed
		} else if spec.SyncParams {
			defaults := DefaultParams(mf.Manifest)
			for k, v := range defaults {
				if _, exists := p.Params[k]; !exists {
					if p.Params == nil {
						p.Params = map[string]string{}
					}
					p.Params[k] = v
				}
			}
		}
		p.Version = target
		p.Description = mf.Manifest.Description
		p.Author = mf.Manifest.Author
		p.Revision++
		p.UpdatedAt = store.NowUTC()
		msg := spec.Note
		if msg == "" {
			msg = "插件更新"
		}
		st.Changes = append(st.Changes, newChange(id, "update", actor,
			map[string]string{"version": oldVersion + " → " + target,
				"syncParams": fmt.Sprintf("%v", spec.SyncParams)}, msg))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return m.Get(id)
}

// Toggle 启用或停用插件。
func (m *Manager) Toggle(id string, enabled bool, actor string) (View, error) {
	return m.Patch(id, Patch{Enabled: &enabled}, actor)
}

// ---------- 调试 / 检测 / 上传 ----------

type DebugStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"` // pass | warn | fail
	Detail     string `json:"detail"`
	DurationMs int64  `json:"durationMs"`
}

type DebugReport struct {
	PluginID  string      `json:"pluginId"`
	Version   string      `json:"version"`
	Steps     []DebugStep `json:"steps"`
	Healthy   bool        `json:"healthy"`
	CheckedAt time.Time   `json:"checkedAt"`
}

func (m *Manager) loadRunnable(id string) (store.Plugin, Manifest, Uploader, map[string]string, error) {
	var p store.Plugin
	var mf Manifest
	var cfg map[string]string
	err := m.st.View(func(st *store.State) error {
		found := store.PluginByID(st, id)
		if found == nil {
			return ErrPluginNotFound
		}
		if !found.Enabled {
			return ErrPluginDisabled
		}
		p = *found
		p.Params = copyMap(found.Params)
		return nil
	})
	if err != nil {
		return p, mf, nil, nil, err
	}
	d, err := m.registry.Get(p.Kind)
	if err != nil {
		return p, mf, nil, nil, err
	}
	mf = d.Manifest
	cfg, err = OpenParams(p.Params, m.open)
	if err != nil {
		return p, mf, nil, nil, err
	}
	return p, mf, d.New(), cfg, nil
}

// Debug 执行无副作用的诊断：参数校验 → 临时凭据有效期 → 上游连通性。
func (m *Manager) Debug(ctx context.Context, id string) (DebugReport, error) {
	p, mf, up, cfg, err := m.loadRunnable(id)
	if err != nil {
		return DebugReport{}, err
	}
	report := DebugReport{PluginID: id, Version: p.Version, CheckedAt: store.NowUTC(), Steps: []DebugStep{}}
	add := func(name, status, detail string, since time.Time) {
		report.Steps = append(report.Steps, DebugStep{
			Name: name, Status: status, Detail: detail,
			DurationMs: time.Since(since).Milliseconds(),
		})
	}
	start := time.Now()
	if err := ValidateParams(mf, cfg); err != nil {
		add("参数校验", "fail", err.Error(), start)
		return m.finishDebug(id, report, actorSystem), nil
	}
	add("参数校验", "pass", fmt.Sprintf("%d 项参数通过校验", len(mf.Params)), start)

	start = time.Now()
	if w := VolatileWarning(mf, cfg); w != "" {
		status := "warn"
		if strings.Contains(w, "已于") || strings.Contains(w, "未设置") {
			status = "fail"
		}
		add("临时凭据有效期", status, w, start)
	} else {
		add("临时凭据有效期", "pass", "凭据有效或无需轮换", start)
	}

	start = time.Now()
	probeCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if err := up.Check(probeCtx, cfg); err != nil {
		add("上游连通性", "fail", err.Error(), start)
	} else {
		add("上游连通性", "pass", mf.Endpoint, start)
	}
	report.Healthy = true
	for _, s := range report.Steps {
		if s.Status == "fail" {
			report.Healthy = false
			break
		}
	}
	return m.finishDebug(id, report, actorSystem), nil
}

const actorSystem = "system"

func (m *Manager) finishDebug(id string, r DebugReport, actor string) DebugReport {
	_ = m.st.Update(func(st *store.State) error {
		st.Changes = append(st.Changes, newChange(id, "debug", actor, nil,
			fmt.Sprintf("调试完成：healthy=%v，%d 个步骤", r.Healthy, len(r.Steps))))
		return nil
	})
	return r
}

// Check 执行一次健康检测，更新状态机并按需产生/恢复告警。
func (m *Manager) Check(ctx context.Context, id string) error {
	p, mf, up, cfg, err := m.loadRunnable(id)
	if err != nil {
		if errors.Is(err, ErrPluginDisabled) || errors.Is(err, ErrPluginNotFound) {
			return nil
		}
		return err
	}
	timeout := time.Duration(p.Health.TimeoutSecs) * time.Second
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	start := time.Now()
	var checkErr error
	if checkErr = ValidateParams(mf, cfg); checkErr == nil {
		checkErr = up.Check(checkCtx, cfg)
	}
	latency := time.Since(start).Milliseconds()

	return m.st.Update(func(st *store.State) error {
		cur := store.PluginByID(st, id)
		if cur == nil {
			return ErrPluginNotFound
		}
		now := store.NowUTC()
		cur.LastCheckedAt = &now
		cur.LatencyMs = latency
		if checkErr == nil {
			wasBad := cur.Status == store.StatusUnhealthy || cur.Status == store.StatusDegraded
			cur.Status = store.StatusHealthy
			cur.StatusMessage = fmt.Sprintf("正常 · %dms", latency)
			cur.FailCount = 0
			cur.SuccessCount++
			cur.LastSuccessAt = &now
			if wasBad {
				m.ackAlerts(st, id, "system")
				st.Changes = append(st.Changes, newChange(id, "recover", "system", nil, "插件已恢复正常"))
				slog.Info("插件恢复正常", "plugin", id)
			}
			return nil
		}
		cur.FailCount++
		cur.StatusMessage = truncateMsg(checkErr.Error(), 300)
		threshold := cur.Health.FailThreshold
		if threshold <= 0 {
			threshold = 2
		}
		if cur.FailCount >= threshold {
			cur.Status = store.StatusUnhealthy
		} else {
			cur.Status = store.StatusDegraded
		}
		if cur.Status == store.StatusUnhealthy {
			m.raiseAlert(st, cur, mf, checkErr.Error())
		}
		slog.Warn("插件健康检测失败", "plugin", id, "failCount", cur.FailCount, "err", checkErr)
		return nil
	})
}

func (m *Manager) raiseAlert(st *store.State, p *store.Plugin, mf Manifest, reason string) {
	for i := range st.Alerts {
		a := &st.Alerts[i]
		if a.PluginID == p.ID && a.AckedAt == nil && a.Level == "critical" {
			a.Message = truncateMsg(reason, 500)
			a.CreatedAt = store.NowUTC()
			return // 同一插件的未处理告警只保留一条，避免刷屏
		}
	}
	var warn string
	if plain, err := OpenParams(p.Params, m.open); err == nil {
		warn = VolatileWarning(mf, plain)
	}
	msg := reason
	if warn != "" {
		msg = warn + " | " + msg
	}
	st.Alerts = append([]store.Alert{{
		ID: "alt_" + randomHex(8), PluginID: p.ID, PluginName: p.Name,
		Level: "critical", Title: "插件异常：" + p.Name,
		Message: truncateMsg(msg, 500), CreatedAt: store.NowUTC(),
	}}, st.Alerts...)
}

func (m *Manager) ackAlerts(st *store.State, pluginID, by string) {
	now := store.NowUTC()
	for i := range st.Alerts {
		a := &st.Alerts[i]
		if a.PluginID == pluginID && a.AckedAt == nil {
			a.AckedAt = &now
			a.AckedBy = by
		}
	}
}

// Upload 走指定插件完成一次上传，并联动健康状态。
func (m *Manager) Upload(ctx context.Context, id string, req UploadRequest) (*UploadResult, error) {
	_, mf, up, cfg, err := m.loadRunnable(id)
	if err != nil {
		return nil, err
	}
	if err := ValidateParams(mf, cfg); err != nil {
		return nil, err
	}
	result, err := up.Upload(ctx, req, cfg)
	if err != nil {
		_ = m.st.Update(func(st *store.State) error {
			if p := store.PluginByID(st, id); p != nil {
				p.FailCount++
				p.Status = store.StatusDegraded
				p.StatusMessage = truncateMsg("上传失败: "+err.Error(), 300)
				if p.FailCount >= 2 {
					p.Status = store.StatusUnhealthy
					m.raiseAlert(st, p, mf, err.Error())
				}
			}
			return nil
		})
		return nil, fmt.Errorf("插件 %s 上传失败: %w", id, err)
	}
	_ = m.st.Update(func(st *store.State) error {
		if p := store.PluginByID(st, id); p != nil {
			now := store.NowUTC()
			p.SuccessCount++
			p.LastSuccessAt = &now
			p.LastCheckedAt = &now
			p.FailCount = 0
			if p.Status != store.StatusDisabled {
				p.Status = store.StatusHealthy
				p.StatusMessage = "上传成功"
			}
		}
		return nil
	})
	return result, nil
}

// Select 按「已启用 + 健康优先 + 优先级」挑选插件，未指定时使用。
func (m *Manager) Select(preferred string) (string, error) {
	var id string
	err := m.st.View(func(st *store.State) error {
		if preferred != "" {
			p := store.PluginByID(st, preferred)
			if p == nil {
				return ErrPluginNotFound
			}
			if !p.Enabled {
				return ErrPluginDisabled
			}
			id = p.ID
			return nil
		}
		best := -1
		for i := range st.Plugins {
			p := &st.Plugins[i]
			if !p.Enabled || p.Status == store.StatusUnhealthy {
				continue
			}
			score := p.Priority
			if p.Status == store.StatusHealthy {
				score += 1000
			}
			if score > best {
				best = score
				id = p.ID
			}
		}
		return nil
	})
	if err != nil {
		return "", err
	}
	if id == "" {
		return "", errors.New("没有可用的上传插件，请在插件管理中检查配置与健康状态")
	}
	return id, nil
}

func (m *Manager) SetHealth(id string, h store.HealthConfig, actor string) (View, error) {
	return m.Patch(id, Patch{Health: &h}, actor)
}

func (m *Manager) AckAlert(id, actor string) error {
	return m.st.Update(func(st *store.State) error {
		a := store.AlertByID(st, id)
		if a == nil {
			return errors.New("告警不存在")
		}
		now := store.NowUTC()
		a.AckedAt = &now
		a.AckedBy = actor
		return nil
	})
}

// ---------- helpers ----------

func mergeParams(base, patch map[string]string) map[string]string {
	out := make(map[string]string, len(base)+len(patch))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range patch {
		out[k] = v
	}
	return out
}

func copyMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func checkRevision(p *store.Plugin, expected *int64) error {
	if expected != nil && *expected != p.Revision {
		return fmt.Errorf("版本冲突：当前 revision=%d，提交的是 %d（请刷新后重试）", p.Revision, *expected)
	}
	return nil
}

func validateHealth(h store.HealthConfig) error {
	if h.IntervalSecs < 10 {
		return errors.New("检测间隔不得小于 10 秒")
	}
	if h.TimeoutSecs < 1 || h.TimeoutSecs > 120 {
		return errors.New("检测超时需在 1~120 秒之间")
	}
	if h.FailThreshold < 1 {
		return errors.New("失败阈值至少为 1")
	}
	if h.ProbeMode != "" && h.ProbeMode != "ping" && h.ProbeMode != "upload-sample" {
		return errors.New("探活模式仅支持 ping / upload-sample")
	}
	return nil
}

func newChange(pluginID, action, actor string, diff map[string]string, msg string) store.ChangeLog {
	return store.ChangeLog{
		ID: "chg_" + randomHex(8), PluginID: pluginID, Action: action,
		Actor: actor, Diff: diff, Message: msg, CreatedAt: store.NowUTC(),
	}
}

func truncateMsg(s string, n int) string {
	s = strings.ReplaceAll(s, "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func randomHex(n int) string {
	b := make([]byte, (n+1)/2)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	const hexChars = "0123456789abcdef"
	out := make([]byte, n)
	for i := range b {
		out[i*2] = hexChars[b[i]>>4]
		if i*2+1 < n {
			out[i*2+1] = hexChars[b[i]&0x0f]
		}
	}
	return string(out)
}
