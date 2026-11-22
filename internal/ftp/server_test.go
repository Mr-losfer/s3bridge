package ftp

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/auth"
	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/upload"
)

// TestFTP 用裸 TCP 客户端走一遍登录、列目录、上传、下载、删除，
// 覆盖 Finder/FileZilla 最常用的命令组合。
func TestFTP(t *testing.T) {
	dir := t.TempDir()
	st, err := store.Open(dir + "/state.json")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = st.Close() })

	authSvc := auth.NewService(st, []byte("ftp-test-master-key-0001"), []byte("ftp-test-session-key"), time.Hour)
	if err := authSvc.Bootstrap("admin", "admin-pass-123"); err != nil {
		t.Fatal(err)
	}
	key, secret, err := authSvc.CreateAPIKey("usr_admin", "ftp", nil, nil)
	if err != nil || key == nil {
		t.Fatalf("创建密钥: %v", err)
	}
	_ = secret

	if err := os.MkdirAll(dir+"/objects", 0o700); err != nil {
		t.Fatal(err)
	}
	ups := upload.New(st, nil, dir+"/objects", 32<<20, []byte("ftp-test-master-key-0001"), time.Hour)
	srv := New(Options{Addr: "127.0.0.1:0", Auth: authSvc, Uploads: ups})
	if err := srv.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = srv.Close() })

	c, err := net.Dial("tcp", srv.Addr())
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	r := bufio.NewReader(c)
	w := bufio.NewWriter(c)

	reply := func() string {
		t.Helper()
		line, err := r.ReadString('\n')
		if err != nil {
			t.Fatalf("读取回复失败: %v", err)
		}
		if len(line) >= 4 && line[3] == '-' {
			code := line[:3]
			for {
				next, err := r.ReadString('\n')
				if err != nil {
					t.Fatalf("读取多行回复失败: %v", err)
				}
				line += next
				if strings.HasPrefix(next, code+" ") {
					break
				}
			}
		}
		return line
	}
	send := func(format string, args ...any) {
		fmt.Fprintf(w, format+"\r\n", args...)
		_ = w.Flush()
	}
	expect := func(want string) string {
		t.Helper()
		got := reply()
		if !strings.HasPrefix(got, want) {
			t.Fatalf("期望 %s，实际 %q", want, got)
		}
		return got
	}
	// passive 发起 PASV 并连上数据端口。
	passive := func() net.Conn {
		t.Helper()
		send("PASV")
		line := expect("227")
		open := line[strings.Index(line, "(")+1 : strings.Index(line, ")")]
		parts := strings.Split(open, ",")
		if len(parts) != 6 {
			t.Fatalf("PASV 回复异常: %q", line)
		}
		host := strings.Join(parts[:4], ".")
		p1, _ := strconv.Atoi(parts[4])
		p2, _ := strconv.Atoi(parts[5])
		dc, err := net.Dial("tcp", net.JoinHostPort(host, strconv.Itoa(p1*256+p2)))
		if err != nil {
			t.Fatalf("连接数据端口: %v", err)
		}
		return dc
	}

	expect("220")
	send("USER anonymous")
	expect("331")
	send("PASS wrong-key")
	expect("530")
	send("PASS %s", key.AccessKey)
	// AccessKey 不是登录凭据，真正的密钥是 secret，这里应继续失败
	expect("530")

	send("USER admin")
	expect("331")
	send("PASS %s", secret)
	expect("230")

	// 根目录：只有一个默认桶
	dc := passive()
	send("LIST")
	expect("150")
	listOut, _ := bufio.NewReader(dc).ReadString('\n')
	_ = dc.Close()
	expect("226")
	if !strings.Contains(listOut, "default") {
		t.Fatalf("根目录应列出 default，实际 %q", listOut)
	}

	send("CWD /default")
	expect("250")
	send("PWD")
	if got := expect("257"); !strings.Contains(got, "/default") {
		t.Fatalf("PWD 异常: %q", got)
	}

	// 上传
	dc = passive()
	send("STOR hello.txt")
	expect("150")
	if _, err := dc.Write([]byte("hello ftp")); err != nil {
		t.Fatal(err)
	}
	_ = dc.Close()
	expect("226")

	send("SIZE hello.txt")
	if got := expect("213"); !strings.Contains(got, "9") {
		t.Fatalf("SIZE 异常: %q", got)
	}

	// 下载
	dc = passive()
	send("RETR hello.txt")
	expect("150")
	buf := make([]byte, 32)
	n, _ := dc.Read(buf)
	_ = dc.Close()
	expect("226")
	if string(buf[:n]) != "hello ftp" {
		t.Fatalf("下载内容异常: %q", buf[:n])
	}

	// 列表里能看到文件
	dc = passive()
	send("LIST")
	expect("150")
	all, _ := io.ReadAll(dc)
	_ = dc.Close()
	expect("226")
	if !strings.Contains(string(all), "hello.txt") {
		t.Fatalf("目录列表缺少文件: %q", all)
	}

	send("DELE hello.txt")
	expect("250")
	send("QUIT")
	expect("221")

	// 确认对象已删除
	if _, _, err := ups.Get("default", "hello.txt", "usr_admin", true); err == nil {
		t.Fatal("DELE 后对象应不存在")
	}
}
