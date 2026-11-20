package obs

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

func TestBuildStorageReport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "bucket-a/image/202601/a.png"), 100)
	writeFile(t, filepath.Join(dir, "bucket-a/image/202601/b.png"), 100)
	writeFile(t, filepath.Join(dir, "bucket-a/doc/202601/c.pdf"), 50)
	// 孤儿文件：磁盘有、元数据无。
	writeFile(t, filepath.Join(dir, "bucket-a/image/202601/lost.png"), 30)
	// 临时上传文件不应计入孤儿。
	writeFile(t, filepath.Join(dir, ".upload-123"), 10)

	buckets := []store.Bucket{
		{ID: "b1", Name: "bucket-a", OwnerID: "u1"},
		{ID: "b2", Name: "bucket-b", OwnerID: "u1"},
	}
	now := time.Now().UTC()
	objects := []store.Object{
		{ID: "o1", Bucket: "bucket-a", ObjectKey: "a.png", FileName: "a.png", Category: store.CategoryImage,
			Size: 100, SHA256: "hash-same", LocalPath: "bucket-a/image/202601/a.png", CreatedAt: now},
		{ID: "o2", Bucket: "bucket-a", ObjectKey: "b.png", FileName: "b.png", Category: store.CategoryImage,
			Size: 100, SHA256: "hash-same", LocalPath: "bucket-a/image/202601/b.png", CreatedAt: now},
		{ID: "o3", Bucket: "bucket-a", ObjectKey: "c.pdf", FileName: "c.pdf", Category: store.CategoryDoc,
			Size: 50, SHA256: "hash-doc", LocalPath: "bucket-a/doc/202601/c.pdf", CreatedAt: now},
		// 断链：元数据存在但文件不在。
		{ID: "o4", Bucket: "bucket-a", ObjectKey: "gone.png", FileName: "gone.png", Category: store.CategoryImage,
			Size: 999, SHA256: "hash-gone", LocalPath: "bucket-a/image/202601/gone.png", CreatedAt: now.Add(-100 * 24 * time.Hour)},
	}

	rep := BuildStorageReport(objects, buckets, AuditOptions{
		ObjectDir: dir, RetentionDays: 90, TopLimit: 5,
	})

	if rep.Objects != 4 || rep.Bytes != 1249 {
		t.Fatalf("总量 = %d objects / %d bytes", rep.Objects, rep.Bytes)
	}
	if len(rep.Buckets) != 2 {
		t.Fatalf("桶数量 = %d, want 2（含空桶也要列出）", len(rep.Buckets))
	}
	if rep.Buckets[0].Name != "bucket-a" || rep.Buckets[0].Objects != 4 {
		t.Fatalf("桶排行 = %+v", rep.Buckets[0])
	}
	if rep.Buckets[0].Health >= 100 {
		t.Fatalf("存在断链时健康度应扣分，得到 %d", rep.Buckets[0].Health)
	}
	if len(rep.Duplicates) != 1 || rep.Duplicates[0].WastedBytes != 100 {
		t.Fatalf("重复组 = %+v", rep.Duplicates)
	}
	if len(rep.Missing) != 1 || rep.Missing[0].Key != "gone.png" {
		t.Fatalf("断链 = %+v", rep.Missing)
	}
	if rep.OrphanScanned != 4 {
		t.Fatalf("扫描文件数 = %d, want 4（临时文件被排除）", rep.OrphanScanned)
	}
	if len(rep.Orphans) != 1 || rep.Orphans[0].Path != "bucket-a/image/202601/lost.png" {
		t.Fatalf("孤儿文件 = %+v", rep.Orphans)
	}
	if rep.OrphanBytes != 30 {
		t.Fatalf("孤儿体积 = %d, want 30", rep.OrphanBytes)
	}
	if len(rep.Stale) != 1 {
		t.Fatalf("超期对象 = %+v", rep.Stale)
	}
	if len(rep.TopObjects) == 0 || rep.TopObjects[0].Size != 999 {
		t.Fatalf("Top 大文件 = %+v", rep.TopObjects)
	}
	var imageStat *CategoryStat
	for i := range rep.Categories {
		if rep.Categories[i].Key == string(store.CategoryImage) {
			imageStat = &rep.Categories[i]
		}
	}
	if imageStat == nil || imageStat.Objects != 3 || imageStat.Bytes != 1199 {
		t.Fatalf("图片类型统计 = %+v", imageStat)
	}
}

func TestAuditorSingleFlight(t *testing.T) {
	a := NewAuditor()
	block := make(chan struct{})
	if !a.Start(func() *StorageReport { <-block; return &StorageReport{Objects: 1} }) {
		t.Fatal("首次启动应成功")
	}
	if a.Start(func() *StorageReport { return nil }) {
		t.Fatal("并发启动应被拒绝")
	}
	close(block)
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if running, rep, _, _, _ := a.State(); !running && rep != nil {
			if rep.Objects != 1 {
				t.Fatalf("报告内容错误: %+v", rep)
			}
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatal("体检任务未在预期时间内完成")
}

func writeFile(t *testing.T, path string, size int) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, make([]byte, size), 0o600); err != nil {
		t.Fatal(err)
	}
}
