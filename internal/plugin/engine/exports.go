package engine

import (
	"reflect"
	"strings"
	"sync"

	"github.com/Mr-losfer/s3bridge/internal/plugin/sdk"
	"github.com/traefik/yaegi/interp"
	"github.com/traefik/yaegi/stdlib"
)

// exports 是解释器可见的 SDK 符号表。
//
// 只导出必要的东西：类型用 (*T)(nil) 表达，函数直接传函数值。
// 类型一旦导出，它的方法集会自动可用（如 Client.Get / Client.Do）。
var exports = interp.Exports{
	SDKPath + "/sdk": {
		// 常量
		"Version":   reflect.ValueOf(sdk.Version),
		"MaxTraces": reflect.ValueOf(sdk.MaxTraces),

		// 类型
		"Config":     reflect.ValueOf((*sdk.Config)(nil)),
		"Input":      reflect.ValueOf((*sdk.Input)(nil)),
		"Output":     reflect.ValueOf((*sdk.Output)(nil)),
		"UploadFunc": reflect.ValueOf((*sdk.UploadFunc)(nil)),
		"CheckFunc":  reflect.ValueOf((*sdk.CheckFunc)(nil)),
		"Client":     reflect.ValueOf((*sdk.Client)(nil)),
		"Result":     reflect.ValueOf((*sdk.Result)(nil)),
		"Trace":      reflect.ValueOf((*sdk.Trace)(nil)),
		"Collector":  reflect.ValueOf((*sdk.Collector)(nil)),

		// 入口与客户端
		"Serve":           reflect.ValueOf(sdk.Serve),
		"Handlers":        reflect.ValueOf(sdk.Handlers),
		"NewClient":       reflect.ValueOf(sdk.NewClient),
		"NewClientFrom":   reflect.ValueOf(sdk.NewClientFrom),
		"MultipartStream": reflect.ValueOf(sdk.MultipartStream),
		"NewCollector":    reflect.ValueOf(sdk.NewCollector),
		"WithCollector":   reflect.ValueOf(sdk.WithCollector),
		"CollectorFrom":   reflect.ValueOf(sdk.CollectorFrom),

		// 加解密与编码
		"DeriveKey":     reflect.ValueOf(sdk.DeriveKey),
		"AESGCMSeal":    reflect.ValueOf(sdk.AESGCMSeal),
		"AESGCMOpen":    reflect.ValueOf(sdk.AESGCMOpen),
		"HMACSHA256Hex": reflect.ValueOf(sdk.HMACSHA256Hex),
		"SHA256Hex":     reflect.ValueOf(sdk.SHA256Hex),
		"MD5Hex":        reflect.ValueOf(sdk.MD5Hex),
		"RandHex":       reflect.ValueOf(sdk.RandHex),
		"Base64":        reflect.ValueOf(sdk.Base64),
		"UnBase64":      reflect.ValueOf(sdk.UnBase64),
		"JWTPayload":    reflect.ValueOf(sdk.JWTPayload),
	},
}

var stdOnce sync.Once
var stdPaths map[string]bool

// stdlibPaths 返回标准库的 import 路径集合，用于提前拦下第三方依赖。
func stdlibPaths() map[string]bool {
	stdOnce.Do(func() {
		stdPaths = make(map[string]bool, len(stdlib.Symbols))
		for key := range stdlib.Symbols {
			// 键形如 "net/http/http"：最后一段是包名，前面才是 import 路径。
			if i := strings.LastIndex(key, "/"); i > 0 {
				stdPaths[key[:i]] = true
			} else {
				stdPaths[key] = true
			}
		}
	})
	return stdPaths
}
