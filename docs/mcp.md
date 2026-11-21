# MCP 能力与工具清单

S3Bridge 内置 **Model Context Protocol（MCP）Server**。后台页面上的每一个按钮都对应一个 MCP 工具，AI 客户端（Claude Desktop、Cursor、Cline、自建 Agent）可以直接查看站点、管理插件、查文档、做开发调试、执行插件差量/全量修改与更新、调整参数。

> 架构要点：**MCP 工具是唯一的业务实现，REST 只是它的 HTTP 适配层**（`api.namedTool` 直接调用 `mcp.CallTool`）。因此「后台点按钮」与「AI 调用工具」得到完全一致的结果与审计记录，不会出现两套逻辑漂移。

---

## 1. 端点与鉴权

| 传输 | 端点 / 命令 | 鉴权 |
| --- | --- | --- |
| Streamable HTTP | `POST http(s)://<host>/mcp` | `Authorization: Bearer <登录令牌或 API Key>` |
| stdio | `s3bridge-mcp` 独立二进制 | 环境变量 `S3BRIDGE_URL` + `S3BRIDGE_TOKEN` |

- 协议版本：`2024-11-05`，JSON-RPC 2.0，支持单条与批量请求。
- 支持方法：`initialize`、`ping`、`tools/list`、`tools/call`、`resources/list`、`resources/read`、`notifications/initialized`。
- 工具调用会按调用者身份做 RBAC 校验（与后台完全一致），越权返回 `{"error":"权限不足：需要 xxx"}`。

### Claude Desktop / Cursor 配置（stdio）

```json
{
  "mcpServers": {
    "s3bridge": {
      "command": "s3bridge-mcp",
      "env": {
        "S3BRIDGE_URL": "http://localhost:8080",
        "S3BRIDGE_TOKEN": "<登录令牌或 API Key>"
      }
    }
  }
}
```

`S3BRIDGE_TOKEN` 可从后台「API 密钥」创建；也可以用登录接口 `POST /api/auth/login` 返回的 JWT。

### HTTP 直连

```bash
curl -s -X POST http://localhost:8080/mcp \
  -H 'Content-Type: application/json' \
  -H 'Authorization: Bearer <TOKEN>' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"site_info","arguments":{}}}'
```

响应：

```json
{
  "jsonrpc": "2.0",
  "id": 1,
  "result": {
    "content": [{"type": "text", "text": "{\n  \"siteName\": \"S3Bridge\", ... }"}],
    "isError": false
  }
}
```

后台「MCP 能力」页顶部直接显示**当前登录用户的 API Key 明文**（Bearer 令牌 / AccessKeyId / SecretAccessKey），以及已经填好真实密钥的 curl、Claude Desktop / Cursor stdio 配置与环境变量代码，可一键复制；没有密钥时可在该页直接创建。页内还提供**在线调试台**：选择工具 → 自动填充必填参数示例 → 执行 `tools/call` → 查看格式化结果。

---

## 2. 工具清单（53 个）

### 站点与系统

| 工具 | 说明 | 主要参数 |
| --- | --- | --- |
| `site_info` | 版本、运行时长、S3 接入点、插件/用户/桶/对象数量、调度器状态 | — |
| `site_stats` | 按文件类型/桶/插件的分布、插件状态统计、告警与变更数 | — |
| `system_settings_get` | 查看系统设置 | — |
| `system_settings_update` | 修改站点名、默认插件、健康检测、图片有效性巡检、告警开关、公告 | `siteName` `defaultPlugin` `healthEnabled` `healthIntervalSecs` `alertOnFailure` `announcement` `imageValidityEnabled` `imageValidityIntervalHours` `imageValidityFailThreshold` |
| `system_update_status` | 查看当前版本、Releases 最新版、结构化更新要点（含重点标注）、自动更新进度与部署策略 | — |
| `system_update_check` | 立即从 GitHub Releases 检测最新版本 | `force` |
| `system_update_apply` | **一键自动更新**：下载 → SHA256 校验 → 备份 → 原子替换 → 自动重启；返回后轮询 `system_update_status` 查看进度 | — |
| `system_update_dismiss` | 忽略指定版本的新版本弹窗（首页卡片仍显示） | `tag` |

> 版本更新工具与后台「版本更新卡片」共用同一份状态机。`site_info` 也会返回 `updateAvailable` / `latestVersion`，便于 AI 巡检时一并判断是否需要升级。

