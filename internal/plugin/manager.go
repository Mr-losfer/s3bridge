package plugin

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/plugin/runtime"
	"github.com/Mr-losfer/s3bridge/internal/plugin/sdk"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

// Builtin 是随二进制发布的内置插件源码（plugin.json + *.go）。
type Builtin struct {
	ID    string
	Files map[string]string
}

// Manager 负责插件实例生命周期：文件夹、加载、调用、参数、调度与告警。
type Manager struct {
	st           store.Store
	rt           *runtime.Runtime
	pluginsDir   string
	seal         func(string) (string, error)
	open         func(string) (string, error)
	defaultEvery time.Duration
	builtins     []Builtin
}

func NewManager(st store.Store, rt *runtime.Runtime, pluginsDir string, seal, open func(string) (string, error), defaultEvery time.Duration, builtins []Builtin) *Manager {
	return &Manager{
		st: st, rt: rt, pluginsDir: pluginsDir,
		seal: seal, open: open, defaultEvery: defaultEvery, builtins: builtins,
	}
}

func (m *Manager) Store() store.Store { return m.st }

// Runtime 暴露执行引擎信息，供 API 展示与测试使用。
func (m *Manager) Runtime() *runtime.Runtime { return m.rt }

// View 是插件列表/详情的对外视图，敏感参数已脱敏。
type View struct {
	store.Plugin
	Params      map[string]any `json:"params"`
	Definition  string         `json:"-"`
	Manifest    Manifest       `json:"manifest"`
	Warning     string         `json:"warning"`
	Grouped     []ParamGroup   `json:"grouped"`
	HasManifest bool           `json:"hasManifest"`
	Editable    bool           `json:"editable"`
	Toolchain   bool           `json:"toolchain"`
	Built       bool           `json:"built"`
	SourceFiles int            `json:"sourceFiles"`
}

// DebugReport 是一次诊断结果。
type DebugReport struct {
	PluginID  string      `json:"pluginId"`
	Version   string      `json:"version"`
	Steps     []DebugStep `json:"steps"`
	Traces    []Trace     `json:"traces,omitempty"`
	Healthy   bool        `json:"healthy"`
	CheckedAt time.Time   `json:"checkedAt"`
}

type DebugStep struct {
	Name       string `json:"name"`
	Status     string `json:"status"` // pass|warn|fail
	Detail     string `json:"detail"`
	DurationMs int64  `json:"durationMs"`
}

type BuildReport struct {
	OK         bool      `json:"ok"`
	Output     string    `json:"output"`
	DurationMs int64     `json:"durationMs"`
	SHA256     string    `json:"sha256,omitempty"`
	Size       int64     `json:"size,omitempty"`
	At         time.Time `json:"at"`
}

// ---------- 内置插件与迁移 ----------

// EnsureBuiltin 落盘内置插件源码、同步存储记录，并迁移旧声明式插件。
func (m *Manager) EnsureBuiltin() error {
	for _, b := range m.builtins {
		if err := m.ensureBuiltin(b); err != nil {
			return err
		}
	}
	return m.migrateLegacy()
}

func (m *Manager) ensureBuiltin(b Builtin) error {
	dir := filepath.Join(m.pluginsDir, b.ID)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	// 只在文件缺失时写入：用户对内置插件的本地修改不会被覆盖。
	for name, content := range b.Files {
		path := filepath.Join(dir, name)
		if _, err := os.Stat(path); err == nil {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			return err
		}
		if err := writeFileAtomic(path, []byte(content)); err != nil {
			return fmt.Errorf("写出内置插件文件 %s: %w", name, err)
		}
	}
	mf, err := LoadManifest(dir)
	if err != nil {
		return fmt.Errorf("内置插件 %s 清单无效: %w", b.ID, err)
	}

	// 同步存储：保留用户参数、调度与健康设置，只更新描述性字段。
	err = m.st.Update(func(st *store.State) error {
		now := store.NowUTC()
		p := store.PluginByID(st, b.ID)
		if p == nil {
			every := int(m.defaultEvery.Seconds())
			if mf.Health.IntervalSecs > 0 {
				every = mf.Health.IntervalSecs
			}
			types, _ := ParseFileTypes(mf.FileTypes)
			st.Plugins = append(st.Plugins, store.Plugin{
				ID: b.ID, Kind: b.ID, Name: mf.Name, Description: mf.Description,
				Version: mf.Version, Author: mf.Author, Enabled: true,
				Priority: mf.Priority, FileTypes: types, Params: DefaultParams(mf),
				Status: store.StatusUnknown, StatusMessage: "尚未执行健康检测",
				Health: store.HealthConfig{
					Enabled: mf.Health.Enabled, IntervalSecs: every,
					TimeoutSecs: mf.Health.TimeoutSecs, FailThreshold: mf.Health.FailThreshold,
					ProbeMode: mf.Health.ProbeMode,
				},
				Dir: dir, ValidityHours: mf.Validity.IntervalHours,
				Revision: 1, CreatedAt: now, UpdatedAt: now,
			})
			return nil
		}
		p.Kind, p.Name, p.Description, p.Version, p.Author = b.ID, mf.Name, mf.Description, mf.Version, mf.Author
		p.Dir = dir
		p.ValidityHours = mf.Validity.IntervalHours
		p.Definition = "" // 旧声明式源码已迁移为文件夹
		for _, f := range mf.Params {
			if _, ok := p.Params[f.Key]; !ok && f.Default != "" {
				if p.Params == nil {
					p.Params = map[string]string{}
				}
				p.Params[f.Key] = f.Default
			}
		}
		p.UpdatedAt = now
		return nil
	})
	if err != nil {
		return err
	}
	return nil
}

