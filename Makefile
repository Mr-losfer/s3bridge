SHELL := /bin/bash
BINARY := s3bridge
MCP_BINARY := s3bridge-mcp
VERSION ?= $(shell git describe --tags --always --dirty 2>/dev/null || echo dev)
LDFLAGS := -s -w -X main.version=$(VERSION)

.PHONY: all build fmt vet test race cover web docs-sync check run clean docker help

all: fmt vet test build

## build: 构建主服务与 MCP 服务（前端产物已随仓库提供，无需 Node）
build:
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(BINARY) ./cmd/s3bridge
	CGO_ENABLED=0 go build -trimpath -ldflags "$(LDFLAGS)" -o bin/$(MCP_BINARY) ./cmd/s3bridge-mcp

## fmt: gofmt 检查（有输出表示未格式化）
fmt:
	@test -z "$$(gofmt -s -l .)" || (echo "需要 gofmt:" && gofmt -s -l . && exit 1)

## vet: 静态检查
vet:
	go vet ./...

## test: 单元测试
test:
	go test ./...

## race: 竞态检测
race:
	go test -race ./...

## cover: 覆盖率
cover:
	go test -coverprofile=coverage.out ./... && go tool cover -func=coverage.out | tail -1

## web: 构建 Vue 前端到 internal/web/dist（需要 pnpm）
web:
	cd web && pnpm install --frozen-lockfile && pnpm build

## docs-sync: 同步 docs/*.md 到 internal/docs/files/（embed 副本，改文档后必须执行）
docs-sync:
	@cp docs/*.md internal/docs/files/
	@echo "docs/ 已同步到 internal/docs/files/"

## check: 完整质量门禁（fmt + vet + race + cover + build）
check: fmt vet race cover build

## run: 本地运行（默认数据目录 ./data）
run:
	go run ./cmd/s3bridge

## docker: 构建镜像
docker:
	docker build -t s3bridge:$(VERSION) -f deployments/Dockerfile .

## clean: 清理构建产物
clean:
	rm -rf bin coverage.out coverage.html

## help: 显示帮助
help:
	@grep -E '^## ' Makefile | sed 's/## //'
