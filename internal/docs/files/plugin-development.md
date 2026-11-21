# 插件开发文档

> **v2.0 起插件就是 Go 程序。** 一个插件 = 一个文件夹，里面是 `plugin.json`（元信息与参数 Schema）
> 加任意多个 `.go` 文件。服务端在运行时用 `go build` 把它编译成独立可执行文件，以子进程方式调用。
> 因此你可以做**任意请求、任意 HTTP 方法（含 PUT/DELETE/multipart）、任意加解密与签名**，
> 而不只是「填一个请求模板」——没有模板能表达的逻辑，写代码就行。

本文档按「读一份能跑的代码 → 动手改 → 调试 → 发布」的顺序组织：

| 章节 | 你会得到 |
| --- | --- |
| [1. 五分钟写出第一个插件](#1-五分钟写出第一个插件) | 一个能真实上传的插件 |
| [2. 文件夹结构与协议](#2-文件夹结构与协议) | 插件由什么组成、主服务如何调用它 |
| [3. SDK 参考](#3-sdk-参考) | `Input`/`Output`/`Config`/`Client`/加解密工具 |
| [4. 参数 Schema](#4-参数-schemainpluginjson) | 后台表单怎么来的、敏感值怎么加密 |
| [5. 用 MCP 让 Agent 全程开发](#5-用-mcp-让-agent-全程开发) | 创建 → 写码 → 编译 → 调试 → 上传验证 |
| [6. 编译与运行时](#6-编译与运行时) | 离线编译、超时、崩溃隔离、空闲回收 |
| [7. 调试与请求追踪](#7-调试与请求追踪) | 看到插件到底发了什么请求 |
| [8. 图片有效性巡检](#8-图片有效性巡检) | 归档图片按周期自检，失效自动告警 |
| [9. 常见问题](#9-常见问题) | 编译失败、Token 过期、迁移 |

---

## 1. 五分钟写出第一个插件

后台「插件管理 → 创建插件」选择模板 **HTTP 表单上传**，ID 填 `my-uploader`，点创建。
服务端会生成文件夹并立即编译，成功后会跳到详情页的「插件源码」标签：

```
my-uploader/
├── plugin.json   # 参数 Schema 与元信息（系统维护，只读）
├── go.mod        # 独立模块，replace 指向服务端落盘的 SDK（系统维护）
└── main.go       # 你的代码
```

打开 `main.go`，把 `cfg["upload_url"]` 指向你的接口，保存即可生效。核心只有一个方法：

```go
func (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {
	f, err := os.Open(in.Path) // 文件已落盘，直接按路径读，不必 base64 传输
	if err != nil {
		return nil, fmt.Errorf("打开文件: %w", err)
	}
	defer f.Close()

	body, contentType := sdk.MultipartStream(cfg["file_field"], in.FileName, in.MimeType, f, nil)
	req, _ := http.NewRequestWithContext(ctx, http.MethodPost, cfg["upload_url"], body)
	req.Header.Set("Content-Type", contentType)
	req.Header.Set("Authorization", "Bearer "+cfg["token"])

	resp, err := sdk.NewClient(30 * time.Second).Do(req) // 用 sdk.Client 才有请求追踪
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	// …解析响应，返回标准输出
}
```

保存后后台会立刻编译并热切换，**不需要重启服务**。

---

## 2. 文件夹结构与协议

### 2.1 目录约定

| 文件 | 谁维护 | 说明 |
| --- | --- | --- |
| `plugin.json` | 系统 | 元信息 + 参数 Schema + 健康检测建议 + 巡检周期，只读（用 `plugin_manifest_update` 改） |
| `go.mod` / `go.sum` | 系统 | 独立模块，`replace github.com/Mr-losfer/s3bridge/sdk => {数据目录}/sdk/s3bridge-sdk` |
| `*.go` | 你 | 任意多个文件，至少一个包含 `func main()` |
| `*.md` / `*.txt` / `*.yaml` | 你 | 说明与配置样例，不参与编译 |

限制：单文件 ≤ 512 KB，总源码 ≤ 2 MB，最多 64 个文件；文件名不能带路径分隔符或以点开头。
编译产物写在 `.build/plugin`，不会出现在文件列表里。

### 2.2 进程协议

主服务以子进程方式启动编译产物，双方用**每行一条 JSON** 通信：

```
主服务 → 插件  {"id":"c1","method":"upload","config":{...},"input":{...},"timeoutSecs":60}
插件 → 主服务  {"id":"c1","ok":true,"result":{"url":"https://..."},"traces":[...]}
```

- `method` 只有三种：`ping`（握手）、`upload`、`check`。
- 同一插件的调用**串行**执行，插件里不需要自己加锁。
- 写 stdout 的只能是协议消息；**日志一律写 stderr**（会被收集用于崩溃诊断）。
- `sdk.Serve(plugin{})` 已经实现了这个循环，正常开发不用关心。

### 2.3 为什么不是 Go 的 `plugin` 包

`plugin` 包要求插件与主程序用完全相同的 Go 版本、相同的依赖版本编译，且只支持 Linux/macOS。
独立进程方案换来的是：跨平台、插件崩溃不会拖垮主服务、插件可用任意第三方库、单个插件超时可被强杀。

---

## 3. SDK 参考

导入路径：`github.com/Mr-losfer/s3bridge/sdk`

### 3.1 必须实现的接口

```go
type Uploader interface {
	// Upload 把文件转发到第三方并返回统一结果。
	Upload(ctx context.Context, in Input, cfg Config) (*Output, error)
	// Check 做无副作用探活，不得上传真实业务文件。
	Check(ctx context.Context, cfg Config) error
}
```

`main()` 里只需 `sdk.Serve(plugin{})`。

### 3.2 输入与输出

```go
type Input struct {
	FileName  string // 原始文件名
	MimeType  string // 客户端声明的 MIME
	Size      int64  // 字节数
	Path      string // 落盘绝对路径，直接 os.Open
	Bucket    string // 目标存储桶
	ObjectKey string // 归档对象键
	UserID    string // 上传者
}

type Output struct {
	URL         string         // 必填：第三方返回的可访问地址
	ObjectKey   string         // 远端对象键，留空则沿用本地
	FileName    string         // 留空则沿用原始文件名
	MimeType    string
	FileSize    int64
	URLExpireAt *time.Time     // 临时签名链接的到期时间，用于后台提醒
	Extra       map[string]any // 任意附加字段，会原样存进对象元数据
}
```

`URL` 是唯一的硬性要求：没有它，后台列表点不开原图。

### 3.3 带追踪的 HTTP 客户端

```go
c := sdk.NewClient(30 * time.Second)                             // 带超时
res, err := c.Bytes(ctx, http.MethodPut, url, headers, body)     // 原始字节
res, err := c.JSON(ctx, http.MethodPost, url, headers, in, &out) // JSON 进 JSON 出
resp, err := c.Do(req)                                           // 完全自控（流式、重定向策略）
```

用 `sdk.NewClient` 而不是 `http.DefaultClient` 的区别是：**每次出站请求都会被记录**，
后台调试台与 `plugin_debug` 能看到方法、URL、状态码、耗时、字节数与响应片段，
其中 `Authorization`、`Cookie`、`X-Api-Key` 等请求头和 `token`、`signature` 等 query 参数会被自动脱敏为 `***`。

单次调用最多保留 64 条追踪，防止死循环插件撑爆响应。

### 3.4 上传辅助

```go
body, contentType := sdk.MultipartStream("file", in.FileName, in.MimeType, f, map[string]string{
	"biz": "avatar",
})
```

流式构造 multipart，文件内容不会被整体读进内存，适合大文件。

### 3.5 加解密与编码

| 函数 | 用途 |
| --- | --- |
| `DeriveKey(secret) []byte` | 把任意口令派生为 32 字节 AES-256 密钥 |
| `AESGCMSeal(key, plain, aad)` / `AESGCMOpen(key, token, aad)` | AES-256-GCM 加解密（base64 含 nonce） |
| `HMACSHA256Hex(secret, msg)` | 接口签名 |
| `SHA256Hex(data)` / `MD5Hex(data)` | 摘要（兼容只认 MD5 的上游） |
| `RandHex(n)` | 随机串（nonce、追踪号） |
| `Base64` / `UnBase64` | 标准 base64 |
| `JWTPayload(token)` | 解析 JWT payload 读取 `exp` / `sub`，不校验签名 |

---

## 4. 参数 Schema（plugin.json）

后台参数表单不是手写的，而是由 `plugin.json` 的 `params` 渲染：

```json
{
  "schema": 2,
  "id": "my-uploader",
  "name": "我的上传器",
  "version": "1.0.0",
  "entry": "main.go",
  "timeoutSecs": 60,
  "priority": 100,
  "fileTypes": ["image"],
  "health": { "enabled": true, "intervalSecs": 300, "timeoutSecs": 8, "failThreshold": 2, "probeMode": "ping" },
  "validity": { "intervalHours": 72 },
  "params": [
    { "key": "upload_url", "label": "上传地址", "type": "url", "group": "连接", "required": true },
    { "key": "file_field", "label": "文件字段名", "type": "text", "group": "连接", "default": "file" },
    { "key": "token", "label": "Bearer Token", "type": "secret", "group": "身份凭据",
      "sensitive": true, "volatile": true, "expiresField": "token_expires_at" },
    { "key": "token_expires_at", "label": "Token 过期时间", "type": "datetime", "group": "身份凭据", "volatile": true }
  ]
}
```

字段类型：`text` `secret` `number` `bool` `select` `textarea` `url` `datetime` `duration`。

几个影响行为的标记：

| 标记 | 效果 |
| --- | --- |
| `required` | 缺失时插件状态为异常，调试第一步就失败 |
| `sensitive` | 后台脱敏展示，落盘前用主密钥 AES-256-GCM 加密，传给插件时已解密 |
| `volatile` + `expiresField` | 视为临时凭据：到期前 24 小时提醒，过期后直接判为异常 |
| `group` | 表单分组标题，省略归入「通用」 |

`validity.intervalHours` 为该插件产出的图片单独指定巡检周期（0 表示跟随系统设置）。

### 通过 MCP 修改清单

```jsonc
// plugin_manifest_update
{ "id": "my-uploader", "manifest": { /* 完整 plugin.json 内容，id 会被强制为当前插件 */ } }
```

---

## 5. 用 MCP 让 Agent 全程开发

连接 MCP 后（参见 [MCP 能力与工具清单](mcp.md)），Agent 可以不碰后台界面完成全部工作：

| 步骤 | 工具 | 说明 |
| --- | --- | --- |
| 1 看示例 | `plugin_files` + `plugin_file_read` | 读内置 `workbuddy` 的 `main.go`，就是一份真实可跑的参考实现 |
| 2 建插件 | `plugin_create` | `{id,name,template,files,params,priority,fileTypes}`，创建即编译 |
| 3 写代码 | `plugin_file_write` | 写单个文件并**自动重新编译**；失败会保留源码并返回编译器输出 |
| 4 试编译 | `plugin_validate` | 在临时目录编译一份源码，不落库、不影响运行中的插件 |
| 5 加文件 | `plugin_file_write` + `plugin_file_rename` / `plugin_file_delete` | 多文件拆分、改名（入口文件改名会同步 `entry`）、删除 |
| 6 改清单 | `plugin_manifest_update` | 加参数、改超时、改文件类型、改巡检周期 |
| 7 配参数 | `plugin_params_set` | 敏感值加密落盘 |
| 8 编译 | `plugin_build` | 手动触发，返回输出、耗时、产物 SHA-256 |
| 9 调试 | `plugin_debug` | 参数 → 凭据有效期 → 产物 → 自检，并带回请求追踪 |
| 10 真传 | `upload_test` | 用 base64 内容走一次真实上传 |
| 11 备份/迁移 | `plugin_export` | 导出全部源码与清单 |

典型的一轮修复循环：

```
plugin_file_write(id="my-uploader", name="main.go", content="…")
→ build.ok=false, output="main.go:23:2: undefined: sdk.HmacSHA256"
→ 按输出改一处
→ plugin_file_write(...) → build.ok=true
→ plugin_debug → 看到 PUT 请求 200
→ upload_test → 归档里出现该文件
```

Agent 不需要你告诉它 SDK 有哪些函数：`plugin_file_read(id="workbuddy")` 就是最好的示例，
`plugin_docs` 工具会返回本文档。

---

## 6. 编译与运行时

### 6.1 构建环境

- 需要 **Go 1.21+** 工具链在主服务所在主机上可用（`PATH` 里能找到 `go`）。
  未安装时服务仍可启动，但只能运行已编译好的 `.build/plugin`，后台会显示「未检测到 Go 工具链」。
- SDK 源码内嵌在二进制里，启动时落盘到 `{数据目录}/sdk/s3bridge-sdk`；
  插件 `go.mod` 通过 `replace` 指向它，因此**离线也能编译**（不访问网络）。
- 编译环境会清理外部干扰：固定 `CGO_ENABLED=0`、`GO111MODULE=on`、`GOTOOLCHAIN=local`、
  `GOFLAGS=-mod=mod`、`GOWORK=off`，`GOCACHE`/`GOMODCACHE`/`GOPATH` 全部落在 `{数据目录}/cache`。
- 引入第三方库时编译器会提示缺少 `go.sum`，服务端自动执行一次 `go mod tidy` 后重试（需要网络）。

### 6.2 运行时

| 行为 | 说明 |
| --- | --- |
| 启动 | 首次调用时懒编译；之后直接复用 `.build/plugin` |
| 握手 | 启动后先发 `ping`，确认产物可用，避免把坏二进制当成业务错误 |
| 并发 | 同一插件的调用串行，不同插件并行 |
| 超时 | `plugin.json` 的 `timeoutSecs`（默认 60，最大 600）；超时直接 kill 进程，下次调用自动重启 |
| 崩溃 | 进程退出后记录 stderr 末尾，状态置为异常并告警；下次调用重新拉起 |
| 回收 | 10 分钟无调用回收子进程，释放内存 |
| 热切换 | 编译成功后立即丢弃旧进程，下一次调用加载新代码 |

### 6.3 健康检测

`Check` 会被按插件独立配置的周期调用（默认 300 秒，可关闭）。失败不会立刻判死：
连续失败达到 `failThreshold` 才标记异常并生成告警，恢复后自动关闭未确认告警。
若插件声明了 `volatile` 凭据且过期时间未设置或已过期，即使 `Check` 成功也会被判为异常——
这是故意的：Token 过期是这类插件最常见的故障。

---

## 7. 调试与请求追踪

后台「调试与检测」标签或 MCP `plugin_debug` 会依次执行：

| 步骤 | 内容 |
| --- | --- |
| 参数校验 | 必填、类型、URL、枚举、正则、时间格式 |
| 临时凭据有效期 | 读取 `expiresField`，提示剩余时间或已过期 |
| 编译产物 | 产物是否存在，缺失时补编译 |
| 插件自检 | 真实调用 `Check`，返回耗时与产生的请求数 |

同时返回**请求追踪**：每次出站请求的方法、URL、状态码、耗时、请求/响应字节数、
脱敏后的请求头与响应片段（前 512 字节）。排查「上游到底返回了什么」不用再抓包。

调试是只读的：`Check` 不允许上传真实文件，调试也不会写归档。

---

## 8. 图片有效性巡检

上传归档到本站的图片，可能因为上游删除、签名过期、对象损坏而失效。系统内置巡检：

- **范围**：只检查本站归档（`localPath` 非空）的图片对象，远程直链不检查。
- **周期**：默认 **72 小时（3 天）** 一次，可在「系统设置 → 图片有效性巡检」改全局间隔，
  也可在插件 `plugin.json` 的 `validity.intervalHours` 单独覆盖。
- **方式**：向本机回环地址发 `HEAD`（上游不支持 HEAD 时退回 `Range: bytes=0-0` 的 GET），
  **只读响应头，绝不下载正文**。判定 `200/206` 且 `Content-Type` 为 `image/*` 即为正常。
- **失败处理**：连续失败达到阈值（默认 2 次）才标记 `invalid` 并生成告警；
  单次网络抖动只会累加计数。恢复后自动清零。
- **可见性**：相册与上传列表会标记失效图片，并支持按有效性筛选。

探测接口 `/api/internal/object-probe` 只接受回环地址，并要求 HMAC-SHA256 签名
（密钥由主密钥派生，不落盘），因此不会被外部当作免费图片代理。

---

## 9. 常见问题

**编译失败：`expected 'package', found 'EOF'`**
新建了空 `.go` 文件。后台新建 `.go` 会写入骨架，脚本直接调用 API 时需要自己写 `package main`。

**编译失败：`missing go.sum entry`**
插件引入了第三方库。服务端会自动 `go mod tidy` 重试一次；离线环境请改用标准库。

**`未找到 go 命令`**
主机没装 Go 工具链。装上 Go 1.21+ 后重启服务，或在别处编译好 `.build/plugin` 拷过来。

**插件一直「异常」，但调试显示自检通过**
`volatile` 凭据过期时间没填或已过期。到参数页补 `token_expires_at`。

**v1.x 的声明式插件去哪了**
升级后自动迁移：内置插件变成同名文件夹（源码就是内置 Go 实现），
自定义声明式插件生成一份带说明的 `main.go` 骨架，原定义留档在 `legacy-definition.json`。
声明式模板引擎已移除，需要按本文档用 Go 重写 —— 好消息是能表达的东西不再受限。

**内置插件能改吗**
能。源码在 `{数据目录}/plugins/workbuddy/`，改完保存即生效；升级不会覆盖你改过的文件
（只在文件缺失时写入）。想保留一份原始实现可先 `plugin_export`。
