package plugin

import (
	"errors"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// 权重决定调度顺序，文件类型限定承接范围：先过滤类型，再按权重降序，
// 同权重时更健康的优先；全部异常时降级到权重最高的已启用插件。
func TestSelectByFileTypeAndPriority(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	mk := func(id string, priority int, enabled bool, status store.PluginStatus, types ...store.FileCategory) store.Plugin {
		return store.Plugin{
			ID: id, Kind: id, Name: id, Enabled: enabled, Priority: priority,
			FileTypes: types, Status: status, Params: map[string]string{},
		}
	}
	if err := st.Update(func(s *store.State) error {
		s.Plugins = []store.Plugin{
			mk("img-hi", 200, true, store.StatusHealthy, store.CategoryImage),
			mk("img-lo", 50, true, store.StatusHealthy, store.CategoryImage),
			mk("doc", 900, true, store.StatusHealthy, store.CategoryDoc),
			mk("all", 100, true, store.StatusUnknown),
			mk("off", 999, false, store.StatusHealthy),
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, NewRegistry(),
		func(s string) (string, error) { return s, nil },
		func(s string) (string, error) { return s, nil },
		time.Minute)

	cases := []struct {
		name     string
		prefer   string
		category store.FileCategory
		want     string
		wantErr  error
	}{
		{"图片取权重最高", "", store.CategoryImage, "img-hi", nil},
		{"文档路由到专用插件", "", store.CategoryDoc, "doc", nil},
		{"无人认领时回落到全部类型插件", "", store.CategoryAudio, "all", nil},
		{"权重最高的插件被停用则跳过", "", store.CategoryArchive, "all", nil},
		{"显式指定且类型匹配", "img-lo", store.CategoryImage, "img-lo", nil},
		{"显式指定但类型不匹配", "img-lo", store.CategoryDoc, "", ErrFileTypeUnsupported},
		{"显式指定停用插件", "off", store.CategoryImage, "", ErrPluginDisabled},
		{"显式指定不存在插件", "ghost", store.CategoryImage, "", ErrPluginNotFound},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := m.Select(tc.prefer, tc.category)
			if tc.wantErr != nil {
				if !errors.Is(err, tc.wantErr) {
					t.Fatalf("期望错误 %v，实际 %v（id=%q）", tc.wantErr, err, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("不应报错: %v", err)
			}
			if got != tc.want {
				t.Fatalf("选中插件 = %q，期望 %q", got, tc.want)
			}
		})
	}
}

func TestSelectPrefersHealthyOnTie(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(s *store.State) error {
		s.Plugins = []store.Plugin{
			{ID: "degraded", Kind: "degraded", Name: "degraded", Enabled: true, Priority: 100, Status: store.StatusDegraded},
			{ID: "healthy", Kind: "healthy", Name: "healthy", Enabled: true, Priority: 100, Status: store.StatusHealthy},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, NewRegistry(),
		func(s string) (string, error) { return s, nil },
		func(s string) (string, error) { return s, nil },
		time.Minute)
	got, err := m.Select("", store.CategoryImage)
	if err != nil {
		t.Fatal(err)
	}
	if got != "healthy" {
		t.Fatalf("同权重应选健康插件，实际 %q", got)
	}
}

func TestSelectFallsBackWhenAllUnhealthy(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	if err := st.Update(func(s *store.State) error {
		s.Plugins = []store.Plugin{
			{ID: "bad-lo", Kind: "bad-lo", Name: "bad-lo", Enabled: true, Priority: 10, Status: store.StatusUnhealthy},
			{ID: "bad-hi", Kind: "bad-hi", Name: "bad-hi", Enabled: true, Priority: 500, Status: store.StatusUnhealthy},
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	m := NewManager(st, NewRegistry(),
		func(s string) (string, error) { return s, nil },
		func(s string) (string, error) { return s, nil },
		time.Minute)
	got, err := m.Select("", store.CategoryImage)
	if err != nil {
		t.Fatal(err)
	}
	if got != "bad-hi" {
		t.Fatalf("全部异常时应降级到权重最高者，实际 %q", got)
	}
}

func TestParseFileTypes(t *testing.T) {
	if got, err := ParseFileTypes(nil); err != nil || got != nil {
		t.Fatalf("空列表应表示全部类型: %v %v", got, err)
	}
	got, err := ParseFileTypes([]string{"IMAGE", "archive", "image"})
	if err != nil {
		t.Fatal(err)
	}
	want := []store.FileCategory{store.CategoryImage, store.CategoryArchive}
	if len(got) != 2 || got[0] != want[0] || got[1] != want[1] {
		t.Fatalf("类型归一化错误: %v", got)
	}
	if _, err := ParseFileTypes([]string{"exe"}); err == nil {
		t.Fatal("未知类型应报错")
	}
	if got, err := ParseFileTypes([]string{"*"}); err != nil || got != nil {
		t.Fatalf("* 应表示全部类型: %v %v", got, err)
	}
}

func TestDefinitionFileTypes(t *testing.T) {
	d := testDefinition("https://api.example.com")
	d.FileTypes = []string{"image", "video"}
	if err := d.Validate(); err != nil {
		t.Fatal(err)
	}
	if mf := d.ToManifest(); len(mf.FileTypes) != 2 || mf.FileTypes[0] != "image" {
		t.Fatalf("Manifest 未携带文件类型: %v", mf.FileTypes)
	}
	d.FileTypes = []string{"bogus"}
	if err := d.Validate(); err == nil {
		t.Fatal("非法文件类型应报错")
	}
}
