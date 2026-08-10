# Unio Gateway

Unio Gateway 是 UnioAPI 的 Go 实现仓库，包含公开 Gateway API、Admin API、后台 Worker 和运行态维护工具。

## 权威文档

Gateway 的产品边界、公开契约、协议兼容、Provider 适配、请求与账务生命周期、路由、准入、熔断、
运行控制、数据生命周期和领域决策统一维护在
[Unio Blueprint Gateway 文档](https://github.com/unioapi/unio-blueprint/tree/main/docs/products/gateway)。

当前实现行为以本仓库代码、数据库 Schema 和测试为准。本仓库只保留仓库入口、本地开发说明、
第三方许可声明和正在实施的临时改造计划，不维护长期产品或架构文档。

## 程序入口

| 入口 | 当前用途 |
| --- | --- |
| `cmd/gateway-server` | 提供 `/v1/*` 公开 Gateway API 和已配置的内部只读端点（默认 `:8520`）。 |
| `cmd/admin-server` | 提供 `/v1/*` Admin API（默认 `:8521`；与 Gateway 分端口/分域名，路径同为 `/v1`）。 |
| `cmd/worker-server` | 运行后台任务；`sync-models` 子命令执行一次 models.dev 目录同步。 |

## 本地启动

```bash
cp .env.example .env
make infra
make dev
```

`make dev` 启动 Gateway、Admin 和 Worker 的热加载进程。其他命令与数据库、测试、sqlc 说明见
[DEVELOPMENT.md](DEVELOPMENT.md)。

models.dev 数据源的许可和 attribution 见 [THIRD_PARTY_NOTICES.md](THIRD_PARTY_NOTICES.md)。
