# Phase 13 Status

状态：in_progress（admin-server，垂直切片推进；Slice 6 已交付）

阶段 13 只做 admin-server（`/admin/v1/*`）。模块分解、资源地图与推进顺序见 [ADMIN_MODULES_DRAFT.md](ADMIN_MODULES_DRAFT.md)；已交付契约见 [CONTRACT.md](CONTRACT.md)。

> 产品定位与定价 / 路由档界已由 [DEC-017](../../production/DECISIONS.md) 锚定：分档网关（卖档 Path B），渠道 / 供应商对用户隐藏，售价覆盖档内最贵渠道，路由 / 重试锁档内绝不降级；全透明聚合市场顺延为「有量之后的第二产品线」。后台 = 运营内部工具。

## 已交付（Slice 1，2026-06-09）

M1 静态 token 认证 + M3 provider/channel CRUD + M2 channel 凭据只写轮换，端到端打通并验证。

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-13.01 | done | 静态 `ADMIN_API_TOKEN` 认证（常量时间比对）+ `AdminPrincipal` context 缝 + `/healthz`、`/admin/v1/ping`；JWT/RBAC/审计 deferred。 |
| TASK-13.03 | in_progress | provider CRUD + channel CRUD（List/Get/Create/Update）已交付；channel↔model 绑定、model provisioning、定价、能力管理待后续切片。 |
| TASK-13.02 | in_progress | channel 凭据只写轮换（`PUT /channels/{id}/credential`，AES-GCM 加密入库、不回读）已交付；凭据存储方案（master key vs KMS，GAP-6-001）待 13.02 规划定稿。 |

新增内部包：`internal/core/adminauth`、`internal/app/adminapi`(+`/middleware`)、`internal/service/admin/provider`、`internal/service/admin/channel`、`cmd/admin-server`、`internal/bootstrap/admin_server.go`+`admin_http.go`；新增 `sql/queries/providers.sql` 与扩展 `channels.sql`（sqlc 已生成）。

GAP 收口：**GAP-6-003 已关闭**——channel 写入路径用 adapter registry 校验 (protocol, adapter_key) 复合键，未注册返回 `admin_adapter_binding_unsupported`(422)。

## 已交付（Slice 2，2026-06-11）

M3A channel↔model 绑定 + Model CRUD/provisioning + M4 定价（售价 + 成本价），端到端打通并验证。

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-13.03 | done | provider CRUD + channel CRUD + **channel↔model 绑定**（`channel_models`：upstream_model/status）+ **Model CRUD/provisioning**（model_id 不可变、display_name、owned_by、lab、status、max_output_tokens、source）全部交付。这是关 GAP-12-007（adapter 画像物化）与 GAP-11-006（Codex 默认模型名映射）的前提，已就绪。 |
| TASK-13.03B（M4 定价） | done | **客户售价** `prices` CRUD（pricing_unit/currency/input/output/cached/reasoning 明确金额、effective_from/to；金额走 `pgtype.Numeric`，DTO 用字符串 decimal）+ **provider 成本价** `channel_cost_prices` CRUD。售价生效窗不重叠由 **DB `EXCLUDE` 约束（`tstzrange`）** 按 (model, currency, pricing_unit) 强制；成本价用应用层重叠校验。沿用 DEC-008/DEC-017：只填明确金额、无倍率；价格不可删（只 PATCH effective_to / status 关闭窗口），快照只读。 |

新增内部包：`internal/service/admin/{channelmodel,model,costprice,price}`；新增 `internal/app/adminapi/{channel_models,models,cost_prices,prices}.go` + 路由装配；新增 `sql/queries/{channel_models,models,channel_cost_prices,prices}.sql`（sqlc 已生成）；`failure/code.go` 新增定价相关失败码。服务层单测 + adminapi handler 状态码测试齐备。

