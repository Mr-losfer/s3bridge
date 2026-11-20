package obs

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 事件级别。
const (
	LevelInfo  = "info"
	LevelWarn  = "warn"
	LevelError = "error"
)

// 事件分类（前端「什么异常」的过滤维度）。
const (
	CatRequest = "request" // 通用请求错误
	CatAuth    = "auth"    // 登录失败、密钥无效、权限不足
	CatUpload  = "upload"  // 上传失败、文件过大、空文件
	CatPlugin  = "plugin"  // 插件不可用、调度失败、健康检测失败
	CatS3      = "s3"      // S3 协议错误
	CatStorage = "storage" // 磁盘/文件系统异常、对象缺失
	CatSystem  = "system"  // 内部错误、panic、更新失败
)

// CategoryLabels 供前端展示。
var CategoryLabels = map[string]string{
	CatRequest: "请求异常", CatAuth: "鉴权", CatUpload: "上传",
	CatPlugin: "插件", CatS3: "S3 接口", CatStorage: "存储", CatSystem: "系统",
}

// Event 是一条可检索的异常/关键操作记录。
// 只在出错、慢请求或写操作时产生，普通 2xx 请求只进指标，避免日志放大。
type Event struct {
	ID        string            `json:"id"`
	Time      time.Time         `json:"time"`
	Level     string            `json:"level"`
	Category  string            `json:"category"`
	Status    int               `json:"status"`
	Method    string            `json:"method"`
	Route     string            `json:"route"`
	Path      string            `json:"path"`
	Actor     string            `json:"actor"`
	IP        string            `json:"ip"`
	LatencyMs float64           `json:"latencyMs"`
	Code      string            `json:"code,omitempty"`
	Message   string            `json:"message"`
	Fields    map[string]string `json:"fields,omitempty"`
}

// EventFilter 是日志查询条件。
type EventFilter struct {
	Level    string
	Category string
	Query    string
	Since    time.Time
	Until    time.Time
	Limit    int
	Offset   int
}

const (
	eventCapacity = 5000
	maxEventFile  = 8 << 20
	eventKeepOld  = 3
)

// EventLog 是带磁盘轮转的事件环形缓冲。
//
// 内存里保留最近 eventCapacity 条供快速查询；磁盘按行追加，
// 超过 maxEventFile 后轮转，最多保留 eventKeepOld 份历史文件。
type EventLog struct {
	mu    sync.RWMutex
	buf   []Event
	head  int // 下一个写入位置
	count int

	fileMu sync.Mutex
	dir    string
	path   string
	f      *os.File
	w      *bufio.Writer
	closed bool

	dropped int64
}

// OpenEventLog 打开（或新建）事件日志目录。
func OpenEventLog(dir string) (*EventLog, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("创建日志目录: %w", err)
	}
	l := &EventLog{
		buf:  make([]Event, eventCapacity),
		dir:  dir,
		path: filepath.Join(dir, "events.jsonl"),
	}
	if err := l.loadTail(); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, fmt.Errorf("打开事件日志: %w", err)
	}
	l.f = f
	l.w = bufio.NewWriterSize(f, 32<<10)
	return l, nil
}

// loadTail 只加载文件尾部，避免历史日志拖慢启动。
func (l *EventLog) loadTail() error {
	f, err := os.Open(l.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("读取事件日志: %w", err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil
	}
	const tailMax = 4 << 20
	offset := int64(0)
	if st.Size() > tailMax {
		offset = st.Size() - tailMax
	}
	if _, err := f.Seek(offset, 0); err != nil {
		return nil
	}
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 64<<10), 1<<20)
	first := offset > 0
	for sc.Scan() {
		line := sc.Bytes()
		if first {
			// 截断处可能半行，丢弃第一条。
			first = false
			continue
		}
		var e Event
		if err := json.Unmarshal(line, &e); err != nil {
			continue
		}
		l.push(e)
	}
	return nil
}

func (l *EventLog) push(e Event) {
	l.buf[l.head] = e
	l.head = (l.head + 1) % eventCapacity
	if l.count < eventCapacity {
		l.count++
	}
}

