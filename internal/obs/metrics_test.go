package obs

import (
	"testing"
	"time"
)

func TestSeriesRecordAndSummary(t *testing.T) {
	c := &Collector{
		log:       mustEventLog(t),
		dir:       t.TempDir(),
		m1:        newSeries(60, 720),
		m5:        newSeries(300, 2016),
		m1h:       newSeries(3600, 720),
		startedAt: time.Now(),
		stop:      make(chan struct{}),
	}
	defer c.log.Close()

	c.Record(Record{Status: 200, Latency: 12 * time.Millisecond, Route: "/api/plugins", BytesOut: 100})
	c.Record(Record{Status: 201, Latency: 30 * time.Millisecond, Route: "/api/upload", Upload: true, BytesIn: 1024})
	c.Record(Record{Status: 500, Latency: 1500 * time.Millisecond, Route: "/api/plugins"})
	c.Record(Record{Status: 404, Latency: 2 * time.Millisecond, Route: "/api/plugins/{id}"})

	sum := c.Summary(Range{Key: "1h", Secs: 3600}, time.Now())
	if sum.Totals.Req != 4 {
		t.Fatalf("请求数 = %d, want 4", sum.Totals.Req)
	}
	if sum.Totals.Ok2 != 2 || sum.Totals.Err4 != 1 || sum.Totals.Err5 != 1 {
		t.Fatalf("状态分层错误: ok2=%d err4=%d err5=%d", sum.Totals.Ok2, sum.Totals.Err4, sum.Totals.Err5)
	}
	if sum.Totals.Upload != 1 || sum.Totals.BytesIn != 1024 {
		t.Fatalf("上传统计错误: upload=%d bytesIn=%d", sum.Totals.Upload, sum.Totals.BytesIn)
	}
	if sum.Totals.ErrRate <= 0.49 || sum.Totals.ErrRate >= 0.51 {
		t.Fatalf("错误率 = %v, want 0.5", sum.Totals.ErrRate)
	}
	if sum.Totals.P95 < 1000 {
		t.Fatalf("P95 = %v, 期望落在慢请求桶", sum.Totals.P95)
	}
	if len(sum.Series) == 0 {
		t.Fatal("时序点为空")
	}
	if len(sum.Slowest) == 0 || sum.Slowest[0].Route != "/api/plugins" {
		t.Fatalf("最慢接口 = %+v, want /api/plugins", sum.Slowest)
	}
	if len(sum.ErrRoutes) == 0 || sum.ErrRoutes[0].Errors != 1 {
		t.Fatalf("错误接口排行 = %+v", sum.ErrRoutes)
	}
}

func TestSeriesAdvanceResetsBucket(t *testing.T) {
	s := newSeries(60, 8)
	base := time.Now().Unix()
	s.advance(base)
	old := s.cur.Load()
	// 强制推进两个桶，验证旧桶被清零。
	s.advance(base + 180)
	if s.cur.Load() == old {
		t.Fatal("当前桶未推进")
	}
	if got := s.slotAt(old).req.Load(); got != 0 {
		t.Fatalf("旧桶未清零: %d", got)
	}
}

func TestSeriesQuantile(t *testing.T) {
	var hist [histBuckets]int64
	hist[0] = 90 // 1ms
	hist[9] = 10 // 1000ms
	if q := quantile(hist, 100, 0.95); q != 1000 {
		t.Fatalf("P95 = %v, want 1000", q)
	}
	if q := quantile(hist, 100, 0.5); q != 1 {
		t.Fatalf("P50 = %v, want 1", q)
	}
}

func TestMergePointsKeepsWeightedLatency(t *testing.T) {
	pts := []point{
		{T: 60, Req: 10, AvgMs: 10, P95: 20},
		{T: 120, Req: 10, AvgMs: 30, P95: 40},
	}
	out := mergePoints(pts, 2)
	if len(out) != 1 {
		t.Fatalf("合并后点数 = %d", len(out))
	}
	if out[0].AvgMs != 20 {
		t.Fatalf("合并后平均延迟 = %v, want 20", out[0].AvgMs)
	}
	if out[0].P95 != 30 {
		t.Fatalf("合并后 P95 = %v, want 30", out[0].P95)
	}
}

func TestParseRangeFallback(t *testing.T) {
	if r := ParseRange("7d"); r.Key != "7d" {
		t.Fatalf("解析 7d 失败: %+v", r)
	}
	if r := ParseRange("不存在的范围"); r.Key != "24h" {
		t.Fatalf("默认范围 = %s, want 24h", r.Key)
	}
}

func mustEventLog(t *testing.T) *EventLog {
	t.Helper()
	log, err := OpenEventLog(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return log
}
