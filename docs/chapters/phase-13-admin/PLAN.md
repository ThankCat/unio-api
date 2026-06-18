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

## 交付方式与模块分解

阶段 13 只做 **admin-server**（`/admin/v1/*`，平台运营闭环）；console-server、支付/充值、GAP-12-006 全部 deferred。完整模块分解（M1~M9）、RESTful 资源地图、接口约定与 GAP 收口映射见 [ADMIN_MODULES_DRAFT.md](ADMIN_MODULES_DRAFT.md)（已审查，作为北极星契约）。

交付按**垂直切片、前后端协同**推进，每片只定该 UI 需要的最小端点，后端 → 前端（`unio-admin` 独立仓库）→ 联调 → 下一片，不一次性铺满 API。

- **Slice 1（已交付，2026-06-09）**：M1 静态 token 认证 + M3 provider/channel CRUD + M2 channel 凭据只写轮换。对应 TASK-13.01（done）与 TASK-13.03 的 provider/channel 子集（in_progress）；关闭 GAP-6-003。已落地契约见 [CONTRACT.md](CONTRACT.md)。
- 后续切片：channel↔model 绑定、model provisioning、定价、能力管理、只读查询台、工作台看板，按 [ADMIN_MODULES_DRAFT.md](ADMIN_MODULES_DRAFT.md) 推进顺序展开。

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
### TASK-13.01 Admin auth（单管理员极简版）

状态：done（Slice 1，2026-06-09）

目标：

```text
为全部 /admin/v1/* 建立认证入口门；单运营者阶段砍到最小，不做登录/会话/权限。
```

已实现：

1. 静态 env `ADMIN_API_TOKEN`（强随机），启动期校验非空（缺失即 fail-fast）。
2. `core/adminauth.StaticTokenAuthenticator` 用常量时间比对（`crypto/subtle`）校验 `Authorization: Bearer <token>`。
3. `app/adminapi/middleware.AdminAuth` 守护 `/admin/v1/*`：缺 token → `adminauth_missing_token`(401)，不匹配 → `adminauth_invalid_token`(401)。
4. 身份缝：认证后往 context 塞恒定 `AdminPrincipal`，handler 一律从 context 取身份；日后换登录/多管理员/RBAC 只改中间件，不动 handler。
5. 端点：`GET /healthz`（免鉴权）、`GET /admin/v1/ping`（鉴权探针）。
6. 单测覆盖 authenticator（空/错/对 token + 启动校验）与 router（ping 401/200、healthz 公开）。

本阶段 deferred（升级触发：接 web 登录页 / 第二个管理员 / 对外开放）：

1. JWT、access/refresh token、登录/刷新/登出流程。
2. RBAC（role）与 admin 操作审计日志（含 before/after diff）。
3. admin_users / admin_sessions / admin_audit_logs 表、密码 + argon2id、create-admin CLI。
4. 登录暴力破解节流 / 锁定。

关键约束（已满足）：

1. 管理员权限不是客户 API 调用权限。
2. customer API key 不能访问后台（独立进程 + 独立 token）。
3. 后台 token 不能用于 `/v1/chat/completions`。

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

状态：in_progress（Slice 1 已交付 provider/channel CRUD + 凭据轮换，2026-06-09）

目标：

```text
让 provider、channel、model、price 和模型能力声明成为后台可管理的业务数据。
```

已实现（Slice 1）：

1. **provider CRUD**：`List/Get/Create/Update`，校验 slug 格式、name、status 枚举；唯一冲突 → `admin_conflict`(409)。
2. **channel CRUD**：`List`（可按 `provider_id` 过滤）`/Get/Create/Update`，支持 protocol、adapter_key、base_url、status、priority、timeout_ms。
   - 写入校验：(protocol, adapter_key) 复合键必须存在于 adapter registry（`registry.HasAny`），未注册 → `admin_adapter_binding_unsupported`(422)，**关闭 GAP-6-003**。
   - provider 外键不存在 → `admin_not_found`(404)；同 provider 内 channel 名重复 → `admin_conflict`(409)。
3. **channel 凭据只写轮换**（M2 部分）：`PUT /channels/{id}/credential`，明文经 `core/credential` AES-GCM 加密入库；明文/密文绝不回读、不进日志、不出 DTO；成功 204。

待后续切片：

