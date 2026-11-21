package plugin

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// 源码体积上限：防止误贴几 MB 的生成文件把数据目录撑爆。
const (
	MaxSourceFileBytes = 512 << 10
	MaxSourceTotal     = 2 << 20
	MaxSourceFiles     = 64
)

// 只允许文本源码与说明文件；二进制、脚本一律拒绝。
var allowedExt = map[string]bool{
	".go": true, ".md": true, ".txt": true, ".json": true,
	".yaml": true, ".yml": true, ".mod": true, ".sum": true,
}

// 这些文件由系统维护，不允许通过文件接口改写。
var systemFiles = map[string]bool{"plugin.json": true, "go.mod": true, "go.sum": true}

type FileInfo struct {
	Name     string    `json:"name"`
	Size     int64     `json:"size"`
	ModTime  time.Time `json:"modTime"`
	Entry    bool      `json:"entry"`
	System   bool      `json:"system"`   // 系统维护，只读
	Editable bool      `json:"editable"` // 可在后台编辑
	Go       bool      `json:"go"`
}

// ValidateFileName 拒绝路径穿越与不可编辑的扩展名。
func ValidateFileName(name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return fmt.Errorf("%w: 文件名不能为空", ErrSourceInvalid)
	}
	if len(name) > 64 {
		return fmt.Errorf("%w: 文件名过长", ErrSourceInvalid)
	}
	if strings.ContainsAny(name, `/\`) || strings.Contains(name, "..") || strings.HasPrefix(name, ".") {
		return fmt.Errorf("%w: 文件名不能包含路径分隔符或以点开头", ErrSourceInvalid)
	}
	ext := strings.ToLower(filepath.Ext(name))
	if !allowedExt[ext] {
		return fmt.Errorf("%w: 不支持的文件类型 %s（允许 .go/.md/.txt/.json/.yaml）", ErrSourceInvalid, ext)
	}
	return nil
}

// ListFiles 返回文件夹内的源码文件（不含 .build 与临时文件）。
func ListFiles(dir string, m Manifest) ([]FileInfo, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	out := make([]FileInfo, 0, len(entries))
	for _, e := range entries {
		if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		ext := strings.ToLower(filepath.Ext(e.Name()))
		if !allowedExt[ext] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		out = append(out, FileInfo{
			Name: e.Name(), Size: info.Size(), ModTime: info.ModTime().UTC(),
			Entry: e.Name() == m.Entry, System: systemFiles[e.Name()],
			Editable: !systemFiles[e.Name()], Go: ext == ".go",
		})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Go != out[j].Go {
			return out[i].Go // Go 文件排前面
		}
		return out[i].Name < out[j].Name
	})
	return out, nil
}

func ReadSourceFile(dir, name string) (string, error) {
	if err := ValidateFileName(name); err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	info, err := os.Stat(path)
	if err != nil {
		return "", fmt.Errorf("文件不存在: %s", name)
	}
	if info.IsDir() {
		return "", fmt.Errorf("不是文件: %s", name)
	}
	if info.Size() > MaxSourceFileBytes {
		return "", fmt.Errorf("%w: 文件超过 %d 字节", ErrSourceInvalid, MaxSourceFileBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

// WriteSourceFile 写入单个文件，并检查总体积与数量上限。
func WriteSourceFile(dir, name, content string) error {
	if err := ValidateFileName(name); err != nil {
		return err
	}
	if systemFiles[name] {
		return fmt.Errorf("%w: %s 由系统维护，不能直接改写", ErrSourceInvalid, name)
	}
	if len(content) > MaxSourceFileBytes {
		return fmt.Errorf("%w: 单文件不能超过 %d KB", ErrSourceInvalid, MaxSourceFileBytes>>10)
	}
	total, count, err := sourceUsage(dir)
	if err != nil {
		return err
	}
	if _, statErr := os.Stat(filepath.Join(dir, name)); statErr != nil {
		count++
	}
	if count > MaxSourceFiles {
		return fmt.Errorf("%w: 最多 %d 个文件", ErrSourceInvalid, MaxSourceFiles)
	}
	old := int64(0)
	if fi, err := os.Stat(filepath.Join(dir, name)); err == nil {
		old = fi.Size()
	}
	if total-int64(old)+int64(len(content)) > MaxSourceTotal {
		return fmt.Errorf("%w: 插件源码总大小不能超过 %d KB", ErrSourceInvalid, MaxSourceTotal>>10)
	}
	return writeFileAtomic(filepath.Join(dir, name), []byte(content))
}

func DeleteSourceFile(dir, name string) error {
	if err := ValidateFileName(name); err != nil {
		return err
	}
	if systemFiles[name] {
		return fmt.Errorf("%w: %s 由系统维护，不能删除", ErrSourceInvalid, name)
	}
	if strings.HasSuffix(name, ".go") {
		if n, err := countGoFiles(dir); err == nil && n <= 1 {
			return fmt.Errorf("%w: 至少保留一个 .go 文件", ErrSourceInvalid)
		}
	}
	return os.Remove(filepath.Join(dir, name))
}

func RenameSourceFile(dir, from, to string) error {
	if err := ValidateFileName(from); err != nil {
		return err
	}
	if err := ValidateFileName(to); err != nil {
		return err
	}
	if systemFiles[from] || systemFiles[to] {
		return fmt.Errorf("%w: 系统文件不能重命名", ErrSourceInvalid)
	}
	if _, err := os.Stat(filepath.Join(dir, to)); err == nil {
		return fmt.Errorf("%w: 目标文件已存在", ErrSourceInvalid)
	}
	return os.Rename(filepath.Join(dir, from), filepath.Join(dir, to))
}

func sourceUsage(dir string) (int64, int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	var total int64
	var count int
	for _, e := range entries {
		if e.IsDir() || !allowedExt[strings.ToLower(filepath.Ext(e.Name()))] {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		total += info.Size()
		count++
	}
	return total, count, nil
}

func countGoFiles(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			n++
		}
	}
	return n, nil
}

// EnsureGoMod 为插件生成独立模块，并把 SDK 指向主服务落盘的目录。
// 已存在且内容一致时不重写，避免无谓的重新编译。
func EnsureGoMod(dir, id, sdkDir string) error {
	desired := fmt.Sprintf(`module s3bridge.plugin/%s

go 1.21

require github.com/Mr-losfer/s3bridge/sdk v0.0.0

replace github.com/Mr-losfer/s3bridge/sdk => %s
`, id, filepath.ToSlash(sdkDir))
	path := filepath.Join(dir, "go.mod")
	if raw, err := os.ReadFile(path); err == nil && string(raw) == desired {
		return nil
	}
	return writeFileAtomic(path, []byte(desired))
}

// CheckLayout 校验文件夹可编译：至少一个 .go 文件、入口存在。
func CheckLayout(dir string, m Manifest) error {
	files, err := ListFiles(dir, m)
	if err != nil {
		return err
	}
	goFiles := 0
	hasEntry := false
	for _, f := range files {
		if f.Go {
			goFiles++
		}
		if f.Name == m.Entry {
			hasEntry = true
		}
	}
	if goFiles == 0 {
		return fmt.Errorf("%w: 至少需要一个 .go 文件", ErrSourceInvalid)
	}
	if !hasEntry {
		return fmt.Errorf("%w: 入口文件 %s 不存在", ErrSourceInvalid, m.Entry)
	}
	return nil
}

// ---------- 模板 ----------

const blankMain = `package main

import (
	"context"
	"fmt"

	"github.com/Mr-losfer/s3bridge/sdk"
)

// plugin 是插件实现。你可以拆成多个文件、多个类型，甚至引入第三方库。
type plugin struct{}

func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
	// 文件已落盘，可直接按 in.Path 打开；也可以发起任意次 HTTP 请求。
	return nil, fmt.Errorf("尚未实现 Upload")
}

