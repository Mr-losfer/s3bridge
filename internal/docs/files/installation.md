# 安装与部署

S3Bridge 是**单文件 Go 二进制 + 内嵌前端**，无外部数据库、无 Redis、无依赖服务。数据默认落在一个 JSON 文件 + 对象目录里，一条 `docker run` 即可跑起来。

仓库地址：**https://github.com/Mr-losfer/s3bridge**

---

## 1. 最快：Docker 单容器

```bash
docker run -d --name s3bridge \
  -p 8080:8080 \
  -v s3bridge-data:/app/data \
  -e S3BRIDGE_BOOTSTRAP_PASSWORD='请改成你的强密码' \
  --restart unless-stopped \
  ghcr.io/mr-losfer/s3bridge:latest
```

打开 `http://localhost:8080/` 即可看到后台。查看初始密码与运行状态：

```bash
docker logs s3bridge          # 未设置密码时，随机密码只在这里打印一次
curl -s http://localhost:8080/healthz
```

容器内路径固定为 `/app/data`，**务必挂载卷**，否则重启后密钥、用户数据与上传对象都会丢失。

### 从仓库直接构建单容器（不需要先下载 Release）

```bash
git clone https://github.com/Mr-losfer/s3bridge.git
cd s3bridge
docker build -t s3bridge:local -f deployments/Dockerfile .
docker run -d --name s3bridge -p 8080:8080 \
  -v s3bridge-data:/app/data \
  -e S3BRIDGE_BOOTSTRAP_PASSWORD='请改成你的强密码' \
  --restart unless-stopped s3bridge:local
```

也可以直接用 Git 上下文构建，不落地源码：

```bash
docker build -t s3bridge:local \
  -f deployments/Dockerfile \
  https://github.com/Mr-losfer/s3bridge.git#main
```

> Dockerfile 是多阶段构建：Node 阶段编译 Vue 前端，Go 阶段静态编译，最终镜像基于 `scratch`/`alpine` 只包含二进制与 CA 证书，体积约 20MB，非 root 运行。

---

## 2. Docker Compose（推荐生产）

把下面内容保存为 `docker-compose.yml`，并创建 `.env` 写入 `S3BRIDGE_BOOTSTRAP_PASSWORD`：

```yaml
services:
  s3bridge:
    image: ghcr.io/mr-losfer/s3bridge:latest
    container_name: s3bridge
    restart: unless-stopped
    ports:
      - "8080:8080"
    environment:
      S3BRIDGE_BOOTSTRAP_USER: admin
      S3BRIDGE_BOOTSTRAP_PASSWORD: ${S3BRIDGE_BOOTSTRAP_PASSWORD:?请在 .env 中设置管理员密码}
      S3BRIDGE_PUBLIC_URL: https://s3.example.com
      S3BRIDGE_MAX_UPLOAD_BYTES: "67108864"
      S3BRIDGE_LOG_LEVEL: info
    volumes:
      - s3bridge-data:/app/data
    healthcheck:
      test: ["CMD", "wget", "-qO-", "http://127.0.0.1:8080/healthz"]
      interval: 30s
      timeout: 5s
      start_period: 10s
      retries: 3
    security_opt:
      - no-new-privileges:true

volumes:
  s3bridge-data:
```

```bash
echo "S3BRIDGE_BOOTSTRAP_PASSWORD=$(openssl rand -base64 24)" > .env
docker compose up -d
docker compose logs -f
docker compose pull && docker compose up -d   # 升级
```

仓库 `deployments/docker-compose.yml` 里还提供了「从源码构建」与「带 Caddy 自动 HTTPS」两种变体。

---

## 3. Linux 二进制（systemd）

