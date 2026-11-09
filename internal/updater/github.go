package updater

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strings"
	"time"
)

var (
	// ErrNoRelease 表示仓库还没有发布过 Release。
	ErrNoRelease = errors.New("仓库暂无正式 Release")
	repoPattern  = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
)

// defaultAllowedHosts 是允许下载 Release 资产的域名白名单。
// GitHub 的 browser_download_url 会 302 到对象存储，重定向同样走这个白名单。
var defaultAllowedHosts = []string{
	"github.com", "api.github.com", "objects.githubusercontent.com",
	"release-assets.githubusercontent.com", "codeload.github.com",
}

// Release 是给前端与 MCP 使用的标准化发布信息。
type Release struct {
	Tag            string        `json:"tag"`
	Name           string        `json:"name"`
	PublishedAt    string        `json:"publishedAt"`
	URL            string        `json:"url"`
	Prerelease     bool          `json:"prerelease"`
	Notes          []NoteSection `json:"notes"`
	HighlightCount int           `json:"highlightCount"`
	AssetName      string        `json:"assetName"`
	AssetURL       string        `json:"assetURL"`
	AssetSize      int64         `json:"assetSize"`
	ChecksumURL    string        `json:"checksumURL"`
}

type ghAsset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Size               int64  `json:"size"`
}