### 监控与相册

| 工具 | 说明 | 主要参数 |
| --- | --- | --- |
| `monitor_summary` | **监控总览**：请求量、错误率、延迟分位（P50/P95/P99）、上传与流量、最慢/最易错接口、运行时健康（goroutine/内存/磁盘）与最近异常 | `range`（`1h`/`6h`/`24h`/`7d`/`30d`） |
| `monitor_storage` | **存储画像**：桶容量排行与健康分、类型分布、Top 大文件、重复内容、断链、孤儿文件、超期对象与磁盘占用；可触发深度体检 | `scan` `days` |
| `monitor_errors` | **异常日志**：按级别、分类与关键字过滤，返回状态码、路由、耗时、操作者与错误消息 | `level` `category` `q` `seconds` `limit` |
| `gallery_list` | **全站图片巡视**：跨桶按时间倒序列出图片，含缩略图地址，便于快速判断站点素材 | `limit` `bucket` `q` |

> 这四个工具都只读，不需要额外授权即可让 Agent 做「先看监控、再看日志、最后给结论」的巡检。指标口径与异常分类见[监控中心文档](monitoring.md)，相册的游标分页与缩略图缓存见[图片相册文档](gallery.md)。

### 插件开发与运维

| 工具 | 说明 | 主要参数 |
| --- | --- | --- |
| `plugin_list` | 插件列表，**按调度权重降序**（即实际使用顺序），含状态/版本/可承接文件类型/参数警告 | — |
| `plugin_get` | 单个插件详情：参数 Schema、分组、脱敏后的当前值 | `id` |
| `plugin_registry` | 内置 Go 插件模板清单与完整参数 Schema，并列出所有自定义插件 | — |
| `plugin_create` | **创建 Go 插件**：生成 `plugin.json` + `main.go` 骨架并立即编译；可覆盖任意源码文件 | `id` `name` `version` `description` `author` `template` `files` `params` `enabled` `priority` `fileTypes` `timeoutSecs` |
| `plugin_files` | **列出插件文件夹**：文件名、大小、是否入口、是否系统维护（只读） | `id` |
| `plugin_file_read` | **读取单个文件**：`.go` 源码、`plugin.json` 清单、`go.mod` 都能读 | `id` `name` |
| `plugin_file_write` | **写入文件并自动编译**：编译失败保留源码并返回编译器输出，供下一轮修复 | `id` `name` `content` |
| `plugin_file_delete` | 删除文件（至少保留一个 `.go`），删除后自动重新编译 | `id` `name` |
| `plugin_file_rename` | 重命名文件；入口文件改名会同步清单 `entry` | `id` `from` `to` |
| `plugin_build` | **手动编译**：返回编译器输出、耗时、产物大小与 SHA-256 | `id` |
| `plugin_validate` | **试编译**（不落库）：在临时目录编译给定源码，返回编译器输出；`id` 可复用现有清单 | `id` `files` `manifest` |
| `plugin_manifest_update` | **更新清单**：参数 Schema、文件类型、超时、图片有效性周期；不触发重新编译 | `id` `manifest` |
| `plugin_export` | **导出全部源码与清单**，可在另一实例逐文件写回复刻 | `id` |
| `plugin_delete` | **删除自定义插件**（内置插件只能停用），变更记录保留 | `id` |
| `plugin_docs` | 插件开发文档全文 + 实时参数模板 | `topic` |
| `plugin_debug` | **插件调试**：无副作用地执行参数校验、临时凭据有效期检查、上游连通性探测，返回分步诊断 | `id` |
| `plugin_patch` | **差量修改**：只更新传入字段，`params` 按键合并；可改调度权重与文件类型 | `id` `name` `description` `enabled` `priority` `fileTypes` `params` `health` `expectedRevision` |
| `plugin_replace` | **全量修改**：完整配置替换，未提供的参数重置为默认值 | 同上（含 `priority` / `fileTypes`） |
| `plugin_update` | **插件更新**：升级版本、同步清单新增参数 | `id` `version` `syncParams` `resetToDefault` `note` |
| `plugin_params_set` | **参数修改**：按 key 覆盖，用于轮换临时 Token | `id` `params` |
| `plugin_toggle` | 启用/停用插件 | `id` `enabled` |
| `plugin_health_check` | 立即执行一次健康检测并更新状态/告警 | `id` |
| `plugin_health_config` | **每插件独立定时检测**：间隔、超时、失败阈值、探活模式 | `id` `enabled` `intervalSecs` `timeoutSecs` `failThreshold` `probeMode` |
| `plugin_changes` | 变更记录（创建/写文件/改名/编译/清单/调试/参数修改/删除审计） | `id` `limit` |
| `plugin_parse_token` | 解析 WorkBuddy Bearer Token 的 `exp` 与 `sub`，自动得到过期时间与 `x-user-id` | `authorization` |
| `s3_docs` | S3 开放接口文档 | — |
| `installation_docs` | 安装部署文档 | — |

