# Unio Gateway 本地开发

## 环境

- Go 版本由 `go.mod` 固定为 `1.25.5`。
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
跳过。真实上游和故障演练测试还需要各自的显式开关。执行这些测试时使用隔离的测试资源，不使用本地
业务数据。

## 隔离故障演练

`internal/blackbox/p4fault` 只操作测试自身创建的随机 PostgreSQL/Redis 容器、数据库、namespace、volume、
Gateway 进程和 mock upstream。测试不读取开发者 `.env`，并在 cleanup 中删除这些资源。基础演练命令为：

```bash
P4_FAULT_E2E=1 env -u LOG_FORMAT go test -count=1 -v ./internal/blackbox/p4fault
```

长流程用例还要求对应的第二层开关：`P4_FULL_STATE_LOSS_E2E`、`P4_LONG_STREAM_E2E`、
`P4_PREPARE_CRASH_E2E`、`P4_AOF_RESTORE_E2E`、`P4_RDB_RESTORE_E2E`、
`P4_ACTIVE_EPOCH_ROLLBACK_E2E`、`P4_HALF_OPEN_LEASE_E2E` 或
`P4_RESET_STALE_GENERATION_E2E`。Redis Cluster 演练由 `P4_CLUSTER_E2E=1` 单独启用。

## 数据库与 sqlc

- `migrations/` 平铺保存每张表的 `.up.sql` 和 `.down.sql`。
- 当前服务启动路径只连接数据库，不执行 migration runner；启动前由外部迁移工具准备 Schema。
- `sqlc.yaml` 从 `migrations/*.up.sql` 读取 Schema，从 `sql/queries/shared`、`api`、`admin` 和 `worker`
  读取查询。
- `internal/platform/store/sqlc` 是生成目录，修改 Schema 或查询后运行 `sqlc generate`。

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
