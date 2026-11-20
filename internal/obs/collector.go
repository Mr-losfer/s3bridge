package obs

import (
	"encoding/gob"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

// Record 是一次请求的采集样本。由中间件填充，字段必须保持轻量。
type Record struct {
	Method   string
	Path     string
	Route    string
	Actor    string
	IP       string
	Status   int
	Latency  time.Duration
	BytesIn  int64
	BytesOut int64
	Upload   bool
}

// Collector 聚合指标与事件，是 obs 包唯一的对外入口。
type Collector struct {
	log *EventLog
	dir string

	m1  *series // 1 分钟 × 720（12 小时）
	m5  *series // 5 分钟 × 2016（7 天）
	m1h *series // 1 小时 × 720（30 天）

	startedAt time.Time
	stopOnce  sync.Once
	stop      chan struct{}
	wg        sync.WaitGroup

	memMu   sync.Mutex
	memStat runtime.MemStats
	memAt   time.Time

	goroutines atomic.Int64
}

// Options 是采集器配置。
type Options struct {
	Dir string // 指标与事件落盘目录，通常是 {DataDir}/obs
}

// New 创建采集器；目录不可写时返回错误（调用方决定是否降级）。
func New(opts Options) (*Collector, error) {
	dir := opts.Dir
	if dir == "" {
		return nil, fmt.Errorf("obs: 缺少数据目录")
	}
	log, err := OpenEventLog(filepath.Join(dir, "events"))
	if err != nil {
		return nil, err
	}
	c := &Collector{
		log:       log,
		dir:       dir,
		m1:        newSeries(60, 720),
		m5:        newSeries(300, 2016),
		m1h:       newSeries(3600, 720),
		startedAt: time.Now().UTC(),
		stop:      make(chan struct{}),
	}
	_ = c.loadMetrics()
	c.wg.Add(1)
	go c.loop()
	return c, nil
}

// Record 记录一次请求（无锁、无 IO）。
func (c *Collector) Record(r Record) {
	if c == nil {
		return
	}
	c.m1.record(r)
	c.m5.record(r)
	c.m1h.record(r)
}

// Event 写入一条事件。
func (c *Collector) Event(e Event) {
	if c == nil || c.log == nil {
		return
	}
	c.log.Append(e)
}

// Events 查询事件日志。
func (c *Collector) Events(f EventFilter) ([]Event, int) {
	if c == nil || c.log == nil {
		return nil, 0
	}
	return c.log.List(f)
}

// EventCounts 返回区间内的级别/分类计数。
func (c *Collector) EventCounts(since time.Time) (map[string]int, map[string]int) {
	if c == nil || c.log == nil {
		return map[string]int{}, map[string]int{}
	}
	return c.log.Counts(since)
}

func (c *Collector) loop() {
	defer c.wg.Done()
	tick := time.NewTicker(30 * time.Second)
	defer tick.Stop()
	lastPersist := time.Now()
	for {
		select {
		case <-c.stop:
			_ = c.saveMetrics()
			c.log.Flush()
			return
		case now := <-tick.C:
			c.m1.advance(now.Unix())
			c.m5.advance(now.Unix())
			c.m1h.advance(now.Unix())
			c.goroutines.Store(int64(runtime.NumGoroutine()))
			if now.Sub(lastPersist) >= 5*time.Minute {
				_ = c.saveMetrics()
				c.log.Flush()
				lastPersist = now
			}
		}
	}
}

// Close 停止后台协程并落盘。
func (c *Collector) Close() error {
	if c == nil {
		return nil
	}
	c.stopOnce.Do(func() { close(c.stop) })
	c.wg.Wait()
	return c.log.Close()
}

// ---------- 查询 ----------

// Range 是查询的时间窗口。
type Range struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	Secs  int64  `json:"secs"`
}

// Ranges 是前端可选的时间范围。
var Ranges = []Range{
	{Key: "1h", Label: "1 小时", Secs: 3600},
	{Key: "6h", Label: "6 小时", Secs: 6 * 3600},
	{Key: "24h", Label: "24 小时", Secs: 24 * 3600},
	{Key: "7d", Label: "7 天", Secs: 7 * 24 * 3600},
	{Key: "30d", Label: "30 天", Secs: 30 * 24 * 3600},
}

