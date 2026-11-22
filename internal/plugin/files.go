package plugin

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// FileContent 是单个源码文件的读取结果。
type FileContent struct {
	Name     string `json:"name"`
	Content  string `json:"content"`
	Editable bool   `json:"editable"`
	Go       bool   `json:"go"`
	Entry    bool   `json:"entry"`
}

// Files 列出插件的源码与说明文件。
func (m *Manager) Files(id string) ([]FileInfo, error) {
	p, mf, _, dir, err := m.loadMeta(id)
	if err != nil {
		return nil, err
	}
	_ = p
	return ListFiles(dir, mf)
}

func (m *Manager) loadMeta(id string) (store.Plugin, Manifest, []FileInfo, string, error) {
	var p store.Plugin
	err := m.st.View(func(st *store.State) error {
		found := store.PluginByID(st, id)
		if found == nil {
			return ErrPluginNotFound
		}
		p = *found
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
	files, err := ListFiles(dir, mf)
	return p, mf, files, dir, err
}

// FileGet 读取单个文件；system 文件（plugin.json/go.mod）也允许读，便于查看。
func (m *Manager) FileGet(id, name string) (FileContent, error) {
	_, mf, _, dir, err := m.loadMeta(id)
	if err != nil {
		return FileContent{}, err
	}
	content, err := ReadSourceFile(dir, name)
	if err != nil {
		return FileContent{}, err
	}
	return FileContent{
		Name: name, Content: content,
		Editable: !systemFiles[name], Go: strings.HasSuffix(name, ".go"),
		Entry: name == mf.Entry,
	}, nil
}

// FilePut 写入文件；.go 变更会立即重新加载，加载失败保留源码并返回报告。
func (m *Manager) FilePut(ctx context.Context, id, name, content, actor string) (View, *BuildReport, error) {
	_, _, _, dir, err := m.loadMeta(id)
	if err != nil {
		return View{}, nil, err
	}
	if err := WriteSourceFile(dir, name, content); err != nil {
		return View{}, nil, err
	}
	_ = m.st.Update(func(st *store.State) error {
		if p := store.PluginByID(st, id); p != nil {
			p.Revision++
			p.UpdatedAt = store.NowUTC()
		}
		st.Changes = append(st.Changes, newChange(id, "file-write", actor, map[string]string{"file": name},
			fmt.Sprintf("写入 %s（%d 字节）", name, len(content))))
		return nil
	})
	var report *BuildReport
	if strings.HasSuffix(name, ".go") {
		report, _ = m.Build(ctx, id, actor)
	}
	view, err := m.Get(id)
	return view, report, err
}

// FileDelete 删除文件；删除最后一个 .go 会被拒绝。
func (m *Manager) FileDelete(ctx context.Context, id, name, actor string) (View, *BuildReport, error) {
	_, _, _, dir, err := m.loadMeta(id)
	if err != nil {
		return View{}, nil, err
	}
	if err := DeleteSourceFile(dir, name); err != nil {
		return View{}, nil, err
	}
	_ = m.st.Update(func(st *store.State) error {
		if p := store.PluginByID(st, id); p != nil {
			p.Revision++
			p.UpdatedAt = store.NowUTC()
		}
		st.Changes = append(st.Changes, newChange(id, "file-delete", actor, map[string]string{"file": name}, "删除文件 "+name))
		return nil
	})
	var report *BuildReport
	if strings.HasSuffix(name, ".go") {
		report, _ = m.Build(ctx, id, actor)
	}
	view, err := m.Get(id)
	return view, report, err
}

// FileRename 重命名文件（入口文件改名后需要在清单里同步 entry）。
func (m *Manager) FileRename(ctx context.Context, id, from, to, actor string) (View, *BuildReport, error) {
	_, mf, _, dir, err := m.loadMeta(id)
	if err != nil {
		return View{}, nil, err
	}
	if err := RenameSourceFile(dir, from, to); err != nil {
		return View{}, nil, err
	}
	if mf.Entry == from {
		mf.Entry = to
		_ = SaveManifest(dir, mf)
	}
	_ = m.st.Update(func(st *store.State) error {
		if p := store.PluginByID(st, id); p != nil {
			p.Revision++
			p.UpdatedAt = store.NowUTC()
		}
		st.Changes = append(st.Changes, newChange(id, "file-rename", actor,
			map[string]string{"from": from, "to": to}, "重命名文件"))
		return nil
	})
	var report *BuildReport
	if strings.HasSuffix(to, ".go") {
		report, _ = m.Build(ctx, id, actor)
	}
	view, err := m.Get(id)
	return view, report, err
}

// Build 加载插件并记录结果。加载失败不会删除源码，也不影响其他插件。
func (m *Manager) Build(ctx context.Context, id, actor string) (*BuildReport, error) {
	p, mf, _, dir, err := m.loadMeta(id)
	if err != nil {
		return nil, err
	}
	_ = p
	if err := CheckLayout(dir, mf); err != nil {
		report := &BuildReport{OK: false, Output: err.Error(), At: store.NowUTC()}
		m.setBuildState(id, nil, err)
		return report, err
	}
	res, buildErr := m.rt.Load(ctx, id, dir)
	report := &BuildReport{OK: buildErr == nil, At: store.NowUTC()}
	if res != nil {
		report.Output = res.Output
		report.DurationMs = res.DurationMs
		report.SHA256 = res.SHA256
		report.Size = res.Size
	}
	m.setBuildState(id, res, buildErr)
	msg := "加载成功"
	if buildErr != nil {
		msg = "加载失败：" + truncateMsg(buildErr.Error(), 200)
	}
	_ = m.st.Update(func(st *store.State) error {
		st.Changes = append(st.Changes, newChange(id, "build", actor,
			map[string]string{"ok": fmt.Sprint(report.OK)}, msg))
		return nil
	})
	return report, buildErr
}

// ---------- 创建 / 清单 / 导出 ----------

type CreateSpec struct {
	ID          string
	Name        string
	Version     string
	Description string
	Author      string
	Template    string            // blank | http-multipart
	Files       map[string]string // 覆盖模板文件
	Params      map[string]string
	Enabled     bool
	Priority    *int
	FileTypes   []string
	TimeoutSecs int
}

// Create 从模板创建一个 Go 插件文件夹并立即加载。
func (m *Manager) Create(ctx context.Context, spec CreateSpec, actor string) (View, error) {
	id := strings.ToLower(strings.TrimSpace(spec.ID))
	tpl, ok := TemplateByKind(pick(spec.Template, "blank"))
	if !ok {
		return View{}, fmt.Errorf("%w: 未知模板 %s", ErrSourceInvalid, spec.Template)
	}
	name := strings.TrimSpace(spec.Name)
	if name == "" {
		name = id
	}
	mf := Manifest{
		Schema: ManifestSchema, ID: id, Name: name, Version: pick(spec.Version, "1.0.0"),
		Description: pick(spec.Description, tpl.Description), Author: pick(spec.Author, "S3Bridge"),
		FileTypes: spec.FileTypes, Priority: 100, TimeoutSecs: spec.TimeoutSecs,
		Health: HealthHint{Enabled: true, IntervalSecs: 300, TimeoutSecs: 8, FailThreshold: 2, ProbeMode: "ping"},
		Params: tpl.Params,
	}
	if spec.Priority != nil {
		mf.Priority = *spec.Priority
	}
	if err := mf.Validate(); err != nil {
		return View{}, err
	}

	var exists bool
	_ = m.st.View(func(st *store.State) error {
		exists = store.PluginByID(st, id) != nil
		return nil
	})
	if exists {
		return View{}, fmt.Errorf("%w: 插件 %s 已存在", ErrSourceInvalid, id)
	}
	dir := filepath.Join(m.pluginsDir, id)
	if _, err := os.Stat(dir); err == nil {
		return View{}, fmt.Errorf("%w: 目录 %s 已存在", ErrSourceInvalid, dir)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return View{}, err
	}
	files := map[string]string{}
	for k, v := range tpl.Files {
		files[k] = v
	}
	for k, v := range spec.Files {
		if err := ValidateFileName(k); err != nil {
			return View{}, err
		}
		if systemFiles[k] {
			return View{}, fmt.Errorf("%w: 不能覆盖 %s", ErrSourceInvalid, k)
		}
		files[k] = v
	}
	for name, content := range files {
		if err := writeFileAtomic(filepath.Join(dir, name), []byte(content)); err != nil {
			return View{}, err
		}
	}
	if err := SaveManifest(dir, mf); err != nil {
		return View{}, err
	}

	params := DefaultParams(mf)
	for k, v := range spec.Params {
		if strings.TrimSpace(v) != "" {
			params[k] = v
		}
	}
	sealed, err := SealParams(mf, params, m.seal)
	if err != nil {
		return View{}, err
	}
	types, err := ParseFileTypes(mf.FileTypes)
	if err != nil {
		return View{}, err
	}
	now := store.NowUTC()
	err = m.st.Update(func(st *store.State) error {
		st.Plugins = append(st.Plugins, store.Plugin{
			ID: id, Kind: id, Name: mf.Name, Description: mf.Description,
			Version: mf.Version, Author: mf.Author, Enabled: spec.Enabled,
			Priority: mf.Priority, FileTypes: types, Params: sealed,
			Status: store.StatusUnknown, StatusMessage: "新建插件，尚未加载",
			Health: store.HealthConfig{Enabled: true, IntervalSecs: 300, TimeoutSecs: 8, FailThreshold: 2, ProbeMode: "ping"},
			Dir:    dir, ValidityHours: mf.Validity.IntervalHours, Custom: true,
			Revision: 1, CreatedAt: now, UpdatedAt: now,
		})
		st.Changes = append(st.Changes, newChange(id, "create", actor, nil,
			fmt.Sprintf("从模板 %s 创建 Go 插件", tpl.Kind)))
		return nil
	})
	if err != nil {
		return View{}, err
	}

	// 同步加载：创建后即可用；失败保留源码供修改。
	if _, err := m.Build(ctx, id, actor); err != nil {
		view, _ := m.Get(id)
		return view, fmt.Errorf("插件已创建，但加载失败：%w", err)
	}
	return m.Get(id)
}

// UpdateManifest 更新清单（参数 Schema、超时、有效性周期等），不触发重新加载。
func (m *Manager) UpdateManifest(id string, next Manifest, actor string) (View, error) {
	p, _, _, dir, err := m.loadMeta(id)
	if err != nil {
		return View{}, err
	}
	next.ID = p.ID
	next.Schema = ManifestSchema
	if err := next.Validate(); err != nil {
		return View{}, err
	}
	if err := SaveManifest(dir, next); err != nil {
		return View{}, err
	}
	err = m.st.Update(func(st *store.State) error {
		cur := store.PluginByID(st, id)
		if cur == nil {
			return ErrPluginNotFound
		}
		cur.Name, cur.Description, cur.Version = next.Name, next.Description, next.Version
		cur.Priority = next.Priority
		cur.ValidityHours = next.Validity.IntervalHours
		types, _ := ParseFileTypes(next.FileTypes)
		cur.FileTypes = types
		cur.Revision++
		cur.UpdatedAt = store.NowUTC()
		st.Changes = append(st.Changes, newChange(id, "manifest", actor, nil, "更新插件清单"))
		return nil
	})
	if err != nil {
		return View{}, err
	}
	return m.Get(id)
}

// Export 导出全部源码文件，便于备份或迁移到另一台实例。
func (m *Manager) Export(id string) (map[string]string, Manifest, error) {
	p, mf, _, dir, err := m.loadMeta(id)
	if err != nil {
		return nil, Manifest{}, err
	}
	_ = p
	out := map[string]string{}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, mf, err
	}
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		if e.Name() == "go.sum" {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !allowedExt[ext] {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		out[e.Name()] = string(raw)
	}
	return out, mf, nil
}

// ValidateSource 在临时目录加载一份源码，不落库、不覆盖现有插件。
func (m *Manager) ValidateSource(ctx context.Context, files map[string]string, mf Manifest) (*BuildReport, []string, error) {
	if mf.ID == "" {
		mf.ID = "validate"
	}
	mf.Schema = ManifestSchema
	if err := mf.Validate(); err != nil {
		return nil, nil, err
	}
	dir, err := os.MkdirTemp("", "s3bridge-validate-*")
	if err != nil {
		return nil, nil, err
	}
	defer os.RemoveAll(dir)
	names := make([]string, 0, len(files))
	for name, content := range files {
		if err := ValidateFileName(name); err != nil {
			return nil, nil, err
		}
		if systemFiles[name] {
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			return nil, nil, err
		}
		names = append(names, name)
	}
	sort.Strings(names)
	if err := SaveManifest(dir, mf); err != nil {
		return nil, nil, err
	}
	if err := CheckLayout(dir, mf); err != nil {
		return &BuildReport{OK: false, Output: err.Error(), At: store.NowUTC()}, names, nil
	}
	vid := "validate-" + strconv.FormatInt(time.Now().UnixNano(), 36)
	res, buildErr := m.rt.Load(ctx, vid, dir)
	m.rt.Drop(vid)
	report := &BuildReport{OK: buildErr == nil, At: store.NowUTC()}
	if res != nil {
		report.Output, report.DurationMs = res.Output, res.DurationMs
	}
	if buildErr != nil {
		return report, names, nil // 编译失败属于「校验结论」，不是调用错误
	}
	return report, names, nil
}

// ErrNoFiles 表示没有任何源码可加载。
var ErrNoFiles = errors.New("没有可编译的源码")