func (plugin) Check(ctx context.Context, cfg sdk.Config) error {
	return nil
}

func main() { sdk.Serve(plugin{}) }
`

const httpMain = `package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"time"

	"github.com/Mr-losfer/s3bridge/sdk"
)

type plugin struct{}

// Upload 以 multipart 把文件转发到上游，并把返回的 URL 转成标准输出。
// 需要更多步骤（换 token、加密、PUT 直传）时，直接用 sdk.Client 继续发请求。
func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
	f, err := os.Open(in.Path)
	if err != nil {
		return nil, fmt.Errorf("打开文件: %w", err)
	}
	defer f.Close()

	body, contentType := sdk.MultipartStream(cfg["file_field"], in.FileName, in.MimeType, f, nil)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, cfg["upload_url"], body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	if token := cfg["token"]; token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	client := sdk.NewClient(60 * time.Second)
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用上游: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("上游返回 %d", resp.StatusCode)
	}

	var out struct {
		URL string ` + "`json:\"url\"`" + `
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("解析响应: %w", err)
	}
	return &sdk.Output{
		URL: out.URL, ObjectKey: in.ObjectKey, FileName: in.FileName,
		MimeType: in.MimeType, FileSize: in.Size,
	}, nil
}

func (plugin) Check(ctx context.Context, cfg sdk.Config) error {
	client := sdk.NewClient(10 * time.Second)
	_, err := client.Bytes(ctx, http.MethodHead, cfg["upload_url"], nil, nil)
	return err
}

func main() { sdk.Serve(plugin{}) }
`

// BuiltinTemplates 返回可派生的模板列表。
func BuiltinTemplates() []Template {
	return []Template{
		{
			Kind: "blank", Name: "空白 Go 插件",
			Description: "只有入口骨架，适合从零实现任意协议。",
			Files:       map[string]string{"main.go": blankMain},
			Params: []ParamField{
				{Key: "note", Label: "备注", Type: "text", Group: "通用"},
			},
		},
		{
			Kind: "http-multipart", Name: "HTTP 表单上传",
			Description: "把文件以 multipart 转发到任意 HTTP 接口，读取 JSON 中的 url。",
			FileTypes:   []string{"image"},
			Params: []ParamField{
				{Key: "upload_url", Label: "上传地址", Type: "url", Group: "连接", Required: true},
				{Key: "file_field", Label: "文件字段名", Type: "text", Group: "连接", Required: true, Default: "file"},
				{Key: "token", Label: "Bearer Token", Type: "secret", Group: "身份凭据", Sensitive: true, Volatile: true, ExpiresField: "token_expires_at"},
				{Key: "token_expires_at", Label: "Token 过期时间", Type: "datetime", Group: "身份凭据", Volatile: true},
			},
			Files: map[string]string{"main.go": httpMain},
		},
	}
}

// TemplateByKind 查找模板。
func TemplateByKind(kind string) (Template, bool) {
	for _, t := range BuiltinTemplates() {
		if t.Kind == kind {
			return t, true
		}
	}
	return Template{}, false
}

// ErrNotEditable 表示内置插件源码只读。
var ErrNotEditable = errors.New("内置插件源码只读，请复制为新插件后再修改")