// migrateLegacy 处理 v1.x 的声明式插件：内置类型转文件夹，自定义类型给出可编辑骨架。
func (m *Manager) migrateLegacy() error {
	var legacy []store.Plugin
	_ = m.st.View(func(st *store.State) error {
		for _, p := range st.Plugins {
			if p.Definition != "" {
				legacy = append(legacy, p)
			}
		}
		return nil
	})
	for _, p := range legacy {
		dir := filepath.Join(m.pluginsDir, p.ID)
		_, statErr := os.Stat(filepath.Join(dir, "plugin.json"))
		if statErr != nil {
			// 自定义声明式插件无法自动翻译成 Go：生成一份带说明的骨架，原 JSON 留档。
			// 参数键与文件类型要带过去，否则用户在后台看不到自己配过什么。
			mf := Manifest{
				Schema: ManifestSchema, ID: p.ID, Name: p.Name, Version: p.Version,
				Description: p.Description, Author: p.Author, Priority: p.Priority,
				FileTypes:   fileTypesToStrings(p.FileTypes),
				TimeoutSecs: 60,
				Health: HealthHint{
					Enabled: p.Health.Enabled, IntervalSecs: p.Health.IntervalSecs,
					TimeoutSecs: p.Health.TimeoutSecs, FailThreshold: p.Health.FailThreshold,
					ProbeMode: p.Health.ProbeMode,
				},
				Params: legacyParamFields(p.Params),
				Docs: "该插件由 v1.x 的声明式定义迁移而来。原定义见 legacy-definition.json。\n\n" +
					"声明式请求模板已不再支持，请在 main.go 中用 Go 实现上传逻辑，保存并加载通过后手动启用。",
			}
			_ = os.MkdirAll(dir, 0o700)
			_ = SaveManifest(dir, mf)
			_ = writeFileAtomic(filepath.Join(dir, "legacy-definition.json"), []byte(p.Definition))
			_ = writeFileAtomic(filepath.Join(dir, "main.go"), []byte(blankMain))
			if err := m.st.Update(func(st *store.State) error {
				cur := store.PluginByID(st, p.ID)
				if cur == nil {
					return nil
				}
				cur.Dir = dir
				cur.Definition = ""
				// 骨架里 Upload 只会返回「尚未实现」，留着启用会被调度到然后必然失败；
				// 停用并写明原因，等用户重写后自己开启。
				cur.Enabled = false
				cur.Status = store.StatusDisabled
				cur.StatusMessage = "声明式引擎已移除：请用 Go 重写 main.go，加载通过后启用"
				cur.UpdatedAt = store.NowUTC()
				st.Changes = append(st.Changes, newChange(p.ID, "migrate", "system", nil, "声明式插件迁移为 Go 骨架，已停用等待重写"))
				return nil
			}); err != nil {
				return err
			}
			continue
		}
		// 已有文件夹（内置插件或已迁移）：只清空旧字段。
		_ = m.st.Update(func(st *store.State) error {
			cur := store.PluginByID(st, p.ID)
			if cur != nil {
				cur.Definition = ""
				cur.Dir = dir
			}
			return nil
		})
	}
	return nil
}

// fileTypesToStrings 把存储层的文件类型转成清单里的字符串数组。
func fileTypesToStrings(types []store.FileCategory) []string {
	if len(types) == 0 {
		return nil
	}
	out := make([]string, 0, len(types))
	for _, t := range types {
		out = append(out, string(t))
	}
	return out
}

