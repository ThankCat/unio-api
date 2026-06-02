# Project Structure - Unio API 全服务目录方案

更新时间：2026-06-01

本文档记录 Unio API 后端仓库的全服务目标目录结构、分层职责和依赖方向。

该结构用于后续一次性全服务目录改造。改造完成后应长期稳定；如果未来业务阶段、部署形态或团队协作方式发生重大变化，需要通过架构决策更新本文档。

## 设计目标

Unio API 是模块化单体，不是微服务拆分。

目标形态：

```text
一个 Go module
一个后端仓库
四个长期运行进程入口
共享同一套核心业务模块
共享同一个 PostgreSQL 事务边界
可按部署需要构建一个通用镜像或四个进程镜像
```

四个进程入口：

```text
gateway-server  = Gateway API，服务 /v1/*
admin-server    = Admin API，服务 /admin/v1/*
console-server  = Console API，服务 /console/v1/*
worker-server   = 后台任务进程，不直接服务公网 HTTP API
```

API 前缀固定为：

```text
/v1/*
/admin/v1/*
/console/v1/*
```

命名固定为：

```text
gateway-server
admin-server
console-server
worker-server

gatewayapi
adminapi
consoleapi
workers
```

业务编排层统一使用 `service`，不使用 `usecase`。

## 完整目录分布图

```text
/Users/chenhao/Project/unio-api
├── cmd
│   ├── gateway-server
│   │   └── main.go
│   │       # 启动 Gateway API 进程，服务 /v1/*。
│   │
│   ├── admin-server
│   │   └── main.go
│   │       # 启动 Admin API 进程，服务 /admin/v1/*。
│   │
│   ├── console-server
│   │   └── main.go
│   │       # 启动 Console API 进程，服务 /console/v1/*。
│   │
│   └── worker-server
│       └── main.go
│           # 启动后台 worker 进程。
│
├── internal
│   ├── bootstrap
│   │   ├── gateway_server.go
│   │   │   # 装配 gateway-server 依赖：config、DB、Redis、router、adapter、billing、ledger。
│   │   ├── admin_server.go
│   │   │   # 装配 admin-server 依赖。
│   │   ├── console_server.go
│   │   │   # 装配 console-server 依赖。
│   │   └── worker_server.go
│   │       # 装配 worker-server 依赖。
│   │
│   ├── app
│   │   ├── gatewayapi
│   │   │   ├── router.go
│   │   │   ├── middleware
│   │   │   │   ├── api_key_auth.go
│   │   │   │   └── rate_limit.go
│   │   │   ├── openai
│   │   │   │   ├── response.go
│   │   │   │   ├── chatcompletions
│   │   │   │   │   ├── handler.go
│   │   │   │   │   ├── dto.go
│   │   │   │   │   ├── decode.go
│   │   │   │   │   ├── validation.go
│   │   │   │   │   └── stream.go
│   │   │   │   └── models
│   │   │   │       ├── handler.go
│   │   │   │       └── dto.go
│   │   │   └── anthropic
│   │   │       ├── response.go
│   │   │       └── messages
│   │   │           ├── handler.go
│   │   │           ├── dto.go
│   │   │           ├── decode.go
│   │   │           ├── validation.go
│   │   │           └── stream.go
│   │   │
│   │   ├── adminapi
│   │   │   ├── router.go
│   │   │   ├── response.go
│   │   │   ├── middleware
│   │   │   │   └── admin_auth.go
│   │   │   ├── providers
│   │   │   │   ├── routes.go
│   │   │   │   ├── handler.go
│   │   │   │   └── dto.go
│   │   │   ├── channels
│   │   │   ├── models
│   │   │   ├── prices
│   │   │   ├── costprices
│   │   │   ├── users
│   │   │   ├── projects
│   │   │   ├── billing
│   │   │   ├── ledger
│   │   │   ├── audit
│   │   │   ├── jobs
│   │   │   └── system
│   │   │
│   │   ├── consoleapi
│   │   │   ├── router.go
│   │   │   ├── response.go
│   │   │   ├── middleware
│   │   │   │   └── user_auth.go
│   │   │   ├── projects
│   │   │   ├── apikeys
│   │   │   ├── balances
│   │   │   ├── usage
│   │   │   ├── requestlogs
│   │   │   ├── invoices
│   │   │   ├── members
│   │   │   ├── budgets
│   │   │   ├── alerts
│   │   │   └── webhooks
│   │   │
│   │   └── workers
│   │       ├── app.go
│   │       ├── runner.go
│   │       ├── job.go
│   │       ├── lock.go
│   │       ├── settlement_recovery_worker.go
│   │       ├── channel_health_worker.go
│   │       └── usage_rollup_worker.go
│   │
│   ├── service
│   │   ├── gateway
│   │   │   ├── lifecycle
│   │   │   │   ├── executor.go
│   │   │   │   ├── authorization.go
│   │   │   │   ├── attempt.go
│   │   │   │   ├── fallback.go
│   │   │   │   ├── settlement.go
│   │   │   │   ├── recovery.go
│   │   │   │   ├── delivery.go
│   │   │   │   ├── metrics.go
│   │   │   │   └── tracing.go
│   │   │   ├── openai
│   │   │   │   └── chatcompletions
│   │   │   └── anthropic
│   │   │       └── messages
│   │   │
│   │   ├── admin
│   │   │   ├── providers
│   │   │   │   └── service.go
│   │   │   ├── channels
│   │   │   ├── models
│   │   │   ├── prices
│   │   │   ├── costprices
│   │   │   ├── billing
│   │   │   ├── ledger
│   │   │   ├── audit
│   │   │   └── jobs
│   │   │
│   │   └── console
│   │       ├── projects
│   │       ├── apikeys
│   │       ├── balances
│   │       ├── usage
│   │       ├── requestlogs
│   │       ├── invoices
│   │       └── members
│   │
│   ├── core
│   │   ├── billing
│   │   ├── ledger
│   │   ├── requestlog
│   │   ├── usage
│   │   ├── apikey
│   │   ├── auth
│   │   ├── provider
│   │   ├── channel
│   │   ├── modelcatalog
│   │   ├── routing
│   │   ├── adapter
│   │   │   ├── facts.go
│   │   │   ├── registry.go
│   │   │   ├── upstream_error.go
│   │   │   ├── upstreamhttp
│   │   │   ├── sse
│   │   │   ├── openai
│   │   │   │   ├── contract.go
│   │   │   │   ├── dto.go
│   │   │   │   ├── request.go
│   │   │   │   ├── response.go
│   │   │   │   ├── stream.go
│   │   │   │   └── deepseek
│   │   │   └── anthropic
│   │   │       ├── contract.go
│   │   │       ├── dto.go
│   │   │       ├── request.go
│   │   │       ├── response.go
│   │   │       ├── stream.go
│   │   │       └── deepseek
│   │   └── credential
│   │
│   └── platform
│       ├── config
│       ├── store
│       │   └── sqlc
│       ├── redis
│       ├── httpx
│       ├── httpmw
│       ├── ratelimit
│       ├── failure
│       └── observability
│
├── migrations
│   # 保留当前真实 migration 文件。
│   # 继续遵循一张表一组 up/down migration。
│
├── sql
│   └── queries
│       # 保留当前真实 sqlc query 文件。
│       # 继续遵循一张表一个 query 文件。
│
├── docs
│   ├── architecture
│   │   └── PROJECT_STRUCTURE.md
│   ├── chapters
│   └── production
│
├── go.mod
├── go.sum
├── sqlc.yaml
└── AGENTS.md
```