// Append 写入一条事件；磁盘写失败不影响主流程。
func (l *EventLog) Append(e Event) {
	if e.Time.IsZero() {
		e.Time = time.Now().UTC()
	}
	if e.ID == "" {
		e.ID = fmt.Sprintf("ev_%x", time.Now().UnixNano())
	}
	l.mu.Lock()
	l.push(e)
	l.mu.Unlock()

	l.fileMu.Lock()
	defer l.fileMu.Unlock()
	if l.closed || l.w == nil {
		return
	}
	b, err := json.Marshal(e)
	if err != nil {
		return
	}
	if _, err := l.w.Write(append(b, '\n')); err != nil {
		return
	}
	if l.w.Buffered() >= 16<<10 {
		_ = l.w.Flush()
	}
	if info, err := l.f.Stat(); err == nil && info.Size() > maxEventFile {
		l.rotateLocked()
	}
}

// rotateLocked 轮转日志文件，调用方需持有 fileMu。
func (l *EventLog) rotateLocked() {
	if l.w != nil {
		_ = l.w.Flush()
	}
	if l.f != nil {
		_ = l.f.Close()
	}
	for i := eventKeepOld - 1; i >= 1; i-- {
		_ = os.Rename(l.path+"."+itoa(i), l.path+"."+itoa(i+1))
	}
	_ = os.Rename(l.path, l.path+".1")
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		l.f, l.w = nil, nil
		return
	}
	l.f = f
	l.w = bufio.NewWriterSize(f, 32<<10)
}

// Flush 把缓冲刷到磁盘。
func (l *EventLog) Flush() {
	l.fileMu.Lock()
	defer l.fileMu.Unlock()
	if l.w != nil {
		_ = l.w.Flush()
	}
}

// Close 刷盘并关闭文件。
func (l *EventLog) Close() error {
	l.fileMu.Lock()
	defer l.fileMu.Unlock()
	l.closed = true
	if l.w != nil {
		_ = l.w.Flush()
	}
	if l.f != nil {
		return l.f.Close()
	}
	return nil
}

// List 返回按时间倒序过滤后的事件。
func (l *EventLog) List(f EventFilter) ([]Event, int) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	limit := f.Limit
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := strings.ToLower(strings.TrimSpace(f.Query))
	hits := make([]Event, 0, 64)
	// 从最新往旧遍历环形缓冲。
	for i := 0; i < l.count; i++ {
		idx := (l.head - 1 - i + eventCapacity*2) % eventCapacity
		e := l.buf[idx]
		if e.Time.IsZero() {
			continue
		}
		if f.Level != "" && e.Level != f.Level {
			continue
		}
		if f.Category != "" && e.Category != f.Category {
			continue
		}
		if !f.Since.IsZero() && e.Time.Before(f.Since) {
			continue
		}
		if !f.Until.IsZero() && e.Time.After(f.Until) {
			continue
		}
		if q != "" {
			hay := strings.ToLower(e.Message + " " + e.Route + " " + e.Path + " " + e.Actor + " " + e.Code)
			if !strings.Contains(hay, q) {
				continue
			}
		}
		hits = append(hits, e)
	}
	// 环形缓冲的顺序反映写入顺序，但事件可能带着更早的业务时间（如补偿写入），
	// 因此最终结果以事件时间倒序返回，保证界面上的时间线严格单调。
	sort.SliceStable(hits, func(i, j int) bool { return hits[i].Time.After(hits[j].Time) })
	total := len(hits)
	if f.Offset >= total {
		return []Event{}, total
	}
	hits = hits[f.Offset:]
	if len(hits) > limit {
		hits = hits[:limit]
	}
	return hits, total
}

// Counts 返回各级别与分类的计数，供概览卡片使用。
func (l *EventLog) Counts(since time.Time) (byLevel, byCategory map[string]int) {
	l.mu.RLock()
	defer l.mu.RUnlock()
	byLevel, byCategory = map[string]int{}, map[string]int{}
	for i := 0; i < l.count; i++ {
		e := l.buf[i]
		if e.Time.IsZero() || e.Time.Before(since) {
			continue
		}
		byLevel[e.Level]++
		byCategory[e.Category]++
	}
	return byLevel, byCategory
}

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for n > 0 {
		i--
		b[i] = byte('0' + n%10)
		n /= 10
	}
	return string(b[i:])
}
