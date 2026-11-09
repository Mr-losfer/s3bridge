package updater

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"runtime"
	"strings"
	"testing"
)

type mockReleaseOpts struct {
	Tag          string
	Body         string
	Archive      []byte
	Checksums    string
	Status       int
	OmitAsset    bool
	OmitChecksum bool
	BadAssetHost bool
	OnRequest    func()
}

func newMockReleaseServer(t *testing.T, o mockReleaseOpts) *httptest.Server {
	t.Helper()
	assetName := AssetName(runtime.GOOS, runtime.GOARCH)
	var srv *httptest.Server
	mux := http.NewServeMux()

	mux.HandleFunc("/repos/owner/repo/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if o.OnRequest != nil {
			o.OnRequest()
		}
		if got := r.Header.Get("User-Agent"); got == "" {
			t.Errorf("缺少 User-Agent（GitHub API 强制要求）")
		}
		if o.Status != 0 {
			w.WriteHeader(o.Status)
			_, _ = w.Write([]byte(`{"message":"rate limited"}`))
			return
		}
		assets := []map[string]any{}
		assetURL := srv.URL + "/download/" + assetName
		if o.BadAssetHost {
			assetURL = "http://169.254.169.254/latest/meta-data/"
		}
		if !o.OmitAsset {
			assets = append(assets, map[string]any{
				"name": assetName, "browser_download_url": assetURL,
				"size": len(o.Archive),
			})
		}
		if !o.OmitChecksum {
			assets = append(assets, map[string]any{
				"name": "checksums.txt", "browser_download_url": srv.URL + "/download/checksums.txt",
				"size": len(o.Checksums),
			})
		}
		body := map[string]any{
			"tag_name": o.Tag, "name": "S3Bridge " + o.Tag, "body": o.Body,
			"published_at": "2026-11-09T10:00:00Z", "html_url": "https://github.com/owner/repo/releases/tag/" + o.Tag,
			"prerelease": false, "draft": false, "assets": assets,
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(body)
	})
	mux.HandleFunc("/download/"+assetName, func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(o.Archive)
	})
	mux.HandleFunc("/download/checksums.txt", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(o.Checksums))
	})
	srv = httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

func TestFetchLatest(t *testing.T) {
	srv := newMockReleaseServer(t, mockReleaseOpts{
		Tag: "v1.1.0", Body: sampleChangelog, Archive: []byte("archive"),
		Checksums: "abc  s3bridge_linux_amd64.tar.gz\n",
	})
	c, err := NewClient(srv.URL, "owner/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := c.FetchLatest(context.Background(), runtime.GOOS, runtime.GOARCH)
	if err != nil {
		t.Fatalf("FetchLatest: %v", err)
	}
	if rel.Tag != "v1.1.0" || rel.AssetName != AssetName(runtime.GOOS, runtime.GOARCH) {
		t.Fatalf("解析结果不符：%+v", rel)
	}
	if rel.HighlightCount == 0 || len(rel.Notes) == 0 {
		t.Fatalf("更新要点未解析或没有重点标注：%+v", rel)
	}
	if !strings.HasSuffix(rel.ChecksumURL, "/download/checksums.txt") {
		t.Fatalf("校验文件地址错误：%s", rel.ChecksumURL)
	}
}

func TestFetchLatestErrors(t *testing.T) {
	cases := []struct {
		name string
		opts mockReleaseOpts
		want string
	}{
		{"无 Release", mockReleaseOpts{Status: 404}, ErrNoRelease.Error()},
		{"API 限流", mockReleaseOpts{Status: 403}, "限流"},
		{"缺少平台资产", mockReleaseOpts{Tag: "v1.1.0", OmitAsset: true}, "缺少当前平台资产"},
		{"缺少校验文件", mockReleaseOpts{Tag: "v1.1.0", Archive: []byte("x"), OmitChecksum: true}, "缺少 checksums.txt"},
		{"资产域名非法", mockReleaseOpts{Tag: "v1.1.0", Archive: []byte("x"), Checksums: "x", BadAssetHost: true}, "白名单"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := newMockReleaseServer(t, tc.opts)
			c, err := NewClient(srv.URL, "owner/repo", "")
			if err != nil {
				t.Fatal(err)
			}
			_, err = c.FetchLatest(context.Background(), runtime.GOOS, runtime.GOARCH)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("期望错误含 %q，实际 %v", tc.want, err)
			}
		})
	}
}

func TestClientRejectsInvalidConfig(t *testing.T) {
	for _, repo := range []string{"", "owner", "owner/repo/../../admin", "owner/repo?x=1"} {
		if _, err := NewClient("https://api.github.com", repo, ""); err == nil {
			t.Fatalf("仓库 %q 应被拒绝", repo)
		}
	}
	if _, err := NewClient("ftp://example.com", "owner/repo", ""); err == nil {
		t.Fatal("非 HTTP(S) API 地址应被拒绝")
	}
}

func TestDownloadSizeLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(strings.Repeat("x", 4096)))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, "owner/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Download(context.Background(), srv.URL+"/big", 1024, nil); err == nil {
		t.Fatal("超过大小上限应报错")
	}
	if _, err := c.Download(context.Background(), "https://169.254.169.254/x", 1024, nil); err == nil {
		t.Fatal("非白名单域名应被拒绝")
	}
}

func TestFetchChecksums(t *testing.T) {
	digest := strings.Repeat("a", 64)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, "%s  *s3bridge_linux_amd64.tar.gz\ninvalid line\n", strings.ToUpper(digest))
	}))
	defer srv.Close()
	c, err := NewClient(srv.URL, "owner/repo", "")
	if err != nil {
		t.Fatal(err)
	}
	sums, err := c.FetchChecksums(context.Background(), srv.URL+"/checksums.txt")
	if err != nil {
		t.Fatal(err)
	}
	if got := sums["s3bridge_linux_amd64.tar.gz"]; got != digest {
		t.Fatalf("校验值解析错误：%q", got)
	}
}
