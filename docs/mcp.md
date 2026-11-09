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

后台「MCP 能力」页提供**在线调试台**：选择工具 → 自动填充必填参数示例 → 执行 `tools/call` → 查看格式化结果。

---

## 2. 工具清单（37 个）

### 站点与系统

| 工具 | 说明 | 主要参数 |
| --- | --- | --- |
| `site_info` | 版本、运行时长、S3 接入点、插件/用户/桶/对象数量、调度器状态 | — |
| `site_stats` | 按文件类型/桶/插件的分布、插件状态统计、告警与变更数 | — |
| `system_settings_get` | 查看系统设置 | — |
| `system_settings_update` | 修改站点名、默认插件、健康检测总开关、告警开关、公告 | `siteName` `defaultPlugin` `healthEnabled` `healthIntervalSecs` `alertOnFailure` `announcement` |
| `system_update_status` | 查看当前版本、Releases 最新版、结构化更新要点（含重点标注）、自动更新进度与部署策略 | — |
| `system_update_check` | 立即从 GitHub Releases 检测最新版本 | `force` |
| `system_update_apply` | **一键自动更新**：下载 → SHA256 校验 → 备份 → 原子替换 → 自动重启；返回后轮询 `system_update_status` 查看进度 | — |
| `system_update_dismiss` | 忽略指定版本的新版本弹窗（首页卡片仍显示） | `tag` |

> 版本更新工具与后台「版本更新卡片」共用同一份状态机。`site_info` 也会返回 `updateAvailable` / `latestVersion`，便于 AI 巡检时一并判断是否需要升级。

### 插件开发与运维

| 工具 | 说明 | 主要参数 |
| --- | --- | --- |
| `plugin_list` | 插件列表，异常插件排前面，含状态/版本/参数警告 | — |
| `plugin_get` | 单个插件详情：参数 Schema、分组、脱敏后的当前值 | `id` |
| `plugin_registry` | 已编译插件类型清单与完整参数 Schema，供开发新插件 | — |
| `plugin_docs` | 插件开发文档全文 + 实时参数模板 | `topic` |
| `plugin_debug` | **插件调试**：无副作用地执行参数校验、临时凭据有效期检查、上游连通性探测，返回分步诊断 | `id` |
| `plugin_patch` | **差量修改**：只更新传入字段，`params` 按键合并 | `id` `name` `description` `enabled` `priority` `params` `health` `expectedRevision` |
| `plugin_replace` | **全量修改**：完整配置替换，未提供的参数重置为默认值 | 同上 |
| `plugin_update` | **插件更新**：升级版本、同步清单新增参数 | `id` `version` `syncParams` `resetToDefault` `note` |
| `plugin_params_set` | **参数修改**：按 key 覆盖，用于轮换临时 Token | `id` `params` |
| `plugin_toggle` | 启用/停用插件 | `id` `enabled` |
| `plugin_health_check` | 立即执行一次健康检测并更新状态/告警 | `id` |
| `plugin_health_config` | **每插件独立定时检测**：间隔、超时、失败阈值、探活模式 | `id` `enabled` `intervalSecs` `timeoutSecs` `failThreshold` `probeMode` |
| `plugin_changes` | 变更记录（差量/全量/更新/调试/参数修改审计） | `id` `limit` |
| `plugin_parse_token` | 解析 WorkBuddy Bearer Token 的 `exp` 与 `sub`，自动得到过期时间与 `x-user-id` | `authorization` |
| `s3_docs` | S3 开放接口文档 | — |
| `installation_docs` | 安装部署文档 | — |

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
| `apikey_create` | 创建 API Key，返回**一次性** SecretAccessKey | `name` `userId` `scopes` `expiresInDays` |

---

## 3. 典型工作流

### 查看站点健康态势

```json
{"name":"site_info","arguments":{}}
{"name":"plugin_list","arguments":{}}
{"name":"alerts_list","arguments":{"limit":10}}
```

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

`resources/list` 返回 4 份文档资源，`resources/read` 按 URI 读取 Markdown 正文：

| URI | 内容 |
| --- | --- |
| `docs://plugin-development` | 插件开发文档 |
| `docs://s3-api` | S3 开放接口文档 |
| `docs://mcp` | 本文档 |
| `docs://installation` | 安装与部署 |

文档由 `//go:embed` 打包进二进制，与仓库 `docs/*.md`、后台文档中心、`plugin_docs` 工具**始终是同一份内容**，不会出现版本漂移。

---

## 5. 安全说明

- 所有写操作都要求对应权限；`plugin_debug` 需要 `plugin:debug`，`system_settings_update` 与 `system_update_apply` 需要 `system:manage`，`user_*` 需要 `user:manage`。
- `system_update_apply` 会替换服务自身的可执行文件，因此额外要求：Release 必须带 `checksums.txt` 且 SHA256 匹配、下载域名在白名单内、服务已启用自重启能力；任一条件不满足都会中止并回滚。
- 工具返回的敏感参数一律脱敏，SecretAccessKey 明文只在 `apikey_create` 的响应中出现一次。
- 每次变更都会写入 `ChangeLog`，记录 actor（MCP 调用者用户名）、动作与 diff，可在后台或 `plugin_changes` 中回溯。
- 令牌请通过环境变量传给 stdio 进程，不要写进配置文件并提交仓库。
