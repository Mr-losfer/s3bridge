// Package obs 提供轻量级可观测性能力：请求指标（多级时间桶 + 延迟直方图）、
// 异常事件环形缓冲、存储体检与缩略图缓存。
//
// 设计目标是「零依赖、零阻塞」：请求路径只做若干次原子加，落盘与聚合在后台
// 协程完成，因此接入采集后单请求开销保持在微秒级，不会拖慢上传与 S3 主干链路。
package obs

import (
	"math"
	"sort"
	"sync"
	"sync/atomic"
	"time"
)

// 延迟直方图上界（毫秒）。用固定桶替代全量样本，内存恒定且可分位统计。
var latencyBoundsMs = []float64{
	1, 2, 5, 10, 25, 50, 100, 250, 500, 1000, 2500, 5000, 10000, 30000,
}

const histBuckets = 15 // 上界个数 + 最后一个「>30s」

// slot 是一个时间桶。全部字段用原子操作，写路径不加锁。
type slot struct {
	t        int64 // 桶起点（unix 秒）
	req      atomic.Int64
	ok2      atomic.Int64
	ok3      atomic.Int64
	err4     atomic.Int64
	err5     atomic.Int64
	upload   atomic.Int64
	bytesIn  atomic.Int64
	bytesOut atomic.Int64
	latSumUs atomic.Int64
	hist     [histBuckets]atomic.Int64
	// 按路径归集的耗时，用于「最慢接口」排行；键是归一化路由。
	routeMu  sync.Mutex
	routeLat map[string]*routeAgg
}

type routeAgg struct {
	count  int64
	errs   int64
	sumUs  int64
	maxUs  int64
	status map[int]int64
}

func (s *slot) reset(t int64) {
	s.t = t
	s.req.Store(0)
	s.ok2.Store(0)
	s.ok3.Store(0)
	s.err4.Store(0)
	s.err5.Store(0)
	s.upload.Store(0)
	s.bytesIn.Store(0)
	s.bytesOut.Store(0)
	s.latSumUs.Store(0)
	for i := range s.hist {
		s.hist[i].Store(0)
	}
	s.routeMu.Lock()
	s.routeLat = nil
	s.routeMu.Unlock()
}

// series 是一组固定跨度的环形时间桶。
type series struct {
	step  int64 // 桶宽（秒）
	span  int64 // 桶数量
	slots []*slot
	cur   atomic.Int64 // 当前桶起点
	mu    sync.Mutex   // 仅用于跨桶推进，避免并发重置
}

func newSeries(step, span int64) *series {
	s := &series{step: step, span: span, slots: make([]*slot, span)}
	now := time.Now().Unix()
	start := now - now%s.step
	for i := range s.slots {
		s.slots[i] = &slot{t: start, routeLat: map[string]*routeAgg{}}
	}
	s.cur.Store(start)
	return s
}

func (s *series) slotAt(t int64) *slot {
	idx := (t / s.step) % s.span
	if idx < 0 {
		idx += s.span
	}
	return s.slots[idx]
}

// advance 把当前桶推进到 now 所在的桶；跨过的桶会被清零。
// 由写路径 CAS 触发，后台 ticker 兜底，二者都调用它。
func (s *series) advance(now int64) {
	target := now - now%s.step
	cur := s.cur.Load()
	if target == cur {
		return
	}
	if !s.mu.TryLock() {
		return // 其他协程正在推进，本次写入落到旧桶，误差 ≤ 1 个桶
	}
	defer s.mu.Unlock()
	cur = s.cur.Load()
	if target == cur {
		return
	}
	if target < cur {
		return
	}
	// 最多清 span 个桶，避免长时间休眠后空转。
	steps := (target - cur) / s.step
	if steps > s.span {
		steps = s.span
	}
	for i := int64(1); i <= steps; i++ {
		s.slotAt(cur + i*s.step).reset(cur + i*s.step)
	}
	s.cur.Store(target)
}