4. channel↔model 绑定（`channel_models`，upstream_model/enabled）。
5. model CRUD / provisioning（enabled、owned_by、lab/canonical_id、max_output_tokens），关 GAP-12-007/GAP-11-006 的前提。
6. price CRUD（pricing_unit、currency、effective_from/to）+ 生效窗口不重叠校验（依赖 TASK-7.22）。
7. 能力管理：models.dev 同步可视化、人工 capability 覆盖、channel 级 override（见 TASK-13.03C / M5）。
8. provider/channel DELETE 与 channel `/health` 子资源（Slice 1 只做 GET/POST/PATCH + 凭据 PUT）。

约束：后台变更写库即对下一请求生效（routing 实时读库，无需重启）；后台只能从已注册 adapter 中选，不热加载 adapter 代码。

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

<a id="task-13-09-dashboard-cockpit"></a>
### TASK-13.09 工作台看板升级为经营驾驶舱（三层信息架构）

状态：in_progress（D1 决策层重构已交付，Slice 8，2026-06-18；D2–D5 见 GAP）

目标：

```text
把 M9 工作台看板（TASK-13.07）从「数据展示页」升级为「经营驾驶舱」三层信息架构：
首屏=决策层（8 KPI + 环比 + 状态 Banner），二级=分析中心（DB+rollup），三级=实时监控（Prometheus）。
设计与口径以 DESIGN-dashboard-business-overview.md 为准；§9.1 口径已定稿并锚定 DEC-021。
```

切片（设计 §10）：

| 切片 | 范围 | 状态 |
| --- | --- | --- |
| D1 决策层重构 | 首页改 8 KPI（收入/毛利/利润率/缓存贡献/请求数/成功率/异常请求/客户余额）+ 本期/上期环比 + 状态 Banner，砍掉首页明细/排行 | **done（Slice 8，2026-06-18）** |
| D2 rollup 基础设施 | `dashboard_rollups` 表 + 回填 worker + 环比/趋势改读 rollup（上量前必须） | planned（[GAP-13-001](../../production/TODO_REGISTER.md#gap-13-001)） |
| D3 二级中心（批一） | 利润中心 + 渠道中心 + 异常中心 | planned（[GAP-13-002](../../production/TODO_REGISTER.md#gap-13-002)） |
| D4 二级中心（批二） | 模型中心 + 缓存中心 + 用户中心（P95/P99 走 rollup） | planned（[GAP-13-002](../../production/TODO_REGISTER.md#gap-13-002)） |
| D5 实时监控页 | QPS/TPS/P99/错误率接 Prometheus | planned（[GAP-13-003](../../production/TODO_REGISTER.md#gap-13-003)） |

D1 已实现：

1. **后端 service**（`internal/service/admin/dashboard/dashboard.go` + `helpers.go`）：`Overview` 重构为决策层——`Current`/`Previous` 两段同口径环比（上期=同长度紧邻窗口）、`Health` 整体健康状态 Banner（阈值 0.95/0.80）、`MarginRate` 按币种派生利润率（`big.Rat` 精确）、缓存贡献反事实估算（`DashboardCacheSavingsByCurrency`，标「估算」）。
2. **adminapi DTO**（`internal/app/adminapi/dashboard.go`）：响应体重构为 `current`/`previous`/`margin_rate`/`health` + 时点画像（balance/billing_exceptions/channels）；↑↓% 由前端用 current/previous 自算。
3. **前端**（`unio-admin`）：`dashboard.ts` 类型对齐；`DashboardPage.tsx` 重排为状态 Banner + 8-KPI 决策层（金额类带环比 `DeltaBadge`，异常类 invert），保留趋势图与渠道健康；删除旧 `OverviewCards`/`MoneyList`。

口径（定稿，DEC-021）：金额按币种拆卡不引汇率；付费率=区间内有 `debit` 的去重用户；rollup hour 桶保留 90 天、day 桶永久；AI 摘要顺延。

关联：

- 设计：[DESIGN-dashboard-business-overview.md](DESIGN-dashboard-business-overview.md)
- 决策：[DEC-021](../../production/DECISIONS.md#dec-021-看板经营驾驶舱三层架构按币种拆卡不引汇率)
- 升级自：TASK-13.07（M9 工作台看板，Slice 6）
- GAP：[GAP-13-001](../../production/TODO_REGISTER.md#gap-13-001) ~ [GAP-13-005](../../production/TODO_REGISTER.md#gap-13-005)