// legacyParamFields 把 v1.x 的参数键翻译成清单字段。
//
// 只有键名没有元信息，所以一律按字符串处理；命中密钥类关键词的标为敏感，
// 这样旧的 enc: 密文在后台仍然以脱敏形式展示，解密链路也能直接复用。
// 排序后输出，保证同一份数据每次迁移得到的 plugin.json 一致。
func legacyParamFields(params map[string]string) []ParamField {
	if len(params) == 0 {
		return nil
	}
	keys := make([]string, 0, len(params))
	for k := range params {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	out := make([]ParamField, 0, len(keys))
	for _, k := range keys {
		f := ParamField{Key: k, Label: k, Type: "text", Help: "由 v1.x 声明式插件迁移"}
		if looksSecret(k) {
			f.Type = "secret"
			f.Sensitive = true
		}
		out = append(out, f)
	}
	return out
}

// looksSecret 用键名猜测是否为凭据类参数。
func looksSecret(key string) bool {
	k := strings.ToLower(key)
	for _, hint := range []string{"key", "token", "secret", "password", "passwd", "cookie", "credential", "auth"} {
		if strings.Contains(k, hint) {
			return true
		}
	}
	return false
}

// Warmup 预加载已启用的插件。
// 解释执行只要几毫秒，但仍然放在后台：启动阶段不该被任何插件源码拖住。
func (m *Manager) Warmup(ctx context.Context) {
	if m.rt == nil || m.rt.Toolchain() == "" {
		return
	}
	var todo []store.Plugin
	_ = m.st.View(func(st *store.State) error {
		for _, p := range st.Plugins {
			if p.Enabled {
				todo = append(todo, p)
			}
		}
		return nil
	})
	for _, p := range todo {
		if ctx.Err() != nil {
			return
		}
		dir := m.dirOf(&p)
		// 源码不完整时不在启动阶段反复加载，等用户显式触发并看到错误。
		if _, err := LoadManifest(dir); err != nil {
			continue
		}
		start := time.Now()
		res, err := m.rt.Load(ctx, p.ID, dir)
		m.setBuildState(p.ID, res, err)
		if err != nil {
			slog.Warn("插件预加载失败", "plugin", p.ID, "err", err)
			continue
		}
		slog.Info("插件预加载完成", "plugin", p.ID,
			"ms", time.Since(start).Milliseconds(), "files", res.Files)
	}
}

// ---------- 查询 ----------

func (m *Manager) List() ([]View, error) {
	var out []View
	err := m.st.View(func(st *store.State) error {
		out = make([]View, 0, len(st.Plugins))
		for i := range st.Plugins {
			out = append(out, m.viewOf(&st.Plugins[i]))
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		hi, hj := healthRank(out[i].Status), healthRank(out[j].Status)
		if hi != hj {
			return hi > hj
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func (m *Manager) Get(id string) (View, error) {
	var v View
	err := m.st.View(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		v = m.viewOf(p)
		return nil
	})
	return v, err
}

func (m *Manager) dirOf(p *store.Plugin) string {
	if p.Dir != "" {
		return p.Dir
	}
	return filepath.Join(m.pluginsDir, p.ID)
}

// viewOf 读取文件夹内清单并组装视图；清单损坏时降级为「不可用」而不是报错。
func (m *Manager) viewOf(p *store.Plugin) View {
	dir := m.dirOf(p)
	view := View{Plugin: *p, Params: map[string]any{}, Grouped: []ParamGroup{}, Editable: p.Custom}
	view.Toolchain = m.rt != nil
	view.Built = p.BuildSHA != "" && p.BuildError == ""
	mf, err := LoadManifest(dir)
	if err != nil {
		view.Warning = fmt.Sprintf("插件清单不可用：%v", err)
		return view
	}
	view.HasManifest = true
	view.Manifest = mf
	view.Grouped = SortedGroups(mf)
	if files, err := ListFiles(dir, mf); err == nil {
		for _, f := range files {
			if f.Go {
				view.SourceFiles++
			}
		}
	}
	// 参数一律回显（敏感值走脱敏），停用的插件也不例外：
	// 否则用户停用后再打开表单会看到一片空白，一保存就把原有参数抹掉了。
	view.Params = MaskParams(mf, p.Params)
	if plain, err := OpenParams(p.Params, m.open); err != nil {
		if p.Enabled {
			view.Warning = "参数解密失败，请重新填写敏感字段"
		}
	} else if p.Enabled {
		view.Warning = VolatileWarning(mf, plain)
	}
	if p.BuildError != "" {
		if view.Warning != "" {
			view.Warning += "；"
		}
		view.Warning += "最近一次加载失败：" + truncateMsg(p.BuildError, 160)
	}
	return view
}

func mustPlain(params map[string]string, open func(string) (string, error)) map[string]string {
	out, err := OpenParams(params, open)
	if err != nil {
		return params
	}
	return out
}

// ---------- 生命周期 ----------

func (m *Manager) loadRunnable(id string) (store.Plugin, Manifest, map[string]string, string, error) {
	var p store.Plugin
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
		return p, Manifest{}, nil, "", err
	}
	dir := m.dirOf(&p)
	mf, err := LoadManifest(dir)
	if err != nil {
		return p, mf, nil, dir, err
	}
	cfg, err := OpenParams(p.Params, m.open)
	if err != nil {
		return p, mf, nil, dir, err
	}
	return p, mf, cfg, dir, nil
}

// Upload 通过子进程调用插件完成一次上传。
func (m *Manager) Upload(ctx context.Context, id string, req UploadRequest) (*UploadResult, error) {
	p, mf, cfg, dir, err := m.loadRunnable(id)
	if err != nil {
		return nil, err
	}
	if p.BuildError != "" {
		return nil, fmt.Errorf("插件尚未成功加载：%s", p.BuildError)
	}
	if m.rt == nil {
		return nil, errors.New("插件运行时未初始化")
	}
	buildCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	err = m.rt.EnsureLoaded(buildCtx, id, dir)
	cancel()
	if err != nil {
		return nil, fmt.Errorf("插件未就绪: %w", err)
	}

	callCtx, cancel := context.WithTimeout(ctx, time.Duration(mf.TimeoutSecs)*time.Second)
	defer cancel()
	start := time.Now()
	resp, err := m.rt.Call(callCtx, id, dir, sdk.Request{
		ID: newCallID(), Method: "upload", Config: cfg,
		Input: &sdk.Input{
			FileName: req.FileName, MimeType: req.MimeType, Size: req.Size,
			Path: req.Path, Bucket: req.Bucket, ObjectKey: req.ObjectKey, UserID: req.UserID,
		},
		TimeoutSecs: mf.TimeoutSecs,
	})
	latency := time.Since(start).Milliseconds()
	if err != nil {
		_ = m.st.Update(func(st *store.State) error {
			if cur := store.PluginByID(st, id); cur != nil {
				cur.LatencyMs = latency
				cur.StatusMessage = truncateMsg("上传失败："+err.Error(), 300)
			}
			return nil
		})
		return nil, err
	}
	out := convertOutput(resp.Result, req, id)
	_ = m.st.Update(func(st *store.State) error {
		if cur := store.PluginByID(st, id); cur != nil {
			cur.LatencyMs = latency
			cur.LastSuccessAt = ptrTime(store.NowUTC())
		}
		return nil
	})
	return out, nil
}

// Check 执行一次健康检测，更新状态机并按需产生/恢复告警。
func (m *Manager) Check(ctx context.Context, id string) error {
	p, mf, cfg, dir, err := m.loadRunnable(id)
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
	// 首次调用前先确保插件已加载：加载失败要报出源码错误，而不是掩成超时。
	start := time.Now()
	var checkErr error
	if m.rt == nil {
		checkErr = errors.New("插件运行时未初始化")
	} else if err := m.buildIfMissing(ctx, id, dir); err != nil {
		checkErr = err
	}
	checkCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if checkErr == nil {
		if _, callErr := m.rt.Call(checkCtx, id, dir, sdk.Request{
			ID: newCallID(), Method: "check", Config: cfg, TimeoutSecs: p.Health.TimeoutSecs,
		}); callErr != nil {
			checkErr = callErr
		}
	}
	if checkErr == nil {
		if w := VolatileWarning(mf, cfg); strings.Contains(w, "已于") || strings.Contains(w, "未设置") {
			checkErr = errors.New(w)
		}
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

// Debug 执行无副作用诊断：参数 → 凭据有效期 → 加载 → 插件自检，并带回请求追踪。
func (m *Manager) Debug(ctx context.Context, id string) (DebugReport, error) {
	p, mf, cfg, dir, err := m.loadRunnable(id)
	if err != nil {
		return DebugReport{}, err
	}
	report := DebugReport{PluginID: id, Version: p.Version, CheckedAt: store.NowUTC(), Steps: []DebugStep{}}
	add := func(name, status, detail string, since time.Time) {
		report.Steps = append(report.Steps, DebugStep{Name: name, Status: status, Detail: detail, DurationMs: time.Since(since).Milliseconds()})
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
	if m.rt == nil {
		add("插件加载", "fail", "插件运行时未初始化", start)
	} else if err := m.rt.EnsureLoaded(ctx, id, dir); err != nil {
		add("插件加载", "fail", err.Error(), start)
	} else {
		add("插件加载", "pass", "源码已加载，入口已注册", start)
	}

	start = time.Now()
	if m.rt != nil {
		probeCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
		resp, err := m.rt.Call(probeCtx, id, dir, sdk.Request{
			ID: newCallID(), Method: "check", Config: cfg, TimeoutSecs: 10,
		})
		cancel()
		report.Traces = convertTraces(resp.Traces)
		if err != nil {
			add("插件自检", "fail", err.Error(), start)
		} else {
			add("插件自检", "pass", fmt.Sprintf("插件返回正常，产生 %d 次请求", len(resp.Traces)), start)
		}
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
			fmt.Sprintf("调试完成：healthy=%v，%d 个步骤，%d 次请求", r.Healthy, len(r.Steps), len(r.Traces))))
		return nil
	})
	return r
}

// ---------- 修改 ----------

type Patch struct {
	Name             *string               `json:"name,omitempty"`
	Description      *string               `json:"description,omitempty"`
	Enabled          *bool                 `json:"enabled,omitempty"`
	Priority         *int                  `json:"priority,omitempty"`
	FileTypes        *[]store.FileCategory `json:"fileTypes,omitempty"`
	Params           map[string]string     `json:"params,omitempty"`
	Health           *store.HealthConfig   `json:"health,omitempty"`
	ExpectedRevision *int64                `json:"expectedRevision,omitempty"`
}

type Full struct {
	Name             string               `json:"name"`
	Description      string               `json:"description"`
	Enabled          bool                 `json:"enabled"`
	Priority         int                  `json:"priority"`
	FileTypes        []store.FileCategory `json:"fileTypes"`
	Params           map[string]string    `json:"params"`
	Health           store.HealthConfig   `json:"health"`
	ExpectedRevision *int64               `json:"expectedRevision,omitempty"`
}

func (m *Manager) Patch(id string, patch Patch, actor string) (View, error) {
	err := m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		if err := checkRevision(p, patch.ExpectedRevision); err != nil {
			return err
		}
		if patch.Name != nil {
			p.Name = strings.TrimSpace(*patch.Name)
		}
		if patch.Description != nil {
			p.Description = strings.TrimSpace(*patch.Description)
		}
		if patch.Enabled != nil {
			p.Enabled = *patch.Enabled
			if !p.Enabled {
				p.Status = store.StatusDisabled
			} else if p.Status == store.StatusDisabled {
				p.Status = store.StatusUnknown
			}
		}
		if patch.Priority != nil {
			p.Priority = *patch.Priority
		}
		if patch.FileTypes != nil {
			types, err := normalizeFileTypes(*patch.FileTypes)
			if err != nil {
				return err
			}
			p.FileTypes = types
		}
		if patch.Health != nil {
			if err := validateHealth(*patch.Health); err != nil {
				return err
			}
			p.Health = *patch.Health
		}
		if len(patch.Params) > 0 {
			if err := m.mergeParamsInto(p, patch.Params); err != nil {
				return err
			}
		}
		p.Revision++
		p.UpdatedAt = store.NowUTC()
		st.Changes = append(st.Changes, newChange(id, "patch", actor, patchDiff(patch), "差量修改插件配置"))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	m.syncManifestMeta(id)
	return m.Get(id)
}

func (m *Manager) Replace(id string, full Full, actor string) (View, error) {
	err := m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		if err := checkRevision(p, full.ExpectedRevision); err != nil {
			return err
		}
		if strings.TrimSpace(full.Name) == "" {
			return fmt.Errorf("%w: 名称不能为空", ErrInvalidParams)
		}
		types, err := normalizeFileTypes(full.FileTypes)
		if err != nil {
			return err
		}
		if err := validateHealth(full.Health); err != nil {
			return err
		}
		p.Name = strings.TrimSpace(full.Name)
		p.Description = strings.TrimSpace(full.Description)
		p.Enabled = full.Enabled
		p.Priority = full.Priority
		p.FileTypes = types
		p.Health = full.Health
		if !p.Enabled {
			p.Status = store.StatusDisabled
		}
		if err := m.mergeParamsInto(p, full.Params); err != nil {
			return err
		}
		p.Revision++
		p.UpdatedAt = store.NowUTC()
		st.Changes = append(st.Changes, newChange(id, "replace", actor, nil, "全量替换插件配置"))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	m.syncManifestMeta(id)
	return m.Get(id)
}

func (m *Manager) SetParams(id string, params map[string]string, actor string) (View, error) {
	err := m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		if err := m.mergeParamsInto(p, params); err != nil {
			return err
		}
		p.Revision++
		p.UpdatedAt = store.NowUTC()
		st.Changes = append(st.Changes, newChange(id, "params", actor, keysOf(params), fmt.Sprintf("更新 %d 个参数", len(params))))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return m.Get(id)
}

// mergeParamsInto 合并参数并做加密、校验；调用方必须持有存储写锁。
func (m *Manager) mergeParamsInto(p *store.Plugin, patch map[string]string) error {
	dir := m.dirOf(p)
	mf, err := LoadManifest(dir)
	if err != nil {
		return err
	}
	merged := mergeParams(p.Params, patch)
	sealed, err := SealParams(mf, merged, m.seal)
	if err != nil {
		return err
	}
	plain := mustPlain(sealed, m.open)
	if err := ValidateParams(mf, plain); err != nil {
		return err
	}
	p.Params = sealed
	return nil
}

type UpdateSpec struct {
	Version        string `json:"version"`
	SyncParams     bool   `json:"syncParams"`
	ResetToDefault bool   `json:"resetToDefault"`
	Note           string `json:"note"`
}

// Update 同步清单中的版本与新增参数。
func (m *Manager) Update(id string, spec UpdateSpec, actor string) (View, error) {
	err := m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		dir := m.dirOf(p)
		mf, err := LoadManifest(dir)
		if err != nil {
			return err
		}
		if spec.Version != "" {
			mf.Version = spec.Version
			if err := SaveManifest(dir, mf); err != nil {
				return err
			}
		}
		p.Version = mf.Version
		if spec.ResetToDefault {
			p.Params = DefaultParams(mf)
		} else if spec.SyncParams {
			for _, f := range mf.Params {
				if _, ok := p.Params[f.Key]; !ok && f.Default != "" {
					if p.Params == nil {
						p.Params = map[string]string{}
					}
					p.Params[f.Key] = f.Default
				}
			}
		}
		p.Revision++
		p.UpdatedAt = store.NowUTC()
		st.Changes = append(st.Changes, newChange(id, "update", actor, nil, pick(spec.Note, "更新插件版本")))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return m.Get(id)
}

func (m *Manager) Toggle(id string, enabled bool, actor string) (View, error) {
	err := m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		p.Enabled = enabled
		if enabled {
			if p.Status == store.StatusDisabled {
				p.Status = store.StatusUnknown
			}
			p.StatusMessage = "已启用，等待健康检测"
		} else {
			p.Status = store.StatusDisabled
			p.StatusMessage = "已停用"
		}
		p.Revision++
		p.UpdatedAt = store.NowUTC()
		action := "disable"
		if enabled {
			action = "enable"
		}
		st.Changes = append(st.Changes, newChange(id, action, actor, nil, "切换插件状态"))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return m.Get(id)
}

func (m *Manager) SetHealth(id string, h store.HealthConfig, actor string) (View, error) {
	if err := validateHealth(h); err != nil {
		return View{}, err
	}
	err := m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		p.Health = h
		p.Revision++
		p.UpdatedAt = store.NowUTC()
		st.Changes = append(st.Changes, newChange(id, "health-config", actor, nil, "更新健康检测配置"))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return m.Get(id)
}

func (m *Manager) AckAlert(id, actor string) error {
	return m.st.Update(func(st *store.State) error {
		for i := range st.Alerts {
			if st.Alerts[i].ID == id {
				if st.Alerts[i].AckedAt != nil {
					return nil
				}
				now := store.NowUTC()
				st.Alerts[i].AckedAt = &now
				st.Alerts[i].AckedBy = actor
				return nil
			}
		}
		return errors.New("告警不存在")
	})
}

// Delete 删除自定义插件及其文件夹；内置插件只能停用。
func (m *Manager) Delete(id, actor string) error {
	var dir string
	err := m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return ErrPluginNotFound
		}
		if !p.Custom {
			return fmt.Errorf("%w: 内置插件只能停用", ErrSourceInvalid)
		}
		dir = m.dirOf(p)
		st.Plugins = removePlugin(st.Plugins, id)
		st.Alerts = removeAlerts(st.Alerts, id)
		st.Changes = append(st.Changes, newChange(id, "delete", actor, nil, "删除自定义插件"))
		return nil
	})
	if err != nil {
		return err
	}
	if m.rt != nil {
		m.rt.Drop(id)
	}
	if dir != "" {
		_ = os.RemoveAll(dir)
	}
	return nil
}

// ---------- 选择与调度 ----------

func (m *Manager) Select(preferred string, category store.FileCategory) (string, error) {
	var chosen string
	var reason string
	err := m.st.View(func(st *store.State) error {
		if preferred != "" {
			p := store.PluginByID(st, preferred)
			if p == nil {
				return fmt.Errorf("%w: %s", ErrPluginNotFound, preferred)
			}
			if !p.Enabled {
				return fmt.Errorf("%w: %s", ErrPluginDisabled, preferred)
			}
			if !p.Supports(category) {
				return fmt.Errorf("%w: %s 不承接 %s", ErrFileTypeUnsupported, preferred, category)
			}
			chosen = p.ID
			return nil
		}
		var best *store.Plugin
		for i := range st.Plugins {
			p := &st.Plugins[i]
			if !p.Enabled || !p.Supports(category) {
				continue
			}
			if p.Status == store.StatusUnhealthy {
				continue
			}
			if best == nil || p.Priority > best.Priority ||
				(p.Priority == best.Priority && healthRank(p.Status) > healthRank(best.Status)) {
				best = p
			}
		}
		if best == nil {
			reason = "没有可用的插件承接 " + string(category)
			return ErrNoPlugin
		}
		chosen = best.ID
		return nil
	})
	if err != nil {
		if reason != "" {
			return "", errors.New(reason)
		}
		return "", err
	}
	return chosen, nil
}

var ErrNoPlugin = errors.New("没有可用插件")

func (m *Manager) Eligible(category store.FileCategory) []string {
	var out []string
	_ = m.st.View(func(st *store.State) error {
		for _, p := range st.Plugins {
			if p.Enabled && p.Supports(category) {
				out = append(out, p.ID)
			}
		}
		return nil
	})
	sort.Strings(out)
	return out
}

// ---------- 辅助 ----------

// buildIfMissing 确保插件进程已加载，给足 2 分钟预算。
func (m *Manager) buildIfMissing(ctx context.Context, id, dir string) error {
	buildCtx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	return m.rt.EnsureLoaded(buildCtx, id, dir)
}

func (m *Manager) setBuildState(id string, res *runtime.LoadResult, err error) {
	_ = m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return nil
		}
		now := store.NowUTC()
		if err != nil {
			p.BuildError = truncateMsg(err.Error(), 500)
			if p.Status != store.StatusDisabled {
				p.Status = store.StatusUnhealthy
				p.StatusMessage = "加载失败：" + truncateMsg(err.Error(), 200)
			}
		} else {
			p.BuildError = ""
			p.BuiltAt = &now
			p.BuildSHA = res.SHA256
			// 加载成功即可用，但健康状态要等下一次探测才有结论：
			// 停用中的插件保留停用原因，已健康的保留结论，其余一律回落到「等待检测」。
			if p.Status != store.StatusDisabled && p.Status != store.StatusHealthy {
				p.Status = store.StatusUnknown
				p.StatusMessage = "已加载，等待健康检测"
			}
		}
		p.UpdatedAt = now
		return nil
	})
}

