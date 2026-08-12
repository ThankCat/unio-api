# Unio API 本地开发命令。
#
# 解决三件事：
#   1. 自动从 deploy/env/.env.dev 注入环境变量（config.Load 只读 os.Getenv）。
#   2. 端口由 Dev env 的 GATEWAY_HTTP_ADDR / ADMIN_HTTP_ADDR 各自决定，互不冲突。
#   3. 用 air 做热加载（改 .go 自动重新 build + 重启）。
#
# 常用：
#   make dev          一键启动 postgres+redis 与全部服务（热加载，Ctrl+C 全停）
#   make dev-gateway  只热加载 gateway-server（建议各服务开独立终端，日志更清晰）
#   make dev-admin    只热加载 admin-server
#   make dev-worker   只热加载 worker-server
#   make infra        启动本地基础设施
#   make help         查看全部命令
#
# 注意：本机 GNU Make 为 3.81（不支持 .ONESHELL），所以「注入 Dev env + 启动」的 recipe
# 必须写成用反斜杠续行的单条逻辑命令，保证在同一个子 shell 里执行。

SHELL := /bin/bash
.DEFAULT_GOAL := help

# 把 go install 的工具目录（air 等）并入 PATH；make 用的非交互 shell 默认看不到 ~/go/bin。
export PATH := $(shell go env GOPATH)/bin:$(PATH)

DEV_ENV_FILE := deploy/env/.env.dev
DEV_COMPOSE_FILE := deploy/compose.dev.yml
DEV_COMPOSE := docker compose --env-file $(DEV_ENV_FILE) -f $(DEV_COMPOSE_FILE)
ENV_FILE := $(DEV_ENV_FILE)
LUA_DIR := internal/platform/breakerstore/lua
LUACHECK_VERSION := 1.2.0
STYLUA_VERSION := 2.5.2

.PHONY: help dev dev-gateway dev-admin dev-worker infra infra-down infra-logs ensure-dev-volumes build tidy clean check-env check-air check-lua check-lua-tools

help: ## 显示可用命令
	@grep -hE '^[a-zA-Z_-]+:.*?## .*$$' $(MAKEFILE_LIST) | sort | awk 'BEGIN{FS=":.*?## "}{printf "  \033[36m%-13s\033[0m %s\n", $$1, $$2}'

check-env:
	@if [ ! -f "$(ENV_FILE)" ]; then \
		if [ -f ".env" ]; then \
			echo "缺少 $(ENV_FILE)：检测到旧 .env，请执行 mv .env $(ENV_FILE)"; \
		else \
			echo "缺少 $(ENV_FILE)：先执行 cp deploy/env/.env.dev.example $(ENV_FILE) 并填好 ADMIN_PASSWORD"; \
		fi; \
		exit 1; \
	fi

check-air:
	@if ! command -v air >/dev/null 2>&1; then \
		echo "未找到 air：请先安装 go install github.com/air-verse/air@latest"; \
		exit 1; \
	fi

check-lua-tools:
	@if ! command -v luacheck >/dev/null 2>&1; then \
		echo "未找到 luacheck $(LUACHECK_VERSION)"; \
		exit 1; \
	fi
	@if [ "$$(luacheck --version | awk 'NR == 1 { print $$2 }')" != "$(LUACHECK_VERSION)" ]; then \
		echo "luacheck 必须使用 $(LUACHECK_VERSION)"; \
		exit 1; \
	fi
	@if ! command -v stylua >/dev/null 2>&1; then \
		echo "未找到 StyLua $(STYLUA_VERSION)"; \
		exit 1; \
	fi
	@if [ "$$(stylua --version | awk '{ print $$2 }')" != "$(STYLUA_VERSION)" ]; then \
		echo "StyLua 必须使用 $(STYLUA_VERSION)"; \
		exit 1; \
	fi

check-lua: check-lua-tools ## 检查外置 Redis Lua 的静态错误与格式
	UNIO_LUA_STATIC_CHECK=1 go test ./internal/platform/breakerstore -run '^TestAssembledLuaScriptsPassLuacheck$$' -count=1
	stylua --check $(LUA_DIR)

ensure-dev-volumes: check-env
	@set -a; source "$(DEV_ENV_FILE)"; set +a; \
	for volume in \
		"$$POSTGRES_VOLUME_NAME" \
		"$$REDIS_VOLUME_NAME" \
		"$$LOKI_VOLUME_NAME" \
		"$$ALLOY_VOLUME_NAME"; do \
		if [ -z "$$volume" ]; then \
			echo "Dev volume name must not be empty"; \
			exit 1; \
		fi; \
		docker volume inspect "$$volume" >/dev/null 2>&1 || docker volume create "$$volume" >/dev/null; \
	done

infra: ensure-dev-volumes ## 启动本地基础设施（等待 healthy）
	$(DEV_COMPOSE) up -d --wait

infra-down: ## 停止本地基础设施（保留外部卷）
	$(DEV_COMPOSE) down

infra-logs: ## 跟踪本地基础设施日志
	$(DEV_COMPOSE) logs -f

dev: check-env check-air infra ## 一键启动全部服务（热加载，Ctrl+C 全部停止）
	@set -a; source "$(ENV_FILE)"; set +a; \
	trap 'kill 0' INT TERM EXIT; \
	echo "==> gateway  http://localhost$${GATEWAY_HTTP_ADDR}  /v1/*"; \
	echo "==> admin    http://localhost$${ADMIN_HTTP_ADDR}  /v1/*"; \
	echo "==> worker   (无 HTTP)"; \
	air -c .air.gateway.toml & \
	air -c .air.admin.toml & \
	air -c .air.worker.toml & \
	wait

dev-gateway: check-env check-air ## 热加载 gateway-server（GATEWAY_HTTP_ADDR，/v1/*）
	@set -a; source "$(ENV_FILE)"; set +a; \
	air -c .air.gateway.toml

dev-admin: check-env check-air ## 热加载 admin-server（ADMIN_HTTP_ADDR，/v1/*）
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