#### 调度权重与文件类型

每个插件有两个调度属性，Agent 可用 `plugin_patch` 直接调整：

- `priority`（权重）：**数值越大越优先被选中**。自动选择时先按文件类型过滤，再取权重最高者；权重相同时健康状态更好的优先，已判定 `unhealthy` 的插件排到最后。
- `fileTypes`：插件可承接的文件类型多选，取值 `image` / `video` / `audio` / `document` / `archive` / `other`；**空数组表示全部类型**。上传时先按 MIME 与扩展名判定类型，再在该类型的插件池里按权重选择。

```json
{"name":"plugin_patch","arguments":{"id":"workbuddy","priority":320,"fileTypes":["image","document"]}}
```

> 显式传入 `pluginId` 时不再自动挑选，但会校验该插件是否承接当前文件类型；不承接时返回「插件不支持该文件类型」。

### 告警

| 工具 | 说明 | 主要参数 |
| --- | --- | --- |
| `alerts_list` | 异常告警（未确认优先） | `limit` |
| `alerts_ack` | 确认一条告警 | `id` |

### 上传与存储桶

| 工具 | 说明 | 主要参数 |
| --- | --- | --- |
| `upload_test` | 后台上传测试：文件内容 base64，走指定插件完成真实上传 | `fileName` `contentBase64` `contentType` `bucket` `pluginId` |
| `uploads_list` | 上传列表，按桶/类型/插件/关键字过滤 | `bucket` `category` `pluginId` `keyword` `limit` |
| `bucket_list` | 桶列表及每桶对象数与总大小 | — |
| `bucket_create` | 创建桶 | `name` `description` `publicRead` |
| `bucket_update` | 修改桶说明与公开读 | `name` `description` `publicRead` |
| `bucket_delete` | 删除空桶 | `name` |

### 用户与凭据

| 工具 | 说明 | 主要参数 |
| --- | --- | --- |
| `user_list` | 用户列表、角色与权限（管理员） | — |
| `user_create` | 创建用户并指定角色与额外权限 | `username` `password` `displayName` `role` `extraPerms` |
| `user_update` | 修改角色、权限、显示名、禁用状态 | `id` `role` `extraPerms` `revokedPerms` `displayName` `disabled` |
| `apikey_list` | 查看 API Key（含 S3 AccessKeyId） | — |
| `apikey_create` | 创建 API Key，返回 SecretAccessKey 明文（之后仍可随时取回） | `name` `userId` `scopes` `expiresInDays` |
| `apikey_reveal` | 随时查看指定密钥的完整凭据（含明文 / Bearer 令牌），仅本人或管理员可用 | `id` |

---

## 3. 典型工作流

### 查看站点健康态势

```json
{"name":"site_info","arguments":{}}
{"name":"plugin_list","arguments":{}}
{"name":"alerts_list","arguments":{"limit":10}}
```

### 用监控数据做一次巡检

```json
{"name":"monitor_summary","arguments":{"range":"24h"}}
{"name":"monitor_errors","arguments":{"level":"error","seconds":3600,"limit":20}}
{"name":"monitor_storage","arguments":{"scan":false}}
```

判断顺序：先看 `errRate` 与 P95 是否异常，再看错误日志里 5xx 的 `route` 与 `message`，最后看存储是否有断链或磁盘逼近上限。需要精确核对磁盘与元数据是否一致时，再传 `scan=true` 做深度体检（耗时随对象数增长，不要频繁调用）。

### 临时 Token 过期导致插件标红，如何修复

```json
{"name":"plugin_parse_token","arguments":{"authorization":"Bearer eyJhbGciOi..."}}
```