前端 `unio-admin`（commit `a4a589a`）：ModelsPage、定价（成本价 / 售价）管理、渠道模型绑定页面已交付，沿用 TanStack Query + 服务端分页 + enabled/disabled tabs。

## 已交付（Slice 3，M6 只读查询台，2026-06-11）

请求记录（含详情聚合）/ 用量 / 账本流水 / 计费异常的只读查询台，端到端打通。

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-13.04（M6 只读查询台） | done | `request_records`（列表 + 按对外 `request_id` 聚合详情：attempts/usage/ledger/exception）、`usage_records`、`ledger_entries`、`ledger_billing_exceptions` 全部只读。**安全红线**：列表 SQL 从不 SELECT `internal_error_detail`（存储层即脱敏）；详情默认脱敏，仅 `?include_internal=true` 才回显内部错误详情；金额一律十进制字符串不经 float。 |

新增内部包：`internal/service/admin/query`（request/usage/ledger 三只读 service）；新增 `internal/app/adminapi/{requests,usage,ledger}.go` + 路由装配；新增对应 `sql/queries`（sqlc 已生成）。前端 `unio-admin`：RequestsPage（+ 详情弹窗）、UsagePage、LedgerPage（流水/异常 tabs），沿用 TanStack Query + 服务端分页。

## 已交付（Slice 4，M7 客户管理 + API Key 费用上限，2026-06-12）

用户 / 项目（工作空间）只读 + API Key 管理 + 手工调额，端到端打通并真实 PG 验证。

> **范围决策（本切片定调）**：项目仅作工作空间（类比 OpenAI/OpenRouter workspace），**不承载任何启停/预算/策略**；费用上限改为挂在 **API Key**（生命周期累计封顶，口径同 OpenRouter）。原计划的「项目启停 / 项目渠道策略 / 项目预算闸门（GAP-6-005）」据此**废弃**。

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-13.05（M7 客户管理） | done | **用户**：分页列表 + 详情（各币种余额，绝不回 `password_hash`）。**项目**：分页列表（可按 user_id 过滤）+ 详情，纯工作空间无限额。**API Key**：列表/详情/创建（明文只回一次）/启停/吊销（不可逆）/设费用上限；绝不回 `key_hash`，状态按 revoked>disabled>expired>active 计算。**手工调额**：充值/扣款一律走 `core/ledger` 写 `adjustment_credit`/`adjustment_debit` 流水（`request_record_id=NULL`、幂等键、留痕），禁止直接改 `user_balances`。 |
| API Key 费用上限闸门 | done | migration 000026 给 `api_keys` 加 `spend_limit`(可空=不限) + `spent_total`(计数器)。认证路径（`core/auth`）读 SQL 层计算的 `spend_limit_reached` 早拒，命中返回 **HTTP 402**（区别于 401，语义为「Key 有效但额度用尽」）；计数器在 settlement **capture** 时按实扣金额累加（同事务、仅首次结算执行，幂等重放不重复累加）。近上限时并发请求可能轻微超额，符合「生命周期软上限」语义。 |

新增内部包：`internal/service/admin/customer`（user/project/apikey/adjustment service）；新增 `internal/app/adminapi/{users,projects,api_keys,adjustments}.go` + 路由装配；扩展 `sql/queries/{users,projects,api_keys,user_balances}.sql`（sqlc 已生成）；`core/ledger` 新增 `AdjustCredit`/`AdjustDebit`；`failure/code.go` 新增 `auth_api_key_spend_limit_reached`。`admin_server.go` 的 DB 依赖升级为 `sqlc.DBTX + ledger.TxBeginner`（调额需事务）。单测：ledger 调额集成测试、auth 超额拒绝、customer service、adminapi handler 状态码。前端 `unio-admin`：UsersPage（+ 余额/调额弹窗）、ProjectsPage、ApiKeysPage（创建一次性明文展示、启停开关、设上限、吊销确认），新增「客户」导航分组。

## 已交付（Slice 5，M5 能力管理，2026-06-12）

