# Phase 6 Plan - 模型与渠道

## 目标

把 provider、channel、model、channel_model 和基础 routing 从代码常量推进到数据库业务数据。

本阶段要明确 Unio API 的核心产品边界：

1. Provider 是业务服务商，例如 OpenAI、Anthropic、Gemini。
2. Channel 是某个 provider 下的具体上游渠道，包含 base URL、credential_ref、优先级、健康状态和模型映射。
3. Model 是对外暴露的模型 ID，是 opaque string。
4. Adapter 是代码能力，不等于 provider，也不等于 channel。
5. Routing 负责为某个 project/model 选择可用 channel。

## 涉及文件

| 文件 | 作用 |
| --- | --- |
| [migrations/000005_create_providers.up.sql](../../../migrations/000005_create_providers.up.sql) | provider schema。 |
| [migrations/000006_create_channels.up.sql](../../../migrations/000006_create_channels.up.sql) | channel schema。 |
| [migrations/000007_create_models.up.sql](../../../migrations/000007_create_models.up.sql) | model schema。 |
| [migrations/000008_create_channel_models.up.sql](../../../migrations/000008_create_channel_models.up.sql) | channel-model mapping schema。 |
| [sql/queries/models.sql](../../../sql/queries/models.sql) | `/v1/models` 和模型存在性查询。 |
| [sql/queries/channel_models.sql](../../../sql/queries/channel_models.sql) | routing 候选查询。 |
| [sql/queries/project_model_policies.sql](../../../sql/queries/project_model_policies.sql) | project 模型可见性策略查询。 |
| [internal/core/modelcatalog/catalog.go](../../../internal/core/modelcatalog/catalog.go) | model catalog service。 |
| [internal/core/routing/router.go](../../../internal/core/routing/router.go) | routing service。 |
| [internal/core/credential](../../../internal/core/credential) | credential_ref 解析边界。 |
| [internal/core/channel/runtime.go](../../../internal/core/channel/runtime.go) | 传给 adapter 的运行时 channel 参数。 |

## 任务

<a id="task-6-01-model-channel-schema"></a>
### TASK-6.01 Provider/channel/model schema

状态：done

目标：

```text
让模型、渠道和 provider 成为数据库业务数据，而不是写死在代码或 env。
```

已完成：

1. providers。
2. channels。
3. models。
4. channel_models。
5. enabled、priority、base_url、credential_ref、upstream_model。
6. model catalog 基础查询。
7. routing 基础查询。

关键约束：

1. `models.model_id` 是 opaque string，不按 `/` 等格式解析。
2. `channel_models.upstream_model` 表达真实上游模型名。
3. `channels.credential_ref` 只保存凭据引用，不保存长期明文 key。

<a id="task-6-02-credential-resolver"></a>
### TASK-6.02 Credential resolver 生产化

状态：deferred

目标：

```text
让 routing 能根据 credential_ref 安全解析出上游调用需要的明文 credential。
```

当前状态：

```text
第 6 阶段允许开发期 StaticResolver 占位。
```

生产形态：

1. credential_ref 指向密文记录、secret manager path 或 KMS 保护的数据。
2. resolver 根据 credential_ref 解析出明文 credential。
3. 明文 credential 只进入 `channel.Runtime`。
4. adapter 不知道 credential 存储、解密和轮换方式。
5. credential 轮换和读取必须有审计。

计划处理时机：

```text
阶段 11 前置小节，进入后台 channel 管理和凭据管理前。
```

关联 GAP：

- [GAP-6-001](../../production/TODO_REGISTER.md#gap-6-001)


<a id="task-6-03-bootstrap-wiring"></a>
### TASK-6.03 启动装配治理

状态：done

目标：

```text
避免 main 函数膨胀成所有依赖装配和业务规则的堆叠点。
```

当前欠账：

1. 无当前阶段内必须关闭的欠账。

已完成：

1. 新增 `internal/bootstrap` provider adapter preflight。
2. adapter registry 构建已从 `cmd/gateway-server/main.go` 迁入 `internal/bootstrap`。
3. credential resolver 和 chat routing 构建已从 `cmd/gateway-server/main.go` 迁入 `internal/bootstrap`。
4. chat gateway、request log、settlement、billing calculator 和 ledger service 构建已从 `cmd/gateway-server/main.go` 迁入 `internal/bootstrap`。
5. API key auth、model catalog、rate limit 和 HTTP router 构建已从 `cmd/gateway-server/main.go` 迁入 `internal/bootstrap`。
6. 启动时读取数据库中 enabled channel 的 `protocol + adapter_key` 复合绑定。
7. 启动时校验复合绑定存在于双协议 adapter registry；具体 operation capability 由阶段 10 lifecycle 过滤。
8. preflight 失败时返回 `bootstrap_*` failure code，并携带 channel_id、provider_slug、protocol、adapter_key 和 capability 安全字段。
9. server app wiring 已收敛到 `internal/bootstrap.NewGatewayServerApp`，`cmd/gateway-server/main.go` 只保留 config、资源启动、HTTP server lifecycle 和退出信号处理。
10. 阶段 10 已把 runtime 绑定升级为 channel.protocol + channel.adapter_key；后台写入 registry 校验推迟到阶段 11 provider/channel CRUD。

计划实现：

1. 阶段 11 后台 provider/channel CRUD 写入 channel.protocol + channel.adapter_key 时校验 adapter 必须存在于对应协议 registry。

关联 GAP：

- [GAP-6-002](../../production/TODO_REGISTER.md#gap-6-002)
- [GAP-6-003](../../production/TODO_REGISTER.md#gap-6-003)


<a id="task-6-04-routing-policy"></a>
### TASK-6.04 Routing project policy

状态：done

目标：

```text
让不同 project 能拥有不同模型可见性；预算、禁用和专属 channel 策略进入后续阶段。
```

当前欠账：

1. 无当前阶段内必须关闭的欠账。

已完成：

1. `project_model_policies` 已支持模型 allow-list/deny-list。
2. routing 和 `/v1/models` 已共用 project 模型可见性语义。
3. 用户看得到的模型一定可路由。
4. 用户不可用的模型不会出现在 `/v1/models`。
5. 同一模型可以因 project 不同而可见性不同。

计划实现：

1. 阶段 7 reservation/余额冻结 统一处理预算约束。
2. 阶段 11 项目策略管理处理 project 禁用、专属 channel 和后台配置入口。

关联 GAP：

- [GAP-6-005](../../production/TODO_REGISTER.md#gap-6-005)
- [GAP-6-006](../../production/TODO_REGISTER.md#gap-6-006)


<a id="task-6-05-routing-error-semantics"></a>
### TASK-6.05 Routing 错误语义

状态：done

目标：

```text
让 gateway 能区分模型不存在、模型不可见、模型存在但无可用 channel、channel 配置错误。
```

计划实现：

1. 增加 model exists 查询。已完成。
2. 增加 project-visible model 查询。已完成。
3. routing candidate 查询为空时区分原因。已完成。
4. 定义 `ErrModelNotFound`、`ErrModelNotAvailable`、`ErrNoAvailableChannel`。已完成。
5. HTTP 层映射成安全 OpenAI-compatible error。已完成。

关联 GAP：

- [GAP-6-007](../../production/TODO_REGISTER.md#gap-6-007)