// record 把一次请求计入当前桶。
func (s *series) record(r Record) {
	now := time.Now().Unix()
	s.advance(now)
	cur := s.cur.Load()
	sl := s.slotAt(cur)
	if sl.t != cur {
		sl.reset(cur)
	}
	sl.req.Add(1)
	switch {
	case r.Status >= 500:
		sl.err5.Add(1)
	case r.Status >= 400:
		sl.err4.Add(1)
	case r.Status >= 300:
		sl.ok3.Add(1)
	default:
		sl.ok2.Add(1)
	}
	if r.Upload {
		sl.upload.Add(1)
	}
	sl.bytesIn.Add(r.BytesIn)
	sl.bytesOut.Add(r.BytesOut)
	us := r.Latency.Microseconds()
	sl.latSumUs.Add(us)
	sl.hist[histIndex(float64(r.Latency.Milliseconds()))].Add(1)
	if r.Route != "" {
		sl.routeMu.Lock()
		if sl.routeLat == nil {
			sl.routeLat = map[string]*routeAgg{}
		}
		agg := sl.routeLat[r.Route]
		if agg == nil {
			agg = &routeAgg{status: map[int]int64{}}
			sl.routeLat[r.Route] = agg
		}
		agg.count++
		agg.sumUs += us
		if us > agg.maxUs {
			agg.maxUs = us
		}
		if r.Status >= 400 {
			agg.errs++
		}
		agg.status[r.Status]++
		sl.routeMu.Unlock()
	}
}

func histIndex(ms float64) int {
	for i, b := range latencyBoundsMs {
		if ms <= b {
			return i
		}
	}
	return histBuckets - 1
}

// point 是对外输出的时间点。
type point struct {
	T        int64   `json:"t"`
	Req      int64   `json:"req"`
	Ok2      int64   `json:"ok2"`
	Ok3      int64   `json:"ok3"`
	Err4     int64   `json:"err4"`
	Err5     int64   `json:"err5"`
	Upload   int64   `json:"upload"`
	BytesIn  int64   `json:"bytesIn"`
	BytesOut int64   `json:"bytesOut"`
	AvgMs    float64 `json:"avgMs"`
	P50      float64 `json:"p50"`
	P95      float64 `json:"p95"`
	P99      float64 `json:"p99"`
	Slow     int64   `json:"slow"`
}

func (s *series) points(from, to int64) []point {
	out := []point{}
	start := from - from%s.step
	for t := start; t <= to; t += s.step {
		sl := s.slotAt(t)
		if sl.t != t {
			continue
		}
		out = append(out, snapshotPoint(sl))
	}
	return out
}

// fold 把若干连续 slot 合并成一个点，用于 range 大于 series 原始精度时降采样。
func fold(slots []*slot, t int64) point {
	var p point
	var total int64
	for _, sl := range slots {
		if sl == nil {
			continue
		}
		p.Req += sl.req.Load()
		p.Ok2 += sl.ok2.Load()
		p.Ok3 += sl.ok3.Load()
		p.Err4 += sl.err4.Load()
		p.Err5 += sl.err5.Load()
		p.Upload += sl.upload.Load()
		p.BytesIn += sl.bytesIn.Load()
		p.BytesOut += sl.bytesOut.Load()
		us := sl.latSumUs.Load()
		p.AvgMs += float64(us) / 1000
		total += sl.req.Load()
	}
	p.T = t
	if total > 0 {
		p.AvgMs = p.AvgMs / float64(total)
	}
	return p
}

func snapshotPoint(sl *slot) point {
	req := sl.req.Load()
	p := point{
		T: sl.t, Req: req,
		Ok2: sl.ok2.Load(), Ok3: sl.ok3.Load(),
		Err4: sl.err4.Load(), Err5: sl.err5.Load(), Upload: sl.upload.Load(),
		BytesIn: sl.bytesIn.Load(), BytesOut: sl.bytesOut.Load(),
	}
	if req > 0 {
		p.AvgMs = float64(sl.latSumUs.Load()) / 1000 / float64(req)
	}
	var hist [histBuckets]int64
	var total int64
	for i := range sl.hist {
		hist[i] = sl.hist[i].Load()
		total += hist[i]
	}
	if total > 0 {
		p.P50 = quantile(hist, total, 0.50)
		p.P95 = quantile(hist, total, 0.95)
		p.P99 = quantile(hist, total, 0.99)
		// 慢请求：超过 1s 的比例。
		for i := 9; i < histBuckets; i++ {
			p.Slow += hist[i]
		}
	}
	return p
}

