# Unio Gateway 本地开发

## 环境

- `go.mod` 声明项目使用 Go `1.26.6`。
- 本地 PostgreSQL、Redis 与观测组件可由 `deploy/compose.dev.yml` 启动。
- 热加载命令需要 `air`：`go install github.com/air-verse/air@latest`。
- 重新生成数据库访问代码时使用 sqlc；当前生成文件标记的版本为 `1.31.1`。

复制 `deploy/env/.env.dev.example` 为 `deploy/env/.env.dev` 并填写本地配置。Makefile 在启动进程前把该文件加载为环境变量。
仓库根目录不提供默认 Compose 文件；Dev 和 Test 命令都必须显式选择对应配置，避免在服务器误启动开发栈。

## 常用命令

| 命令               | 行为                                                     |
| ------------------ | -------------------------------------------------------- |
| `make help`        | 显示 Makefile 中的命令。                                 |
| `make infra`       | 启动并等待本地 PostgreSQL、Redis、Loki 与 Alloy。        |
| `make infra-down`  | 停止 Compose 服务；命名 volume 保留。                    |
| `make infra-logs`  | 跟踪本地基础设施日志。                                   |
| `make dev`         | 启动 Gateway、Admin、Worker 的热加载进程。               |
| `make dev-gateway` | 只启动 Gateway 热加载进程。                              |
| `make dev-admin`   | 只启动 Admin 热加载进程。                                |
| `make dev-worker`  | 只启动 Worker 热加载进程。                               |
| `make build`       | 编译三个常驻程序到 `tmp/`。                              |
| `go test ./...`    | 运行 Go 测试。                                           |
| `sqlc generate`    | 按 `sqlc.yaml` 重新生成 `internal/platform/store/sqlc`。 |

依赖 PostgreSQL 或 Redis 的测试从 `DATABASE_URL`、`REDIS_ADDR` 读取连接信息；未提供所需变量的用例会
跳过。直连真实上游的用例还需要各自的显式开关。执行这些测试时使用隔离的测试资源，不使用本地
业务数据。

## Test 数据库快照恢复到本地 Dev

在测试站仓库中执行在线备份；该命令只读取正在运行的 Test PostgreSQL，不会启动、停止或重启服务：

```bash
./scripts/db_snapshot.sh backup --profile test
```

将生成的 `.dump` 和同名 `.dump.sha256` 一起复制到本地。停止本地 Gateway、Admin 和 Worker 后，先校验再恢复：

```bash
./scripts/db_snapshot.sh verify /path/to/unio-test-YYYYmmdd-HHMMSS.dump
./scripts/db_snapshot.sh restore /path/to/unio-test-YYYYmmdd-HHMMSS.dump --confirm-replace
```

恢复只允许写入 `unio-dev` Compose 项目，会先完整恢复到临时数据库，成功后替换本地 Dev 数据库，并清空本地
Dev Redis 当前 DB。快照包含完整 Schema 和业务数据，本地代码应与 Test Schema 兼容，并按敏感数据保存和传输。
如果本地与 Test 使用不同的凭据主密钥，恢复后的加密凭据不能直接使用，应在 Admin 中重新填写本地测试凭据。

## 数据库与 sqlc

- `migrations/` 平铺保存每张表的 `.up.sql` 和 `.down.sql`。
- 当前服务启动路径只连接数据库，不执行 migration runner；启动前由外部迁移工具准备 Schema。
- `sqlc.yaml` 从 `migrations/*.up.sql` 读取 Schema，从 `sql/queries/shared`、`gateway`、`admin`、`console` 和 `worker`
  读取查询。
- `internal/platform/store/sqlc` 是生成目录，修改 Schema 或查询后运行 `sqlc generate`。

### 迁移规范：已有大表上的新索引

发布流程是 `migrate` 容器先行、旧 Gateway 容器仍在服务。普通 `CREATE INDEX` 会持有目标表的 SHARE 锁直到
建完，期间全部写入阻塞；对 `request_records`、`request_attempts`、`routing_decision_traces`、`usage_records`、
`ledger_entries` 这类只增不减的大表，新索引一律使用 `CREATE INDEX CONCURRENTLY IF NOT EXISTS`，并遵守：

1. **一个索引一个迁移文件，文件内只有这一条语句。** golang-migrate 把整个文件作为一次 `Exec` 发送，
   多语句串会被 PostgreSQL 视为隐式事务块，`CONCURRENTLY` 会直接报错；单语句文件不在事务块内，才能并发建索引。
2. `down` 同样单语句：`DROP INDEX CONCURRENTLY IF EXISTS <name>;`。
3. `CONCURRENTLY` 中途失败会留下 `INVALID` 索引；重跑前先 `DROP INDEX` 该名称（`IF NOT EXISTS` 不会替换无效索引）。
4. 需要回填全表的迁移（如按历史聚合初始化统计表）在文件头注明预期数据量、锁面与预估时长，发布前在 Test 快照上
   实测一次。

新建表随建的索引不受此限制（表为空且尚无人写）。

## Test Docker 部署

