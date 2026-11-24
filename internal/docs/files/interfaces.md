# 接入方式：开放上传、WebDAV、FTP

S3Bridge 除了后台页面与 S3 标准接口，还提供三种轻量接入方式，**都只用一个 API Key**
（`sk_…`，在后台「API 密钥」页创建）。三者与后台登录会话相互独立：Token 过期、改密码都不影响它们。

| 方式 | 入口 | 适用场景 |
| --- | --- | --- |
| 开放上传 | `POST /api/open/upload` | 脚本、浏览器、第三方系统直接投递文件并拿回直链 |
| WebDAV | `/dav/{bucket}/{key}` | 挂载成网络盘（Finder、资源管理器、Cyberduck、rclone） |
| FTP | 独立端口（默认 `2121`） | 老设备、扫描仪、只支持 FTP 的客户端 |

---

## 1. 开放上传

只认 API Key，不需要登录。三种头部写法等价：

```bash
# 方式一：X-API-Key
curl -X POST http://localhost:8080/api/open/upload \
  -H 'X-API-Key: sk_xxxxxxxx' \
  -F 'file=@cat.png'

# 方式二：Authorization Bearer
curl -X POST http://localhost:8080/api/open/upload \
  -H 'Authorization: Bearer sk_xxxxxxxx' \
  -F 'file=@cat.png'

# 方式三：Basic（用户名随意，密码填 API Key）
curl -X POST http://localhost:8080/api/open/upload \
  -u 'me:sk_xxxxxxxx' \
  -F 'file=@cat.png'
```

### 参数

| 位置 | 名称 | 说明 |
| --- | --- | --- |
| Query / 表单字段 | `bucket` | 目标存储桶；留空时按系统设置「随机挑一个桶」（默认）或落到固定桶 |
| Query / 表单字段 | `path` | 对象键；以 `/` 结尾视为目录前缀，自动拼上文件名；留空则按 `类型/年月/哈希_文件名` 生成 |
| Query / 表单字段 | `name` | 原始文件名（原始 body 上传时使用，也可用 `X-File-Name` 头） |

支持两种请求体：

- `multipart/form-data`：文件字段名任意，第一个文件即被接收；**放在文件之前的文本字段**会被当作 `bucket` / `path`。
- 原始 body：直接把文件字节放在请求体里，用 `X-File-Name` 或 `?name=` 指定文件名，适合 `curl --data-binary` 与流式上传。

### 响应

```json
{
  "ok": true,
  "id": "obj_2f8a7d309c214f0b",
  "bucket": "default",
  "key": "image/202611/2f8a7d309c-cat.png",
  "fileName": "cat.png",
  "size": 2033070,
  "mimeType": "image/png",
  "sha256": "…",
  "category": "image",
  "url": "https://bridge.example.com/f/default/image/202611/2f8a7d309c-cat.png?e=1797000000&s=…",
  "directPath": "/f/default/image/202611/2f8a7d309c-cat.png?e=1797000000&s=…",
  "accessMode": "redirect",
  "storage": "plugin",
  "provider": "example",
  "pluginId": "example",
  "upstreamUrl": "https://cdn.example.com/uploads/2f8a7d309c-cat.png",
  "upstreamExpiresAt": "2026-12-23T01:00:00Z",
  "createdAt": "2026-11-23T01:00:00Z"
}
```

| 字段 | 含义 |
| --- | --- |
| `url` | 本站直链，可直接嵌入 `<img>` / `<video>` / Markdown，无需鉴权头。按「文件访问」设置会 302 到上游或由本站转发 |
| `upstreamUrl` | **上游返回的真实地址**（CDN 直连）。想完全绕开本站、把流量直接打到对象存储时用它 |
| `upstreamExpiresAt` | 上游地址的过期时间；为空表示长期有效。过期后从本站直链仍可访问（会回退到本地归档） |
| `storage` | `plugin` 表示已上传到第三方并拿到上游地址；`local` 表示只在本机归档 |
| `provider` / `pluginId` | 实际承接这次上传的插件标识；`local` 表示走了本地回退 |
| `accessMode` | 当前生效的访问方式：`redirect`（302 跳上游）或 `proxy`（本站中转） |

`url` 默认 7 天有效；重新获取时调用 `GET /api/link/sign?bucket=&key=`（同样返回 `upstreamUrl`）。

> 直链响应带 `Accept-Ranges: bytes`，视频可以拖动进度条；文件类型与文件名保持原样。

### 上传落点策略

开放上传默认**不写本地**，按下面的顺序决定：

1. 请求带了 `bucket` → 用请求的桶；否则开启「随机落桶」时从现有桶里随机挑一个，再退回固定桶。
2. 按文件真实类型（MIME 嗅探，不看扩展名）随机挑一个**健康插件**上传。
   健康 = 已启用、能承接该类型、未被健康检测标记异常、且曾经成功过。
3. 没有可用插件或插件上传失败 → 按「插件不可用时回退本地」决定：开启则本地归档，关闭则返回错误。

三项策略都在「系统设置 → 接入方式 → 开放上传」里，MCP 的 `system_settings_update`
对应字段为 `openUploadRandomBucket` / `openUploadUsePlugins` / `openUploadFallbackLocal`。

### 文件访问方式：302 还是本站中转

`/f/{bucket}/{key}` 对**有上游地址**的对象有两种出站方式，在「系统设置 → 文件访问」切换：

| 方式 | 行为 | 带宽 |
| --- | --- | --- |
| `redirect`（默认） | 返回 `302 Location: 上游地址`，浏览器直接连 CDN | 本机只回几百字节 |
| `proxy` | 服务端拉取上游再转发，透传 Range，可拖进度条 | 走本机带宽 |

