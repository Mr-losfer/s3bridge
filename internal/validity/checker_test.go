package validity

import (
	"context"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

func newStore(t *testing.T) *store.JSONStore {
	t.Helper()
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })
	return st
}

func seedImage(t *testing.T, st *store.JSONStore, id, pluginID string, checked *time.Time, fail int) {
	t.Helper()
	err := st.Update(func(s *store.State) error {
		s.Objects = append(s.Objects, store.Object{
			ID: id, Bucket: "default", ObjectKey: "images/" + id + ".png",
			FileName: id + ".png", MimeType: "image/png", Category: store.CategoryImage,
			Provider: pluginID, PluginID: pluginID, LocalPath: "default/images/" + id + ".png",
			Validity: store.ObjectValidity{CheckedAt: checked, FailCount: fail},
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func seedObject(t *testing.T, st *store.JSONStore, obj store.Object) {
	t.Helper()
	if err := st.Update(func(s *store.State) error {
		s.Objects = append(s.Objects, obj)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
}

func findObj(t *testing.T, st *store.JSONStore, id string) store.Object {
	t.Helper()
	var out store.Object
	_ = st.View(func(s *store.State) error {
		for _, o := range s.Objects {
			if o.ID == id {
				out = o
			}
		}
		return nil
	})
	return out
}

// TestDueObjects 覆盖到期筛选：只挑本站图片，支持插件覆盖周期，从未检查的优先。
func TestDueObjects(t *testing.T) {
	now := time.Now()
	old := now.Add(-100 * time.Hour)
	recent := now.Add(-1 * time.Hour)
	oldPtr, recentPtr := old, recent

	cases := []struct {
		name    string
		objects []store.Object
		plugins []store.Plugin
		want    []string
	}{
		{
			name: "从未检查过的图片到期",
			objects: []store.Object{{
				ID: "a", Category: store.CategoryImage, LocalPath: "a.png", PluginID: "workbuddy",
			}},
			want: []string{"a"},
		},
		{
			name: "非图片对象永不检查",
			objects: []store.Object{{
				ID: "doc", Category: store.CategoryDoc, LocalPath: "d.pdf", PluginID: "workbuddy",
			}},
			want: nil,
		},
		{
			name: "没有本地归档（只存远端）的图片不检查",
			objects: []store.Object{{
				ID: "remote", Category: store.CategoryImage, PluginID: "workbuddy",
			}},
			want: nil,
		},
		{
			name: "未到全局间隔则跳过",
			objects: []store.Object{{
				ID: "fresh", Category: store.CategoryImage, LocalPath: "f.png",
				Validity: store.ObjectValidity{CheckedAt: &recentPtr},
			}},
			want: nil,
		},
		{
			name: "超过全局间隔则到期",
			objects: []store.Object{{
				ID: "stale", Category: store.CategoryImage, LocalPath: "s.png",
				Validity: store.ObjectValidity{CheckedAt: &oldPtr},
			}},
			want: []string{"stale"},
		},
		{
			name: "插件清单可缩短巡检周期",
			objects: []store.Object{{
				ID: "custom", Category: store.CategoryImage, LocalPath: "c.png", PluginID: "fast",
				Validity: store.ObjectValidity{CheckedAt: &recentPtr},
			}},
			plugins: []store.Plugin{{ID: "fast", ValidityHours: 1}},
			want:    []string{"custom"},
		},
		{
			name: "从未检查的排在已检查之前",
			objects: []store.Object{
				{ID: "checked", Category: store.CategoryImage, LocalPath: "1.png", Validity: store.ObjectValidity{CheckedAt: &oldPtr}},
				{ID: "never", Category: store.CategoryImage, LocalPath: "2.png"},
			},
			want: []string{"never", "checked"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			st := newStore(t)
			for _, o := range tc.objects {
				seedObject(t, st, o)
			}
			if len(tc.plugins) > 0 {
				if err := st.Update(func(s *store.State) error { s.Plugins = tc.plugins; return nil }); err != nil {
					t.Fatal(err)
				}
			}
			c := New(st, Options{Enabled: true, Interval: 72 * time.Hour, BaseURL: "http://127.0.0.1:1"})
			got := c.dueObjects(72*time.Hour, 10)
			ids := make([]string, 0, len(got))
			for _, o := range got {
				ids = append(ids, o.ID)
			}
			if strings.Join(ids, ",") != strings.Join(tc.want, ",") {
				t.Fatalf("到期对象不符：got=%v want=%v", ids, tc.want)
			}
		})
	}
}

// TestRunOnceMarksValidity 覆盖探测判定与失败阈值：只读响应头、不下载正文。
func TestRunOnceMarksValidity(t *testing.T) {
	var (
		headCalls int
		getCalls  int
	)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.URL.Query().Get("key")
		switch {
		case r.Method == http.MethodHead:
			headCalls++
			switch {
			case strings.Contains(key, "nohead"):
				// 明确不支持 HEAD，逼迫巡检回退到带 Range 的 GET。
				w.WriteHeader(http.StatusMethodNotAllowed)
			case strings.Contains(key, "gone"):
				w.WriteHeader(http.StatusNotFound)
			case strings.Contains(key, "html"):
				w.Header().Set("Content-Type", "text/html")
				w.WriteHeader(http.StatusOK)
			default:
				w.Header().Set("Content-Type", "image/png")
				w.Header().Set("Content-Length", "1048576")
				w.WriteHeader(http.StatusOK)
			}
		case r.Method == http.MethodGet:
			getCalls++
			// 巡检若真去下载整图，就不会带 Range 头。
			if r.Header.Get("Range") == "" {
				t.Error("回退 GET 必须带 Range，避免下载整图")
			}
			w.Header().Set("Content-Type", "image/jpeg")
			_, _ = w.Write(make([]byte, 4096))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer srv.Close()

	st := newStore(t)
	seedImage(t, st, "ok", "workbuddy", nil, 0)
	seedImage(t, st, "gone", "workbuddy", nil, 0)
	seedImage(t, st, "html", "workbuddy", nil, 0)
	// HEAD 不支持 → 回退 Range GET 的插件对象
	seedObject(t, st, store.Object{
		ID: "nohead", Bucket: "default", ObjectKey: "images/nohead.png", FileName: "nohead.png",
		MimeType: "image/png", Category: store.CategoryImage, PluginID: "legacy",
		LocalPath: "default/images/nohead.png",
	})
	c := New(st, Options{Enabled: true, BaseURL: srv.URL, FailThreshold: 2, Workers: 2})
	c.checkOne(context.Background(), findObj(t, st, "ok"), 2)
	c.checkOne(context.Background(), findObj(t, st, "html"), 2)
	c.checkOne(context.Background(), findObj(t, st, "gone"), 2)
	c.checkOne(context.Background(), findObj(t, st, "nohead"), 2)

	if got := findObj(t, st, "ok").Validity; got.Status != "ok" || got.FailCount != 0 || got.HTTPStatus != http.StatusOK {
		t.Fatalf("正常图片判定错误: %+v", got)
	}
	if got := findObj(t, st, "html").Validity; got.Status == "ok" {
		t.Fatalf("非图片 Content-Type 不应判定为正常: %+v", got)
	}
	if got := findObj(t, st, "gone").Validity; got.Status == "ok" || got.HTTPStatus != http.StatusNotFound {
		t.Fatalf("404 不应判定为正常: %+v", got)
	}
	if got := findObj(t, st, "nohead").Validity; got.Status != "ok" || got.ContentType == "" {
		t.Fatalf("HEAD 405 回退 GET 失败: %+v", got)
	}
	if getCalls != 1 || headCalls < 3 {
		t.Fatalf("请求方式不符: head=%d get=%d", headCalls, getCalls)
	}
}

// TestFailThresholdAndAlert 验证连续失败达阈值才标记失效，且同一插件只产生一条未确认告警。
func TestFailThresholdAndAlert(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	st := newStore(t)
	seedImage(t, st, "a", "workbuddy", nil, 0)
	seedImage(t, st, "b", "workbuddy", nil, 0)
	c := New(st, Options{Enabled: true, BaseURL: srv.URL, FailThreshold: 2})

	c.checkOne(context.Background(), findObj(t, st, "a"), 2)
	if got := findObj(t, st, "a").Validity; got.Status != "unknown" || got.FailCount != 1 {
		t.Fatalf("首次失败应为待观察而非失效: %+v", got)
	}
	c.checkOne(context.Background(), findObj(t, st, "a"), 2)
	if got := findObj(t, st, "a").Validity; got.Status != "invalid" || got.FailCount != 2 {
		t.Fatalf("达到阈值应标记失效: %+v", got)
	}
	c.checkOne(context.Background(), findObj(t, st, "b"), 2)
	c.checkOne(context.Background(), findObj(t, st, "b"), 2)

	_ = st.View(func(s *store.State) error {
		var alerts int
		for _, a := range s.Alerts {
			if strings.HasPrefix(a.Message, "图片失效") {
				alerts++
			}
		}
		if alerts != 1 {
			t.Fatalf("同一插件应只产生一条未确认告警，实际 %d", alerts)
		}
		return nil
	})

	// 恢复后应清零失败次数。
	srv2 := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
	}))
	defer srv2.Close()
	c.opt.BaseURL = srv2.URL
	c.checkOne(context.Background(), findObj(t, st, "a"), 2)
	if got := findObj(t, st, "a").Validity; got.Status != "ok" || got.FailCount != 0 {
		t.Fatalf("恢复后应清零: %+v", got)
	}
}

// TestRunOnceDisabled 验证关闭开关后不产生任何请求。
func TestRunOnceDisabled(t *testing.T) {
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++ }))
	defer srv.Close()

	st := newStore(t)
	seedImage(t, st, "a", "workbuddy", nil, 0)
	c := New(st, Options{Enabled: false, BaseURL: srv.URL})
	c.RunOnce(context.Background())
	if calls != 0 {
		t.Fatalf("关闭巡检后不应发请求，实际 %d", calls)
	}

	// 运行中打开开关应立即生效（每分钟的唤醒循环读取实时配置）。
	c.Configure(true, time.Hour, 3)
	c.RunOnce(context.Background())
	if calls != 1 {
		t.Fatalf("打开开关后应巡检，实际 %d", calls)
	}
}

func TestProbeSignature(t *testing.T) {
	secret := []byte("probe-secret")
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	sig := Sign(secret, "default", "a/b.png", ts)

	cases := []struct {
		name        string
		secret      []byte
		bucket, key string
		ts, sig     string
		want        bool
	}{
		{"正确签名", secret, "default", "a/b.png", ts, sig, true},
		{"大小写无关", secret, "default", "a/b.png", ts, strings.ToUpper(sig), true},
		{"密钥不符", []byte("other"), "default", "a/b.png", ts, sig, false},
		{"对象被篡改", secret, "default", "a/c.png", ts, sig, false},
		{"时间戳过期", secret, "default", "a/b.png", strconv.FormatInt(time.Now().Add(-5*time.Minute).Unix(), 10), sig, false},
		{"时间戳非法", secret, "default", "a/b.png", "abc", sig, false},
		{"签名为空", secret, "default", "a/b.png", ts, "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := Verify(tc.secret, tc.bucket, tc.key, tc.ts, tc.sig); got != tc.want {
				t.Fatalf("Verify=%v want=%v", got, tc.want)
			}
		})
	}
}

func TestIsLoopback(t *testing.T) {
	cases := map[string]bool{
		"127.0.0.1:1234": true,
		"[::1]:1234":     true,
		"10.1.2.3:1234":  false,
		"192.168.1.5:80": false,
		"127.0.0.1":      true,
		"not-an-ip:1234": false,
		"":               false,
	}
	for in, want := range cases {
		if got := IsLoopback(in); got != want {
			t.Fatalf("IsLoopback(%q)=%v want=%v", in, got, want)
		}
	}
}

func TestLoopbackBase(t *testing.T) {
	cases := map[string]string{
		":8080":           "http://127.0.0.1:8080",
		"0.0.0.0:9000":    "http://127.0.0.1:9000",
		"10.0.0.5:8080":   "http://127.0.0.1:8080",
		"127.0.0.1:18080": "http://127.0.0.1:18080",
		"":                "",
		"nonsense":        "",
	}
	for in, want := range cases {
		if got := LoopbackBase(in); got != want {
			t.Fatalf("LoopbackBase(%q)=%q want=%q", in, got, want)
		}
	}
}