// syncManifestMeta 把后台修改的名称/描述/优先级写回 plugin.json，保持单一事实来源。
func (m *Manager) syncManifestMeta(id string) {
	_ = m.st.Update(func(st *store.State) error {
		p := store.PluginByID(st, id)
		if p == nil {
			return nil
		}
		dir := m.dirOf(p)
		mf, err := LoadManifest(dir)
		if err != nil {
			return nil
		}
		mf.Name, mf.Description, mf.Priority = p.Name, p.Description, p.Priority
		_ = SaveManifest(dir, mf)
		return nil
	})
}

func (m *Manager) raiseAlert(st *store.State, p *store.Plugin, mf Manifest, reason string) {
	for i := range st.Alerts {
		a := &st.Alerts[i]
		if a.PluginID == p.ID && a.AckedAt == nil && a.Level == "critical" {
			a.Message = truncateMsg(reason, 500)
			a.CreatedAt = store.NowUTC()
			return
		}
	}
	st.Alerts = append(st.Alerts, store.Alert{
		ID: randomHex(8), PluginID: p.ID, PluginName: p.Name, Level: "critical",
		Message: truncateMsg(reason, 500), CreatedAt: store.NowUTC(),
	})
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

func healthRank(s store.PluginStatus) int {
	switch s {
	case store.StatusHealthy:
		return 4
	case store.StatusUnknown:
		return 3
	case store.StatusDegraded:
		return 2
	case store.StatusDisabled:
		return 1
	default:
		return 0
	}
}

func normalizeFileTypes(in []store.FileCategory) ([]store.FileCategory, error) {
	if len(in) == 0 {
		return nil, nil
	}
	seen := map[store.FileCategory]bool{}
	out := make([]store.FileCategory, 0, len(in))
	for _, c := range in {
		if !store.ValidCategory(c) {
			return nil, fmt.Errorf("%w: 未知文件类型 %s", ErrInvalidParams, c)
		}
		if seen[c] {
			continue
		}
		seen[c] = true
		out = append(out, c)
	}
	return out, nil
}

func mergeParams(base, patch map[string]string) map[string]string {
	out := copyMap(base)
	for k, v := range patch {
		if strings.TrimSpace(v) == "" {
			delete(out, k)
			continue
		}
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
		return fmt.Errorf("版本冲突：当前 revision=%d，请刷新后重试", p.Revision)
	}
	return nil
}

func validateHealth(h store.HealthConfig) error {
	if h.IntervalSecs < 10 {
		return fmt.Errorf("%w: 检测间隔不能小于 10 秒", ErrInvalidParams)
	}
	if h.TimeoutSecs < 1 || h.TimeoutSecs > 120 {
		return fmt.Errorf("%w: 超时必须在 1-120 秒", ErrInvalidParams)
	}
	if h.FailThreshold < 1 || h.FailThreshold > 20 {
		return fmt.Errorf("%w: 失败阈值必须在 1-20", ErrInvalidParams)
	}
	return nil
}

func newChange(pluginID, action, actor string, diff map[string]string, msg string) store.ChangeLog {
	return store.ChangeLog{
		ID: randomHex(8), PluginID: pluginID, Action: action, Actor: actor,
		Diff: diff, Message: truncateMsg(msg, 500), CreatedAt: store.NowUTC(),
	}
}

func truncateMsg(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

func randomHex(n int) string {
	const hexdigits = "0123456789abcdef"
	b := make([]byte, n*2)
	seed := time.Now().UnixNano()
	for i := range b {
		seed = seed*6364136223846793005 + 1442695040888963407
		b[i] = hexdigits[byte(seed>>56)%16]
	}
	return string(b)
}

func ptrTime(t time.Time) *time.Time { return &t }

func convertOutput(out *sdk.Output, req UploadRequest, id string) *UploadResult {
	res := &UploadResult{Provider: id}
	if out != nil {
		res.URL, res.ObjectKey = out.URL, out.ObjectKey
		res.FileName, res.MimeType = out.FileName, out.MimeType
		res.FileSize, res.URLExpireAt, res.Extra = out.FileSize, out.URLExpireAt, out.Extra
	}
	if res.FileName == "" {
		res.FileName = req.FileName
	}
	if res.MimeType == "" {
		res.MimeType = req.MimeType
	}
	if res.FileSize <= 0 {
		res.FileSize = req.Size
	}
	if res.ObjectKey == "" {
		res.ObjectKey = req.ObjectKey
	}
	return res
}

func convertTraces(in []sdk.Trace) []Trace {
	if len(in) == 0 {
		return nil
	}
	out := make([]Trace, 0, len(in))
	for _, t := range in {
		out = append(out, Trace{
			At: t.At, Method: t.Method, URL: t.URL, Status: t.Status,
			DurationMs: t.DurationMs, ReqBytes: t.ReqBytes, RespBytes: t.RespBytes,
			ReqHeaders: t.ReqHeaders, RespHeaders: t.RespHeaders,
			Snippet: t.Snippet, Error: t.Error,
		})
	}
	return out
}

func newCallID() string { return "c" + randomHex(6) }

func shortLog(res *runtime.LoadResult) string {
	if res == nil {
		return ""
	}
	return truncateMsg(res.Output, 300)
}

func patchDiff(p Patch) map[string]string {
	out := map[string]string{}
	if p.Name != nil {
		out["name"] = *p.Name
	}
	if p.Priority != nil {
		out["priority"] = fmt.Sprint(*p.Priority)
	}
	if p.Enabled != nil {
		out["enabled"] = fmt.Sprint(*p.Enabled)
	}
	if p.FileTypes != nil {
		out["fileTypes"] = fmt.Sprint(*p.FileTypes)
	}
	return out
}

func keysOf(m map[string]string) map[string]string {
	out := make(map[string]string, len(m))
	for k := range m {
		out[k] = "updated"
	}
	return out
}

func pick(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

func removePlugin(list []store.Plugin, id string) []store.Plugin {
	out := list[:0]
	for _, p := range list {
		if p.ID != id {
			out = append(out, p)
		}
	}
	return out
}

func removeAlerts(list []store.Alert, id string) []store.Alert {
	out := list[:0]
	for _, a := range list {
		if a.PluginID != id {
			out = append(out, a)
		}
	}
	return out
}
