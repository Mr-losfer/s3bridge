# S3Bridge

> **插件化的多源上传聚合网关** —— 把任意第三方上传接口转换成统一标准输出，再以 **S3 兼容接口** 对外提供；配套多用户权限后台、插件热配置、健康检测告警与完整 MCP 能力。

语言：**Go（后端，零外部依赖，标准库实现）** · Vue 3 + Vite（后台，构建产物嵌入单二进制）

仓库：[github.com/Mr-losfer/s3bridge](https://github.com/Mr-losfer/s3bridge)

---

## 为什么需要它

对接多家第三方上传服务时，每家的协议、鉴权、临时凭据、响应结构都不一样；业务侧却只想要一套稳定的「上传 → 拿 URL → 按桶/按类型管理 → 用标准 S3 SDK 取回」接口。

S3Bridge 把这些差异收敛到**一个插件文件**里：插件作者只写「协议转换」，配置表单、参数校验、脱敏加密、文档、调试、更新、告警、MCP 工具全部由插件清单自动生成。

```
第三方 A / B / C 上传接口
        │  各写各的插件（几百行 Go）
        ▼
   S3Bridge 统一标准输出 + 按 bucket / 文件类型归档
        │
        ├── 后台管理页面（多用户 / 权限 / 插件 / 告警 / 上传测试）
        ├── S3 兼容接口（SigV4，每用户独立 API Key，aws-cli / boto3 / SDK 直连）
        └── MCP Server（33 个工具，AI 客户端直接运维与开发调试）
```

---

## 30 秒启动

```bash
docker run -d --name s3bridge \
  -p 8080:8080 \
  -v s3bridge-data:/app/data \
  -e S3BRIDGE_BOOTSTRAP_PASSWORD='请改成你的强密码' \
  --restart unless-stopped \
  ghcr.io/mr-losfer/s3bridge:latest
```

打开 **http://localhost:8080/** ，用 `admin` + 你设置的密码登录。

从源码 / Compose / Linux systemd / 各平台二进制安装，见 [安装与部署文档](docs/installation.md)。

---

## 功能一览

| 模块 | 能力 |
| --- | --- |
| **插件框架** | 清单（Manifest）驱动的动态表单、参数分组、脱敏与 AES-256-GCM 加密存储；差量修改 / 全量修改 / 参数修改 / 版本更新；调试分步诊断；变更审计（actor + diff） |
| **临时参数** | 针对第三方会过期的 Bearer Token 等：标记 `Volatile` + 过期时间字段，到期前 24h 提醒、过期判定异常、支持一键从 JWT 解析 `exp` / `sub` |
| **健康检测** | 每插件独立配置间隔 / 超时 / 失败阈值 / 探活模式，保存即重建调度；异常时**插件列表整行标红并置顶**、自动写告警、恢复自动销警 |
| **上传聚合** | 后台图片上传测试（拖拽 + 进度）、统一标准输出、按 **bucket + 文件类型** 双维度归档、按类型/桶/插件/关键字过滤、图片预览与下载 |
| **S3 接口** | SigV4 校验（含 Presigned URL）、`ListBuckets` / `PutObject` / `GetObject` / `HeadObject` / `DeleteObject` / `ListObjectsV2` / `CreateBucket`，标准 XML 响应与错误码 |
| **多用户与权限** | RBAC 三角色 + 逐项授予/撤销；每用户独立 API Key（同时是 S3 凭据与 REST Bearer），支持作用域与有效期，可随时撤销 |
| **存储桶** | 创建 / 编辑 / 公开读 / 删除，对象数与容量统计，与 S3 接口双向同步 |
| **MCP** | 33 个工具 + 4 个文档资源，stdio 与 Streamable HTTP 双通道；后台每个按钮都走同一份工具实现，AI 与人工操作结果/审计完全一致 |
| **文档中心** | 插件开发、S3 接口、MCP、安装部署四份文档随二进制 embed，后台 / MCP / 仓库三处同源 |

---

## 后台页面

| 页面 | 内容 |
| --- | --- |
| 概览 | 运行态势、异常插件置顶、最近上传、站点信息 |
| 插件管理 | 插件列表（异常标红）、启停、立即检测、定时检测设置抽屉 |
| 插件详情 | **Schema 驱动的动态参数表单**、临时参数提醒、Token 解析、调试、差量/全量修改、更新、变更记录 |
| 告警中心 | 未确认优先，一键确认 |
| 上传测试 | 拖拽上传、进度、统一标准输出与签名到期时间 |
| 上传列表 | 按 bucket / 类型 / 插件 / 关键字筛选，图片预览、下载、复制 `s3://` URI、删除 |
| 存储桶 | 桶管理 + S3 接入示例 |
| API 密钥 | 创建（一次性明文）、作用域、有效期、撤销、S3 接入点 |
| MCP 能力 | 工具清单、**在线调试台**、Claude Desktop / Cursor 接入配置 |
| 文档中心 | 四份文档在线渲染 |
| 用户与权限 | 用户管理、角色默认权限、权限矩阵（额外授予 / 撤销） |
| 系统设置 | 站点名、默认插件、健康检测总开关、告警开关、公告、修改密码 |

设计语言：工业实用风 / 编辑式排版，浅色暖白纸感 + 朱红告警，支持深浅主题，状态使用「颜色 + 形状 + 文字」三重编码。

---

## 内置插件：WorkBuddy

完整实现了抓包报文中的开放上传接口：

```
POST https://copilot.tencent.com/v2/backgroundagent/localProxy/upload
Content-Type: multipart/form-data
Authorization: Bearer <JWT> · x-user-id · x-client-version · x-client-product
x-client-platform · user-agent · cookie
```

响应 `data.url` / `data.objectKey` / `data.fileInfo` 映射为统一标准输出，并从 COS 签名参数 `q-sign-time` 解析出链接到期时间。

**所有会变化的临时参数（Bearer Token、Token 过期时间、X-User-Id、Cookie）都在后台表单里配置**，不需要改代码；Token 可一键解析自动填好过期时间与用户 ID。开发自己的插件见 [插件开发文档](docs/plugin-development.md)。

---

## 统一标准输出

无论接哪家第三方，落库对象结构一致：

```json
{
  "id": "obj_xxx",
  "bucket": "default",
  "objectKey": "image/202611/a1b2.png",
  "fileName": "83420.png",
  "mimeType": "image/png",
  "category": "image",
  "size": 2033070,
  "sha256": "…",
  "provider": "workbuddy",
  "remoteUrl": "https://…/83420.png?q-sign-algorithm=sha1&…",
  "urlExpireAt": "2026-12-08T12:00:00Z",
  "createdAt": "2026-11-08T23:58:31Z"
}
```

> 第三方返回的是**临时签名链接**，S3Bridge 同时把文件本体归档到本地桶，因此链接过期后通过 S3 接口取回的对象依然长期有效。

---

## 接口速查

```bash
# 登录
curl -X POST http://localhost:8080/api/auth/login \
  -H 'Content-Type: application/json' \
  -d '{"username":"admin","password":"<你的密码>"}'

# 站点信息
curl http://localhost:8080/api/site

# S3（用后台创建的 API Key）
export AWS_ACCESS_KEY_ID=AK_xxx
export AWS_SECRET_ACCESS_KEY=<明文>
aws --endpoint-url http://localhost:8080/s3 s3 ls
aws --endpoint-url http://localhost:8080/s3 s3 cp ./a.png s3://default/images/a.png

# MCP
curl -X POST http://localhost:8080/mcp \
  -H 'Authorization: Bearer <TOKEN>' -H 'Content-Type: application/json' \
  -d '{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"site_info","arguments":{}}}'
```

详见 [S3 开放接口文档](docs/s3-api.md) 与 [MCP 能力与工具清单](docs/mcp.md)。

---

## 项目结构

```
s3bridge/
├── cmd/
│   ├── s3bridge/          # 主服务（HTTP + 后台 + S3 + MCP over HTTP）
│   └── s3bridge-mcp/      # stdio MCP 客户端
├── internal/
│   ├── config/            # 环境变量配置、主密钥管理
│   ├── store/             # Store 接口 + JSON 原子落盘 + 领域模型
│   ├── auth/              # 密码哈希、JWT、API Key、RBAC
│   ├── plugin/            # 插件契约、参数 Schema、Manager（差量/全量/更新/调试）
│   ├── plugins/workbuddy/ # WorkBuddy 插件实现
│   ├── health/            # 每插件独立定时检测调度器
│   ├── upload/            # 统一上传、按类型与 bucket 归档
│   ├── s3gateway/         # SigV4 校验 + S3 REST 接口（XML）
│   ├── mcp/               # JSON-RPC 协议 + 33 个工具
│   ├── docs/              # 文档 embed（files/ 为 docs/ 的同步副本）
│   ├── api/               # REST 路由、中间件、处理器
│   ├── app/               # 依赖装配
│   └── web/               # 前端 embed（dist/ 为构建产物，随仓库提交）
├── web/src/               # Vue 3 后台源码
├── docs/                  # 四份正式文档（唯一源）
├── deployments/           # Dockerfile / Compose / Caddyfile / systemd unit
├── .github/workflows/     # CI 与 Release 流水线
├── .goreleaser.yml        # 多平台打包
└── Makefile
```

架构上刻意保持**零第三方 Go 依赖**：路由、SigV4、加密、JWT、MCP 协议全部用标准库实现，`go build` 即可得到完整单二进制，部署机不需要 Node.js（前端产物已嵌入）。

---

## 开发命令

```bash
make build        # 构建 bin/s3bridge 与 bin/s3bridge-mcp
make run          # 本地运行（数据目录 ./data）
make test         # go test ./...
make race         # go test -race ./...
make cover        # 覆盖率
make vet          # go vet ./...
make fmt          # gofmt 检查
make web          # 构建前端（需 Node 22 + pnpm）
make docs-sync    # 把 docs/*.md 同步到 internal/docs/files/
make all          # fmt + vet + test + build
make docker       # 构建镜像
```

---

## 质量保障

- 表驱动单元测试、端到端集成测试（登录 → 插件配置 → 上传 → S3 取回 → MCP）、SigV4 客户端交叉验证、`httptest` 上游 mock
- `go test -race ./...` 竞态检测；调度器 goroutine 均满足「可取消、可等待、有所有者」
- CI：gofmt / vet / test -race / coverage / 前端构建 / 文档同步 / govulncheck / 冒烟启动
- Release：GoReleaser 构建 5 个平台压缩包 + checksums，Actions 推送 linux/amd64 + linux/arm64 多架构镜像到 GHCR
- 安全：密码 bcrypt、敏感参数 AES-256-GCM、API Key 明文只显示一次、对象键路径穿越清洗、登录限流、SigV4 常量时间比较

---

## 文档

| 文档 | 内容 |
| --- | --- |
| [插件开发文档](docs/plugin-development.md) | 契约、ParamField 参考、临时参数机制、完整示例、差量/全量修改语义、调试与测试 |
| [S3 开放接口文档](docs/s3-api.md) | 每用户 API Key、SigV4、支持的 API、aws-cli / boto3 / Go / Node 示例 |
| [MCP 能力与工具清单](docs/mcp.md) | 接入配置、33 个工具、典型工作流、Resources |
| [安装与部署](docs/installation.md) | Docker 单容器、Compose、从仓库构建、Linux systemd、版本矩阵、环境变量、故障排查 |

以上文档同时出现在后台「文档中心」与 MCP 的 `plugin_docs` / `s3_docs` / `installation_docs` 工具及 `docs://` 资源中，永远同源。

---

## 许可证

[MIT](LICENSE)