模型能力（手工覆盖）/ 渠道收紧 CRUD + models.dev 同步（内联触发/dry-run/job 展示）+ adapter 画像物化 + enforce 只读状态与 observe 分布，端到端打通并真实 PG 验证。

> **范围决策（本切片定调）**：复用阶段 12 已交付的能力三层 schema（`model_capabilities` / `channel_capability_overrides` / `model_capability_sync_jobs`）与 `core/capability` 校验基座，**不绕过校验**直写。enforce **只读不切**——admin 仅展示各 ingress 表面的 observe/enforce 现状（读 admin 自身进程的 `CAPABILITY_ENFORCE_*` env 快照，标注 `deploy_env`）+ observe 期 `capability_check_result` 分布；真正翻 enforce 仍是改 gateway env + 重启的运营动作（DB runtime 热切留作独立切片）。

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-13.06（M5 能力管理） | done | **模型能力**：list/upsert（`source=manual`、`updated_by=admin`）/delete，写入前过 key 注册表 + 支持级别校验，limits 仅 limited 级别允许。**渠道收紧**：list/upsert（**只能减**：`limited`/`unsupported`，禁止 `full`）/delete + reason 留痕。**能力 key 注册表**：`GET /capability/keys` 暴露 `RegisteredKeys()`。**models.dev 同步**：admin 内联触发 `modelcatalog.Syncer.Sync`（`dry_run` 只算合并计划不写库；实际应用由 Syncer 内部建并推进 sync job）+ 最近 job 列表。**adapter 画像物化**：从装配期注册的 profile（目前仅 DeepSeek 的 openai/anthropic 两协议）对选定 model 调 `MaterializeAdapterSeed`（`source=adapter_seed`，幂等覆盖同 key）。**enforce 只读**：三表面 observe/enforce 现状 + observe 期判定分布。 |

新增内部包：`internal/service/admin/capability`（CapabilityService / SyncService / SeedService / EnforcementService）；新增 `internal/app/adminapi/{capabilities,capability_sync,capability_seed,capability_enforcement}.go` + 路由装配；`sql/queries` 新增 `DeleteModelCapability` / `ListSyncJobs` / `CountRequestsByCapabilityResult`（sqlc 已生成），`core/capability.Store` 补 `DeleteModelCapability` / `ListSyncJobs`；`adminapi/errors.go` 把 `capability_invalid_*`/`capability_not_found` 映射为可读 4xx；`admin_server.go` 装配 capability store + Syncer + DeepSeek profile 注册表 + `config.Capability` 快照。单测：服务层（渠道只减校验、source=manual、profile 未注册、observe NULL 映射、limit 夹紧）+ adminapi handler 状态码。前端 `unio-admin`：ModelsPage 行操作「能力」(`ModelCapabilitiesDialog`)、ChannelsPage 行操作「能力收紧」(`ChannelCapabilityOverridesDialog`)、CapabilityPage（同步 / Adapter 画像 / Enforce 状态 三 tab），新增「能力」导航。

## 已交付（Slice 6，M9 工作台看板，2026-06-12）

运营首页只读聚合：KPI 概览 + 时间序列，复用现有事实表，端到端打通并真实 PG 验证。

