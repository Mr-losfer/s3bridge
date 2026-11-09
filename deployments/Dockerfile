# S3Bridge 多阶段构建：Node 编译前端 → Go 静态编译 → 极小运行镜像
# 用法（仓库根目录）：
#   docker build -t s3bridge:local -f deployments/Dockerfile .

# ---------- Stage 1: 前端 ----------
FROM node:22-alpine AS web
WORKDIR /src
COPY web/package.json web/pnpm-lock.yaml ./web/
RUN corepack enable && cd web && pnpm install --frozen-lockfile
COPY web/ ./web/
# vite.config.js 的 outDir 指向 ../internal/web/dist，产物落在 /src/internal/web/dist
RUN cd web && pnpm build

# ---------- Stage 2: 后端 ----------
FROM golang:1.23-alpine AS build
WORKDIR /src
COPY go.mod ./
COPY . .
# 用 Stage 1  freshly 构建的前端产物覆盖仓库内预构建产物
COPY --from=web /src/internal/web/dist ./internal/web/dist
ARG VERSION=dev
RUN CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/s3bridge ./cmd/s3bridge \
 && CGO_ENABLED=0 GOOS=linux go build -trimpath \
      -ldflags "-s -w -X main.version=${VERSION}" \
      -o /out/s3bridge-mcp ./cmd/s3bridge-mcp

# ---------- Stage 3: 运行 ----------
FROM alpine:3.20
RUN apk add --no-cache ca-certificates tzdata wget \
 && addgroup -S s3bridge \
 && adduser -S -G s3bridge s3bridge
WORKDIR /app
COPY --from=build /out/s3bridge /usr/local/bin/s3bridge
COPY --from=build /out/s3bridge-mcp /usr/local/bin/s3bridge-mcp
RUN mkdir -p /app/data && chown -R s3bridge:s3bridge /app/data
USER s3bridge
ENV S3BRIDGE_ADDR=:8080 \
    S3BRIDGE_DATA_DIR=/app/data \
    TZ=Asia/Shanghai
EXPOSE 8080
VOLUME ["/app/data"]
HEALTHCHECK --interval=30s --timeout=5s --start-period=10s --retries=3 \
  CMD wget -qO- http://127.0.0.1:8080/healthz >/dev/null 2>&1 || exit 1
ENTRYPOINT ["/usr/local/bin/s3bridge"]