func quantile(hist [histBuckets]int64, total int64, q float64) float64 {
	target := int64(math.Ceil(float64(total) * q))
	var acc int64
	for i, c := range hist {
		acc += c
		if acc >= target {
			if i >= len(latencyBoundsMs) {
				return latencyBoundsMs[len(latencyBoundsMs)-1]
			}
			return latencyBoundsMs[i]
		}
	}
	return latencyBoundsMs[len(latencyBoundsMs)-1]
}

// mergePoints 把多个点合并（用于把分钟点合成更粗的粒度）。
func mergePoints(pts []point, group int) []point {
	if group <= 1 {
		return pts
	}
	out := make([]point, 0, len(pts)/group+1)
	for i := 0; i < len(pts); i += group {
		end := i + group
		if end > len(pts) {
			end = len(pts)
		}
		chunk := pts[i:end]
		var p point
		var wsum, p50, p95, p99 float64
		var total int64
		for _, x := range chunk {
			p.Req += x.Req
			p.Ok2 += x.Ok2
			p.Ok3 += x.Ok3
			p.Err4 += x.Err4
			p.Err5 += x.Err5
			p.Upload += x.Upload
			p.BytesIn += x.BytesIn
			p.BytesOut += x.BytesOut
			p.Slow += x.Slow
			if x.Req > 0 {
				w := float64(x.Req)
				wsum += x.AvgMs * w
				p50 += x.P50 * w
				p95 += x.P95 * w
				p99 += x.P99 * w
				total += x.Req
			}
		}
		if total > 0 {
			p.AvgMs = wsum / float64(total)
			p.P50 = p50 / float64(total)
			p.P95 = p95 / float64(total)
			p.P99 = p99 / float64(total)
		}
		p.T = chunk[0].T
		out = append(out, p)
	}
	return out
}

// topRoutes 汇总区间内的慢接口排行。
type routeStat struct {
	Route  string        `json:"route"`
	Count  int64         `json:"count"`
	Errors int64         `json:"errors"`
	AvgMs  float64       `json:"avgMs"`
	MaxMs  float64       `json:"maxMs"`
	Status map[int]int64 `json:"status,omitempty"`

	sumMs float64
}

func (s *series) routes(from, to int64, limit int, byErrors bool) []routeStat {
	agg := map[string]*routeStat{}
	start := from - from%s.step
	for t := start; t <= to; t += s.step {
		sl := s.slotAt(t)
		if sl.t != t {
			continue
		}
		sl.routeMu.Lock()
		for name, a := range sl.routeLat {
			st := agg[name]
			if st == nil {
				st = &routeStat{Route: name, Status: map[int]int64{}}
				agg[name] = st
			}
			st.Count += a.count
			st.Errors += a.errs
			st.sumMs += float64(a.sumUs) / 1000
			if ms := float64(a.maxUs) / 1000; ms > st.MaxMs {
				st.MaxMs = ms
			}
			for code, n := range a.status {
				st.Status[code] += n
			}
		}
		sl.routeMu.Unlock()
	}
	out := make([]routeStat, 0, len(agg))
	for _, st := range agg {
		if st.Count > 0 {
			st.AvgMs = st.sumMs / float64(st.Count)
		}
		out = append(out, *st)
	}
	sort.Slice(out, func(i, j int) bool {
		if byErrors {
			if out[i].Errors != out[j].Errors {
				return out[i].Errors > out[j].Errors
			}
		}
		if out[i].AvgMs != out[j].AvgMs {
			return out[i].AvgMs > out[j].AvgMs
		}
		return out[i].Count > out[j].Count
	})
	if limit > 0 && len(out) > limit {
		out = out[:limit]
	}
	return out
}
