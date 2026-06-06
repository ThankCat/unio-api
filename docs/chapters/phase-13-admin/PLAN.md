# Phase 13 Plan - 后台管理

## 目标

提供后台管理能力，让 user、project、API key、provider、channel、model、price、capability、request logs 和 billing logs 可以被安全管理。

阶段 13 不是“做几个 CRUD 页面”，而是把前面阶段的数据库业务数据真正变成可运营系统：

1. 管理员可以管理用户和项目。
2. 用户或管理员可以管理 API key。
3. 管理员可以管理 provider、channel、model、price 和模型能力声明。
4. channel credential 可以安全录入、轮换和审计。
5. request、attempt、usage、ledger 可以按权限查询。
6. 后台修改 provider/channel/model/price/capability 后能影响 routing、`/v1/models` 和 capability gating，不需要改 env 重启。

## 与阶段 12 的关系

阶段 12 已经把能力架构（模型能力声明、运行时能力闸门、models.dev 同步、cap-tags API）建好；阶段 13 把这些能力数据接入后台 CRUD，让运营人员可以可视化管理：

```text
阶段 12 交付：models / model_capabilities / channel_capability_overrides 表 + cron + 闸门
阶段 13 交付：admin CRUD 入口（增删改查、override、批量同步触发、人工 patch）
```

## 涉及文件

| 文件 | 作用 |
| --- | --- |
| [internal/core/auth](../../../internal/core/auth) | customer API key auth 已存在，后台 admin auth 需要独立设计。 |
| [internal/core/apikey](../../../internal/core/apikey) | API key 管理 service。 |
| [internal/core/credential](../../../internal/core/credential) | credential_ref 解析边界。 |
| [internal/core/routing](../../../internal/core/routing) | 后台 channel/model 变更影响 routing。 |
| [internal/core/modelcatalog](../../../internal/core/modelcatalog) | 后台模型可见性影响 `/v1/models`。 |
| [internal/core/capability](../../../internal/core/capability) | 阶段 12 引入的能力矩阵；本阶段加 admin 写入路径。 |
| [internal/service/gateway](../../../internal/service/gateway) | request log 和 billing log 查询的事实来源。 |
| [docs/production/DECISIONS.md](../../production/DECISIONS.md) | 关键商业语义决策。 |

## 任务

<a id="task-13-01-admin-auth"></a>
### TASK-13.01 Admin auth

状态：planned

目标：

```text
建立后台登录、JWT、管理员权限和审计日志边界。
```

计划实现：

1. 设计 admin user 或 admin role。
2. 密码使用 argon2id。
3. JWT 使用成熟库，固定算法白名单。
4. access token 和 refresh token 策略分开。
5. 后台敏感操作必须写 audit log。
6. admin auth 不与 customer API key auth 混用。

关键约束：

1. 管理员权限不是客户 API 调用权限。
2. customer API key 不能访问后台。
3. 后台 JWT 不能用于 `/v1/chat/completions`。

<a id="task-13-02-credential-management"></a>
### TASK-13.02 Credential 管理

状态：planned

目标：

```text
让 channel credential 能安全录入、解析、轮换和审计。
```

计划实现：

1. 设计 credential storage。
2. `channels.credential_ref` 只保存引用。
3. 明文 credential 不长期落库。
4. 使用 KMS/master key 或 secret manager。
5. resolver 根据 credential_ref 返回上游调用所需明文。
6. credential 读取、更新、轮换写审计日志。
7. adapter 只接收 `channel.Runtime.Credential`。

关联 GAP：

- [GAP-6-001](../../production/TODO_REGISTER.md#gap-6-001)


<a id="task-13-03-provider-channel-admin"></a>
### TASK-13.03 Provider/channel/model/price/capability 管理

状态：planned

目标：

```text
让 provider、channel、model、price 和模型能力声明成为后台可管理的业务数据。
```

计划实现：

1. provider CRUD。
2. channel CRUD，支持 protocol、adapter_key、enabled、priority、base_url、credential_ref、health。
3. model CRUD，支持 enabled、owned_by 与 lab/canonical_id 元数据。
4. channel_model CRUD，支持 upstream_model、enabled。
5. price CRUD，支持 pricing_unit、currency、effective_from/to。
6. price 修改时避免生效窗口重叠。
7. 能力管理：models.dev 同步结果可视化、人工 capability 覆盖、channel 级 capability override。
8. 后台变更影响 routing、`/v1/models` 和阶段 12 的 capability gating。

依赖：

1. [TASK-6.03](../phase-06-model-channel-routing/PLAN.md#task-6-03-bootstrap-wiring)
2. [TASK-6.04](../phase-06-model-channel-routing/PLAN.md#task-6-04-routing-policy)
3. [TASK-7.22](../phase-07-billing-ledger/PLAN.md#task-7-22-price-effective-window)
4. [TASK-12.01](../phase-12-capability-architecture/PLAN.md#task-12-01-capability-schema)
5. [TASK-12.02](../phase-12-capability-architecture/PLAN.md#task-12-02-capability-inference)
6. [TASK-12.04](../phase-12-capability-architecture/PLAN.md#task-12-04-models-dev-sync)

<a id="task-13-04-request-billing-dashboard"></a>
### TASK-13.04 Request 与 billing dashboard

状态：planned

目标：

```text
让管理员和后续客户侧页面能查询请求、用量、账单和异常结算。
```

计划实现：

1. request records 查询。
2. attempt records 查询。
3. usage records 查询。
4. price snapshot 查询。
5. ledger entries 查询。
6. 按 user/project/api_key/model/provider/channel/status 时间范围过滤。
7. 错误信息区分 safe_user_message 和 internal_error_detail。
8. 默认脱敏 credential、API key 和上游敏感错误。

依赖：

1. [TASK-7.18](../phase-07-billing-ledger/PLAN.md#task-7-18-request-state-machine)
2. [TASK-7.19](../phase-07-billing-ledger/PLAN.md#task-7-19-settlement-idempotency)
3. [TASK-7.21](../phase-07-billing-ledger/PLAN.md#task-7-21-error-usage-audit)
4. [TASK-8.02](../phase-08-observability-stability/PLAN.md#task-8-02-metrics)

<a id="task-13-05-customer-project-billing-controls"></a>
### TASK-13.05 客户、项目与预算控制

状态：planned

目标：

```text
为个人账户模式下的项目隔离、预算和用量归集建立后台入口。
```

计划实现：

1. user 维度余额查询。
2. project 用量统计。
3. API key 用量统计。
4. project 级模型可见性配置。
5. project 禁用或启用状态配置。
6. project 级预算或用量上限。
7. project 专属 channel 或禁用 channel 策略。
8. 后续团队/organization 模式迁移预留。

依赖：

1. [TASK-6.04](../phase-06-model-channel-routing/PLAN.md#task-6-04-routing-policy)
2. [TASK-7.17](../phase-07-billing-ledger/PLAN.md#task-7-17-preauthorization)