返回：

```json
{
  "tokenExpiresAt": "2026-12-08T12:00:00Z",
  "unix": 1796601498,
  "xUserId": "31d95b4d-cf30-47d9-aae8-ffca2051be28",
  "hint": "把 tokenExpiresAt 填入 token_expires_at，把 xUserId 填入 x_user_id。"
}
```

接着一次性更新参数：

```json
{
  "name": "plugin_params_set",
  "arguments": {
    "id": "workbuddy",
    "params": {
      "authorization": "Bearer eyJhbGciOi...",
      "token_expires_at": "2026-12-08T12:00:00Z",
      "x_user_id": "31d95b4d-cf30-47d9-aae8-ffca2051be28"
    }
  }
}
```

最后调试并复检：

```json
{"name":"plugin_debug","arguments":{"id":"workbuddy"}}
{"name":"plugin_health_check","arguments":{"id":"workbuddy"}}
```

### 差量修改 vs 全量修改

```json
// 差量：只改优先级，其他字段原样保留
{"name":"plugin_patch","arguments":{"id":"workbuddy","priority":50,"expectedRevision":7}}

// 全量：用完整配置替换，未列出的参数回到清单默认值
{"name":"plugin_replace","arguments":{"id":"workbuddy","name":"WorkBuddy","enabled":true,"priority":100,"params":{}}}
```

`expectedRevision` 是乐观锁：并发修改导致版本不匹配时返回冲突，避免静默覆盖他人配置。

### 开发一个插件前先查参数模板

```json
{"name":"plugin_registry","arguments":{}}
{"name":"plugin_docs","arguments":{"topic":"ParamField"}}
```

### 从零开发一个插件（Agent 完全掌控）

插件是真正的 Go 程序：一个文件夹（`plugin.json` + 任意多个 `.go`），服务端运行时 `go build` 编译成独立进程调用。
**签名、换 token、PUT 直传、分片、加密请求体**都可以写，不再受模板能力限制。详情见[插件开发文档](plugin-development.md)。

**① 读一份真实可跑的参考实现**（内置 WorkBuddy 就是普通插件文件夹）：

```json
{"name":"plugin_files","arguments":{"id":"workbuddy"}}
{"name":"plugin_file_read","arguments":{"id":"workbuddy","name":"main.go"}}
```

**② 创建插件并编译**（`files` 可覆盖模板文件；不传则用 `template` 生成的骨架）：

```json
{
  "name": "plugin_create",
  "arguments": {
    "id": "my-uploader",
    "name": "我的上传器",
    "template": "http-multipart",
    "enabled": true,
    "priority": 260,
    "fileTypes": ["image", "document"],
    "params": {"upload_url": "https://api.example.com/upload", "token": "Bearer xxx", "file_field": "file"}
  }
}
```

**③ 写代码并自动编译**：`plugin_file_write` 每次写入 `.go` 都会立即编译，
失败时 `build.ok=false` 且 `build.output` 是原始编译器输出，源码会保留供下一轮修复：

```json
{"name":"plugin_file_write","arguments":{"id":"my-uploader","name":"main.go","content":"package main\n\nimport (\n\t\"context\"\n\t\"github.com/Mr-losfer/s3bridge/sdk\"\n)\n\ntype plugin struct{}\n\nfunc (plugin) Upload(ctx context.Context, in sdk.Input, cfg sdk.Config) (*sdk.Output, error) {\n\treturn &sdk.Output{URL: cfg[\"upload_url\"] + \"/\" + in.FileName}, nil\n}\n\nfunc (plugin) Check(ctx context.Context, cfg sdk.Config) error { return nil }\n\nfunc main() { sdk.Serve(plugin{}) }\n"}}
```

多文件拆分用 `plugin_file_write` 写新文件名即可；改名用 `plugin_file_rename`（入口改名会同步 `entry`），
删除用 `plugin_file_delete`（至少保留一个 `.go`）。

**④ 试编译 → 改清单 → 配参数 → 调试 → 真实上传**：

