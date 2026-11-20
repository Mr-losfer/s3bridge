package api_test

import (
	"encoding/json"
	"fmt"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/api"
	"github.com/Mr-losfer/s3bridge/internal/app"
	"github.com/Mr-losfer/s3bridge/internal/config"
	"github.com/Mr-losfer/s3bridge/internal/store"
)

// TestMonitorAndGallery 覆盖：采集中间件 → 概览指标 → 异常日志 →
// 存储画像与深度体检 → 全站相册分页 → 缩略图生成。
func TestMonitorAndGallery(t *testing.T) {
	dataDir := t.TempDir()
	t.Setenv("S3BRIDGE_DATA_DIR", dataDir)
	t.Setenv("S3BRIDGE_BOOTSTRAP_PASSWORD", "admin-pass-123")
	t.Setenv("S3BRIDGE_MASTER_KEY", "monitor-master-key")
	t.Setenv("S3BRIDGE_ADDR", "127.0.0.1:0")
	cfg, err := config.Load()
	if err != nil {
		t.Fatal(err)
	}
	core, err := app.New(cfg, "test")
	if err != nil {
		t.Fatal(err)
	}
	defer core.Close()

	if core.Obs == nil || core.Thumbs == nil {
		t.Fatal("监控采集或缩略图缓存未初始化")
	}

	// ---- 造 3 张图片对象 + 1 个孤儿文件 ----
	var bucket string
	_ = core.Store.View(func(st *store.State) error {
		if len(st.Buckets) > 0 {
			bucket = st.Buckets[0].Name
		}
		return nil
	})
	if bucket == "" {
		t.Fatal("默认存储桶未创建")
	}
	now := time.Now().UTC()
	makeImage := func(name string, w, h int) (string, int64) {
		img := image.NewRGBA(image.Rect(0, 0, w, h))
		for y := 0; y < h; y++ {
			for x := 0; x < w; x++ {
				img.Set(x, y, color.RGBA{R: uint8(x % 256), G: uint8(y % 256), B: 90, A: 255})
			}
		}
		rel := filepath.Join(bucket, "image", "202611", name)
		abs := filepath.Join(cfg.ObjectDir, rel)
		if err := os.MkdirAll(filepath.Dir(abs), 0o700); err != nil {
			t.Fatal(err)
		}
		f, err := os.Create(abs)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		info, _ := f.Stat()
		_ = f.Close()
		return rel, info.Size()
	}

	objs := []store.Object{}
	for i := 1; i <= 3; i++ {
		rel, size := makeImage(fmt.Sprintf("pic-%d.png", i), 320, 240)
		objs = append(objs, store.Object{
			ID: fmt.Sprintf("obj_test_%d", i), Bucket: bucket,
			ObjectKey: fmt.Sprintf("image/202611/pic-%d.png", i),
			FileName:  fmt.Sprintf("pic-%d.png", i), MimeType: "image/png",
			Category: store.CategoryImage, Size: size,
			SHA256:    fmt.Sprintf("%064d", i),
			LocalPath: rel, CreatedAt: now.Add(time.Duration(i) * time.Second),
		})
	}
	if err := core.Store.Update(func(st *store.State) error {
		st.Objects = append(st.Objects, objs...)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	orphan := filepath.Join(cfg.ObjectDir, bucket, "image", "202611", "orphan.png")
	_ = os.WriteFile(orphan, make([]byte, 64), 0o600)

	srv := httptest.NewServer(api.New(core))
	defer srv.Close()
	client := srv.Client()

	// 未登录请求会被记录成鉴权异常，用来验证事件采集。
	if status, _ := doJSON(t, client, http.MethodGet, srv.URL+"/api/monitor/summary", "", nil); status != http.StatusUnauthorized {
		t.Fatalf("未登录应 401，实际 %d", status)
	}

	status, body := doJSON(t, client, http.MethodPost, srv.URL+"/api/auth/login", "",
		map[string]any{"username": "admin", "password": "admin-pass-123"})
	if status != http.StatusOK {
		t.Fatalf("登录失败: %d %s", status, body)
	}
	var login struct {
		Token string `json:"token"`
	}
	if err := json.Unmarshal([]byte(body), &login); err != nil || login.Token == "" {
		t.Fatalf("登录响应异常: %s", body)
	}
	token := login.Token

	// ---- 概览 ----
	status, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/monitor/summary?range=1h", token, nil)
	if status != http.StatusOK {
		t.Fatalf("监控概览失败: %d %s", status, body)
	}
	var sum struct {
		Summary struct {
			Totals struct {
				Req     int64   `json:"req"`
				Err4    int64   `json:"err4"`
				ErrRate float64 `json:"errRate"`
			} `json:"totals"`
			Series []map[string]any `json:"series"`
			Health struct {
				Goroutines int64 `json:"goroutines"`
			} `json:"health"`
		} `json:"summary"`
		Ranges []map[string]any `json:"ranges"`
	}
	if err := json.Unmarshal([]byte(body), &sum); err != nil {
		t.Fatal(err)
	}
	if sum.Summary.Totals.Req < 1 {
		t.Fatalf("请求计数未采集: %s", body)
	}
	if len(sum.Summary.Series) == 0 {
		t.Fatal("时序点为空")
	}
	if sum.Summary.Health.Goroutines <= 0 {
		t.Fatal("运行时健康信息缺失")
	}
	if len(sum.Ranges) != 5 {
		t.Fatalf("时间范围选项 = %d, want 5", len(sum.Ranges))
	}

	// ---- 异常日志：应包含未登录的 401 ----
	status, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/monitor/events?limit=20", token, nil)
	if status != http.StatusOK {
		t.Fatalf("异常日志失败: %d %s", status, body)
	}
	var ev struct {
		Items []struct {
			Status   int    `json:"status"`
			Category string `json:"category"`
			Route    string `json:"route"`
		} `json:"items"`
		Total int `json:"total"`
	}
	if err := json.Unmarshal([]byte(body), &ev); err != nil {
		t.Fatal(err)
	}
	if ev.Total == 0 {
		t.Fatalf("应至少记录 1 条鉴权异常: %s", body)
	}
	foundAuth := false
	for _, it := range ev.Items {
		if it.Status == 401 && it.Category == "auth" {
			foundAuth = true
		}
		if it.Route == "" {
			t.Fatalf("事件缺少路由模板: %+v", it)
		}
	}
	if !foundAuth {
		t.Fatalf("未找到 401 鉴权事件: %s", body)
	}

	// ---- 存储画像（内存聚合） ----
	status, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/monitor/storage", token, nil)
	if status != http.StatusOK {
		t.Fatalf("存储画像失败: %d %s", status, body)
	}
	var st1 struct {
		Summary struct {
			Objects    int `json:"objects"`
			Categories []struct {
				Key     string `json:"key"`
				Objects int    `json:"objects"`
			} `json:"categories"`
			Disk struct {
				Available bool `json:"available"`
			} `json:"disk"`
		} `json:"summary"`
		Scanning bool `json:"scanning"`
	}
	if err := json.Unmarshal([]byte(body), &st1); err != nil {
		t.Fatal(err)
	}
	if st1.Summary.Objects != 3 {
		t.Fatalf("对象数 = %d, want 3", st1.Summary.Objects)
	}
	if !st1.Summary.Disk.Available {
		t.Fatal("磁盘容量应可读")
	}

	// ---- 深度体检 ----
	status, body = doJSON(t, client, http.MethodPost, srv.URL+"/api/monitor/storage/scan", token, nil)
	if status != http.StatusAccepted {
		t.Fatalf("触发体检失败: %d %s", status, body)
	}
	var deep struct {
		Scanning bool `json:"scanning"`
		Deep     *struct {
			Orphans     []map[string]any `json:"orphans"`
			OrphanBytes int64            `json:"orphanBytes"`
			Missing     []map[string]any `json:"missing"`
			DurationMs  int64            `json:"durationMs"`
		} `json:"deep"`
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		status, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/monitor/storage/scan", token, nil)
		if status != http.StatusOK {
			t.Fatalf("体检状态失败: %d %s", status, body)
		}
		deep = struct {
			Scanning bool `json:"scanning"`
			Deep     *struct {
				Orphans     []map[string]any `json:"orphans"`
				OrphanBytes int64            `json:"orphanBytes"`
				Missing     []map[string]any `json:"missing"`
				DurationMs  int64            `json:"durationMs"`
			} `json:"deep"`
		}{}
		if err := json.Unmarshal([]byte(body), &deep); err != nil {
			t.Fatal(err)
		}
		if !deep.Scanning && deep.Deep != nil {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if deep.Deep == nil {
		t.Fatalf("体检未返回结果: %s", body)
	}
	if len(deep.Deep.Orphans) != 1 || deep.Deep.OrphanBytes != 64 {
		t.Fatalf("孤儿检测 = %+v / %d", deep.Deep.Orphans, deep.Deep.OrphanBytes)
	}
	if len(deep.Deep.Missing) != 0 {
		t.Fatalf("不应有断链: %+v", deep.Deep.Missing)
	}

	// ---- 相册分页 ----
	status, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/gallery?limit=2", token, nil)
	if status != http.StatusOK {
		t.Fatalf("相册失败: %d %s", status, body)
	}
	var page struct {
		Items []struct {
			ID        string `json:"id"`
			FileName  string `json:"fileName"`
			CreatedAt string `json:"createdAt"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
		Total      int    `json:"total"`
		Buckets    []struct {
			Name string `json:"name"`
		} `json:"buckets"`
	}
	if err := json.Unmarshal([]byte(body), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 3 || len(page.Items) != 2 || page.NextCursor == "" {
		t.Fatalf("首屏分页异常: total=%d items=%d cursor=%q", page.Total, len(page.Items), page.NextCursor)
	}
	if page.Items[0].FileName != "pic-3.png" {
		t.Fatalf("应按时间倒序，首条 = %s", page.Items[0].FileName)
	}
	if len(page.Buckets) != 1 || page.Buckets[0].Name != bucket {
		t.Fatalf("桶聚合异常: %+v", page.Buckets)
	}

	status, body = doJSON(t, client, http.MethodGet, srv.URL+"/api/gallery?limit=2&cursor="+page.NextCursor, token, nil)
	if status != http.StatusOK {
		t.Fatalf("第二页失败: %d %s", status, body)
	}
	var page2 struct {
		Items []struct {
			FileName string `json:"fileName"`
		} `json:"items"`
		NextCursor string `json:"nextCursor"`
	}
	if err := json.Unmarshal([]byte(body), &page2); err != nil {
		t.Fatal(err)
	}
	if len(page2.Items) != 1 || page2.Items[0].FileName != "pic-1.png" {
		t.Fatalf("第二页内容异常: %s", body)
	}
	if page2.NextCursor != "" {
		t.Fatalf("最后一页不应有游标: %q", page2.NextCursor)
	}

	// ---- 缩略图 ----
	// 客户端不会自动带上 Bearer，因此显式构造请求。
	req, _ := http.NewRequest(http.MethodGet, fmt.Sprintf("%s/api/objects/thumb?bucket=%s&key=%s&w=160",
		srv.URL, bucket, objs[0].ObjectKey), nil)
	req.Header.Set("Authorization", "Bearer "+token)
	imgResp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer imgResp.Body.Close()
	if imgResp.StatusCode != http.StatusOK {
		t.Fatalf("缩略图失败: %d", imgResp.StatusCode)
	}
	if ct := imgResp.Header.Get("Content-Type"); ct != "image/jpeg" {
		t.Fatalf("缩略图类型 = %s, want image/jpeg", ct)
	}
	if _, err := jpeg.DecodeConfig(imgResp.Body); err != nil {
		t.Fatalf("缩略图不是合法 JPEG: %v", err)
	}
}
