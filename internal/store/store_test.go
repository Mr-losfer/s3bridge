package store

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newTestStore(t *testing.T) *JSONStore {
	t.Helper()
	p := filepath.Join(t.TempDir(), "state.json")
	s, err := Open(p)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = s.Close() })
	return s
}

func TestOpenCreatesDefaultState(t *testing.T) {
	s := newTestStore(t)
	if _, err := os.Stat(s.Path()); err != nil {
		t.Fatalf("状态文件未创建: %v", err)
	}
	if err := s.View(func(st *State) error {
		if st.Settings.SiteName != "S3Bridge" {
			t.Fatalf("默认站点名异常: %q", st.Settings.SiteName)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func TestUpdateRollsBackOnCallbackError(t *testing.T) {
	s := newTestStore(t)
	sentinel := errors.New("拒绝写入")
	err := s.Update(func(st *State) error {
		st.Users = append(st.Users, User{ID: "should-not-exist"})
		return sentinel
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("错误应透传，实际 %v", err)
	}
	_ = s.View(func(st *State) error {
		if len(st.Users) != 0 {
			t.Fatal("回调失败后内存状态必须回滚")
		}
		return nil
	})
	// 重新打开，磁盘也不应有半截写入。
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	_ = reopened.View(func(st *State) error {
		if len(st.Users) != 0 {
			t.Fatal("回调失败后磁盘状态必须回滚")
		}
		return nil
	})
}

func TestUpdatePersistsAndIncrementsRevision(t *testing.T) {
	s := newTestStore(t)
	if err := s.Update(func(st *State) error {
		st.Buckets = append(st.Buckets, Bucket{ID: "b1", Name: "photos", CreatedAt: time.Now().UTC()})
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reopened, err := Open(s.Path())
	if err != nil {
		t.Fatal(err)
	}
	_ = reopened.View(func(st *State) error {
		if len(st.Buckets) != 1 || st.Buckets[0].Name != "photos" {
			t.Fatalf("持久化内容异常: %+v", st.Buckets)
		}
		if st.Revision != 1 {
			t.Fatalf("revision = %d, want 1", st.Revision)
		}
		return nil
	})
}

func TestConcurrentUpdates(t *testing.T) {
	s := newTestStore(t)
	const n = 40
	done := make(chan error, n)
	for i := 0; i < n; i++ {
		go func(i int) {
			done <- s.Update(func(st *State) error {
				st.Alerts = append(st.Alerts, Alert{ID: string(rune('a' + i%26))})
				return nil
			})
		}(i)
	}
	for i := 0; i < n; i++ {
		if err := <-done; err != nil {
			t.Fatal(err)
		}
	}
	_ = s.View(func(st *State) error {
		if len(st.Alerts) != n {
			t.Fatalf("并发写入丢失：%d != %d", len(st.Alerts), n)
		}
		return nil
	})
}

func TestRolePermissions(t *testing.T) {
	tests := []struct {
		role    Role
		perm    Permission
		allowed bool
	}{
		{RoleAdmin, PermUserManage, true},
		{RoleAdmin, PermPluginDebug, true},
		{RoleEditor, PermPluginWrite, true},
		{RoleEditor, PermUserManage, false},
		{RoleViewer, PermPluginRead, true},
		{RoleViewer, PermUploadWrite, false},
	}
	for _, tt := range tests {
		t.Run(string(tt.role)+"/"+string(tt.perm), func(t *testing.T) {
			u := &User{Role: tt.role}
			if got := u.Can(tt.perm); got != tt.allowed {
				t.Fatalf("Can = %v, want %v", got, tt.allowed)
			}
		})
	}
}

func TestUserPermissionOverrides(t *testing.T) {
	u := &User{Role: RoleViewer, ExtraPerms: []Permission{PermUploadWrite}}
	if !u.Can(PermUploadWrite) {
		t.Fatal("额外权限应生效")
	}
	u.RevokedPerms = []Permission{PermUploadWrite}
	if u.Can(PermUploadWrite) {
		t.Fatal("撤销权限应优先于额外权限")
	}
	u.Disabled = true
	if u.Can(PermPluginRead) {
		t.Fatal("禁用用户不应有任何权限")
	}
}

func TestTrimKeepsStateBounded(t *testing.T) {
	st := &State{}
	for i := 0; i < maxObjects+10; i++ {
		st.Objects = append(st.Objects, Object{ID: string(rune(i))})
	}
	trim(st)
	if len(st.Objects) != maxObjects {
		t.Fatalf("trim 后对象数 = %d, want %d", len(st.Objects), maxObjects)
	}
}