上面的目录树只列目标目录和确定会创建或迁移的文件；migration 和 query 文件以仓库真实文件为准，不在结构文档中虚构示例文件。

## 分层职责

### `cmd`

`cmd` 只放进程入口。

每个 `main.go` 只负责：

1. 读取配置。
2. 初始化日志和基础资源。
3. 调用 `internal/bootstrap` 装配进程。
4. 启动对应 server 或 worker runner。
5. 处理 graceful shutdown。

`cmd` 不写业务规则，不直接拼 SQL，不直接实现 HTTP handler。

### `internal/bootstrap`

`bootstrap` 只做依赖装配。

它负责把这些组件组装起来：

```text
config
postgres / sqlc
redis
core services
application services
HTTP router
worker runner
```

`bootstrap` 可以 import `app`、`service`、`core`、`platform`，但这些包不应反向 import `bootstrap`。

### `internal/app`

`app` 是入口适配层。

```text
app/gatewayapi = /v1/* 双协议 HTTP handler、DTO、router、middleware
app/adminapi   = /admin/v1/* HTTP handler、DTO、router、middleware
app/consoleapi = /console/v1/* HTTP handler、DTO、router、middleware
app/workers    = worker job loop、claim、runner、调度入口
```

`app` 可以做：

1. HTTP JSON decode / encode。
2. request DTO 校验。
3. 认证 middleware。
4. 路由挂载。
5. 调用 `service`。
6. 把内部错误映射为用户可见响应。

`app` 不做：

1. 账务金额计算。
2. ledger capture/release 规则。
3. routing 策略。
4. settlement 幂等规则。
5. provider adapter 协议转换。

### `internal/service`

`service` 是应用业务编排层。

它负责把多个 `core` 能力编排成一个完整业务动作。

