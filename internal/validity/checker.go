// Package validity 实现图片有效性巡检。
//
// 规则（与后台设置一致）：
//   - 默认每 72 小时检查一次，可在系统设置里改全局间隔，也可在插件清单里覆盖；
//   - 只对「本站归档的图片」检查，方式是向本机回环地址发 HEAD（不支持时退回
//     带 Range: bytes=0-0 的 GET），只读取响应头，**不下载正文**；
//   - 判定 200/206 且 Content-Type 为 image/* 即正常；
//   - 连续失败达到阈值才标记「失效」并产生告警，单次网络抖动不会误报。
package validity

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// Options 是巡检器配置。
type Options struct {
	Enabled       bool
	Interval      time.Duration // 全局默认间隔
	FailThreshold int           // 连续失败多少次判定失效
	BaseURL       string        // 本机回环地址，如 http://127.0.0.1:8080
	Secret        []byte        // 与 HTTP 层共享的探测签名密钥
	Timeout       time.Duration
	Workers       int
}

// Checker 周期性检查本站图片是否仍可正常返回。
type Checker struct {
	st  store.Store
	opt Options
	hc  *http.Client

	intervalMu sync.RWMutex
	interval   time.Duration
	enabled    bool
	threshold  int

	stopOnce sync.Once
	stop     chan struct{}
	wg       sync.WaitGroup
}

func New(st store.Store, opt Options) *Checker {
	if opt.Interval <= 0 {
		opt.Interval = 72 * time.Hour
	}
	if opt.FailThreshold <= 0 {
		opt.FailThreshold = 2
	}
	if opt.Timeout <= 0 {
		opt.Timeout = 8 * time.Second
	}
	if opt.Workers <= 0 {
		opt.Workers = 4
	}
	return &Checker{
		st: st, opt: opt,
		hc:       &http.Client{Timeout: opt.Timeout},
		interval: opt.Interval, enabled: opt.Enabled, threshold: opt.FailThreshold,
		stop: make(chan struct{}),
	}
}

// Configure 在系统设置变化后热更新参数。
func (c *Checker) Configure(enabled bool, interval time.Duration, threshold int) {
	c.intervalMu.Lock()
	defer c.intervalMu.Unlock()
	c.enabled = enabled
	if interval > 0 {
		c.interval = interval
	}
	if threshold > 0 {
		c.threshold = threshold
	}
}

func (c *Checker) snapshot() (bool, time.Duration, int) {
	c.intervalMu.RLock()
	defer c.intervalMu.RUnlock()
	return c.enabled, c.interval, c.threshold
}

// Start 启动巡检协程；重复调用安全。
func (c *Checker) Start(parent context.Context) {
	c.wg.Add(1)
	go func() {
		defer c.wg.Done()
		// 启动后等 30 秒再首轮，避开启动高峰。
		select {
		case <-parent.Done():
			return
		case <-c.stop:
			return
		case <-time.After(30 * time.Second):
		}
		c.RunOnce(parent)
		t := time.NewTicker(time.Minute)
		defer t.Stop()
		for {
			select {
			case <-parent.Done():
				return
			case <-c.stop:
				return
			case <-t.C:
				// 每分钟醒来一次：到期对象由 RunOnce 内部按各自间隔筛选，
				// 因此这里只需固定节奏触发，设置变更也能立即生效。
				c.intervalMu.RLock()
				on := c.enabled
				c.intervalMu.RUnlock()
				if !on {
					continue
				}
				c.RunOnce(parent)
			}
		}
	}()
}

func (c *Checker) Stop() {
	c.stopOnce.Do(func() { close(c.stop) })
	c.wg.Wait()
}

