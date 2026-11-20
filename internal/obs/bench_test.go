package obs

import (
	"fmt"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

func newBenchCollector(b *testing.B) *Collector {
	b.Helper()
	return &Collector{
		dir:       b.TempDir(),
		log:       mustEventLogB(b),
		m1:        newSeries(60, 720),
		m5:        newSeries(300, 2016),
		m1h:       newSeries(3600, 720),
		startedAt: time.Now(),
		stop:      make(chan struct{}),
	}
}

// BenchmarkRecord 是采集热路径的开销基准：每个请求写入三条时间线。
func BenchmarkRecord(b *testing.B) {
	c := newBenchCollector(b)
	rec := Record{
		Method: "GET", Path: "/api/plugins/abc", Route: "/api/plugins/{id}",
		Status: 200, Latency: 12 * time.Millisecond, BytesOut: 1024,
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		c.Record(rec)
	}
}

// BenchmarkRecordParallel 模拟并发请求下的原子累加与路由聚合锁竞争。
func BenchmarkRecordParallel(b *testing.B) {
	c := newBenchCollector(b)
	rec := Record{
		Method: "GET", Path: "/api/uploads", Route: "/api/uploads",
		Status: 200, Latency: 5 * time.Millisecond,
	}
	b.ReportAllocs()
	b.ResetTimer()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Record(rec)
		}
	})
}

// BenchmarkSummary 覆盖前端每 20 秒一次的概览查询。
func BenchmarkSummary(b *testing.B) {
	c := newBenchCollector(b)
	for i := 0; i < 3000; i++ {
		c.Record(Record{Status: 200, Route: "/api/uploads", Latency: 8 * time.Millisecond, BytesOut: 2048})
	}
	rg := ParseRange("24h")
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = c.Summary(rg, time.Now())
	}
}

// BenchmarkBuildStorageReport 覆盖存储画像的内存聚合（不含磁盘扫描）。
func BenchmarkBuildStorageReport(b *testing.B) {
	objs, buckets := syntheticObjects(20000)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_ = BuildStorageReport(objs, buckets, AuditOptions{TopLimit: 20})
	}
}

func syntheticObjects(n int) ([]store.Object, []store.Bucket) {
	buckets := []store.Bucket{{Name: "bucket-a"}, {Name: "bucket-b", PublicRead: true}}
	now := time.Now().UTC()
	objs := make([]store.Object, n)
	for i := 0; i < n; i++ {
		objs[i] = store.Object{
			ID:        fmt.Sprintf("obj_%08d", i),
			Bucket:    buckets[i%len(buckets)].Name,
			ObjectKey: fmt.Sprintf("image/202611/%08d.png", i),
			FileName:  fmt.Sprintf("pic-%08d.png", i),
			MimeType:  "image/png",
			Category:  store.CategoryImage,
			Size:      int64(20_000 + i%800_000),
			SHA256:    fmt.Sprintf("%064d", i%15000),
			LocalPath: fmt.Sprintf("bucket-a/image/202611/%08d.png", i),
			CreatedAt: now.Add(-time.Duration(i) * time.Minute),
		}
	}
	return objs, buckets
}

func mustEventLogB(b *testing.B) *EventLog {
	b.Helper()
	log, err := OpenEventLog(b.TempDir())
	if err != nil {
		b.Fatal(err)
	}
	b.Cleanup(func() { _ = log.Close() })
	return log
}
