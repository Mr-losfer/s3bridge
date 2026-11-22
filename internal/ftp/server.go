// Package ftp 提供一个只读/读写的极简 FTP 服务，用 API Key 登录。
//
// 设计目标不是实现完整 RFC 959，而是让 Finder、Windows 资源管理器、
// FileZilla、rclone 这类常见客户端能把存储桶当磁盘用：
//
//	主机: 服务地址     端口: 2121（可配置）
//	用户名: 随意填     密码: API Key（sk_ 开头）
//
// 目录结构直接映射存储桶：/ 是桶列表，/bucket/a/b.png 是对象键。
// 只支持被动模式（PASV/EPSV），不支持主动模式与断点续传。
package ftp

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"os"
	"path"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/upload"
)

type Options struct {
	Addr    string // 监听地址，默认 :2121
	Auth    *auth.Service
	Uploads *upload.Service
	// PassivePorts 限定被动模式端口范围，如 "30000-30010"。
	// 为空时用系统随机端口——本机使用没问题，但在 Docker/NAT 后无法映射。
	PassivePorts string
	// PublicIP 是 PASV 回复里通告给客户端的地址（IPv4）。
	// 容器或 NAT 后填公网/宿主地址，否则客户端会连到容器内网 IP。
	PublicIP string
}

type Server struct {
	opts  Options
	ports *portRange

	ln     net.Listener
	mu     sync.Mutex
	conns  map[net.Conn]struct{}
	closed bool
	wg     sync.WaitGroup
}

// New 创建服务；调用 Start 后才开始监听。
func New(opts Options) *Server {
	if opts.Addr == "" {
		opts.Addr = ":2121"
	}
	return &Server{
		opts: opts, ports: parsePortRange(opts.PassivePorts),
		conns: map[net.Conn]struct{}{},
	}
}

// portRange 在给定区间内轮转分配被动端口，用尽时回退到系统随机端口。
type portRange struct {
	start, end int
	mu         sync.Mutex
	next       int
}

func parsePortRange(raw string) *portRange {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	lo, hi, ok := strings.Cut(raw, "-")
	if !ok {
		hi = lo
	}
	start, err1 := strconv.Atoi(strings.TrimSpace(lo))
	end, err2 := strconv.Atoi(strings.TrimSpace(hi))
	if err1 != nil || err2 != nil || start <= 0 || end < start || end > 65535 {
		slog.Warn("FTP 被动端口范围无效，已回退随机端口", "value", raw)
		return nil
	}
	return &portRange{start: start, end: end}
}

func (p *portRange) listen(ip string) (net.Listener, error) {
	if p == nil {
		return net.Listen("tcp", net.JoinHostPort(ip, "0"))
	}
	p.mu.Lock()
	start := p.next
	p.mu.Unlock()
	size := p.end - p.start + 1
	for i := 0; i < size; i++ {
		port := p.start + (start+i)%size
		ln, err := net.Listen("tcp", net.JoinHostPort(ip, strconv.Itoa(port)))
		if err != nil {
			continue
		}
		p.mu.Lock()
		p.next = (port - p.start + 1) % size
		p.mu.Unlock()
		return ln, nil
	}
	return nil, fmt.Errorf("被动端口 %s 全部占用", p.String())
}

func (p *portRange) String() string {
	return fmt.Sprintf("%d-%d", p.start, p.end)
}

// Start 开始监听并在后台接受连接。
func (s *Server) Start() error {
	ln, err := net.Listen("tcp", s.opts.Addr)
	if err != nil {
		return fmt.Errorf("FTP 监听 %s: %w", s.opts.Addr, err)
	}
	s.ln = ln
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		for {
			c, err := ln.Accept()
			if err != nil {
				s.mu.Lock()
				closed := s.closed
				s.mu.Unlock()
				if closed {
					return
				}
				continue
			}
			s.mu.Lock()
			s.conns[c] = struct{}{}
			s.mu.Unlock()
			s.wg.Add(1)
			go func() {
				defer s.wg.Done()
				defer func() {
					s.mu.Lock()
					delete(s.conns, c)
					s.mu.Unlock()
					_ = c.Close()
				}()
				newConn(s.opts, s.ports, c).serve()
			}()
		}
	}()
	slog.Info("FTP 服务已启动", "addr", ln.Addr().String())
	return nil
}

