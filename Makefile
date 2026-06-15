# Unio API 本地开发命令。
#
# 解决三件事：
#   1. 自动从 .env 注入环境变量（config.Load 只读 os.Getenv，不再手动 export）。
#   2. 端口由 .env 的 GATEWAY_HTTP_ADDR / ADMIN_HTTP_ADDR / CONSOLE_HTTP_ADDR 各自决定，互不冲突。
#   3. 用 air 做热加载（改 .go 自动重新 build + 重启）。
#
# 常用：
#   make dev          一键启动 postgres+redis 与全部服务（热加载，Ctrl+C 全停）
#   make dev-gateway  只热加载 gateway-server（建议各服务开独立终端，日志更清晰）
#   make dev-admin    只热加载 admin-server
#   make dev-worker   只热加载 worker-server
#   make infra        启动本地 postgres + redis
#   make help         查看全部命令
#
# 注意：本机 GNU Make 为 3.81（不支持 .ONESHELL），所以「注入 .env + 启动」的 recipe
# 必须写成用反斜杠续行的单条逻辑命令，保证在同一个子 shell 里执行。

SHELL := /bin/bash
.DEFAULT_GOAL := help

# 把 go install 的工具目录（air 等）并入 PATH；make 用的非交互 shell 默认看不到 ~/go/bin。
export PATH := $(shell go env GOPATH)/bin:$(PATH)

ENV_FILE := .env

.PHONY: help dev dev-gateway dev-admin dev-worker infra infra-down infra-logs build tidy clean check-env check-air

help: ## 显示可用命令
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-13s\033[0m %s\n", $$1, $$2}'

check-env:
	@if [ ! -f "$(ENV_FILE)" ]; then \
		echo "缺少 $(ENV_FILE)：先执行 cp .env.example .env 并填好 CREDENTIAL_MASTER_KEY / ADMIN_API_TOKEN"; \
		exit 1; \
	fi

check-air:
	@if ! command -v air >/dev/null 2>&1; then \
		echo "未找到 air：请先安装 go install github.com/air-verse/air@latest"; \
		exit 1; \
	fi

infra: ## 启动本地 postgres + redis（等待 healthy）
	docker compose up -d --wait

infra-down: ## 停止本地 postgres + redis
	docker compose down

infra-logs: ## 跟踪 postgres + redis 日志
	docker compose logs -f

dev: check-env check-air infra ## 一键启动全部服务（热加载，Ctrl+C 全部停止）
	@set -a; source "$(ENV_FILE)"; set +a; \
	trap 'kill 0' INT TERM EXIT; \
	echo "==> gateway  http://localhost$${GATEWAY_HTTP_ADDR}  /v1/*"; \
	echo "==> admin    http://localhost$${ADMIN_HTTP_ADDR}  /admin/v1/*"; \
	echo "==> worker   (无 HTTP)"; \
	air -c .air.gateway.toml & \
	air -c .air.admin.toml & \
	air -c .air.worker.toml & \
	wait

dev-gateway: check-env check-air ## 热加载 gateway-server（GATEWAY_HTTP_ADDR，/v1/*）
	@set -a; source "$(ENV_FILE)"; set +a; \
	air -c .air.gateway.toml

dev-admin: check-env check-air ## 热加载 admin-server（ADMIN_HTTP_ADDR，/admin/v1/*）
	@set -a; source "$(ENV_FILE)"; set +a; \
	air -c .air.admin.toml

dev-worker: check-env check-air ## 热加载 worker-server（后台任务）
	@set -a; source "$(ENV_FILE)"; set +a; \
	air -c .air.worker.toml

build: ## 编译三个服务到 ./tmp（不运行）
	go build -o ./tmp/gateway-server ./cmd/gateway-server
	go build -o ./tmp/admin-server ./cmd/admin-server
	go build -o ./tmp/worker-server ./cmd/worker-server

tidy: ## 整理 go.mod / go.sum
	go mod tidy

clean: ## 清理 air 构建产物
	rm -rf ./tmp
