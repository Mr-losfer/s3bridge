package api

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
	"github.com/Mr-losfer/s3bridge/internal/upload"
)

// upstreamClient 专供「本站中转」出站。
//
// 刻意不设 Client.Timeout：视频动辄几十分钟，整体超时会把正常传输掐断。
// 靠拨号、TLS 握手、响应头三段超时兜底，流的生命周期跟随请求 context，
// 客户端断开时上游连接会一起取消，不会留下泄漏的 goroutine。
var upstreamClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: 5 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          64,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   5 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		ExpectContinueTimeout: time.Second,
	},
	CheckRedirect: func(_ *http.Request, via []*http.Request) error {
		if len(via) >= 5 {
			return errors.New("上游重定向过多")
		}
		return nil
	},
}

// proxyUpstream 把上游内容转发给客户端，供「本站中转」模式使用。
//
// 只在两种情况下用得到：上游禁止直连（需要固定出口/无 Referer）、
// 或者调用方明确要求所有流量都走本站。默认模式是 302，不经过这里。
func (s *Server) proxyUpstream(w http.ResponseWriter, r *http.Request, obj *store.Object, upstream, cache string) {
	if !isHTTPURL(upstream) {
		// 上游地址不是 http(s)（插件返回了相对路径之类），只能回本地归档。
		s.serveLocal(w, r, obj, cache)
		return
	}
	req, err := http.NewRequestWithContext(r.Context(), r.Method, upstream, nil)
	if err != nil {
		s.serveLocal(w, r, obj, cache)
		return
	}
	// 刻意不带 Referer：上游多数开了防盗链，带本站来源必被拒；
	// 这也与页面级 no-referrer 的语义保持一致。
	if rg := r.Header.Get("Range"); rg != "" {
		req.Header.Set("Range", rg)
	}
	for _, h := range []string{"If-None-Match", "If-Modified-Since"} {
		if v := r.Header.Get(h); v != "" {
			req.Header.Set(h, v)
		}
	}

	resp, err := upstreamClient.Do(req)
	if err != nil {
		// 上游连不上时用本地归档兜底：既然当初归档过一份，就别让用户看破图。
		s.serveLocal(w, r, obj, cache)
		return
	}
	defer resp.Body.Close()

	// 上游明确报错（403/404）时原样透传，方便排查是签名字典还是防盗链问题；
	// 但 5xx 更像上游临时故障，本地有归档就先用着。
	if resp.StatusCode >= 500 {
		s.serveLocal(w, r, obj, cache)
		return
	}

	h := w.Header()
	for _, k := range []string{
		"Content-Type", "Content-Length", "Content-Range",
		"Accept-Ranges", "ETag", "Last-Modified", "Content-Disposition",
	} {
		if v := resp.Header.Get(k); v != "" {
			h.Set(k, v)
		}
	}
	if h.Get("Content-Type") == "" && obj.MimeType != "" {
		h.Set("Content-Type", obj.MimeType)
	}
	h.Set("Cache-Control", cache)
	h.Set("X-Access-Mode", store.AccessProxy)
	w.WriteHeader(resp.StatusCode)
	if r.Method == http.MethodHead {
		return
	}
	// 用固定缓冲区拷贝，避免 io.Copy 在大文件上反复分配。
	buf := make([]byte, 32<<10)
	_, _ = io.CopyBuffer(w, resp.Body, buf)
}

// serveLocal 输出本地归档副本；没有归档时退回 502。
func (s *Server) serveLocal(w http.ResponseWriter, r *http.Request, obj *store.Object, cache string) {
	_, path, err := s.core.Uploads.GetAny(obj.Bucket, obj.ObjectKey)
	if err == nil {
		if st, statErr := os.Stat(path); statErr == nil && st.Size() > 0 {
			w.Header().Set("Content-Disposition", "inline")
			if serveErr := upload.ServeFile(w, r, obj, path, cache); serveErr == nil {
				return
			}
		}
	}
	http.Error(w, "上游内容不可用", http.StatusBadGateway)
}

func isHTTPURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil {
		return false
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return false
	}
	return u.Host != ""
}