// Addr 返回实际监听地址（端口为 0 时用于测试）。
func (s *Server) Addr() string {
	if s.ln == nil {
		return ""
	}
	return s.ln.Addr().String()
}

// Close 停止监听并断开所有连接。
func (s *Server) Close() error {
	s.mu.Lock()
	s.closed = true
	conns := make([]net.Conn, 0, len(s.conns))
	for c := range s.conns {
		conns = append(conns, c)
	}
	s.mu.Unlock()
	if s.ln != nil {
		_ = s.ln.Close()
	}
	for _, c := range conns {
		_ = c.Close()
	}
	s.wg.Wait()
	return nil
}

type conn struct {
	opts Options
	c    net.Conn
	r    *bufio.Reader
	w    *bufio.Writer

	ports      *portRange
	principal  *auth.Principal
	userOK     bool
	cwd        string // 形如 /bucket/dir
	passive    net.Listener
	renameFrom string
}

func newConn(opts Options, ports *portRange, c net.Conn) *conn {
	return &conn{
		opts: opts, ports: ports, c: c,
		r: bufio.NewReaderSize(c, 32<<10), w: bufio.NewWriterSize(c, 32<<10),
		cwd: "/",
	}
}

func (c *conn) serve() {
	c.reply(220, "S3Bridge FTP ready")
	for {
		line, err := c.r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		if line == "" {
			continue
		}
		cmd, arg, _ := strings.Cut(line, " ")
		cmd = strings.ToUpper(strings.TrimSpace(cmd))
		arg = strings.TrimSpace(arg)
		if !c.handle(cmd, arg) {
			return
		}
	}
}

func (c *conn) handle(cmd, arg string) bool {
	switch cmd {
	case "USER":
		c.userOK = true
		c.reply(331, "需要密码：请填 API Key")
	case "PASS":
		if !c.userOK {
			c.reply(503, "请先发送 USER")
			return true
		}
		p, err := c.opts.Auth.Authenticate("Bearer " + arg)
		if err != nil || p == nil || p.Via != "apikey" {
			c.reply(530, "API Key 无效或已过期")
			return true
		}
		c.principal = p
		c.reply(230, "登录成功，根目录为全部存储桶")

	case "SYST":
		c.reply(215, "UNIX Type: L8")
	case "FEAT":
		c.multiline(211, []string{"UTF8", "SIZE", "MDTM", "MLSD", "MLST type*;size*;modify*;perm*;"}, "Features")
	case "OPTS":
		c.reply(200, "OK")
	case "PWD", "XPWD":
		c.reply(257, `"`+c.cwd+`"`)
	case "CWD", "XCWD":
		c.chdir(arg)
	case "CDUP", "XCUP":
		c.chdir("..")
	case "TYPE":
		c.reply(200, "使用二进制模式")
	case "MODE", "STRU":
		c.reply(200, "OK")
	case "NOOP":
		c.reply(200, "OK")
	case "QUIT":
		c.reply(221, "再见")
		return false

	case "PASV":
		c.passiveMode(false)
	case "EPSV":
		c.passiveMode(true)
	case "PORT", "EPRT":
		c.reply(502, "只支持被动模式（PASV/EPSV）")

	case "LIST", "NLST", "MLSD":
		c.list(cmd, arg)
	case "RETR":
		c.retr(arg)
	case "STOR":
		c.stor(arg)
	case "APPE":
		c.reply(502, "不支持追加写入")
	case "DELE":
		c.dele(arg)
	case "MKD", "XMKD":
		c.mkd(arg)
	case "RMD", "XRMD":
		c.rmd(arg)
	case "SIZE":
		c.size(arg)
	case "MDTM":
		c.mdtm(arg)
	case "RNFR":
		c.renameFrom = arg
		c.reply(350, "请发送 RNTO")
	case "RNTO":
		c.renameTo(arg)
	case "REST":
		c.reply(502, "不支持断点续传")
	case "STAT":
		c.reply(211, "S3Bridge FTP")
	case "AUTH", "PBSZ", "PROT":
		c.reply(502, "不支持 TLS，请使用普通 FTP")
	default:
		c.reply(502, "命令未实现: "+cmd)
	}
	return true
}

// ---------- 回复 ----------

func (c *conn) reply(code int, msg string) {
	fmt.Fprintf(c.w, "%d %s\r\n", code, msg)
	_ = c.w.Flush()
}