从 [Releases](https://github.com/Mr-losfer/s3bridge/releases) 下载对应平台的压缩包：

```bash
# amd64 / arm64 按需替换；其他平台见下方版本矩阵
ARCH=amd64
curl -fsSL -o s3bridge.tar.gz \
  "https://github.com/Mr-losfer/s3bridge/releases/latest/download/s3bridge_linux_${ARCH}.tar.gz"
tar -xzf s3bridge.tar.gz
sudo install -m 0755 s3bridge      /usr/local/bin/s3bridge
sudo install -m 0755 s3bridge-mcp  /usr/local/bin/s3bridge-mcp
s3bridge --help 2>/dev/null || true   # 服务无 help 参数，直接运行即可
```

创建专用用户与数据目录：

```bash
sudo useradd --system --home /var/lib/s3bridge --shell /usr/sbin/nologin s3bridge
sudo install -d -o s3bridge -g s3bridge -m 0700 /var/lib/s3bridge
sudo tee /etc/s3bridge.env >/dev/null <<'EOF'
S3BRIDGE_ADDR=127.0.0.1:8080
S3BRIDGE_DATA_DIR=/var/lib/s3bridge
S3BRIDGE_BOOTSTRAP_USER=admin
S3BRIDGE_BOOTSTRAP_PASSWORD=请改成你的强密码
S3BRIDGE_PUBLIC_URL=https://s3.example.com
S3BRIDGE_LOG_LEVEL=info
EOF
sudo chmod 600 /etc/s3bridge.env
```

systemd unit `/etc/systemd/system/s3bridge.service`：

```ini
[Unit]
Description=S3Bridge 插件化上传聚合与 S3 网关
Documentation=https://github.com/Mr-losfer/s3bridge
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=s3bridge
Group=s3bridge
WorkingDirectory=/var/lib/s3bridge
EnvironmentFile=/etc/s3bridge.env
ExecStart=/usr/local/bin/s3bridge
Restart=always
RestartSec=5
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=strict
ProtectHome=true
ReadWritePaths=/var/lib/s3bridge

[Install]
WantedBy=multi-user.target
```

```bash
sudo systemctl daemon-reload
sudo systemctl enable --now s3bridge
sudo journalctl -u s3bridge -f
```

---

## 4. 从源码构建（Go 1.21+）

前端构建产物已随仓库提交（`internal/web/dist/`），**部署机不需要 Node.js**：

```bash
git clone https://github.com/Mr-losfer/s3bridge.git
cd s3bridge
make build            # 生成 bin/s3bridge 与 bin/s3bridge-mcp
./bin/s3bridge
```

需要修改前端时（要求 Node 22 + pnpm）：

```bash
make web              # pnpm install && vite build → internal/web/dist/
make all              # gofmt 检查 + vet + test + build
```

也可以用 `go install` 直接安装：

```bash
go install github.com/Mr-losfer/s3bridge/cmd/s3bridge@latest
go install github.com/Mr-losfer/s3bridge/cmd/s3bridge-mcp@latest
```

---

## 5. 版本矩阵（Release 产物）

每个 tag 都会由 GitHub Actions + GoReleaser 构建并发布：

| 平台 | 压缩包 |
| --- | --- |
| Linux x86_64 | `s3bridge_linux_amd64.tar.gz` |
| Linux ARM64 | `s3bridge_linux_arm64.tar.gz` |
| macOS Intel | `s3bridge_darwin_amd64.tar.gz` |
| macOS Apple Silicon | `s3bridge_darwin_arm64.tar.gz` |
| Windows x86_64 | `s3bridge_windows_amd64.zip` |

每个包内含 `s3bridge`（主服务）、`s3bridge-mcp`（stdio MCP 客户端）、`README.md`、`LICENSE` 与校验文件 `checksums.txt`。容器镜像同时推送到 `ghcr.io/mr-losfer/s3bridge`（`linux/amd64` + `linux/arm64` 多架构 manifest）。

---

## 6. 环境变量

| 变量 | 默认值 | 说明 |
| --- | --- | --- |
| `S3BRIDGE_ADDR` | `:8080` | 监听地址，如 `127.0.0.1:8080` |
| `S3BRIDGE_DATA_DIR` | `./data` | 数据目录（容器内 `/app/data`），存放 `state.json`、`master.key`、`objects/` |
| `S3BRIDGE_MASTER_KEY` | 自动生成 | 32 字节主密钥（base64 或原始串），用于加密敏感参数与 API Key。**生产建议显式设置并备份** |
| `S3BRIDGE_BOOTSTRAP_USER` | `admin` | 初始管理员用户名 |
| `S3BRIDGE_BOOTSTRAP_PASSWORD` | 随机生成 | 初始管理员密码；不设置时随机生成并**只在启动日志打印一次** |
| `S3BRIDGE_PUBLIC_URL` | 空 | 对外访问根地址，用于生成 S3/文档示例中的完整 URL；反代后务必设置 |
| `S3BRIDGE_LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `S3BRIDGE_MAX_UPLOAD_BYTES` | `67108864` | 单文件上限（字节），默认 64 MiB |
| `S3BRIDGE_HEALTH_INTERVAL` | `5m` | 全局默认健康检测间隔，最小 10s |
| `S3BRIDGE_SESSION_TTL` | `24h` | 登录 JWT 有效期 |
| `S3BRIDGE_READ_TIMEOUT` | `60s` | HTTP 读超时 |
| `S3BRIDGE_UPDATE_REPO` | `Mr-losfer/s3bridge` | 检测更新所用的 GitHub 仓库（`owner/repo`） |
| `S3BRIDGE_UPDATE_API` | `https://api.github.com` | Releases API 地址；自建镜像或本地测试可覆盖 |
| `S3BRIDGE_UPDATE_TOKEN` | 空 | 可选 GitHub Token，用于提高 API 限额；**只走环境变量，绝不写入仓库文件** |
| `S3BRIDGE_UPDATE_INTERVAL` | `6h` | 自动检测间隔，最小 `1m` |
| `S3BRIDGE_UPDATE_AUTO_CHECK` | `true` | 是否启用定时检测；关闭后仍可在后台手动点「检测更新」 |
| `S3BRIDGE_UPDATE_STRATEGY` | `auto` | `auto`（自动识别容器）/ `binary` / `container` |

> **密钥管理**：未显式设置 `S3BRIDGE_MASTER_KEY` 时，服务会在数据目录生成 `master.key`（权限 0600）。**丢失这个文件等于丢失所有已加密的插件参数与 API Key**，请连同 `state.json` 一起备份。

---

## 7. 首次启动后要做的三件事

1. **登录并改密码**：`http://<host>/` → 用 `S3BRIDGE_BOOTSTRAP_USER` / 密码登录 → 「系统设置」→ 修改密码。
2. **配置插件临时参数**：「插件管理」→ 点击 WorkBuddy → 填入 Bearer Token、`X-User-Id`、Token 过期时间（可用「解析 Token」自动填）→「调试」确认连通 →「检测」恢复健康状态。
3. **创建 API Key**：「API 密钥」→ 创建（明文随时可在列表点「查看」取回，无需一次性抄录）→ 用 `aws --endpoint-url http://<host>/s3 s3 ls` 验证 S3 接口；「MCP 能力」页顶部也会直接显示当前账号的密钥明文与已填好的对接代码。

---

## 8. 反向代理与 HTTPS

生产环境务必用 TLS：SigV4 不加密请求体，且登录令牌在 Header 中明文传输。

Nginx 示例（注意放开大文件上传）：

```nginx
server {
    listen 443 ssl http2;
    server_name s3.example.com;

    ssl_certificate     /etc/letsencrypt/live/s3.example.com/fullchain.pem;
    ssl_certificate_key /etc/letsencrypt/live/s3.example.com/privkey.pem;

    client_max_body_size 128m;      # ≥ S3BRIDGE_MAX_UPLOAD_BYTES
    proxy_request_buffering off;    # 大文件流式转发

    location / {
        proxy_pass http://127.0.0.1:8080;
        proxy_set_header Host $host;
        proxy_set_header X-Real-IP $remote_addr;
        proxy_set_header X-Forwarded-For $proxy_add_x_forwarded_for;
        proxy_set_header X-Forwarded-Proto $scheme;
        proxy_read_timeout 300s;
    }
}
```

Caddy 更简单（自动证书）：

```caddyfile
s3.example.com {
    reverse_proxy 127.0.0.1:8080
    request_body {
        max_size 128MB
    }
}
```

---

## 9. 版本更新、备份与卸载

### 9.1 后台一键自动更新（推荐）

后台首页顶部固定显示「版本更新卡片」。服务每 6 小时自动从本仓库 GitHub Releases 检测最新版，也可以点「检测更新」立即检查。发现新版本时：

1. 卡片显示 `当前版本 → 最新版本`，并把 **重要 / 安全 / 破坏性变更** 以及 Release Notes 中加粗的内容高亮标出；
2. 登录后自动弹出新版本弹窗，展示完整结构化更新内容（可「稍后提醒」或「忽略此版本」）；
3. 管理员点击「立即更新」后自动完成：下载当前平台发布资产 → 校验 `checksums.txt` 的 SHA256 → 备份旧版本到 `data/updates/` → 原子替换可执行文件 → 优雅关闭并原地重启 → 前端自动刷新显示新版本。**全程无需 SSH 或手工敲命令**，进度与日志在弹窗中实时可见。

安全约束：

- 仅允许从 GitHub 官方域名白名单下载（含 302 重定向目标），被篡改的资产地址会被拒绝；
- 缺少 `checksums.txt` 或 SHA256 不匹配时直接中止，不安装；
- 下载、校验、替换、重启任一阶段失败都会从备份自动回滚，服务保持可用；
- 更新接口需要 `system:manage` 权限，普通用户可查看更新内容但不能触发安装。

不同部署方式的行为：

| 部署方式 | 新版本写入位置 | 重启 / 重建后是否保持 |
| --- | --- | --- |
| Linux 二进制 / systemd | 当前可执行文件（同目录原子替换） | 是，磁盘文件已替换 |
| Docker 单容器 / Compose | 数据卷 `/app/data/bin/s3bridge` | 是，`entrypoint.sh` 优先加载数据卷版本，重建容器不回退 |

容器部署若还想同步更新官方镜像，更新完成弹窗会给出可复制命令：`docker compose pull && docker compose up -d`。

如不希望自动检测，设置 `S3BRIDGE_UPDATE_AUTO_CHECK=false`；内网环境可通过 `S3BRIDGE_UPDATE_TOKEN` 提高 GitHub API 限额。

### 9.2 手动备份与升级

```bash
# 备份（先停服务保证 JSON 一致；运行中也可用，Store 是原子写）
docker compose stop
sudo tar -czf s3bridge-backup-$(date +%F).tar.gz /var/lib/s3bridge
docker compose start

# 升级容器
docker compose pull && docker compose up -d

# 升级二进制
sudo systemctl stop s3bridge
sudo install -m 0755 s3bridge /usr/local/bin/s3bridge
sudo systemctl start s3bridge
```

数据结构是纯目录，回滚只需替换二进制、恢复数据目录。

---

## 10. 故障排查

| 现象 | 排查 |
| --- | --- |
| 忘记管理员密码 | 删除 `state.json` 中该用户的 `passwordHash` 不可行；用新的空数据目录重启并显式设置 `S3BRIDGE_BOOTSTRAP_PASSWORD` 重建，或由管理员在「用户与权限」重置 |
| 插件标红 | 插件详情 →「调试」看分步诊断；多为临时 Token 过期或必填参数缺失 |
| S3 返回 `SignatureDoesNotMatch` | 检查 Endpoint 是否带 `/s3`、Region 是否 `us-east-1`、是否使用 path-style、系统时钟偏移是否超过 15 分钟 |
| S3 返回 `AccessDenied` | 密钥是否撤销/过期，或 `scopes` 未包含对应权限 |
| 上传 413 | 调整 `S3BRIDGE_MAX_UPLOAD_BYTES`，并同步放大反代 `client_max_body_size` |
| 后台白屏 | 二进制内嵌前端资源缺失；确认使用官方 Release 或执行过 `make web` |
| 健康检查失败 | `curl http://127.0.0.1:8080/healthz`，容器 healthcheck 也用该端点 |

查看服务状态：`curl -s http://<host>/api/site`（公开接口）。