// RunOnce 扫描一批到期对象。每次最多处理 200 张，避免长时间占用资源。
func (c *Checker) RunOnce(ctx context.Context) {
	on, interval, threshold := c.snapshot()
	if !on || c.opt.BaseURL == "" {
		return
	}
	due := c.dueObjects(interval, 200)
	if len(due) == 0 {
		return
	}
	sem := make(chan struct{}, c.opt.Workers)
	var wg sync.WaitGroup
	for _, o := range due {
		select {
		case <-ctx.Done():
			return
		default:
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(obj store.Object) {
			defer wg.Done()
			defer func() { <-sem }()
			c.checkOne(ctx, obj, threshold)
		}(o)
	}
	wg.Wait()
}

// dueObjects 选出到期对象：从未检查过的优先，其次按上次检查时间升序。
func (c *Checker) dueObjects(interval time.Duration, limit int) []store.Object {
	var out []store.Object
	now := time.Now()
	_ = c.st.View(func(st *store.State) error {
		type item struct {
			obj  store.Object
			last time.Time
		}
		items := make([]item, 0, len(st.Objects))
		pluginEvery := map[string]time.Duration{}
		for _, p := range st.Plugins {
			if p.ValidityHours > 0 {
				pluginEvery[p.ID] = time.Duration(p.ValidityHours) * time.Hour
			}
		}
		for _, o := range st.Objects {
			if o.Category != store.CategoryImage || o.LocalPath == "" {
				continue
			}
			every := interval
			if d := pluginEvery[o.PluginID]; d > 0 {
				every = d
			}
			var last time.Time
			if o.Validity.CheckedAt != nil {
				last = *o.Validity.CheckedAt
			}
			if !last.IsZero() && now.Sub(last) < every {
				continue
			}
			items = append(items, item{obj: o, last: last})
		}
		// 从未检查过的排最前，其余按上次检查时间升序。
		sort.Slice(items, func(i, j int) bool {
			if items[i].last.IsZero() != items[j].last.IsZero() {
				return items[i].last.IsZero()
			}
			return items[i].last.Before(items[j].last)
		})
		if len(items) > limit {
			items = items[:limit]
		}
		out = make([]store.Object, 0, len(items))
		for _, it := range items {
			out = append(out, it.obj)
		}
		return nil
	})
	return out
}

func (c *Checker) checkOne(ctx context.Context, obj store.Object, threshold int) {
	status, ctype, errMsg := c.probe(ctx, obj.Bucket, obj.ObjectKey)
	now := store.NowUTC()
	ok := (status == http.StatusOK || status == http.StatusPartialContent) && strings.HasPrefix(strings.ToLower(ctype), "image/")
	if errMsg != "" {
		ok = false
	}
	_ = c.st.Update(func(st *store.State) error {
		cur := findObject(st.Objects, obj.ID)
		if cur == nil {
			return nil
		}
		cur.Validity.CheckedAt = &now
		cur.Validity.HTTPStatus = status
		cur.Validity.ContentType = ctype
		cur.Validity.Error = errMsg
		if ok {
			cur.Validity.Status = "ok"
			cur.Validity.FailCount = 0
			return nil
		}
		cur.Validity.FailCount++
		if cur.Validity.FailCount >= threshold {
			cur.Validity.Status = "invalid"
			c.raiseAlert(st, cur)
		} else if cur.Validity.Status == "" {
			cur.Validity.Status = "unknown"
		}
		return nil
	})
}

// probe 发 HEAD，若上游不支持 HEAD（405/501）再退回带 Range 的 GET。
// 两种情况都只读响应头，不读取正文。
func (c *Checker) probe(ctx context.Context, bucket, key string) (int, string, string) {
	status, ctype, errMsg := c.request(ctx, http.MethodHead, bucket, key, "")
	if status == http.StatusMethodNotAllowed || status == http.StatusNotImplemented {
		status, ctype, errMsg = c.request(ctx, http.MethodGet, bucket, key, "bytes=0-0")
	}
	return status, ctype, errMsg
}

func (c *Checker) request(ctx context.Context, method, bucket, key, rng string) (int, string, string) {
	q := "bucket=" + urlQueryEscape(bucket) + "&key=" + urlQueryEscape(key)
	req, err := http.NewRequestWithContext(ctx, method, strings.TrimRight(c.opt.BaseURL, "/")+"/api/internal/object-probe?"+q, nil)
	if err != nil {
		return 0, "", err.Error()
	}
	ts := strconv.FormatInt(time.Now().Unix(), 10)
	req.Header.Set("X-S3Bridge-Probe", ts)
	req.Header.Set("X-S3Bridge-Signature", Sign(c.opt.Secret, bucket, key, ts))
	if rng != "" {
		req.Header.Set("Range", rng)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return 0, "", err.Error()
	}
	defer resp.Body.Close()
	// 只读响应头：立即关闭正文，绝不下载。
	return resp.StatusCode, resp.Header.Get("Content-Type"), ""
}

func (c *Checker) raiseAlert(st *store.State, o *store.Object) {
	for i := range st.Alerts {
		a := &st.Alerts[i]
		if a.PluginID == o.PluginID && a.AckedAt == nil && strings.HasPrefix(a.Message, "图片失效") {
			return
		}
	}
	name := "图片有效性"
	for _, p := range st.Plugins {
		if p.ID == o.PluginID {
			name = p.Name
			break
		}
	}
	st.Alerts = append(st.Alerts, store.Alert{
		ID: randomID(), PluginID: o.PluginID, PluginName: name, Level: "warning",
		Message: fmt.Sprintf("图片失效：%s/%s（%s），已连续 %d 次无法返回图片",
			o.Bucket, o.ObjectKey, o.FileName, o.Validity.FailCount),
		CreatedAt: store.NowUTC(),
	})
}

// Sign 计算探测签名：HMAC-SHA256(secret, bucket\nkey\nts)，十六进制。
func Sign(secret []byte, bucket, key, ts string) string {
	mac := hmac.New(sha256.New, secret)
	mac.Write([]byte(bucket + "\n" + key + "\n" + ts))
	return hex.EncodeToString(mac.Sum(nil))
}

// Verify 校验探测签名，允许 90 秒时钟偏移。
func Verify(secret []byte, bucket, key, ts, sig string) bool {
	n, err := strconv.ParseInt(ts, 10, 64)
	if err != nil {
		return false
	}
	if d := time.Since(time.Unix(n, 0)); d > 90*time.Second || d < -90*time.Second {
		return false
	}
	want := Sign(secret, bucket, key, ts)
	return hmac.Equal([]byte(want), []byte(strings.ToLower(strings.TrimSpace(sig))))
}

// IsLoopback 判断请求是否来自本机，探测接口只接受回环地址。
func IsLoopback(remote string) bool {
	host, _, err := net.SplitHostPort(remote)
	if err != nil {
		host = remote
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

func findObject(list []store.Object, id string) *store.Object {
	for i := range list {
		if list[i].ID == id {
			return &list[i]
		}
	}
	return nil
}

func urlQueryEscape(s string) string {
	// 只做最小转义，避免引入额外依赖；对象键里可能出现 & = + 空格。
	replacer := strings.NewReplacer(
		"%", "%25", "&", "%26", "=", "%3D", "+", "%2B", " ", "%20", "?", "%3F", "#", "%23",
	)
	return replacer.Replace(s)
}

func randomID() string {
	b := make([]byte, 6)
	seed := time.Now().UnixNano()
	for i := range b {
		seed = seed*6364136223846793005 + 1
		b[i] = byte(seed >> 56)
	}
	return "val_" + hex.EncodeToString(b)
}

// LoopbackBase 把监听地址转换为本机回环 HTTP 基地址。
// ":8080" / "0.0.0.0:8080" 都变成 "http://127.0.0.1:8080"。
func LoopbackBase(addr string) string {
	host, port, err := net.SplitHostPort(strings.TrimSpace(addr))
	if err != nil || port == "" {
		return ""
	}
	if host == "" || host == "0.0.0.0" || host == "::" || host == "[::]" {
		host = "127.0.0.1"
	}
	if ip := net.ParseIP(strings.Trim(host, "[]")); ip != nil && !ip.IsLoopback() {
		host = "127.0.0.1"
	}
	return "http://" + net.JoinHostPort(host, port)
}