func (c *conn) multiline(code int, lines []string, title string) {
	fmt.Fprintf(c.w, "%d-%s\r\n", code, title)
	for _, l := range lines {
		fmt.Fprintf(c.w, " %s\r\n", l)
	}
	fmt.Fprintf(c.w, "%d End\r\n", code)
	_ = c.w.Flush()
}

func (c *conn) requireLogin() bool {
	if c.principal == nil {
		c.reply(530, "请先登录")
		return false
	}
	return true
}

func (c *conn) can(p store.Permission) bool {
	if c.principal != nil && c.principal.Can(p) {
		return true
	}
	c.reply(550, "该 API Key 没有权限")
	return false
}

// ---------- 路径 ----------

// abs 把客户端路径解析为绝对虚拟路径 /bucket/key。
func (c *conn) abs(arg string) string {
	if arg == "" {
		return c.cwd
	}
	p := arg
	if !strings.HasPrefix(p, "/") {
		p = path.Join(c.cwd, p)
	}
	clean := path.Clean("/" + strings.TrimPrefix(p, "/"))
	if clean == "/." {
		clean = "/"
	}
	return clean
}

// split 把绝对虚拟路径拆成桶与对象键。
func split(abs string) (bucket, key string) {
	rest := strings.TrimPrefix(abs, "/")
	if i := strings.IndexByte(rest, '/'); i >= 0 {
		return rest[:i], rest[i+1:]
	}
	return rest, ""
}

// filePath 解析文件操作目标。客户端在根目录直接 STOR/RETR 一个裸文件名时，
// 归入 default 桶，避免「必须先 CWD 再传」这种反直觉行为。
func (c *conn) filePath(arg string) (bucket, key string) {
	bucket, key = split(c.abs(arg))
	if bucket != "" && key == "" {
		return "default", bucket
	}
	return bucket, key
}

func (c *conn) chdir(arg string) {
	target := c.abs(arg)
	if target != "/" {
		bucket, key := split(target)
		if !c.bucketExists(bucket) {
			c.reply(550, "目录不存在")
			return
		}
		_ = key
	}
	c.cwd = target
	c.reply(250, "目录已切换")
}

func (c *conn) bucketExists(name string) bool {
	for _, b := range c.opts.Uploads.BucketNames() {
		if b == name {
			return true
		}
	}
	return false
}

// ---------- 列表 ----------

func (c *conn) list(cmd, arg string) {
	if !c.requireLogin() || !c.can(store.PermUploadRead) {
		return
	}
	// 忽略 -la 之类的参数
	for strings.HasPrefix(arg, "-") {
		_, rest, _ := strings.Cut(arg, " ")
		arg = strings.TrimSpace(rest)
	}
	target := c.abs(arg)
	bucket, key := split(target)

	dc, err := c.openData()
	if err != nil {
		c.reply(425, "无法建立数据连接")
		return
	}
	defer dc.Close()
	c.reply(150, "开始传输列表")

	w := bufio.NewWriter(dc)
	if bucket == "" {
		names := c.opts.Uploads.BucketNames()
		sort.Strings(names)
		for _, name := range names {
			c.writeEntry(w, cmd, name, true, 0, time.Time{})
		}
	} else {
		res, err := c.opts.Uploads.List(upload.ListFilter{
			Bucket: bucket, OwnerID: c.principal.UserID, IsAdmin: c.principal.IsAdmin(),
		})
		if err != nil {
			c.reply(550, err.Error())
			return
		}
		base := ""
		if key != "" {
			base = strings.TrimSuffix(key, "/") + "/"
		}
		seen := map[string]bool{}
		for _, o := range res.Items {
			if !strings.HasPrefix(o.ObjectKey, base) {
				continue
			}
			rest := strings.TrimPrefix(o.ObjectKey, base)
			if rest == "" {
				continue
			}
			if i := strings.IndexByte(rest, '/'); i >= 0 {
				dir := rest[:i]
				if !seen[dir] {
					seen[dir] = true
					c.writeEntry(w, cmd, dir, true, 0, time.Time{})
				}
				continue
			}
			c.writeEntry(w, cmd, o.FileName, false, o.Size, o.CreatedAt)
		}
	}
	_ = w.Flush()
	_ = dc.Close()
	c.reply(226, "传输完成")
}

