# 插件开发文档

> **v2.1 起插件由内嵌解释器直接执行，不需要安装 Go 工具链。**
> 一个插件 = 一个文件夹，里面是 `plugin.json`（元信息与参数 Schema）加任意多个 `.go` 文件。
> 后台保存源码后服务端在**毫秒级**完成加载，立即生效；跑的仍然是 Go，标准库随便用——
> 可以做**任意请求、任意 HTTP 方法（含 PUT/DELETE/multipart）、任意加解密与签名**，
> 而不只是「填一个请求模板」。

本文档按「读一份能跑的代码 → 动手改 → 调试 → 发布」的顺序组织：

| 章节 | 你会得到 |
| --- | --- |
| [1. 五分钟写出第一个插件](#1-五分钟写出第一个插件) | 一个能真实上传的插件 |
| [2. 文件夹结构与入口约定](#2-文件夹结构与入口约定) | 插件由什么组成、怎么被加载 |
| [3. SDK 参考](#3-sdk-参考) | `Input`/`Output`/`Config`/`Client`/加解密工具 |
| [4. 参数 Schema](#4-参数-schemainpluginjson) | 后台表单怎么来的、敏感值怎么加密 |
| [5. 用 MCP 让 Agent 全程开发](#5-用-mcp-让-agent-全程开发) | 创建 → 写码 → 加载 → 调试 → 上传验证 |
| [6. 执行与运行时](#6-执行与运行时) | 多文件合并、能力边界、超时、崩溃隔离 |
| [7. 调试与请求追踪](#7-调试与请求追踪) | 看到插件到底发了什么请求 |
| [8. 图片有效性巡检](#8-图片有效性巡检) | 归档图片按周期自检，失效自动告警 |
| [9. 常见问题](#9-常见问题) | 加载失败、Token 过期、迁移 |

---

## 1. 五分钟写出第一个插件

后台「插件管理 → 创建插件」选择模板 **HTTP 表单上传**，ID 填 `my-uploader`，点创建。
服务端会生成文件夹并立即加载，成功后会跳到详情页的「插件源码」标签：

```
my-uploader/
├── plugin.json   # 参数 Schema 与元信息（系统维护，只读）
└── main.go       # 你的代码
```

打开 `main.go`，把 `cfg["upload_url"]` 指向你的接口，保存即可生效。核心只有两个方法：

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

func (plugin) Check(ctx context.Context, cfg sdk.Config) error {
	_, err := sdk.NewClient(10 * time.Second).Bytes(ctx, http.MethodHead, cfg["upload_url"], nil, nil)
	return err
}

func main() { sdk.Serve(plugin{}.Upload, plugin{}.Check) }
```

保存后后台会立刻重新加载并热切换，**不需要重启服务，也不需要编译**。

---

## 2. 文件夹结构与入口约定

### 2.1 目录约定

| 文件 | 谁维护 | 说明 |
| --- | --- | --- |
| `plugin.json` | 系统 | 元信息 + 参数 Schema + 健康检测建议 + 巡检周期，只读（用 `plugin_manifest_update` 改） |
| `*.go` | 你 | 任意多个文件，同一 `package main`；可以拆成 `helper.go`、`sign.go` 等 |
| `*.md` / `*.txt` / `*.yaml` | 你 | 说明与配置样例，不参与执行 |
| `go.mod` | 忽略 | v2.0 生成的历史文件，存在也不影响；可以删掉 |

限制：单文件 ≤ 512 KB，总源码 ≤ 2 MB，最多 64 个文件；文件名不能带路径分隔符或以点开头。

### 2.2 入口约定：为什么传函数而不是传实例

```go
func main() { sdk.Serve(plugin{}.Upload, plugin{}.Check) }
```

解释器与宿主分属两套类型系统，**接口值无法跨边界**（把 `plugin{}` 直接交给宿主会丢失方法集），
函数值则可以无损传递。所以入口固定为「把两个函数交给 `sdk.Serve`」，宿主取回后直接调用。

> v2.0 的旧写法 `sdk.Serve(plugin{})` 会在加载时**自动改写**为注册式并写回文件，
> 升级后不需要手工修改；改写记录会出现在变更日志里。

### 2.3 多文件怎么合并

解释器按「一个包一份源码」执行，服务端会在加载前把目录内所有 `.go` 文件合并：

1. 校验每个文件都是 `package main`，语法错误直接报 `文件名:行:列`；
2. 去重 import（同一路径不同别名会报错，避免歧义）；
3. 按文件名顺序拼接声明，去掉重复的 `package` 与 `import` 块；
4. 交给解释器执行，行号会映射回原始文件。

所以你可以自由拆分文件，不需要 `go.mod`，也不会有「包循环引用」这类问题。

### 2.4 为什么仍然跑在子进程里

解释执行的死循环、栈溢出无法被宿主安全回收。服务端用**自身二进制**启动一个隐藏的
`__plugin_runner__` 子进程来跑解释器：崩溃或超时可以强杀，重新加载只需重启这个进程。
因为 runner 就是主程序本身，所以**部署仍然只有一个二进制，不需要 Go 工具链**。

### 2.5 能力边界

| 能力 | 支持情况 |
| --- | --- |
| 标准库 | 全部可用（`net/http`、`crypto/*`、`encoding/*`、`os`、`io`、`mime/multipart`…） |
| 第三方库 | **不支持**。导入非标准库会直接报错并提示改用标准库 |
| `cgo` | 不支持 |
| Go 语法 | 基线 Go 1.21；泛型仅基础用法，`range int`、迭代器等新语法可能不被识别 |
| 并发 | 可以用 goroutine，但请自己保证在 `ctx` 取消后退出；同一插件的调用是串行的 |

绝大多数图床上传、签名、加密需求用标准库都能完成；确实需要第三方 SDK 时，
把它的核心逻辑（签名算法、请求格式）用标准库重写即可。

---

## 3. SDK 参考

导入路径：`github.com/Mr-losfer/s3bridge/sdk`

### 3.1 两个方法 + 一个入口

```go
type UploadFunc func(ctx context.Context, in Input, cfg Config) (*Output, error)
type CheckFunc  func(ctx context.Context, cfg Config) error

// Upload 把文件转发到第三方并返回统一结果。
// Check  做无副作用探活（不得上传真实业务文件）。
func main() { sdk.Serve(plugin{}.Upload, plugin{}.Check) }
```

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

```json
{
  "schema": 2,
  "id": "my-uploader",
  "name": "我的图床",
  "params": [
    { "key": "upload_url", "label": "上传地址", "type": "url", "required": true, "group": "通用" },
    { "key": "token", "label": "Token", "type": "secret", "sensitive": true, "volatile": true,
      "expiresField": "token_expires_at", "group": "凭据" },
    { "key": "token_expires_at", "label": "Token 过期时间", "type": "datetime", "group": "凭据" }
  ]
}
```

| 字段 | 说明 |
| --- | --- |
| `type` | `text` / `secret` / `number` / `bool` / `select` / `textarea` / `url` / `datetime` / `duration` |
| `required` | 必填校验；留空且非必填时不会触发任何提示 |
| `sensitive` | 后台脱敏显示，服务端 AES-256-GCM 加密落盘 |
| `volatile` + `expiresField` | 临时凭据（Token/Cookie），到期前 24 小时提醒；**参数留空时不提醒** |
| `group` | 后台表单分组，按首次出现顺序排列 |
| `validity.intervalHours` | 本插件归档图片的巡检间隔覆盖（0 表示跟随系统） |

参数在传给插件前由主服务解密，插件拿到的 `cfg` 就是明文。

### 通过 MCP 修改清单

`plugin_manifest_update` 可以改参数 Schema、文件类型、超时、巡检周期，**不会触发重新加载**；
改完参数记得用 `plugin_params_set` 填值。

---

## 5. 用 MCP 让 Agent 全程开发

连接 MCP 后（参见 [MCP 能力与工具清单](mcp.md)），Agent 可以不碰后台界面完成全部工作：

| 步骤 | 工具 | 说明 |
| --- | --- | --- |
| 1 看示例 | `plugin_files` + `plugin_file_read` | 读内置 `workbuddy` 的 `main.go`，就是一份真实可跑的参考实现 |
| 2 建插件 | `plugin_create` | `{id,name,template,files,params,priority,fileTypes}`，创建即加载 |
| 3 写代码 | `plugin_file_write` | 写单个文件并**自动重新加载**；失败会保留源码并返回 `文件:行:列` |
| 4 试加载 | `plugin_validate` | 在临时目录加载一份源码，不落库、不影响运行中的插件 |
| 5 加文件 | `plugin_file_write` + `plugin_file_rename` / `plugin_file_delete` | 多文件拆分、改名（入口文件改名会同步 `entry`）、删除 |
| 6 改清单 | `plugin_manifest_update` | 加参数、改超时、改文件类型、改巡检周期 |
| 7 配参数 | `plugin_params_set` | 敏感值加密落盘 |
| 8 重新加载 | `plugin_build` | 手动触发，返回加载日志、耗时、源码 SHA-256 |
| 9 调试 | `plugin_debug` | 参数 → 凭据有效期 → 加载 → 自检，并带回请求追踪 |
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

## 6. 执行与运行时

### 6.1 加载流程

| 阶段 | 做什么 | 典型耗时 |
| --- | --- | --- |
| 入口升级 | 把 `sdk.Serve(x)` 改写为 `sdk.Serve(x.Upload, x.Check)` | < 1ms |
| 合并校验 | 解析每个 `.go`、检查 package 与 import、合并成一份源码 | < 2ms |
| 解释执行 | yaegi 执行包级声明与 `main`，拿到注册的两个函数 | ~10ms |
| 握手 | 向 runner 进程发 `ping`，确认入口已注册 | < 5ms |

以上是串行总耗时，实测一次完整的「保存 → 生效」约 **10–15 ms**（含进程启动）；进程已在运行时，
手工点「重新加载」也是同一量级。作为对比，v2.0 的 `go build` 每次约 7 秒。

失败时返回的错误会带上**原始文件名与行号**，例如：

```
main.go:58:6: undefined: notDefined
main.go:63:58: expected '}', found 'EOF'
main.go: 不支持导入 "github.com/pkg/errors" —— 内嵌解释器只提供标准库
```

### 6.2 运行时

| 行为 | 说明 |
| --- | --- |
| 加载 | 保存源码即加载；启动时后台预加载已启用插件，首次上传不必等 |
| 隔离 | 每个插件一个 runner 子进程（主程序自身的隐藏子命令） |
| 并发 | 同一插件的调用串行，不同插件并行 |
| 超时 | `plugin.json` 的 `timeoutSecs`（默认 60，最大 600）；超时直接 kill 进程，下次调用自动重启 |
| 崩溃 | 进程退出后记录 stderr 末尾，状态置为异常并告警；下次调用重新拉起 |
| 回收 | 10 分钟无调用回收子进程，释放约 20–30 MB 内存 |
| 热切换 | 重新加载成功后立即丢弃旧进程，下一次调用跑新代码 |

### 6.3 健康检测

`Check` 会被按插件独立配置的周期调用（默认 300 秒，可关闭）。失败不会立刻判死：
连续失败达到 `failThreshold` 才标记异常并生成告警，恢复后自动关闭未确认告警。
若插件声明了 `volatile` 凭据且过期时间未设置或已过期，即使 `Check` 成功也会被判为异常——
这是故意的：Token 过期是这类插件最常见的故障。参数留空时不会告警。

---

## 7. 调试与请求追踪

后台「调试与检测」标签或 MCP `plugin_debug` 会依次执行：

| 步骤 | 内容 |
| --- | --- |
| 参数校验 | 必填、类型、URL、枚举、正则、时间格式 |
| 临时凭据有效期 | 读取 `expiresField`，提示剩余时间或已过期 |
| 插件加载 | 源码是否已加载、入口是否注册，失败时给出源码错误 |
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

**加载失败：`expected '}', found 'EOF'`**
大括号没闭合。错误会带 `文件:行:列`，按位置补上即可。

**加载失败：`undefined: xxx`**
用了未定义的变量或函数，或者忘了写在哪个文件里。多文件会合并执行，不需要 import 同包文件。

**加载失败：`不支持导入 "github.com/..."`**
插件只能用标准库。把第三方库的核心逻辑（签名、请求格式）用标准库重写；
实在绕不开可以先用 `http.NewRequest` 手工构造请求。

**加载失败：`没有找到入口`**
`main()` 里必须调用 `sdk.Serve(plugin{}.Upload, plugin{}.Check)`。
只写方法不注册，宿主拿不到实现。

**插件一直「异常」，但调试显示自检通过**
`volatile` 凭据过期时间没填或已过期。到参数页补 `token_expires_at`。
参数留空时不会告警 —— 可选凭据没配置属于「未启用」，不算过期。

**从 v2.0 升级要注意什么**
只有一个变化：入口从 `sdk.Serve(plugin{})` 变成 `sdk.Serve(plugin{}.Upload, plugin{}.Check)`。
服务端会在加载时自动改写并写回文件，通常你不需要做任何事；
如果改写失败（例如入口写法过于特殊），错误信息会给出确切的修改方法。

**v1.x 的声明式插件去哪了**
升级后自动迁移，规则如下：

| 情况 | 迁移结果 |
| --- | --- |
| 内置插件（workbuddy） | 变成同名文件夹，源码就是内置 Go 实现，参数与健康设置保留 |
| 自定义声明式插件 | 生成 `main.go` 骨架，原 JSON 留档到 `legacy-definition.json` |
| 参数与文件类型 | 全部保留：参数键按名称推断类型（含 key/token/secret 的标为敏感），旧的 `enc:` 密文继续可解密 |
| 启用状态 | **迁移后自动停用**，状态说明「请用 Go 重写 main.go，加载通过后启用」 |

之所以停用，是因为骨架里的 `Upload` 只会返回「尚未实现」，留着启用会被调度到然后必然失败。
重写 `main.go`、保存并加载通过后，到「调度设置」里手动启用即可。

**内置插件能改吗**
能。源码在 `{数据目录}/plugins/workbuddy/`，改完保存即生效；升级不会覆盖你改过的文件
（只在文件缺失时写入）。想保留一份原始实现可先 `plugin_export`。