例如 `service/gateway`：

```text
创建 request record
选择 channel
创建 authorization
调用 adapter
写 usage
写 price snapshot
写 cost snapshot
capture / release reservation
标记 request succeeded / failed
```

例如 `service/admin`：

```text
创建 provider
配置 channel
修改价格生效窗口
查询内部账务事实
写 admin audit
```

例如 `service/console`：

```text
创建 project
创建 API key
查询余额
查询自己的 request logs
查询自己的 usage
```

`service` 不是通用工具箱。禁止在 `service` 下创建 `common`、`util`、`helper`。

### `internal/core`

`core` 是稳定核心业务规则层。

允许的核心领域：

```text
billing        售价、成本价、金额计算
ledger         余额、流水、冻结、capture、release
requestlog     request_records / request_attempts 状态机
usage          usage 查询、聚合、报表基础能力
apikey         API key 创建、吊销、权限
auth           API key / admin / console auth 领域能力
provider       provider 业务规则
channel        channel 业务规则和 runtime DTO
modelcatalog   模型目录和模型可见性
routing        provider/channel/model 路由选择
adapter        上游协议适配、stream parser、tokenizer
credential     credential_ref 解析，未来 KMS/密文
```

`core` 规则：

1. 禁止 `core/common`。
2. 禁止 `core/util`。
3. 禁止 `core/helper`。
4. `core` 不 import `app`。
5. `core` 不 import `bootstrap`。
6. `core` 不关心 HTTP response 格式。

### `internal/platform`

`platform` 是基础设施和技术支撑层。

```text
config         配置加载和 env 解析
store          PostgreSQL、pgx、sqlc
redis          Redis client
httpx          HTTP JSON/response/request helper
httpmw         通用 HTTP middleware：logger、recoverer、request id
ratelimit      Redis 限流 primitive
failure        稳定错误码和 failure.Wrap/New
observability  metrics、tracing、structured logs
```

`platform` 不表达产品业务语义。它可以被 `app`、`service`、`core` 使用，但不应反向依赖业务层。

## 依赖方向

固定依赖方向：

```text
cmd
  -> bootstrap
      -> app
          -> service
              -> core
                  -> platform/store/sqlc
          -> platform/httpx/httpmw/failure
```

禁止反向依赖：

```text
core      -> app
core      -> bootstrap
platform  -> service
platform  -> app
service   -> bootstrap
```

允许 `service` 同时调用多个 `core` 包，因为它的职责就是业务编排。

允许 `app` 调用 `platform/httpx`、`platform/httpmw`、`platform/failure`，因为这些是入口层必须使用的技术能力。

## HTTP 边界

Gateway API：

```text
/v1/*
```

服务客户程序调用，使用 opaque API key 认证。

当前公开协议族：

```text
OpenAI:
  POST /v1/chat/completions

Anthropic:
  POST /v1/messages
```

目录边界：

```text
app/gatewayapi/openai
= OpenAI 协议族公开 HTTP 包根目录；本身只放协议族级共享类型，
  每个 operation 必须落到独立子包。

app/gatewayapi/openai/chatcompletions
= POST /v1/chat/completions 专属 handler / DTO / decode / validation / SSE / 错误响应。

app/gatewayapi/openai/models
= GET /v1/models 专属 handler。

app/gatewayapi/anthropic
= Anthropic 协议族公开 HTTP 包根目录；本身只放协议族级共享类型
  （当前是公开 error shape），每个 operation 必须落到独立子包。

app/gatewayapi/anthropic/messages
= POST /v1/messages 专属 handler / DTO / decode / validation / SSE / 错误响应。

service/gateway/openai/chatcompletions
= OpenAI Chat Completions operation 的编排骨架。

service/gateway/anthropic/messages
= Anthropic Messages operation 的编排骨架。

service/gateway/lifecycle
= API key 身份之后的 request、routing、authorization、attempt、fallback、
   settlement、recovery、metrics、tracing、request log 文案与 delivery audit
   等所有协议无关共享能力；双协议骨架必须复用，不得在两侧重复实现。

core/adapter/openai/deepseek
= DeepSeek OpenAI endpoint 的请求与响应转换。

core/adapter/anthropic/deepseek
= DeepSeek Anthropic endpoint 的请求与响应转换。
```

硬规则（Phase 10.15 复核后写入）：

```text
1. gatewayapi 协议族根包（openai/、anthropic/）不允许平铺单个 operation 的
   handler/dto/validation；新 operation 必须新建子包（例如未来的
   openai/responses、openai/embeddings、anthropic/messages_count_tokens）。

2. service/gateway 协议族 + operation 子包结构（openai/chatcompletions、
   anthropic/messages、未来的 openai/responses 等）必须与 gatewayapi 子包名一一对应。

3. 协议无关共享能力（结算、recovery、metrics、tracing、request log 等）必须落到
   service/gateway/lifecycle；两个 operation 骨架不允许重复实现同一份协议无关纯
   函数或 thin wrapper。
```