// ParseRange 解析前端传入的范围键，默认 24h。
func ParseRange(key string) Range {
	key = strings.ToLower(strings.TrimSpace(key))
	for _, r := range Ranges {
		if r.Key == key {
			return r
		}
	}
	return Ranges[2]
}

// Totals 是区间汇总。
type Totals struct {
	Req      int64   `json:"req"`
	Err4     int64   `json:"err4"`
	Err5     int64   `json:"err5"`
	ErrRate  float64 `json:"errRate"`
	Upload   int64   `json:"upload"`
	BytesIn  int64   `json:"bytesIn"`
	BytesOut int64   `json:"bytesOut"`
	AvgMs    float64 `json:"avgMs"`
	P50      float64 `json:"p50"`
	P95      float64 `json:"p95"`
	P99      float64 `json:"p99"`
	Slow     int64   `json:"slow"`
	Ok2      int64   `json:"ok2"`
	Ok3      int64   `json:"ok3"`
}

// Summary 是监控概览的完整响应。
type Summary struct {
	Range     Range          `json:"range"`
	From      int64          `json:"from"`
	To        int64          `json:"to"`
	StepSecs  int64          `json:"stepSecs"`
	Totals    Totals         `json:"totals"`
	Series    []point        `json:"series"`
	ErrRoutes []routeStat    `json:"errRoutes"`
	Slowest   []routeStat    `json:"slowest"`
	Levels    map[string]int `json:"levels"`
	Category  map[string]int `json:"categories"`
	Health    Health         `json:"health"`
}

// Health 是运行时健康信息。
type Health struct {
	UptimeSecs int64     `json:"uptimeSecs"`
	Goroutines int64     `json:"goroutines"`
	HeapBytes  uint64    `json:"heapBytes"`
	SysBytes   uint64    `json:"sysBytes"`
	NumGC      uint32    `json:"numGC"`
	Disk       DiskUsage `json:"disk"`
}

// Summary 计算区间汇总。range 决定采样精度与降采样倍数。
func (c *Collector) Summary(rg Range, now time.Time) Summary {
	to := now.Unix()
	from := to - rg.Secs
	out := Summary{
		Range: rg, From: from, To: to,
		Levels: map[string]int{}, Category: map[string]int{},
		Totals: Totals{},
	}
	if c == nil {
		return out
	}
	var pts []point
	var step int64
	switch {
	case rg.Secs <= 3600:
		pts, step = c.m1.points(from, to), 60
	case rg.Secs <= 6*3600:
		pts, step = mergePoints(c.m1.points(from, to), 5), 300
	case rg.Secs <= 24*3600:
		pts, step = mergePoints(c.m5.points(from, to), 3), 900
	case rg.Secs <= 7*24*3600:
		pts, step = mergePoints(c.m1h.points(from, to), 3), 3*3600
	default:
		pts, step = mergePoints(c.m1h.points(from, to), 12), 12*3600
	}
	out.Series = pts
	out.StepSecs = step

	var latWeighted float64
	for _, p := range pts {
		out.Totals.Req += p.Req
		out.Totals.Ok2 += p.Ok2
		out.Totals.Ok3 += p.Ok3
		out.Totals.Err4 += p.Err4
		out.Totals.Err5 += p.Err5
		out.Totals.Upload += p.Upload
		out.Totals.BytesIn += p.BytesIn
		out.Totals.BytesOut += p.BytesOut
		out.Totals.Slow += p.Slow
		if p.Req > 0 {
			latWeighted += p.AvgMs * float64(p.Req)
		}
		if p.P95 > out.Totals.P95 {
			out.Totals.P95 = p.P95
		}
		if p.P99 > out.Totals.P99 {
			out.Totals.P99 = p.P99
		}
		if p.P50 > out.Totals.P50 {
			out.Totals.P50 = p.P50
		}
	}
	if out.Totals.Req > 0 {
		out.Totals.AvgMs = latWeighted / float64(out.Totals.Req)
	}
	if out.Totals.Req > 0 {
		out.Totals.ErrRate = float64(out.Totals.Err4+out.Totals.Err5) / float64(out.Totals.Req)
	}

	out.Slowest = c.routes(from, to, 6, false)
	out.ErrRoutes = c.routes(from, to, 6, true)

	out.Levels, out.Category = c.EventCounts(time.Unix(from, 0))
	out.Health = c.health()
	return out
}