完整步骤（架构、Cloudflare、Caddy、环境变量、前端发布与排障）见
**[deploy/TEST-DEPLOY.md](./deploy/TEST-DEPLOY.md)**。

以下为摘要。Test 部署使用 `deploy/compose.test.yml`，与 `deploy/compose.dev.yml` 的本地开发 Compose 分离。首次使用时从
`deploy/env/.env.docker.example` 和 `deploy/env/.env.test.example` 分别创建实际环境文件并替换占位密码。
实际的 `.env.dev`、`.env.docker`、`.env.test` 已由 `.gitignore` 排除；包含凭据的文件权限应设置为 `600`。

四个发布镜像独立维护 tag。构建单个服务时显式提供新 tag：

```bash
./deploy/build-image.sh admin 0.0.4
```

脚本只允许在 `develop` 或 `main` 分支和干净工作树执行，不要求 Git tag。它只构建指定服务，使用服务 tag
作为镜像 `version`，把当前完整 Git commit 和 UTC 构建时间写入镜像 Label；镜像及 Label 校验成功后，才原子
更新 `.env.docker` 中对应的 `*_IMAGE_TAG`。脚本不会切换、拉取、合并分支或重启容器。

Compose 变量按顺序加载，后面的 Test 文件可以覆盖 Docker 构建默认值：

```bash
docker compose \
  --env-file deploy/env/.env.docker \
  --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml \
  config
```

首次部署时分别构建四个镜像，再启动整套 Test 服务：

```bash
./deploy/build-image.sh gateway 0.0.1
./deploy/build-image.sh admin 0.0.1
./deploy/build-image.sh worker 0.0.1
./deploy/build-image.sh migration 0.0.1

docker compose --env-file deploy/env/.env.docker --env-file deploy/env/.env.test \
  -f deploy/compose.test.yml up -d --no-build --wait
```

`migrate` 是一次性容器；迁移成功退出后 Gateway、Admin 和 Worker 才会启动，使用 `docker compose ... logs migrate`
可以检查迁移结果。Gateway、Admin、Worker、migration 是同一个 Dockerfile 的四个独立 target，各自维护版本
和构建 provenance；每个 runtime 镜像只包含自己的二进制。同一 Git 仓库不要求四个制品同步升级，修改共享
代码时则必须按实际依赖范围构建全部受影响服务。Test 在一台服务器运行全部服务，构建制品边界与未来
Production 保持一致。Test 使用固定的 `unio-test` 项目、network 和 volume 名称与 Dev 隔离，不连接本地
开发数据。停止环境时使用 `down` 保留 Test 数据卷；仅在确认不再需要 Test 数据时才使用 `down --volumes`。

部署拓扑的硬性约束：

- **worker-server 只能单实例运行。** 有分布式锁的只有结算补偿（`FOR UPDATE SKIP LOCKED`）、权限复检（Redis claim）
  与令牌刷新（Redis 锁）；孤儿/搁浅清扫、汇率同步、模型目录同步、渠道检测、发现验证、工单维护均无锁，双实例会重复
  探测（重复消耗上游额度）与重复扫描。`VerifySingleNodeDeployment` 只拒绝 Redis Cluster，拦不住第二个 worker 进程。
- **反代必须屏蔽 `/metrics` 与 `/internal/*`**（`deploy/nginx/test.conf` 已对 API 域显式 404）。进程内另有第二道防线：
  配置 `GATEWAY_INTERNAL_TOKEN` 后，Gateway 与 Admin 的 `/metrics` 都要求 `Authorization: Bearer <token>`；
  不配置 token 时端点不鉴权，只能依赖反代。
- **Admin 位于反代之后时必须配置 `ADMIN_TRUSTED_PROXY_CIDRS`**，否则登录限速的「来源」退化为代理地址，全网共享一个
  失败桶（与 `CONSOLE_TRUSTED_PROXY_CIDRS` 同一网段）。

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
Dev 的 `unio-dev-gateway-logs` volume、Test 的 `unio-test-gateway-logs` volume 均由 Alloy 只读采集并发送到 Loki。Alloy 不采集 Gateway stdout，避免 Loki 中重复日志。
Loki 默认保留 14 天，Admin 通过 Compose 内网地址 `http://loki:3100` 查询。

## 目录

| 路径                 | 当前职责                                        |
| -------------------- | ----------------------------------------------- |
| `cmd/`               | 进程和命令行入口。                              |
| `internal/app/`      | HTTP 与 Worker 入口装配。                       |
| `internal/service/`  | 业务编排。                                      |
| `internal/core/`     | 领域能力。                                      |
| `internal/platform/` | 配置、存储、Redis、HTTP、日志与可观测基础设施。 |
| `migrations/`        | PostgreSQL Schema。                             |
| `sql/queries/`       | sqlc 查询源。                                   |
| `scripts/`           | 本地种子脚本。                                  |

## 文档交接

长期 Gateway 文档维护在
[Unio Blueprint](https://github.com/unioapi/unio-blueprint/tree/main/docs/products/gateway)。代码改造期间可在
`docs/changes/<change-id>/PLAN.md` 保存临时计划；实现和测试完成后，按实际代码行为更新 Blueprint，并删除
临时计划。