只有本地归档（没有上游地址）的对象才必然由本机输出。上游连不上或返回 5xx 时，
中转模式会自动回退到本地归档副本，而不是给页面一个坏图。

### 失效图片

图片有效性巡检连续失败达到阈值后，对象被标记为失效：

- 直链不再回源，直接返回占位图，响应头带 `X-Object-Invalid: 1`；
- 配置了「失效图」地址 → `302` 到该图；未配置 → 返回内置 SVG 占位（「文件已失效」）；
- 失效图指向对象自己时自动退回内置占位，不会 302 死循环；
- 上传列表与相册里该对象的预览字段被清空，前端同样走占位图，不会出现破图。

### 直链为什么不用登录

`<img>` 与 `<video>` 无法附加 `Authorization` 头，所以直链用 HMAC 签名代替会话：
链接里携带 `bucket`、`key`、到期时间和签名，服务端只验签，不查库。
签名密钥由主密钥派生，改主密钥会让所有旧直链立即失效。

公开读的存储桶可以省略签名，直接访问 `/f/{bucket}/{key}`；非公开桶必须带签名。

---

## 2. WebDAV

WebDAV 挂在主服务同一端口下，**不需要额外开放端口**：

```
http://localhost:8080/dav/
```

认证用 HTTP Basic：**用户名随意填，密码填 API Key**。

| 客户端 | 填写方式 |
| --- | --- |
| macOS Finder | 前往 → 连接服务器 → `http://host:8080/dav/` |
| Windows 资源管理器 | 映射网络驱动器 → `http://host:8080/dav/` |
| Cyberduck / RaiDrive | 协议选 WebDAV，路径 `/dav/` |
| rclone | `rclone config` → WebDAV → url `http://host:8080/dav/`，user 任意，pass 填 API Key |

### 路径

```
/dav/                    根目录，列出全部存储桶
/dav/{bucket}/           桶根
/dav/{bucket}/a/b.png    对象
```

### 支持的方法

`OPTIONS` `PROPFIND` `PROPPATCH` `GET` `HEAD` `PUT` `DELETE` `MKCOL` `MOVE` `COPY` `LOCK` `UNLOCK`

- `MKCOL /dav/{bucket}` 创建存储桶；更深层的 `MKCOL` 直接返回成功（对象存储没有真实目录）。
- `PUT` 覆盖同名对象（先删后写）；`MOVE`/`COPY` 支持跨桶。
- `LOCK`/`UNLOCK` 返回固定令牌，兼容客户端但不提供真正的并发写保护。
- `PROPFIND` 的 `Depth` 支持 `0` 与 `1`（`infinity` 按 `1` 处理）。

可在「系统设置 → 接入方式」里关闭 WebDAV，关闭后该路径返回 404。

---

## 3. FTP

FTP 需要独立端口，默认 `:2121`（在「系统设置 → 接入方式」里启用并修改监听地址，
保存后自动重启监听，无需重启进程）。

被动模式还会临时开一个数据端口。本机/内网使用可以留空让它随机分配；
**Docker 或 NAT 之后**必须固定端口范围并映射，否则客户端能登录但列不出目录：

| 设置项 | 示例 | 说明 |
| --- | --- | --- |
| 监听地址 | `:2121` | 控制连接端口 |
| 被动端口范围 | `30000-30010` | 数据连接端口，需在 Docker `-p` 或防火墙里放行 |
| 对外通告地址 | `203.0.113.10` | PASV 回复里给客户端的地址；留空则用控制连接的内网 IP |

```bash
docker run -d -p 8080:8080 -p 2121:2121 -p 30000-30010:30000-30010 \
  -e S3BRIDGE_BOOTSTRAP_PASSWORD=... s3bridge
```

```
主机：服务器地址
端口：2121
用户名：随意填
密码：API Key
```

目录结构与 WebDAV 完全一致：`/` 是桶列表，`/bucket/a/b.png` 是对象。

### 能力范围

| 命令 | 说明 |
| --- | --- |
| `USER` / `PASS` | 密码填 API Key |
| `LIST` / `NLST` / `MLSD` | 列目录，虚拟目录按对象键的 `/` 切分 |
| `RETR` / `STOR` | 下载 / 上传（覆盖同名对象） |
| `DELE` / `MKD` / `RMD` | 删除文件 / 建桶 / 删除目录下全部对象 |
| `SIZE` / `MDTM` | 文件大小 / 修改时间 |
| `RNFR` + `RNTO` | 重命名（内部按复制后删除实现） |
| `PASV` / `EPSV` | 只支持被动模式 |

不支持：主动模式（`PORT`/`EPRT`）、断点续传（`REST`）、TLS（`AUTH TLS`）、追加写（`APPE`）。

> FTP 是明文协议，密码与内容都可被旁路嗅探。建议只在内网使用，
> 或通过 SSH 隧道转发端口；公网优先用 WebDAV 或开放上传。

---

## 4. 权限与安全

三种方式都会把 API Key 解析成对应用户，按该用户的权限执行：

- 读取对象需要 `upload:read`，写入/删除需要 `upload:write`；
- 非管理员只能看到自己名下的对象，以及公开读桶里的对象；
- 密钥被撤销或过期后，三种方式同时失效；
- 建议给脚本单独建密钥，只勾选需要的权限，并设置有效期。

创建密钥：后台「API 密钥 → 新建」，或调用 MCP 工具 `apikey_create`。
密钥明文只在创建时展示一次，之后可用 `apikey_reveal` 再次取回（本人或管理员）。