请求协议和响应协议保持分离；账务、审计和商业生命周期按统一事实复用。详细方案见
[Phase 10 Architecture](../chapters/phase-10-dual-protocol-gateway/ARCHITECTURE.md)。

Admin API：

```text
/admin/v1/*
```

服务平台管理员后台，管理 provider、channel、model、price、cost、user、billing、audit、jobs 等。

Console API：

```text
/console/v1/*
```

服务普通用户后台，管理 project、API key、balance、usage、request logs、invoice 等。

Admin API 和 Console API 不能绕过 `core/billing`、`core/ledger`、`core/routing` 等核心规则直接修改账务事实。

## 当前结构状态

当前代码已经按本文档的 `cmd`、`app`、`service`、`core`、`platform` 主分层完成迁移。

Phase 10 将在主分层不变的前提下，把 `gatewayapi`、`service/gateway` 与 `core/adapter`
继续按 OpenAI / Anthropic 协议族整理。Phase 10.15 复核已经把 `gatewayapi/openai`
重组成 `chatcompletions/` 与 `models/` operation 子包，并与 `anthropic/messages/`
保持对称；`service/gateway/openai/chatcompletions` 与 `service/gateway/anthropic/messages`
也按同名子包到位。`core/adapter/openai`、`core/adapter/anthropic` 当前每个协议族
只有一个 operation，文件平铺；如未来加入第二个 operation，必须按 protocol/operation
子包重组并保持与 gatewayapi、service/gateway 对称。

后续新增代码必须直接进入目标子包，不允许在协议族根包平铺。

当前已预留以下进程入口目录：

```text
cmd/admin-server/.gitkeep
cmd/console-server/.gitkeep
cmd/worker-server/.gitkeep
```

这些 `.gitkeep` 只用于让 Git 跟踪目标入口目录，不代表对应进程已经实现。进入 Admin API、Console API 或 Worker 阶段时，再用真实 `main.go` 替换占位文件。

## Worker 结构落点

Settlement recovery worker 后续按以下位置落地：

```text
cmd/worker-server/main.go
internal/bootstrap/worker_server.go
internal/app/workers/runner.go
internal/app/workers/settlement_recovery_worker.go
internal/service/gateway/chat_settlement_recovery.go
migrations/000021_create_settlement_recovery_jobs.up.sql
sql/queries/settlement_recovery_jobs.sql
```

职责分布：

```text
app/workers
= 什么时候跑，如何 claim job，如何 retry，如何更新 job 状态。

service/gateway
= 如何根据 request/usage/reservation 等事实把 settlement 正确补回来。

core/billing / core/ledger / core/requestlog
= 保持各自核心账务规则。
```

## 命名规则

1. API 入口包使用 `xxxapi`：`gatewayapi`、`adminapi`、`consoleapi`。
2. 进程入口使用 `xxx-server`：`gateway-server`、`admin-server`、`console-server`、`worker-server`。
3. worker 入口包使用复数 `workers`，避免和进程名混淆。
4. 业务编排层使用 `service`，不使用 `usecase`。
5. 核心领域直接用业务名：`billing`、`ledger`、`routing`、`adapter`。
6. 禁止新建无业务语义的 `common`、`util`、`helper` 目录。

## 与部署形态的关系

推荐部署形态：

```text
一个 unio-api 通用应用镜像，四个应用容器使用不同 command 启动
或四个进程镜像：gateway-server、admin-server、console-server、worker-server
再加 PostgreSQL 和 Redis 容器
```

容器示例：

```text
gateway-server
admin-server
console-server
worker-server
postgres
redis
```

这仍然是模块化单体，不是微服务。判断标准：

```text
同一个 repo
同一个 go.mod
可以是同一个应用镜像，也可以是同一代码构建出的四个进程镜像
本地函数调用共享核心业务模块
共享 PostgreSQL 事务边界
不通过 RPC 调 billing/ledger/routing
```

## 结构改造规则

后续执行目录迁移时必须遵守：

1. 先迁移包路径和 import，不改变业务行为。
2. 每批迁移后运行 `go test ./...`。
3. 每批迁移后运行 `sqlc generate`，确认生成物无意外变化。
4. 不把结构迁移和业务逻辑改造混在同一个小步骤里。
5. 不在迁移中删除 production TODO。
6. 后续新增代码必须遵守本文档的目标结构和依赖规则。