func (c *Collector) routes(from, to int64, limit int, byErrors bool) []routeStat {
	switch {
	case to-from <= 24*3600:
		return c.m1.routes(from, to, limit, byErrors)
	case to-from <= 7*24*3600:
		return c.m1h.routes(from, to, limit, byErrors)
	default:
		return c.m1h.routes(from, to, limit, byErrors)
	}
}

func (c *Collector) health() Health {
	h := Health{UptimeSecs: int64(time.Since(c.startedAt).Seconds())}
	h.Goroutines = c.goroutines.Load()
	if h.Goroutines == 0 {
		h.Goroutines = int64(runtime.NumGoroutine())
	}
	c.memMu.Lock()
	if time.Since(c.memAt) > 10*time.Second {
		runtime.ReadMemStats(&c.memStat)
		c.memAt = time.Now()
	}
	ms := c.memStat
	c.memMu.Unlock()
	h.HeapBytes, h.SysBytes, h.NumGC = ms.HeapAlloc, ms.Sys, ms.NumGC
	h.Disk = DiskStats(c.dir)
	return h
}

// ---------- 指标持久化 ----------

type persistSlot struct {
	T                           int64
	Req, Err4, Err5, Upload     int64
	Ok2, Ok3, BytesIn, BytesOut int64
	LatSumUs                    int64
	Hist                        [histBuckets]int64
}

type persistFile struct {
	Version int
	SavedAt int64
	M5      []persistSlot
	M1h     []persistSlot
}

func (c *Collector) metricPath() string { return filepath.Join(c.dir, "metrics.gob") }

func (c *Collector) saveMetrics() error {
	if c == nil {
		return nil
	}
	pf := persistFile{Version: 1, SavedAt: time.Now().Unix()}
	pf.M5 = dumpSeries(c.m5)
	pf.M1h = dumpSeries(c.m1h)
	tmp := c.metricPath() + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	if err := gob.NewEncoder(f).Encode(pf); err != nil {
		_ = f.Close()
		_ = os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, c.metricPath())
}

func dumpSeries(s *series) []persistSlot {
	out := make([]persistSlot, 0, s.span)
	for _, sl := range s.slots {
		ps := persistSlot{
			T: sl.t, Req: sl.req.Load(), Ok2: sl.ok2.Load(), Ok3: sl.ok3.Load(),
			Err4: sl.err4.Load(), Err5: sl.err5.Load(),
			Upload: sl.upload.Load(), BytesIn: sl.bytesIn.Load(), BytesOut: sl.bytesOut.Load(),
			LatSumUs: sl.latSumUs.Load(),
		}
		for i := range sl.hist {
			ps.Hist[i] = sl.hist[i].Load()
		}
		out = append(out, ps)
	}
	return out
}

func (c *Collector) loadMetrics() error {
	f, err := os.Open(c.metricPath())
	if err != nil {
		return err
	}
	defer f.Close()
	var pf persistFile
	if err := gob.NewDecoder(f).Decode(&pf); err != nil {
		return err
	}
	restoreSeries(c.m5, pf.M5)
	restoreSeries(c.m1h, pf.M1h)
	return nil
}

func restoreSeries(s *series, slots []persistSlot) {
	for _, ps := range slots {
		idx := (ps.T / s.step) % s.span
		if idx < 0 {
			idx += s.span
		}
		sl := s.slots[idx]
		sl.t = ps.T
		sl.req.Store(ps.Req)
		sl.ok2.Store(ps.Ok2)
		sl.ok3.Store(ps.Ok3)
		sl.err4.Store(ps.Err4)
		sl.err5.Store(ps.Err5)
		sl.upload.Store(ps.Upload)
		sl.bytesIn.Store(ps.BytesIn)
		sl.bytesOut.Store(ps.BytesOut)
		sl.latSumUs.Store(ps.LatSumUs)
		for i := range ps.Hist {
			sl.hist[i].Store(ps.Hist[i])
		}
	}
}

// Categories 返回固定顺序的分类键，供前端渲染筛选器。
func Categories() []string {
	out := make([]string, 0, len(CategoryLabels))
	for k := range CategoryLabels {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