func (c *conn) writeEntry(w io.Writer, cmd, name string, dir bool, size int64, mod time.Time) {
	switch cmd {
	case "NLST":
		fmt.Fprintf(w, "%s\r\n", name)
	case "MLSD":
		typ := "file"
		if dir {
			typ = "dir"
		}
		fmt.Fprintf(w, "type=%s;size=%d;modify=%s; %s\r\n", typ, size,
			mod.Format("20060102150405"), name)
	default:
		mode := "-rw-r--r--"
		if dir {
			mode = "drwxr-xr-x"
		}
		ts := mod
		if ts.IsZero() {
			ts = time.Now()
		}
		fmt.Fprintf(w, "%s 1 owner group %12d %s %s\r\n", mode, size,
			ts.Format("Jan _2 15:04"), name)
	}
}

// ---------- 传输 ----------

func (c *conn) retr(arg string) {
	if !c.requireLogin() || !c.can(store.PermUploadRead) {
		return
	}
	bucket, key := c.filePath(arg)
	if bucket == "" || key == "" {
		c.reply(550, "需要指定文件")
		return
	}
	obj, filePath, err := c.opts.Uploads.Get(bucket, key, c.principal.UserID, c.principal.IsAdmin())
	if err != nil {
		c.reply(550, "文件不存在")
		return
	}
	f, err := os.Open(filePath)
	if err != nil {
		c.reply(550, "文件数据不可用")
		return
	}
	defer f.Close()
	dc, err := c.openData()
	if err != nil {
		c.reply(425, "无法建立数据连接")
		return
	}
	defer dc.Close()
	c.reply(150, fmt.Sprintf("开始传输 %d 字节", obj.Size))
	if _, err := io.Copy(dc, f); err != nil {
		c.reply(426, "传输中断")
		return
	}
	_ = dc.Close()
	c.reply(226, "传输完成")
}

func (c *conn) stor(arg string) {
	if !c.requireLogin() || !c.can(store.PermUploadWrite) {
		return
	}
	bucket, key := c.filePath(arg)
	if bucket == "" || key == "" {
		c.reply(550, "需要指定文件名")
		return
	}
	dc, err := c.openData()
	if err != nil {
		c.reply(425, "无法建立数据连接")
		return
	}
	defer dc.Close()
	c.reply(150, "准备接收")
	if _, err := c.opts.Uploads.PutLocal(context.Background(), upload.PutRequest{
		Reader: dc, FileName: path.Base(key), MimeType: upload.MimeFromName(key),
		Bucket: bucket, ObjectKey: key, Overwrite: true,
		OwnerID: c.principal.UserID, IsAdmin: c.principal.IsAdmin(),
	}); err != nil {
		c.reply(550, "写入失败: "+err.Error())
		return
	}
	_ = dc.Close()
	c.reply(226, "上传完成")
}

func (c *conn) dele(arg string) {
	if !c.requireLogin() || !c.can(store.PermUploadWrite) {
		return
	}
	bucket, key := c.filePath(arg)
	if bucket == "" || key == "" {
		c.reply(550, "需要指定文件")
		return
	}
	if err := c.opts.Uploads.Delete(bucket, key, c.principal.UserID, c.principal.IsAdmin()); err != nil {
		c.reply(550, "删除失败: "+err.Error())
		return
	}
	c.reply(250, "已删除")
}

func (c *conn) mkd(arg string) {
	if !c.requireLogin() || !c.can(store.PermUploadWrite) {
		return
	}
	bucket, key := c.filePath(arg)
	if bucket == "" {
		c.reply(550, "需要指定目录名")
		return
	}
	if key == "" {
		if err := c.opts.Uploads.CreateBucket(bucket, c.principal.UserID); err != nil {
			c.reply(550, "创建失败: "+err.Error())
			return
		}
	}
	c.reply(257, `"`+c.abs(arg)+`" 已创建`)
}

func (c *conn) rmd(arg string) {
	if !c.requireLogin() || !c.can(store.PermUploadWrite) {
		return
	}
	bucket, key := c.filePath(arg)
	if bucket == "" || key == "" {
		c.reply(550, "不能删除根目录或存储桶（请用后台）")
		return
	}
	res, err := c.opts.Uploads.List(upload.ListFilter{
		Bucket: bucket, OwnerID: c.principal.UserID, IsAdmin: c.principal.IsAdmin(),
	})
	if err != nil {
		c.reply(550, err.Error())
		return
	}
	n := 0
	for _, o := range res.Items {
		if !strings.HasPrefix(o.ObjectKey, key+"/") {
			continue
		}
		if err := c.opts.Uploads.Delete(bucket, o.ObjectKey, c.principal.UserID, c.principal.IsAdmin()); err != nil {
			c.reply(550, "删除失败: "+err.Error())
			return
		}
		n++
	}
	if n == 0 {
		c.reply(550, "目录不存在或为空")
		return
	}
	c.reply(250, "已删除")
}

