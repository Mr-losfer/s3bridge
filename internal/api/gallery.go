package api

import (
	"encoding/base64"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// galleryItem 是相册墙上的一个格子，只携带渲染所需字段。
type galleryItem struct {
	ID        string             `json:"id"`
	Bucket    string             `json:"bucket"`
	Key       string             `json:"objectKey"`
	FileName  string             `json:"fileName"`
	MimeType  string             `json:"mimeType"`
	Size      int64              `json:"size"`
	Category  store.FileCategory `json:"category"`
	Width     int                `json:"width,omitempty"`
	Height    int                `json:"height,omitempty"`
	OwnerID   string             `json:"ownerId,omitempty"`
	PluginID  string             `json:"pluginId,omitempty"`
	CreatedAt time.Time          `json:"createdAt"`
}

type galleryBucket struct {
	Name    string `json:"name"`
	Objects int    `json:"objects"`
	Bytes   int64  `json:"bytes"`
}

// gallery 提供全站图片的流式分页查询。
//
// 采用「创建时间 + ID」游标而不是 offset：相册是无限滚动，
// offset 在深分页时会越来越慢，游标始终是 O(limit) 定位。
func (s *Server) gallery(c *Ctx) error {
	limit := c.IntQuery("limit", 48)
	if limit <= 0 || limit > 120 {
		limit = 48
	}
	bucket := c.Query("bucket", "")
	keyword := strings.ToLower(c.Query("q", ""))
	from, to := parseSince(c.Query("from", "")), parseSince(c.Query("to", ""))
	cursor, hasCursor := decodeCursor(c.Query("cursor", ""))

	owner := c.UserID()
	isAdmin := c.IsAdmin()

	var items []galleryItem
	buckets := map[string]*galleryBucket{}
	var total int
	var allBytes int64

	_ = s.core.Store.View(func(st *store.State) error {
		// 倒序遍历实现「最新在前」，并在同一遍完成计数与筛选。
		for i := len(st.Objects) - 1; i >= 0; i-- {
			o := st.Objects[i]
			if o.Category != store.CategoryImage {
				continue
			}
			if !isAdmin && o.OwnerID != owner {
				continue
			}
			if bucket != "" && o.Bucket != bucket {
				continue
			}
			if keyword != "" && !strings.Contains(strings.ToLower(o.FileName+o.ObjectKey), keyword) {
				continue
			}
			if !from.IsZero() && o.CreatedAt.Before(from) {
				continue
			}
			if !to.IsZero() && o.CreatedAt.After(to) {
				continue
			}
			total++
			allBytes += o.Size
			b := buckets[o.Bucket]
			if b == nil {
				b = &galleryBucket{Name: o.Bucket}
				buckets[o.Bucket] = b
			}
			b.Objects++
			b.Bytes += o.Size
			if hasCursor && !afterCursor(o, cursor) {
				continue
			}
			if len(items) >= limit {
				continue
			}
			items = append(items, galleryItem{
				ID: o.ID, Bucket: o.Bucket, Key: o.ObjectKey, FileName: o.FileName,
				MimeType: o.MimeType, Size: o.Size, Category: o.Category,
				OwnerID: o.OwnerID, PluginID: o.PluginID, CreatedAt: o.CreatedAt,
			})
		}
		return nil
	})
	if items == nil {
		items = []galleryItem{}
	}
	next := ""
	if len(items) == limit {
		last := items[len(items)-1]
		next = encodeCursor(last.CreatedAt, last.ID)
	}
	list := make([]galleryBucket, 0, len(buckets))
	for _, b := range buckets {
		list = append(list, *b)
	}
	sort.Slice(list, func(i, j int) bool { return list[i].Objects > list[j].Objects })

	c.JSON(http.StatusOK, map[string]any{
		"items": items, "nextCursor": next,
		"total": total, "totalBytes": allBytes, "buckets": list,
		"limit": limit,
	})
	return nil
}

// afterCursor 判断对象是否严格晚于游标位置（倒序遍历时即「更旧」）。
func afterCursor(o store.Object, cur cursorPos) bool {
	t := o.CreatedAt.UnixNano()
	if t != cur.T {
		return t < cur.T
	}
	return o.ID < cur.ID
}

type cursorPos struct {
	T  int64
	ID string
}

func encodeCursor(t time.Time, id string) string {
	raw := fmt.Sprintf("%d|%s", t.UnixNano(), id)
	return base64.RawURLEncoding.EncodeToString([]byte(raw))
}

func decodeCursor(s string) (cursorPos, bool) {
	if s == "" {
		return cursorPos{}, false
	}
	b, err := base64.RawURLEncoding.DecodeString(s)
	if err != nil {
		return cursorPos{}, false
	}
	parts := strings.SplitN(string(b), "|", 2)
	if len(parts) != 2 {
		return cursorPos{}, false
	}
	var t int64
	if _, err := fmt.Sscanf(parts[0], "%d", &t); err != nil {
		return cursorPos{}, false
	}
	return cursorPos{T: t, ID: parts[1]}, true
}

// objectThumb 返回缩略图；无法生成时回退到原图，保证相册永远有图可显示。
func (s *Server) objectThumb(c *Ctx) error {
	bucket, key := c.Query("bucket", ""), c.Query("key", "")
	if bucket == "" || key == "" {
		return errf(http.StatusBadRequest, "bad_request", "需要 bucket 与 key 参数")
	}
	width := c.IntQuery("w", 480)
	if width < 64 {
		width = 64
	}
	if width > 1600 {
		width = 1600
	}
	obj, path, err := s.core.Uploads.Get(bucket, key, c.UserID(), c.IsAdmin())
	if err != nil {
		return errf(http.StatusNotFound, "not_found", "%v", err)
	}
	if s.core.Thumbs != nil && obj.Category == store.CategoryImage {
		if thumbPath, err := s.core.Thumbs.Get(obj.SHA256, path, width); err == nil {
			etag := `"` + obj.SHA256 + fmt.Sprintf("@%d", width) + `"`
			if c.R.Header.Get("If-None-Match") == etag {
				c.W.Header().Set("ETag", etag)
				c.W.WriteHeader(http.StatusNotModified)
				return nil
			}
			c.W.Header().Set("Content-Type", "image/jpeg")
			c.W.Header().Set("Cache-Control", "private, max-age=604800, immutable")
			c.W.Header().Set("ETag", etag)
			http.ServeFile(c.W, c.R, thumbPath)
			return nil
		}
	}
	// 回退：直接给原图，避免前端出现破图。
	http.Redirect(c.W, c.R,
		"/api/objects/download?bucket="+url.QueryEscape(bucket)+"&key="+url.QueryEscape(key),
		http.StatusTemporaryRedirect)
	return nil
}

// parseSince 解析时间参数：既支持「最近 N 秒」也支持 RFC3339。
func parseSince(raw string) time.Time {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return time.Time{}
	}
	if n, err := strconv.Atoi(raw); err == nil && n > 0 {
		return time.Now().Add(-time.Duration(n) * time.Second)
	}
	if t, err := time.Parse(time.RFC3339, raw); err == nil {
		return t
	}
	return time.Time{}
}