type ghRelease struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	Body        string    `json:"body"`
	PublishedAt string    `json:"published_at"`
	HTMLURL     string    `json:"html_url"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Assets      []ghAsset `json:"assets"`
}

// Client 负责访问 GitHub Releases API 与下载发布资产。
type Client struct {
	http      *http.Client
	baseURL   string
	repo      string
	token     string
	extraHost string
	proxy     string // 形如 https://gh-proxy.com，为空表示直连
	proxyHost string
}

// NewClient 创建客户端。baseURL 可指向本地 mock（测试用），token 可为空，
// proxy 用于加速 GitHub 访问（如 https://gh-proxy.com），为空则直连。
func NewClient(baseURL, repo, token, proxy string) (*Client, error) {
	repo = strings.Trim(strings.TrimSpace(repo), "/")
	if !repoPattern.MatchString(repo) {
		return nil, fmt.Errorf("更新仓库格式非法，应为 owner/repo：%q", repo)
	}
	u, err := url.Parse(strings.TrimRight(strings.TrimSpace(baseURL), "/"))
	if err != nil || u.Host == "" || (u.Scheme != "https" && u.Scheme != "http") {
		return nil, fmt.Errorf("Releases API 地址非法：%q", baseURL)
	}
	proxy = strings.TrimRight(strings.TrimSpace(proxy), "/")
	proxyHost := ""
	if proxy != "" {
		pu, err := url.Parse(proxy)
		if err != nil || pu.Host == "" || (pu.Scheme != "https" && pu.Scheme != "http") {
			return nil, fmt.Errorf("更新代理地址非法：%q", proxy)
		}
		proxyHost = strings.ToLower(pu.Hostname())
	}
	c := &Client{baseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"), repo: repo,
		token: strings.TrimSpace(token), extraHost: strings.ToLower(u.Hostname()),
		proxy: proxy, proxyHost: proxyHost}
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 8
	c.http = &http.Client{
		Timeout: 60 * time.Second,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 8 {
				return errors.New("重定向次数过多")
			}
			return c.checkHost(req.URL)
		},
		Transport: transport,
	}
	return c, nil
}

func (c *Client) checkHost(u *url.URL) error {
	if u.Scheme != "https" && u.Scheme != "http" {
		return fmt.Errorf("不允许的下载协议：%s", u.Scheme)
	}
	host := strings.ToLower(u.Hostname())
	for _, h := range defaultAllowedHosts {
		if host == h {
			return nil
		}
	}
	if host == c.extraHost {
		return nil // 自定义 API 地址（本地测试 / 自建镜像）
	}
	if c.proxyHost != "" && host == c.proxyHost {
		return nil // 更新代理（gh-proxy 等）
	}
	return fmt.Errorf("下载域名不在白名单内：%s", host)
}

func (c *Client) allowedURL(raw string) (*url.URL, error) {
	u, err := url.Parse(raw)
	if err != nil {
		return nil, fmt.Errorf("资产地址非法：%w", err)
	}
	if err := c.checkHost(u); err != nil {
		return nil, err
	}
	return u, nil
}

// candidates 返回按优先级排列的请求地址：配置了代理时先走代理，再回退直连。
// 只代理 GitHub 官方域名，本地 mock / 自建镜像保持原样，避免把内网地址发到公网代理。
func (c *Client) candidates(raw string) []string {
	out := make([]string, 0, 2)
	u, err := url.Parse(raw)
	if err == nil {
		host := strings.ToLower(u.Hostname())
		if c.proxy != "" && isGitHubHost(host) {
			out = append(out, c.proxy+"/"+raw)
		}
	}
	out = append(out, raw)
	return out
}

func isGitHubHost(host string) bool {
	switch host {
	case "github.com", "api.github.com", "codeload.github.com",
		"objects.githubusercontent.com", "release-assets.githubusercontent.com":
		return true
	}
	return strings.HasSuffix(host, ".githubusercontent.com")
}

func (c *Client) getJSON(ctx context.Context, raw string) (body []byte, status int, via string, err error) {
	var lastErr error
	for _, target := range c.candidates(raw) {
		req, reqErr := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
		if reqErr != nil {
			return nil, 0, "", fmt.Errorf("构造检查请求: %w", reqErr)
		}
		req.Header.Set("Accept", "application/vnd.github+json")
		req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
		req.Header.Set("User-Agent", "s3bridge-updater")
		// Token 只发给 GitHub 官方 API，绝不透传给第三方代理。
		if c.token != "" && req.URL.Hostname() == c.extraHost {
			req.Header.Set("Authorization", "Bearer "+c.token)
		}
		resp, reqErr := c.http.Do(req)
		if reqErr != nil {
			lastErr = fmt.Errorf("%s: %w", req.URL.Hostname(), reqErr)
			continue
		}
		b, readErr := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
		_ = resp.Body.Close()
		if readErr != nil {
			lastErr = fmt.Errorf("%s 读取响应: %w", req.URL.Hostname(), readErr)
			continue
		}
		if resp.StatusCode == http.StatusOK {
			return b, resp.StatusCode, req.URL.Hostname(), nil
		}
		lastErr = fmt.Errorf("%s 返回 HTTP %d", req.URL.Hostname(), resp.StatusCode)
		status, via = resp.StatusCode, req.URL.Hostname()
	}
	return nil, status, via, lastErr
}

// FetchLatest 读取最新正式 Release，并匹配当前平台的资产。
func (c *Client) FetchLatest(ctx context.Context, goos, goarch string) (*Release, error) {
	endpoint := c.baseURL + "/repos/" + c.repo + "/releases/latest"
	body, status, via, err := c.getJSON(ctx, endpoint)
	if err != nil && status == 0 {
		return nil, fmt.Errorf("连接 Releases 接口失败: %w", err)
	}

	switch status {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, ErrNoRelease
	case http.StatusForbidden, http.StatusTooManyRequests:
		hint := "可配置 S3BRIDGE_UPDATE_TOKEN 提高额度"
		if c.proxy != "" {
			hint = fmt.Sprintf("已优先经代理 %s 访问；%s，也可更换 S3BRIDGE_UPDATE_PROXY", c.proxy, hint)
		}
		return nil, fmt.Errorf("GitHub API 触发限流（HTTP %d，最后一次经 %s）：%s", status, via, hint)
	default:
		return nil, fmt.Errorf("Releases 接口返回 HTTP %d（经 %s）", status, via)
	}
	var gr ghRelease
	if err := json.Unmarshal(body, &gr); err != nil {
		return nil, fmt.Errorf("解析 Release 响应: %w", err)
	}
	if gr.TagName == "" || gr.Draft {
		return nil, ErrNoRelease
	}

	want := AssetName(goos, goarch)
	var asset, checksum *ghAsset
	for i := range gr.Assets {
		a := &gr.Assets[i]
		switch a.Name {
		case want:
			asset = a
		case "checksums.txt":
			checksum = a
		}
	}
	if asset == nil || asset.BrowserDownloadURL == "" {
		return nil, fmt.Errorf("Release %s 缺少当前平台资产 %s", gr.TagName, want)
	}
	if checksum == nil || checksum.BrowserDownloadURL == "" {
		return nil, fmt.Errorf("Release %s 缺少 checksums.txt，出于安全考虑拒绝自动更新", gr.TagName)
	}
	if _, err := c.allowedURL(asset.BrowserDownloadURL); err != nil {
		return nil, err
	}
	if _, err := c.allowedURL(checksum.BrowserDownloadURL); err != nil {
		return nil, err
	}

	notes := ParseNotes(gr.Body, 8, 40)
	highlights := 0
	for _, s := range notes {
		for _, it := range s.Items {
			if it.Important {
				highlights++
			}
		}
	}
	return &Release{
		Tag: gr.TagName, Name: gr.Name, PublishedAt: gr.PublishedAt, URL: gr.HTMLURL,
		Prerelease: gr.Prerelease, Notes: notes, HighlightCount: highlights,
		AssetName: asset.Name, AssetURL: asset.BrowserDownloadURL,
		AssetSize: asset.Size, ChecksumURL: checksum.BrowserDownloadURL,
	}, nil
}

// Download 下载资产到内存，带大小上限与进度回调。
// 配置代理时优先经代理，失败（含代理 403/超时）自动回退直连。
func (c *Client) Download(ctx context.Context, rawURL string, limit int64, progress func(done, total int64)) ([]byte, error) {
	var lastErr error
	for _, target := range c.candidates(rawURL) {
		data, err := c.downloadOnce(ctx, target, limit, progress)
		if err == nil {
			return data, nil
		}
		lastErr = err
	}
	if lastErr == nil {
		lastErr = errors.New("下载资产失败")
	}
	if c.proxy != "" {
		return nil, fmt.Errorf("%w（已尝试代理 %s 与直连）", lastErr, c.proxy)
	}
	return nil, lastErr
}

func (c *Client) downloadOnce(ctx context.Context, rawURL string, limit int64, progress func(done, total int64)) ([]byte, error) {
	if _, err := c.allowedURL(rawURL); err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return nil, fmt.Errorf("构造下载请求: %w", err)
	}
	req.Header.Set("User-Agent", "s3bridge-updater")
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("下载资产失败: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("资产下载返回 HTTP %d", resp.StatusCode)
	}
	total := resp.ContentLength
	if total > limit {
		return nil, fmt.Errorf("资产体积 %d 超过上限 %d 字节", total, limit)
	}
	var buf bytes.Buffer
	src := io.LimitReader(resp.Body, limit+1)
	chunk := make([]byte, 32<<10)
	var done int64
	for {
		n, readErr := src.Read(chunk)
		if n > 0 {
			if int64(buf.Len())+int64(n) > limit {
				return nil, fmt.Errorf("资产超过大小上限 %d 字节", limit)
			}
			_, _ = buf.Write(chunk[:n])
			done += int64(n)
			if progress != nil {
				progress(done, total)
			}
		}
		if readErr == io.EOF {
			break
		}
		if readErr != nil {
			return nil, fmt.Errorf("读取下载内容: %w", readErr)
		}
	}
	return buf.Bytes(), nil
}

// FetchChecksums 解析 GoReleaser 生成的 checksums.txt。
func (c *Client) FetchChecksums(ctx context.Context, rawURL string) (map[string]string, error) {
	b, err := c.Download(ctx, rawURL, 1<<20, nil)
	if err != nil {
		return nil, err
	}
	out := map[string]string{}
	scanner := bufio.NewScanner(bytes.NewReader(b))
	for scanner.Scan() {
		fields := strings.Fields(scanner.Text())
		if len(fields) < 2 {
			continue
		}
		name := strings.TrimPrefix(fields[len(fields)-1], "*")
		if len(fields[0]) == 64 {
			out[name] = strings.ToLower(fields[0])
		}
	}
	if err := scanner.Err(); err != nil {
		return nil, fmt.Errorf("解析校验文件: %w", err)
	}
	if len(out) == 0 {
		return nil, errors.New("checksums.txt 内容为空")
	}
	return out, nil
}

// AssetName 与 .goreleaser.yml 的 archives.name_template 保持一致。
func AssetName(goos, goarch string) string {
	ext := "tar.gz"
	if goos == "windows" {
		ext = "zip"
	}
	return fmt.Sprintf("s3bridge_%s_%s.%s", goos, goarch, ext)
}