> **范围决策（本切片定调）**：纯只读、不新增业务事实/不加 migration，复用 M6 的事实表（`request_records` / `usage_records` / `ledger_entries` / `cost_snapshots` / `user_balances` / `ledger_billing_exceptions` / `request_attempts`）。收入/成本/毛利/余额一律**按币种分组、绝不跨币种相加**；毛利用 `math/big.Rat` 精确相减。channel **无 health 列**，健康从区间内 `request_attempts` 成功率推导并分桶（healthy≥0.95 / degraded≥0.80 / unhealthy / no_data）。计费异常表**无 resolved 状态**，仅按 `event_type` 计数 + `platform_amount` 汇总（语义为「区间内新增异常」）。

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-13.07（M9 工作台看板） | done | **KPI 概览** `GET /dashboard/overview?from=&to=`：区间内请求数 / 成功率 / 错误率（分母为终态请求，不含 pending+running）、token 用量（input/output/total）、收入（`ledger_entries` debit 按币种）、平台成本（`cost_snapshots` 按币种）与毛利、用户余额总额（`user_balances` 按币种，时点值，含可用=余额−冻结）、计费异常（按 event_type）、启用 channel 数 + 健康分布。**时间序列** `GET /dashboard/timeseries?metric=requests\|tokens\|spend&interval=hour\|day&from=&to=`：`date_trunc`（UTC 截断）按桶聚合，spend 按币种多线；metric/interval 非法返回 400，from/to 缺省默认近 7 天。 |

新增内部包：`internal/service/admin/dashboard`（Service.Overview + Timeseries，含 numeric→string / big.Rat 毛利 / 健康分桶 helper）；新增 `internal/app/adminapi/dashboard.go` + 路由装配；`sql/queries/dashboard.sql` 新增 11 条只读聚合（status 计数 / token 汇总 / 收入·成本·余额·异常按币种或 event_type / 启用 channel 计数 / channel 健康 LEFT JOIN / 三条 timeseries，sqlc 已生成）；`admin_server.go` 复用同一 `sqlc.Queries` 装配。单测：服务层（成功率分母、big.Rat 毛利含仅成本侧币种、健康分桶、timeseries metric 分派、metric/interval 非法）+ adminapi handler 状态码 + `store/sqlc` DB 门控 SQL well-formed 校验。前端 `unio-admin`：引入 `recharts@2.15.4` + `ui/chart.tsx`，重写首页 `DashboardPage`（区间预设 24h/7d/30d + KPI 卡片 + recharts 趋势折线[请求/Token/收入] + 渠道健康分布与降级清单）。

## 验证（2026-06-09，真实 Postgres）

```bash
go build ./... ; go vet ./...                                   # 通过
DATABASE_URL=postgres://unio:***@localhost:5432/unio?sslmode=disable \
  go test ./...                                                 # 43 包全绿，0 失败
```

包含：adminauth / adminapi handler 状态码 / provider+channel service / DB 门控 provider+channel CRUD 集成测试。

> 运行 DB 测试前先把本地库重置到当前迁移（`migrate ... drop -f` + `up`）：源 migration 曾原地改表（如 `request_records.capability_check_result`），旧本地库 schema 会落后于迁移文件导致 ledger/requestlog/usage/settlement 等 DB 测试失败。

## 验证（2026-06-12，真实 Postgres，含 M6 + M7）

```bash
migrate -path migrations -database "$DATABASE_URL" up                # 应用至版本 26（api_keys spend_limit/spent_total）
go build ./... ; go vet ./...                                        # 通过
DATABASE_URL=postgres://unio:***@localhost:5432/unio?sslmode=disable \
  REDIS_ADDR=localhost:6380 go test ./...                            # 全绿，0 失败
```

新增覆盖：ledger 调额（adjustment_credit/debit + 幂等 + 余额不足）集成测试、auth API Key 超额拒绝、customer service 单测（调额校验/方向、API Key 状态计算/创建明文）、adminapi 客户管理 handler 状态码（含调额 422、一次性明文、无 key_hash）。settlement 计数器累加（capture 同事务）经既有 lifecycle DB 测试回归验证。前端 `unio-admin`：`bun run build`（tsc + vite）通过；`bun run lint` 仅余既有 shadcn 生成文件的 5 处告警，M7 新增文件零报错。

## 验证（2026-06-12，真实 Postgres，含 M5）

```bash
go build ./... ; go vet ./...                                       # 通过
DATABASE_URL=postgres://unio:***@localhost:5432/unio?sslmode=disable \
  REDIS_ADDR=localhost:6380 go test ./...                            # 全绿，0 失败
```

