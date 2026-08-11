# Unio Gateway 本地开发

## 环境

- `go.mod` 声明项目使用 Go `1.26.5`。
- 本地 PostgreSQL 与 Redis 可由 Docker Compose 启动。
- 热加载命令需要 `air`：`go install github.com/air-verse/air@latest`。
- 重新生成数据库访问代码时使用 sqlc；当前生成文件标记的版本为 `1.31.1`。

复制 `.env.example` 为 `.env` 并填写本地配置。Makefile 在启动进程前把该文件加载为环境变量。

## 常用命令

| 命令 | 行为 |
| --- | --- |
| `make help` | 显示 Makefile 中的命令。 |
| `make infra` | 启动并等待本地 PostgreSQL 16 与 Redis 7。 |
| `make infra-down` | 停止 Compose 服务；命名 volume 保留。 |
| `make infra-logs` | 跟踪 PostgreSQL 与 Redis 日志。 |
| `make dev` | 启动 Gateway、Admin、Worker 的热加载进程。 |
| `make dev-gateway` | 只启动 Gateway 热加载进程。 |
| `make dev-admin` | 只启动 Admin 热加载进程。 |
| `make dev-worker` | 只启动 Worker 热加载进程。 |
| `make build` | 编译三个常驻程序到 `tmp/`。 |
| `go test ./...` | 运行 Go 测试。 |
| `sqlc generate` | 按 `sqlc.yaml` 重新生成 `internal/platform/store/sqlc`。 |

依赖 PostgreSQL 或 Redis 的测试从 `DATABASE_URL`、`REDIS_ADDR` 读取连接信息；未提供所需变量的用例会
跳过。直连真实上游的用例还需要各自的显式开关。执行这些测试时使用隔离的测试资源，不使用本地
业务数据。

## 数据库与 sqlc

- `migrations/` 平铺保存每张表的 `.up.sql` 和 `.down.sql`。
- 当前服务启动路径只连接数据库，不执行 migration runner；启动前由外部迁移工具准备 Schema。
- `sqlc.yaml` 从 `migrations/*.up.sql` 读取 Schema，从 `sql/queries/shared`、`gateway`、`admin` 和 `worker`
  读取查询。
- `internal/platform/store/sqlc` 是生成目录，修改 Schema 或查询后运行 `sqlc generate`。

## Test Docker 部署

完整步骤（架构、Cloudflare、Caddy、环境变量、前端发布与排障）见
**[deploy/TEST-DEPLOY.md](./deploy/TEST-DEPLOY.md)**。

以下为摘要。Test 部署使用 `deploy/compose.test.yml`，与根目录的本地开发 Compose 分离。首次使用时从
`deploy/env/.env.docker.example` 和 `deploy/env/.env.test.example` 分别创建实际环境文件并替换占位密码。
实际的 `.env.docker`、`.env.test` 已由 `.gitignore` 排除；包含 Test 凭据的文件权限应设置为 `600`。

构建前使用脚本从当前 Git HEAD 写入镜像版本信息：

```bash
./deploy/prepare-image-env.sh  # develop 自动识别为 Test，main 自动识别为 Production
```

脚本只允许在 `develop` 或 `main` 分支执行，并要求工作树干净，且 HEAD 恰好存在一个合法的 Git tag。
`develop` 自动生成 Test 配置，`main` 自动生成 Production 配置。该 tag 同时作为 `IMAGE_TAG` 和
`IMAGE_VERSION`，完整 commit 写入 `IMAGE_REVISION`，脚本执行时间写入 `IMAGE_CREATED`。脚本不会切换、拉取或
合并分支。

Compose 变量按顺序加载，后面的 Test 文件可以覆盖 Docker 构建默认值：

```bash
docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  config
```

先构建版本相同的四个独立镜像，再启动整套 Test 服务：

```bash
docker compose --env-file deploy/env/.env.docker --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml build gateway admin worker migrate
docker compose --env-file deploy/env/.env.docker --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml up -d --no-build --wait
```

`migrate` 是一次性容器；迁移成功退出后 Gateway、Admin 和 Worker 才会启动，使用 `docker compose ... logs migrate`
可以检查迁移结果。Gateway、Admin、Worker、migration 是同一个 Dockerfile 的四个独立 target，共用版本和
revision，但每个 runtime 镜像只包含自己的二进制。Test 在一台服务器运行全部服务，构建制品边界与未来
Production 保持一致。Test PostgreSQL、Redis、network 和 volume 由 `COMPOSE_PROJECT_NAME` 隔离，不连接本地
开发数据。停止环境时使用 `down` 保留 Test 数据卷；仅在确认不再需要 Test 数据时才使用 `down --volumes`。

Nginx 是 Test 环境唯一映射到宿主机的 HTTP 入口，默认地址为 `http://127.0.0.1:18080`。按 Host 分流：
`test-api.unioapi.com` 除 `/metrics` 和 `/internal/*` 外统一转发到 Gateway，由 Gateway Router 负责业务路径和
`/v1` 前缀兼容；`test-admin.unioapi.com/v1/*` 转发到 Admin（同域还托管 `/var/www/admin` 静态前端）。因此
Gateway 客户端 BaseURL 填 `https://test-api.unioapi.com` 或 `https://test-api.unioapi.com/v1` 均可。
`/nginx-healthz` 检查代理本身，`/healthz` 与 `/readyz` 检查 Gateway。Gateway 和 Admin 的容器端口仅在
Compose 网络内可见。

公网访问时，由宿主机 Caddy 监听 80，再反代到 `127.0.0.1:18080`（Cloudflare Flexible 场景下源站用 HTTP）。
配置见 `deploy/caddy/Caddyfile.test`：

```bash
sudo apt update && sudo apt install -y caddy
sudo cp deploy/caddy/Caddyfile.test /etc/caddy/Caddyfile
sudo systemctl enable --now caddy
sudo systemctl reload caddy
```

Admin 前端静态文件放在宿主机 `/var/www/admin`（compose 只读挂载进容器）。部署前请 `mkdir -p` 并保证部署用户可写，或用
`rsync` 同步 `unio-admin` 的 `dist/`。

Test 日志分为两条独立路径：所有容器 stdout/stderr 使用 Docker `json-file` driver，并由 `.env.docker` 中的
`DOCKER_LOG_MAX_SIZE`、`DOCKER_LOG_MAX_FILE` 控制轮转；Gateway 的结构化 `gateway.jsonl` 写入
`gateway_logs` volume，由 Alloy 只读采集并发送到 Loki。Alloy 不采集 Gateway stdout，避免 Loki 中重复日志。
Loki 默认保留 14 天，Admin 通过 Compose 内网地址 `http://loki:3100` 查询。

## 目录

| 路径 | 当前职责 |
| --- | --- |
| `cmd/` | 进程和命令行入口。 |
| `internal/app/` | HTTP 与 Worker 入口装配。 |
| `internal/service/` | 业务编排。 |
| `internal/core/` | 领域能力。 |
| `internal/platform/` | 配置、存储、Redis、HTTP、日志与可观测基础设施。 |
| `migrations/` | PostgreSQL Schema。 |
| `sql/queries/` | sqlc 查询源。 |
| `scripts/` | 本地种子脚本。 |

## 文档交接

长期 Gateway 文档维护在
[Unio Blueprint](https://github.com/unioapi/unio-blueprint/tree/main/docs/products/gateway)。代码改造期间可在
`docs/changes/<change-id>/PLAN.md` 保存临时计划；实现和测试完成后，按实际代码行为更新 Blueprint，并删除
临时计划。