func (c *conn) size(arg string) {
	if !c.requireLogin() || !c.can(store.PermUploadRead) {
		return
	}
	bucket, key := c.filePath(arg)
	obj, _, err := c.opts.Uploads.Get(bucket, key, c.principal.UserID, c.principal.IsAdmin())
	if err != nil {
		c.reply(550, "文件不存在")
		return
	}
	c.reply(213, strconv.FormatInt(obj.Size, 10))
}

func (c *conn) mdtm(arg string) {
	if !c.requireLogin() || !c.can(store.PermUploadRead) {
		return
	}
	bucket, key := c.filePath(arg)
	obj, _, err := c.opts.Uploads.Get(bucket, key, c.principal.UserID, c.principal.IsAdmin())
	if err != nil {
		c.reply(550, "文件不存在")
		return
	}
	c.reply(213, obj.CreatedAt.UTC().Format("20060102150405"))
}

func (c *conn) renameTo(arg string) {
	if !c.requireLogin() || !c.can(store.PermUploadWrite) {
		return
	}
	if c.renameFrom == "" {
		c.reply(503, "请先发送 RNFR")
		return
	}
	srcBucket, srcKey := split(c.abs(c.renameFrom))
	dstBucket, dstKey := split(c.abs(arg))
	obj, filePath, err := c.opts.Uploads.Get(srcBucket, srcKey, c.principal.UserID, c.principal.IsAdmin())
	if err != nil {
		c.reply(550, "源文件不存在")
		return
	}
	f, err := os.Open(filePath)
	if err != nil {
		c.reply(550, "源文件数据不可用")
		return
	}
	defer f.Close()
	if _, err := c.opts.Uploads.PutLocal(context.Background(), upload.PutRequest{
		Reader: f, FileName: path.Base(dstKey), MimeType: obj.MimeType,
		Bucket: dstBucket, ObjectKey: dstKey, Overwrite: true,
		OwnerID: c.principal.UserID, IsAdmin: c.principal.IsAdmin(),
	}); err != nil {
		c.reply(550, "重命名失败: "+err.Error())
		return
	}
	if err := c.opts.Uploads.Delete(srcBucket, srcKey, c.principal.UserID, c.principal.IsAdmin()); err != nil {
		c.reply(550, "复制成功但删除源文件失败")
		return
	}
	c.renameFrom = ""
	c.reply(250, "重命名完成")
}

// ---------- 被动模式 ----------

func (c *conn) openData() (net.Conn, error) {
	if c.passive == nil {
		return nil, errors.New("请先发送 PASV/EPSV")
	}
	if tl, ok := c.passive.(*net.TCPListener); ok {
		_ = tl.SetDeadline(time.Now().Add(30 * time.Second))
	}
	dc, err := c.passive.Accept()
	_ = c.passive.Close()
	c.passive = nil
	if err != nil {
		return nil, err
	}
	_ = dc.SetDeadline(time.Now().Add(5 * time.Minute))
	return dc, nil
}

func (c *conn) passiveMode(extended bool) {
	if c.passive != nil {
		_ = c.passive.Close()
		c.passive = nil
	}
	// 监听本机地址：控制连接是哪个网卡进来的，数据也用同一张网卡。
	local := c.c.LocalAddr().(*net.TCPAddr)
	ln, err := c.ports.listen(local.IP.String())
	if err != nil {
		c.reply(425, "无法开启被动端口: "+err.Error())
		return
	}
	c.passive = ln
	port := ln.Addr().(*net.TCPAddr).Port
	if extended {
		c.reply(229, fmt.Sprintf("Entering Extended Passive Mode (|||%d|)", port))
		return
	}
	ip := local.IP.To4()
	if v := net.ParseIP(strings.TrimSpace(c.opts.PublicIP)).To4(); v != nil {
		ip = v
	}
	if ip == nil {
		ip = net.IPv4(127, 0, 0, 1).To4()
	}
	c.reply(227, fmt.Sprintf("Entering Passive Mode (%d,%d,%d,%d,%d,%d)",
		ip[0], ip[1], ip[2], ip[3], port/256, port%256))
}
