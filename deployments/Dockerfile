# S3Bridge 多阶段构建：Node 编译前端 → Go 静态编译 → 极小运行镜像
# 用法（仓库根目录）：
#   docker build -t s3bridge:local -f deployments/Dockerfile .
#
# 多架构构建说明：前两个阶段固定跑在 $BUILDPLATFORM（构建机架构），
# 只有最后阶段按目标架构生成。否则 buildx 会让 pnpm install 与 go build
# 在 QEMU 模拟的 arm64 下执行，单次构建从几分钟涨到一小时以上。

# ---------- Stage 1: 前端 ----------
FROM --platform=$BUILDPLATFORM node:22-alpine AS web
WORKDIR /src
COPY web/package.json web/pnpm-lock.yaml ./web/
RUN corepack enable && cd web && pnpm install --frozen-lockfile
COPY web/ ./web/
# vite.config.js 的 outDir 指向 ../internal/web/dist，产物落在 /src/internal/web/dist
RUN cd web && pnpm build

# ---------- Stage 2: 后端 ----------
FROM --platform=$BUILDPLATFORM golang:1.27-alpine AS build
WORKDIR /src
# 模块代理可覆盖：内网/受限网络构建时传 --build-arg GOPROXY=https://你的镜像,direct
ARG GOPROXY=https://proxy.golang.org,direct
ENV GOPROXY=${GOPROXY}
# 依赖层单独缓存：go.mod/go.sum 没变时不必重下模块（目前只有内嵌解释器一个依赖）。
COPY go.mod go.sum ./
RUN go mod download
COPY . .
# 用 Stage 1  freshly 构建的前端产物覆盖仓库内预构建产物
COPY --from=web /src/internal/web/dist ./internal/web/dist
ARG VERSION=dev
# 交叉编译：在构建机架构上直接产出目标架构二进制，无需 QEMU。
ARG TARGETOS=linux
ARG TARGETARCH=amd64
RUN CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/s3bridge ./cmd/s3bridge \
 && CGO_ENABLED=0 GOOS=${TARGETOS} GOARCH=${TARGETARCH} go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/s3bridge-mcp ./cmd/s3bridge-mcp

# ---------- Stage 3: 运行 ----------
# 插件由内嵌解释器直接执行，镜像里不需要 Go 工具链。
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget \
 && addgroup -S s3bridge \
 && adduser -S -G s3bridge s3bridge
WORKDIR /app
# 内置版本放 /app/bin；自更新版本放数据卷 /app/data/bin，由 entrypoint 优先加载。
# --chown/--chmod 直接写在 COPY 上：若改用 RUN chown -R，overlayfs 会把 19MB 的二进制
# 整份复制进新层，镜像直接翻倍。
COPY --from=build --chown=s3bridge:s3bridge --chmod=0755 /out/s3bridge /app/bin/s3bridge
COPY --from=build --chown=s3bridge:s3bridge --chmod=0755 /out/s3bridge-mcp /app/bin/s3bridge-mcp
COPY --chown=s3bridge:s3bridge --chmod=0755 deployments/entrypoint.sh /app/entrypoint.sh
RUN mkdir -p /app/data/bin \
 && chown -R s3bridge:s3bridge /app/data
USER s3bridge
ENV S3BRIDGE_ADDR=:8080 \
    S3BRIDGE_DATA_DIR=/app/data \
    S3BRIDGE_UPDATE_STRATEGY=container \
    PATH=/app/data/bin:/app/bin:/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin \
    TZ=Asia/Shanghai
EXPOSE 8080
VOLUME ["/app/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/app/entrypoint.sh"]