```json
{"name":"plugin_validate","arguments":{"id":"my-uploader","files":{"main.go":"<源码>"}}}
{"name":"plugin_manifest_update","arguments":{"id":"my-uploader","manifest":{"schema":2,"id":"my-uploader","name":"我的上传器","version":"1.1.0","entry":"main.go","timeoutSecs":45,"priority":260,"fileTypes":["image"],"validity":{"intervalHours":48},"params":[{"key":"upload_url","label":"上传地址","type":"url","required":true},{"key":"token","label":"Token","type":"secret","sensitive":true,"volatile":true,"expiresField":"token_expires_at"},{"key":"token_expires_at","label":"过期时间","type":"datetime","volatile":true}]}}}
{"name":"plugin_params_set","arguments":{"id":"my-uploader","params":{"upload_url":"https://api.example.com/upload","token":"Bearer xxx","token_expires_at":"2099-01-01T00:00:00Z"}}}
{"name":"plugin_build","arguments":{"id":"my-uploader"}}
{"name":"plugin_debug","arguments":{"id":"my-uploader"}}
{"name":"upload_test","arguments":{"fileName":"a.png","contentType":"image/png","contentBase64":"UE5HREFBUEE=","bucket":"default","pluginId":"my-uploader"}}
```

`plugin_debug` 会带回**请求追踪**：插件发出的每个请求的方法、URL、状态码、耗时与响应片段，
敏感请求头与 query 参数自动脱敏——这是排查「上游到底返回了什么」最快的方式。

**⑤ 导出 / 迁移 / 删除**：`plugin_export` 返回全部源码与清单，逐项 `plugin_file_write` 即可在另一实例复刻；
`plugin_delete` 删除自定义插件，变更记录仍可用 `plugin_changes` 回溯。

> 源码写操作要求 `plugin:write`；`plugin_debug` 要求 `plugin:debug`。所有写入都记录到 `ChangeLog`
> （动作：`create` / `file-write` / `file-rename` / `file-delete` / `build` / `manifest` / `params` / `delete`）。
> 内置插件源码可读、可改（改动只影响本地，不随升级覆盖），但内置插件本身不可删除，只能停用。

### 上传测试

```json
{
  "name": "upload_test",
  "arguments": {
    "fileName": "test.png",
    "contentType": "image/png",
    "contentBase64": "iVBORw0KGgoAAAANS...",
    "bucket": "default",
    "pluginId": "workbuddy"
  }
}
```

---

## 4. MCP 资源（Resources）

`resources/list` 返回 6 份文档资源，`resources/read` 按 URI 读取 Markdown 正文：

| URI | 内容 |
| --- | --- |
| `docs://plugin-development` | 插件开发文档 |
| `docs://s3-api` | S3 开放接口文档 |
| `docs://mcp` | 本文档 |
| `docs://installation` | 安装与部署 |
| `docs://monitoring` | 监控中心与存储体检 |
| `docs://gallery` | 全站图片相册 |

文档由 `//go:embed` 打包进二进制，与仓库 `docs/*.md`、后台文档中心、`plugin_docs` 工具**始终是同一份内容**，不会出现版本漂移。

---

## 5. 安全说明

- 所有写操作都要求对应权限；`plugin_debug` 需要 `plugin:debug`，`system_settings_update` 与 `system_update_apply` 需要 `system:manage`，`user_*` 需要 `user:manage`。
- `system_update_apply` 会替换服务自身的可执行文件，因此额外要求：Release 必须带 `checksums.txt` 且 SHA256 匹配、下载域名在白名单内、服务已启用自重启能力；任一条件不满足都会中止并回滚。
- 工具返回的敏感参数一律脱敏；SecretAccessKey 明文只在 `apikey_create` / `apikey_reveal` 与后台「API 密钥」页、「MCP 能力」页顶部出现，且仅密钥本人或管理员登录后可取回。密钥列表始终只返回 `secretHint` 脱敏前缀。
- 插件源码写入有多重约束：文件名拒绝路径穿越，只允许文本类扩展名，单文件 512 KB、总量 2 MB、最多 64 个文件；编译在子进程内完成，`CGO_ENABLED=0`，超时会被强杀。
- 插件以**子进程**运行，崩溃或超时不会影响主服务；同一插件的调用串行执行，插件里无需自行加锁。
- 请求追踪只保留脱敏后的头部与 512 字节响应片段，不会把凭据写进日志或接口返回。
- 每次变更都会写入 `ChangeLog`，记录 actor（MCP 调用者用户名）、动作与 diff，可在后台或 `plugin_changes` 中回溯。
- 令牌请通过环境变量传给 stdio 进程，不要写进配置文件并提交仓库。
