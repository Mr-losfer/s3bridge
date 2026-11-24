package upload

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/Mr-losfer/s3bridge/internal/store"
)

// LinkSigner 为对象生成无需登录即可访问的直链。
//
// 直链只在签名有效期内可用，因此不需要把桶设为公开读；链接里带的是
// bucket/key/到期时间与 HMAC，服务端校验时不必回查状态。
// 之所以需要直链：<img>/<video> 无法附加 Authorization 头，而视频拖动
// 进度条还要求服务端支持 Range，只有走同源的 /f/ 路径才能同时满足。
type LinkSigner struct {
	key []byte
	ttl time.Duration
}

// NewLinkSigner 创建签名器；ttl <= 0 时默认 7 天。
func NewLinkSigner(masterKey []byte, ttl time.Duration) *LinkSigner {
	if ttl <= 0 {
		ttl = 7 * 24 * time.Hour
	}
	if len(masterKey) == 0 {
		return nil
	}
	return &LinkSigner{key: masterKey, ttl: ttl}
}

// Path 返回带签名与到期时间的相对直链。
func (l *LinkSigner) Path(bucket, key string) string {
	if l == nil {
		return ""
	}
	exp := time.Now().Add(l.ttl).Unix()
	u := url.URL{Path: "/f/" + bucket + "/" + key}
	q := url.Values{}
	q.Set("e", strconv.FormatInt(exp, 10))
	q.Set("s", l.sign(bucket, key, exp))
	u.RawQuery = q.Encode()
	return u.String()
}

// Verify 校验直链签名。exp 为 Unix 秒，过期即拒绝。
func (l *LinkSigner) Verify(bucket, key string, exp int64, sig string) bool {
	if l == nil || exp <= 0 || sig == "" {
		return false
	}
	if time.Now().Unix() > exp {
		return false
	}
	want, err := base64.RawURLEncoding.DecodeString(l.sign(bucket, key, exp))
	if err != nil {
		return false
	}
	got, err := base64.RawURLEncoding.DecodeString(sig)
	if err != nil {
		return false
	}
	return hmac.Equal(want, got)
}

func (l *LinkSigner) sign(bucket, key string, exp int64) string {
	mac := hmac.New(sha256.New, l.key)
	// 前缀固定为 f，避免同一把主密钥的签名被其它用途复用。
	mac.Write([]byte("f\n" + bucket + "\n" + key + "\n" + strconv.FormatInt(exp, 10)))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// Decorate 填充只在接口响应里使用的展示字段。
//
// 预览优先用上游直链：浏览器直接连第三方，主服务不转发字节，
// 大图与视频不会占用本机带宽；上游链接缺失或临近过期时才回退到本站直链。
// 已被巡检判定失效的对象不给上游地址：让前端改走 /f/，由 /f/ 统一吐失效占位图，
// 否则页面上会是一张破图（上游可能已经 404，浏览器也不会告诉我们原因）。
func (s *Service) Decorate(o *store.Object) {
	if o == nil {
		return
	}
	o.PreviewURL, o.PreviewNoReferer = "", false
	o.Invalid = o.Validity.Status == store.ValidityInvalid
	if !o.Invalid {
		if u, ok := UsableRemoteURL(o); ok {
			o.PreviewURL = u
			o.PreviewNoReferer = true
		}
	}
	o.DirectPath = ""
	if s.links != nil {
		o.DirectPath = s.links.Path(o.Bucket, o.ObjectKey)
	}
}

// UsableRemoteURL 判断上游链接是否还能直接用于预览/跳转。
// 提前一分钟视为过期，避免浏览器拿到已经失效的签名地址。
func UsableRemoteURL(o *store.Object) (string, bool) {
	if o == nil || strings.TrimSpace(o.RemoteURL) == "" {
		return "", false
	}
	if o.URLExpireAt != nil && time.Now().After(o.URLExpireAt.Add(-time.Minute)) {
		return "", false
	}
	return o.RemoteURL, true
}