新增覆盖：capability service 单测（渠道 override 只减校验、模型能力 `source=manual` upsert/delete、limits 仅 limited 级别 + JSON 校验、SyncService dry-run/apply、SeedService profile 未注册报错与物化）、enforcement service 单测（env 快照各表面模式 + observe 期 `capability_check_result` 分布含 NULL→bypassed 归并）、adminapi 能力管理 handler 状态码（key 非法 400、模型/渠道不存在 404、override 设 `full` 400）。前端 `unio-admin`：`bun run build`（tsc + vite）通过；`bun run lint` 仅余既有 shadcn 生成文件的 5 处告警，M5 新增文件零报错。

## 验证（2026-06-12，真实 Postgres，含 M9）

```bash
go build ./... ; go vet ./...                                       # 通过
DATABASE_URL=postgres://unio:***@localhost:5432/unio?sslmode=disable \
  REDIS_ADDR=localhost:6380 go test ./...                            # M9 相关包全绿
```

新增覆盖：dashboard service 单测（成功率分母为终态请求、`big.Rat` 毛利含仅成本侧币种的负毛利、健康分桶 healthy/degraded/unhealthy/no_data、timeseries metric 分派与 unit 透传、metric/interval 非法报 `admin_invalid_argument`）、adminapi handler 状态码（overview/timeseries 200、非法时间/metric/interval 400、缺 token 401）、`store/sqlc` DB 门控 `TestDashboardQueriesAgainstSchema`（11 条聚合 SQL 在真实 schema 上 well-formed，含 `date_trunc`/`FILTER`/`COALESCE::bigint`）。前端 `unio-admin`：`bun run build`（tsc + vite）通过；`bun run lint` 仅余既有 shadcn 生成文件的 5 处告警，M9 新增文件零报错。

> 已知环境性失败（非 M9 回归）：`internal/core/modelcatalog.TestSyncAppliesAgainstDatabase` 因本地开发库经 admin-server 触发过真实 models.dev 同步、已落 200+ 条 `seed_models_dev` 模型，该测试期望仅删 1 条（`removed=N`）。与 M9 无关（M9 不触及 modelcatalog），重置本地库（`migrate ... drop -f` + `up`）后即恢复。

## 尚未开始

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| M8 系统/任务/健康（横切） | planned | settlement_recovery_jobs / sync 任务视图、channel 熔断状态等运营横切面尚未开始（草稿推进顺序之外的可选切片）。 |

阶段 13 主线切片（按 ADMIN_MODULES_DRAFT.md 推进顺序）已全部交付：M1 认证（Slice 1）、M2 凭据 + M3 provider/channel/model/绑定（Slice 1/2）、M4 定价（Slice 2）、M6 只读查询台（Slice 3）、M7 客户/API Key 费用上限/手工调额（Slice 4）、M5 能力管理（Slice 5）、M9 工作台看板（Slice 6）。剩 M8（系统/任务/健康横切）为可选收尾切片。原 GAP-6-005（项目级路由/预算闸门）已随 M7「项目=工作空间、费用上限挂 API Key」决策废弃。留尾：M5 enforce DB runtime 热切（避免 admin 显示与 gateway env 漂移）+ models.dev 同步对既有模型的能力刷新；M9 大表实时聚合的 rollup/物化视图优化（草稿已预留 `usage_rollup_worker`）+ 跨时区分桶。

## 进入阶段 13 前置条件（已满足）

1. 阶段 7 计费事实链路稳定。
2. 阶段 8 观测字段可支撑后台查询。
3. 阶段 10 双协议 gateway 链路稳定。
4. 阶段 11 OpenAI Responses 已收口（公开 API 表面冻结）。
5. 阶段 12 能力架构已交付（capability schema、运行时闸门、cap-tags API）。
6. credential resolver 生产方案：Slice 1 沿用 master key + 密文列；外部 KMS/secret manager 取舍留待 13.02（GAP-6-001）。
