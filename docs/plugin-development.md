# 插件开发文档

> S3Bridge 的核心思路：**一个插件 = 一段把第三方上传接口转换成统一标准输出的适配器**。
> 新增一家第三方服务时只写插件代码，不改后台、不改前端、不改 MCP——因为配置表单、参数校验、文档和调试能力都由插件清单（`Manifest`）自动生成。

> **两种开发方式，按需选择**：第 2～9 章是编译进二进制的 Go 插件（性能最好、可发布内置版本）；
> **[第 10 章](#10-通过-mcp-完全掌控插件开发声明式插件源码) 是声明式插件源码 + MCP 全流程**——Agent 用 `plugin_create` / `plugin_code_upload` / `plugin_code_patch` 编写一份 JSON 定义即可创建、改码、校验、调试、上传验证并即时生效，无需重新编译或重启服务。若你的 Agent 需要完全掌控插件开发，直接看第 10 章。

---

## 1. 架构与数据流

```
后台上传 / S3 PutObject / MCP upload_test
        │
        ▼
  upload.Service（统一入口）
        │  ① 选择插件（指定 ID 或健康优先）
        │  ② 校验参数 + 解密敏感字段
        ▼
  plugin.Manager → Uploader.Upload(ctx, UploadRequest, cfg)
        │  ③ 插件把请求转成第三方协议（HTTP / SDK / 命令行）
        │  ④ 第三方返回 → 映射为 plugin.UploadResult
        ▼
  统一标准输出：URL / ObjectKey / FileName / MimeType / FileSize / Provider / URLExpireAt
        │
        ▼
  ⑤ 按 文件类型 + 年月 生成对象键并归档到 {bucket}/{类型}/{年月}/ + 落库 + S3 可取回
```

**关键约束**：插件只负责「协议转换」，不负责存储、鉴权、归档、告警。这些由核心层统一处理，因此每家第三方的差异被收敛在一个文件里。

---

## 2. 五分钟接入一家新第三方

以 `internal/plugins/<yourplugin>/` 为例，一共四步。

### 步骤 1：定义清单（Manifest）

清单是**单一事实来源**，同时驱动：

| 消费方 | 用到什么 |
| --- | --- |
| 后台「插件详情」页 | `Params` 渲染动态表单（分组、控件类型、脱敏、临时参数提醒） |
| 后台「插件列表」 | `Name` / `Version` / `Health` 默认检测策略 |
| 参数校验 | `Required` / `Type` / `Options` / `Pattern` / `Volatile` |
| MCP `plugin_registry` / `plugin_docs` | 实时返回参数 Schema 给 AI 客户端 |
| 插件更新 | 对比清单新增参数，自动补齐默认值 |

```go
package myoss

import "github.com/Mr-losfer/s3bridge/internal/plugin"

const Kind = "myoss"

var Manifest = plugin.Manifest{
    Kind:         Kind,
    Name:         "MyOSS 开放上传",
    Version:      "1.0.0",
    Description:  "对接 MyOSS 的 /open/upload 接口，把临时签名链接转换为统一标准输出。",
    Author:       "Your Team",
    Endpoint:     "POST https://api.myoss.example.com/open/upload",
    Capabilities: []string{"upload", "image", "temp-url", "health-probe"},
    Health: plugin.HealthHint{
        Enabled: true, IntervalSecs: 300, TimeoutSecs: 8,
        FailThreshold: 2, ProbeMode: "ping",
    },
    Params: []plugin.ParamField{
        {Key: "base_url", Label: "服务域名", Type: "url", Group: "连接", Required: true,
            Default: "https://api.myoss.example.com"},
        {Key: "upload_path", Label: "上传路径", Type: "text", Group: "连接", Required: true,
            Default: "/open/upload"},
        {Key: "timeout_secs", Label: "请求超时（秒）", Type: "number", Group: "连接",
            Required: true, Default: "30"},

        // ↓ 临时参数：会过期、需要轮换的凭据
        {Key: "api_token", Label: "API Token", Type: "secret", Group: "身份凭据",
            Required: true, Sensitive: true, Volatile: true, ExpiresField: "token_expires_at",
            Placeholder: "Bearer eyJ...", Help: "会过期的临时凭据，到期前 24 小时提醒"},
        {Key: "token_expires_at", Label: "Token 过期时间", Type: "datetime", Group: "身份凭据",
            Volatile: true, Placeholder: "2026-12-08 12:00:00 / 1796601498",
            Help: "支持 RFC3339、Unix 秒/毫秒时间戳"},

        {Key: "region", Label: "区域", Type: "select", Group: "高级", Default: "ap-beijing",
            Options: []plugin.ParamOption{
                {Label: "北京", Value: "ap-beijing"},
                {Label: "上海", Value: "ap-shanghai"},
            }},
        {Key: "extra_headers", Label: "附加 Header（JSON）", Type: "textarea", Group: "高级"},
    },
    Docs:       "### 接入说明\n\n在这里写该插件独有的接入步骤、抓包示例与响应映射规则（Markdown）。\n",
    SampleCode: "// 可选：展示在文档中心的示例代码\n",
}
```

### 步骤 2：实现 Uploader

```go
type Uploader struct{}

func New() plugin.Uploader { return &Uploader{} }

// Upload 把统一请求转换为第三方协议，再把响应映射回统一标准输出。
func (u *Uploader) Upload(ctx context.Context, req plugin.UploadRequest, cfg map[string]string) (*plugin.UploadResult, error) {
    var body bytes.Buffer
    w := multipart.NewWriter(&body)
    part, err := w.CreateFormFile(cfg["file_field"], req.FileName)
    if err != nil { return nil, fmt.Errorf("构造表单: %w", err) }
    if _, err := io.Copy(part, req.Reader); err != nil { return nil, err }
    if err := w.Close(); err != nil { return nil, err }

    secs, _ := strconv.Atoi(cfg["timeout_secs"])
    endpoint := strings.TrimRight(cfg["base_url"], "/") + cfg["upload_path"]
    r, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, &body)
    if err != nil { return nil, err }
    r.Header.Set("Authorization", cfg["api_token"])
    r.Header.Set("Content-Type", w.FormDataContentType())

    resp, err := (&http.Client{Timeout: time.Duration(secs) * time.Second}).Do(r)
    if err != nil { return nil, fmt.Errorf("%w: %v", plugin.ErrUpstream, err) }
    defer resp.Body.Close()
    if resp.StatusCode/100 != 2 {
        return nil, fmt.Errorf("%w: HTTP %d", plugin.ErrUpstream, resp.StatusCode)
    }

    var out struct {
        Code int    `json:"code"`
        Data struct {
            URL       string `json:"url"`
            ObjectKey string `json:"objectKey"`
            FileName  string `json:"fileName"`
            MimeType  string `json:"mimeType"`
            FileSize  int64  `json:"fileSize"`
        } `json:"data"`
    }
    if err := json.NewDecoder(resp.Body).Decode(&out); err != nil { return nil, err }
    if out.Code != 0 { return nil, fmt.Errorf("%w: code=%d", plugin.ErrUpstream, out.Code) }

    return &plugin.UploadResult{
        URL:       out.Data.URL,
        ObjectKey: out.Data.ObjectKey,
        FileName:  req.FileName,
        MimeType:  req.MimeType,
        FileSize:  req.Size,
        Provider:  Kind,
    }, nil
}

// Check 做轻量探活，不应产生真实业务副作用。
// 建议：只校验凭据有效期 + 一次 HEAD/GET 连通性探测，不要上传真实文件。
func (u *Uploader) Check(ctx context.Context, cfg map[string]string) error {
    warning := plugin.VolatileWarning(Manifest, cfg)
    if warning != "" { return errors.New(warning) }
    // 还可做一次 HEAD / 连通性探测…
    return nil
}
```

> `cfg` 已经是**解密后的明文**，插件无需关心加密。敏感字段在落库时由核心层用 AES-256-GCM 加密。

### 步骤 3：注册

在 `internal/app/core.go` 的注册入口加入：

```go
reg.Register(myoss.Descriptor())
```

其中：

```go
func Descriptor() plugin.Descriptor {
    return plugin.Descriptor{Manifest: Manifest, New: New}
}
```

启动时 `Manager.EnsureBuiltin()` 会为每个未创建过的插件类型自动实例化，套用清单里的默认值与默认健康检测配置。

### 步骤 4：测试

```go
func TestUpload(t *testing.T) {
    upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
        // 断言 Authorization / multipart 字段名
        _, _ = w.Write([]byte(`{"code":0,"data":{"url":"https://x/y.png","objectKey":"a/b.png"}}`))
    }))
    defer upstream.Close()

    got, err := New().Upload(context.Background(), plugin.UploadRequest{
        FileName: "a.png", MimeType: "image/png", Size: 3, Reader: strings.NewReader("abc"),
    }, map[string]string{"base_url": upstream.URL, "upload_path": "/", "timeout_secs": "5"})
    if err != nil { t.Fatal(err) }
    if got.Provider != "myoss" || got.ObjectKey != "a/b.png" { t.Fatalf("unexpected: %+v", got) }
}
```

运行 `go test -race ./...` 验证。

---

## 3. ParamField 字段参考

| 字段 | 类型 | 说明 |
| --- | --- | --- |
| `Key` | string | 参数键，贯穿后台表单、MCP、持久化。建议小写下划线 |
| `Label` | string | 表单标签（中文） |
| `Type` | string | `text` / `secret` / `number` / `bool` / `select` / `textarea` / `url` / `datetime` / `duration` |
| `Group` | string | 表单分组名，同组字段折叠在一起展示 |
| `Required` | bool | 必填校验，缺失时插件无法启用/上传 |
| `Sensitive` | bool | 前端脱敏显示，服务端 AES-256-GCM 加密存储 |
| `Volatile` | bool | **临时参数**：会过期、需要定期轮换（如 Bearer Token） |
| `ExpiresField` | string | 与 `Volatile` 配合，指向记录过期时间的另一个字段 key |
| `Default` | string | 默认值，插件实例化与「全量修改/重置」时使用 |
| `Options` | []ParamOption | `select` 类型的枚举项 `{Label, Value}` |
| `Pattern` | string | 正则表达式额外校验 |
| `Placeholder` / `Help` | string | 表单占位符与说明文字 |

`datetime` 支持 RFC3339、`2006-01-02 15:04:05`、Unix 秒/毫秒时间戳，方便直接粘贴抓包里的 `q-sign-time` 或 JWT `exp`。

### 临时参数是怎么工作的

这是本系统针对「第三方有临时参数」这一痛点专门设计的机制：

1. 开发者在清单里把凭据标记为 `Volatile: true` 并绑定 `ExpiresField`。
2. 后台插件列表点击打开 → 该字段显示**临时参数**标记与过期倒计时提醒。
3. 到期前 24 小时：插件列表出现黄色 warning；到期后：校验与探活判定失败，健康检测把插件标记为 `unhealthy`。
4. 插件行**整行标红**、置顶排序，并自动生成一条告警写入告警中心。
5. 运维在插件详情页更新 Token → 可用 `plugin_parse_token` 一键从 JWT 解析 `exp` 与 `sub` → 立即执行调试/检测恢复。

WorkBuddy 插件就是完整范例：`authorization`（Bearer Token）+ `token_expires_at` + `x_user_id` + `cookie` 一组临时参数，全部通过表单配置，无需改代码。

---

## 4. 统一标准输出（UploadResult）

无论接哪家第三方，调用方拿到的永远是这个结构：

```json
{
  "id": "obj_xxx",
  "bucket": "default",
  "objectKey": "image/202611/a1b2.png",
  "fileName": "banner.png",
  "mimeType": "image/png",
  "category": "image",
  "size": 2033070,
  "sha256": "…",
  "provider": "workbuddy",
  "pluginId": "workbuddy",
  "remoteUrl": "https://…/83420.png?q-sign-algorithm=sha1&…",
  "remoteKey": "attachments/…/83420.png",
  "urlExpireAt": "2026-12-08T12:00:00Z",
  "createdAt": "2026-11-08T23:58:31Z"
}
```

`URLExpireAt` 可选：若第三方返回临时签名链接，插件应从 URL 参数（如 COS 的 `q-sign-time`）解析出到期时间填入，系统据此提醒续期。**注意**：S3Bridge 同时把文件本体归档到本地存储桶，因此即使第三方临时链接过期，通过 S3 接口取回的对象依然长期有效。

文件类型（`category`）由核心层按 MIME 自动判定。自动生成的**桶内对象键**为 `{category}/{YYYYMM}/{hash}_{name}`（bucket 由 S3 路径承载，不重复写进 key），**磁盘归档路径**为 `{objectDir}/{bucket}/{category}/{YYYYMM}/{hash}-{name}`。上传列表可按 bucket 与类型双维度过滤，S3 侧可用 `prefix=image/` 直接按类型列举。

---

## 5. 配置、修改与运维能力

所有能力在后台页面和 MCP 工具中**双通道可用**，底层是同一份实现：

| 操作 | 后台 | MCP 工具 | 语义 |
| --- | --- | --- | --- |
| 差量修改 | 插件详情「保存」 | `plugin_patch` | 只更新传入字段，`params` **按键合并**，未传字段保持不变；支持 `expectedRevision` 乐观锁防并发覆盖 |
| 全量修改 | 插件详情「重置为完整配置」 | `plugin_replace` | 用完整配置替换，未提供的参数**重置为清单默认值** |
| 参数修改 | 插件详情参数表单 | `plugin_params_set` | 仅按 key 覆盖指定参数，用于轮换临时 Token |
| 插件更新 | 「更新插件」 | `plugin_update` | 升级版本号，`syncParams` 补齐清单新增参数，`resetToDefault` 重置全部默认值 |
| 启用/停用 | 列表开关 | `plugin_toggle` | 停用后不参与自动选择，调度器同步移除检测任务 |
| 调试 | 「调试」 | `plugin_debug` | 无副作用地执行：参数校验 → 临时凭据有效期检查 → 上游连通性探测，返回分步诊断 |
| 立即检测 | 「检测」 | `plugin_health_check` | 跑一次真实探活并更新状态/告警 |
| 定时检测 | 列表「设置」抽屉 | `plugin_health_config` | 每插件独立配置间隔、超时、失败阈值、探活模式，保存后调度器重建该任务 |
| 变更审计 | 插件详情「变更记录」 | `plugin_changes` | 差量/全量/更新/调试/参数修改全部留痕（actor、diff、时间） |
| 查看文档 | 文档中心 | `plugin_docs` | 返回本文档 + 实时参数模板 |
| 类型清单 | — | `plugin_registry` | 返回所有已编译插件类型的完整清单，供开发新插件参考 |

### 版本与更新

发布新插件时提升 `Manifest.Version`，并在 `Params` 中追加新字段（给 `Default`）。运维执行「插件更新」后：

- 新参数按默认值补齐到已有实例，旧参数值不丢失；
- 版本号与更新说明写入变更记录；
- 需要彻底回到初始状态时才勾选 `resetToDefault`。

---

## 6. 健康检测与告警

- 调度器为每个启用且开启检测的插件启动独立 goroutine，`context` 可取消、`WaitGroup` 可等待，进程退出时干净关闭。
- 连续失败达到 `FailThreshold` → 状态置 `unhealthy` → 生成 `Alert`；恢复成功时未确认告警自动标记「系统已恢复」。
- 后台表现：插件列表**异常行整行标红并置顶**、概览页红色告警条、侧边栏「告警中心」角标。
- 全局总开关在「系统设置」；插件级配置优先。

---

## 7. 安全约定（务必遵守）

1. **不要把密钥写进代码、清单默认值或配置文件**。凭据一律通过后台表单 / MCP 传入，由核心层加密落库。
2. `Upload` 必须使用传入的 `ctx`，让超时与取消能穿透到第三方调用。
3. 所有第三方响应都要校验状态码与业务码，并用 `fmt.Errorf("%w: ...", plugin.ErrUpstream)` 包装错误，便于上层归类告警。
4. `Check` 不得产生真实业务副作用（不要上传真实文件、不要扣费）。
5. `UploadRequest.ObjectKey` 已由核心层做路径穿越清洗，插件不要自行拼接本地文件系统路径。
6. 插件 `Uploader` 应尽量无状态，可被并发复用；需要状态时自行加锁。

---

## 8. 内置参考实现：WorkBuddy

`internal/plugins/workbuddy/` 是完整的生产级范例，对接：

```
POST https://copilot.tencent.com/v2/backgroundagent/localProxy/upload
Content-Type: multipart/form-data
Authorization: Bearer <JWT>
x-user-id / x-client-version / x-client-product / x-client-platform / user-agent / cookie
```

响应转换规则：

| 上游字段 | 标准输出 |
| --- | --- |
| `data.url` | `URL` |
| `data.objectKey` | `ObjectKey` |
| `data.fileInfo.fileName` | `FileName` |
| `data.fileInfo.mimeType` | `MimeType` |
| `data.fileInfo.fileSize` | `FileSize` |
| URL 中的 `q-sign-time` 结束时间 | `URLExpireAt` |

它还实现了 JWT `exp` / `sub` 解析（对应 MCP 工具 `plugin_parse_token`），可从粘贴的 Bearer Token 自动填好过期时间与 `x_user_id`。

---

## 9. 检查清单

- [ ] `Manifest.Kind` 全局唯一，`Version` 语义化
- [ ] 所有必填参数都有 `Required: true` 和合理 `Default`
- [ ] 会过期的凭据已标记 `Volatile` + `ExpiresField`
- [ ] 密钥类字段已标记 `Sensitive`
- [ ] `Upload` 使用 `ctx`，错误用 `%w` 包装
- [ ] `Check` 无业务副作用
- [ ] 有 `httptest` 单元测试，覆盖成功/失败/超时路径
- [ ] `go test -race ./...` 通过
- [ ] 文档（`Docs` 字段）说明了抓包示例与响应映射

---

## 10. 通过 MCP 完全掌控插件开发（声明式插件源码）

前 9 章的 Go 插件需要编译进二进制，Agent 无法在运行时从零创建。为此 S3Bridge 提供**声明式插件源码（Plugin Source）**：一份 JSON 描述参数 Schema、第三方 HTTP 请求模板与响应字段映射，服务端即时编译成 `Uploader`，**改动后无需重启、无需 Go 工具链**。后台的创建/查看/差量修改与 MCP 工具完全同源。

### 10.1 源码结构

| 层级 | 字段 | 说明 |
| --- | --- | --- |
| 根 | `kind` / `name` / `version` / `description` / `author` / `docs` | 与 Go 插件的 `Manifest` 等价，`kind` 必须等于插件 ID |
| 根 | `params` | `ParamField` 数组，驱动后台动态表单、校验与 MCP 参数修改（字段见第 3 章） |
| `request` | `method` / `url` | 请求方法与地址模板，渲染后必须是 `http(s)://` |
| `request` | `headers` / `fields` / `fileField` / `rawBody` | 请求头、额外表单字段、文件字段名；值支持 `{{变量}}` |
| `request` | `mode` | `multipart`（默认）/ `json`（文件转 base64）/ `form` / `raw` |
| `request` | `checkUrl` / `timeoutSecs` | 可选探活地址（GET）；超时 1～120 秒 |
| `response` | `codePath` / `successCode` / `messagePath` | 业务码与错误消息路径，如 `code` / `"0"` / `msg` |
| `response` | `urlPath`（必填）/ `objectKeyPath` | 下载地址与对象键路径 |
| `response` | `fileNamePath` / `sizePath` / `mimeTypePath` / `expirePath` | 文件信息；缺省时沿用上传请求中的值 |
| `health` | `enabled` / `intervalSecs` / `timeoutSecs` / `failThreshold` / `probeMode` | 定时检测策略，保存时同步到插件实例 |

模板变量 `{{key}}` 必须先在 `params` 中声明，否则静态校验直接拒绝（避免运行时静默失败）。内置变量无需声明：`file_name`、`mime_type`、`bucket`、`object_key`、`file_size`、`timestamp`、`unix`。

响应路径支持点号与数组下标，例如 `data.fileInfo.fileName`、`data.items[0].url`；JSON 数字用原始精度解析，不会退化成科学计数法。`expirePath` 支持 RFC3339 与 Unix 秒/毫秒。

### 10.2 MCP 全流程（Agent 只需这些工具）

| 步骤 | 工具 | 作用 |
| --- | --- | --- |
| 1 参考 | `plugin_code_get`（`id=workbuddy`） | 读取内置插件的等价声明式源码作为模板 |
| 2 创建 | `plugin_create` | 从零建实例；可附 `definition`，或用 `template` 从内置插件派生；不带定义则生成可运行骨架 |
| 3 读码 | `plugin_code_get` | 返回 `source`（对象）、`sourceJSON`（规范化文本）、`manifest`、`revision`、`editable` |
| 4 改码 | `plugin_code_upload` / `plugin_code_patch` | 全量替换 / 字段级差量；均支持 `expectedRevision` 乐观锁 |
| 5 自检 | `plugin_validate` | 不落库：结构、模板变量、必填参数、脱敏后的请求预览 |
| 6 配置 | `plugin_params_set` | 填 `base_url` / Token 等，敏感值自动加密 |
| 7 验证 | `plugin_debug` → `upload_test` | 无副作用调试 → 走新插件真实上传并返回统一标准输出 |
| 8 交付 | `plugin_export` | `format=json` 可移植定义；`format=go` 生成可内置化的 `plugin.go` |
| 9 下线 | `plugin_delete` | 删除自定义插件（内置插件只能 `plugin_toggle` 停用） |

`plugin_code_patch` 支持点号路径，例如 `{"request.headers.X-Stage":"v2"}`；值为 `null` 表示删除该字段；`params` 数组按 `key` 做 upsert，元素带 `_delete:true` 表示删除该参数。所有源码写操作都写入 `ChangeLog`（动作 `create` / `code` / `code_patch` / `delete`），可用 `plugin_changes` 回溯。

### 10.3 后台入口

「插件列表」页的 **＋ 创建插件** 可填 ID、名称、版本、说明并选择起始模板；创建后跳到详情页的 **插件源码** 标签，可直接编辑 JSON、静态校验、全量保存、差量修改、导出 JSON / Go 源码，以及删除自定义插件。这与 MCP 调用的是同一组工具，结果一致。

### 10.4 何时用哪种方式

| | 声明式源码 + MCP | Go 插件（第 2 章） |
| --- | --- | --- |
| 接入一家标准 HTTP 上传接口 | ✅ 推荐，分钟级 | 可行但需发版 |
| 需要 SDK、复杂签名、流式转换、自定义协议 | ❌ | ✅ |
| Agent 运行时自主开发 / 调试 | ✅ | ❌ |
| 需要随二进制发布、极致性能 | 用 `plugin_export format=go` 内置化 | ✅ |

两种方式共用同一套 `Uploader` 契约与参数 Schema，导出的 Go 文件放进 `internal/plugins/<kind>/plugin.go` 并在 `internal/app/core.go` 注册 `Descriptor()` 即可内置化，行为与动态版本完全一致。

### 10.5 安全边界

- 源码保存前校验：`kind` 格式、参数 key 唯一性与类型、模板变量必须声明、请求方法/模式、超时范围、请求头名称与 CRLF 注入、响应 `urlPath` 必填。
- 运行时渲染后再次校验目标 URL 只允许 `http/https` 且主机名非空；请求头禁止换行；源码上限 1 MiB、上游响应上限 4 MiB。
- `Check` 只做 TCP 连通性或可选 `checkUrl` 的 GET 探测，不上传真实文件，符合无副作用探活约定。
- 源码写操作要求 `plugin:write`，读操作要求 `plugin:read`；内置插件源码只读，避免误改核心实现。
