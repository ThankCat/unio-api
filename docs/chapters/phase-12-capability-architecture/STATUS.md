# Phase 12 Status

状态：代码层收口（capability 架构代码全交付，默认 observe；console / 模型 provisioning / 观察期切 enforce 受控 deferred 阶段 13）

进入条件：阶段 11 OpenAI Responses 收口（ingress 表面冻结，CAPABILITY_MATRIX 静态文档可作为本阶段种子）。

> 收口决策（用户授权方案 A）：把能力架构**代码层全收口**——TASK-12.02 档位抽取 + TASK-12.03 observe/enforce 闸门 + TASK-12.07 观测审计 + TASK-12.08 enforce 开关/三协议渲染（默认 observe）+ TASK-12.05 `/v1/models` cap-tags。三类需外部前置的工作受控 deferred 阶段 13：(1) `/console/v1/models`（console-server）；(2) adapter 画像物化进真实 `model_capabilities` 行（模型 provisioning / admin）；(3) observe→enforce 实际切换（7~14 天观察期 + 种子能力位校准）。对应 deferred GAP：[GAP-12-002](../../production/TODO_REGISTER.md#gap-12-002) / [GAP-12-006](../../production/TODO_REGISTER.md#gap-12-006) / [GAP-12-007](../../production/TODO_REGISTER.md#gap-12-007) / [GAP-12-009](../../production/TODO_REGISTER.md#gap-12-009)。已关闭：[GAP-12-001](../../production/TODO_REGISTER.md#gap-12-001) / [GAP-12-003](../../production/TODO_REGISTER.md#gap-12-003) / [GAP-12-004](../../production/TODO_REGISTER.md#gap-12-004) / [GAP-12-005](../../production/TODO_REGISTER.md#gap-12-005) / [GAP-12-008](../../production/TODO_REGISTER.md#gap-12-008) / [GAP-12-012](../../production/TODO_REGISTER.md#gap-12-012)。

进度：

- TASK-12.01 capability schema 已完成（schema + sqlc 查询 + `internal/core/capability` 访问层 + 公开 `CAPABILITY_KEYS.md` 注册表 + DB 门控测试），关闭 [GAP-12-001](../../production/TODO_REGISTER.md#gap-12-001)。
- TASK-12.02 ingress capability inference 推断核心已落地：`internal/core/capability` 的 `RequestSignals` + `Infer` 纯函数（集中规则、单测逐条覆盖 + registered-key 守护 + 确定性）与 `Set` 类型；三协议各自 `capabilitySignals` 从自身 DTO 抽取信号（含 content/tools/thinking JSON 解析，OpenAI Chat / Anthropic Messages / OpenAI Responses 各一份 + fuzz）。分层修正：推断逻辑放 `core/capability`（不依赖 app DTO），协议解析留各 app 包。**接线已在 TASK-12.03 完成**（三协议 `RequiredCapabilities(req)` 导出 + 6 service 调用点透传 + observe 消费 + `request_attempts.required_capabilities` 审计），不再是无消费方 dead code。**档位值抽取已收口**：`RequestSignals.ReasoningEffortLevel` + `InferLimits` + 三协议 `RequestLimits(req)` 透传 `routing.ChatRouteRequest.RequestLimits` → 闸门消费 limited 超限（[GAP-12-012](../../production/TODO_REGISTER.md#gap-12-012) 关闭）。[GAP-12-002](../../production/TODO_REGISTER.md#gap-12-002)（推断覆盖面 enforce 切换前复核）受控 deferred 阶段 13。
- TASK-12.06 adapter 对齐部分交付：DeepSeek 两协议 adapter 各落 `CapabilityProfile()`（与 `dropUnsupported` 同源），`capability_profile_test.go` drift 守护“声明级别 ⟺ 实际 Drop/Adapt/透传”、强制 unsupported/limited 必须有探针；`core/capability` 新增 `AdapterProfile` + `MaterializeAdapterSeed`（`source=adapter_seed` 幂等 upsert）+ 单测。GAP-11-010 结论沉淀（reasoning.effort=limited{high,max}）。**未做**：画像→真实 `model_capabilities` 行的 loader/子命令（需模型 provisioning，归阶段 13 / TASK-12.08）。原计划“migrations seed”因 migration 规则与无 provisioning 改为代码同源画像，详见 [PLAN TASK-12.06 落地说明](PLAN.md#task-12-06-adapter-alignment)。[GAP-12-007](../../production/TODO_REGISTER.md#gap-12-007) 转 in_progress（enforce 前置门槛仍在）。
- TASK-12.04 models.dev daily cron 已完成：`internal/core/modelcatalog`（feed 解析 + 纯函数合并规划 + sqlc upsert/标记删除 + HTTP fetcher + Syncer）+ `app/workers` cron worker（interval 门控 + 失败退避 + 连续 3 次失败告警）+ `worker-server sync-models --dry-run` 子命令 + config（默认关闭 opt-in）。合并规则：`source=manual`/`import` 永不被覆盖（SQL `WHERE` 守护 + planner 预过滤）、新模型默认 disabled + 写 `source=models_dev` 粗能力位/`max_output_tokens`/价格基线、上游删除只标记不删本地。单测 + DB 门控集成测试（manual 守护/首次入库/删除标记/sync_job license 审计）全绿。**先决 [GAP-12-005](../../production/TODO_REGISTER.md#gap-12-005) 已关闭**（models.dev = MIT，[MODELS_DEV_LICENSE.md](../../datasources/MODELS_DEV_LICENSE.md) + sync_job license 指纹审计），[GAP-12-004](../../production/TODO_REGISTER.md#gap-12-004) 交付。运营残留（精确 cron 时刻、Prometheus sync 指标、prod 启用签字）归 [GAP-12-011](../../production/TODO_REGISTER.md#gap-12-011)。详见 [PLAN TASK-12.04 落地说明](PLAN.md#task-12-04-models-dev-sync)。
- TASK-12.03 routing capability filter **observe 闭环 + enforce 代码已交付（默认 observe）**：`core/capability/gate.go` 纯判定 `Evaluate`（稳定结论 `ok`/`model_unavailable`/`channel_unavailable`/`unprovisioned`/`no_required`/`error`，零声明放行、有声明严判、channel override 只减、limited 超限判定）+ `gate_test.go`；`core/routing` `CapabilityChecker` 接口 + `observeCapability`（只把结论写进 `ChatRoutePlan.Capability`，**不过滤候选**）+ `enforceCapability`（按 ingress 表面开关把不可用判定升级为 sentinel + `failure` 错误码 + `missing_capabilities` field）；`service/gateway/capabilitygate.Checker`（读 store → Evaluate → metric → 审计，store 异常 fail-open）；三协议推断 + 档位接线 + 6 service 调用点透传 `RequiredCapabilities`/`RequestLimits`/`Operation` + `request_attempts.required_capabilities TEXT[]` 审计持久化 + 三指标 + bootstrap 接线（checker + enforcement）。全量 `go test`（含 gate/checker/router observe+enforce/requestlog DB 往返）全绿。**已收口**：[GAP-12-003](../../production/TODO_REGISTER.md#gap-12-003)（enforce 渲染 + 开关代码）、[GAP-12-012](../../production/TODO_REGISTER.md#gap-12-012)（limited 档位值抽取）均关闭；实际 observe→enforce 切换 = 运营决策（[GAP-12-009](../../production/TODO_REGISTER.md#gap-12-009)，受控 deferred 阶段 13）。详见 [PLAN TASK-12.03 落地说明](PLAN.md#task-12-03-capability-filter)。
- TASK-12.05 public capability surface **`/v1/models` cap-tags 已交付**：`app/gatewayapi/openai/models` handler 扩展 `capabilities` cap-tags 数组（SDK 兼容，未声明返回空数组）+ `?capability=a,b` AND 过滤；`core/modelcatalog.ListAvailableModels` 经 sqlc `array_agg ... FILTER (support_level<>'unsupported')` 按 project 可见性 + cap-tags 输出；`handler_test.go` 覆盖。`/console/v1/models` + 缓存失效依赖 console-server，受控 deferred 阶段 13（[GAP-12-006](../../production/TODO_REGISTER.md#gap-12-006)）。
- TASK-12.07 observability + audit **已交付**：三闸门指标 `unio_gateway_capability_{check,required,missing}_total`（`metrics.go` + `capabilitygate.Checker` 发射）+ 结构化审计日志（required/missing capabilities，would-be 拒绝 Warn）+ `request_records.capability_check_result TEXT`（[000009](../../../migrations/000009_create_request_records.up.sql) CHECK 约束 + sqlc `MarkRequestCapabilityCheckResult` + `requestlog.Store.SetCapabilityCheckResult` + `lifecycle.RecordCapabilityResult` 在 `PlanChat` 成功后 best-effort 写，nil 保持 NULL=bypassed）+ `request_attempts.required_capabilities`；cap-tag 不入 ledger/cost_snapshot。`store_test.go` DB 往返覆盖 NULL→写入回读→CHECK 拒绝未知值。sync 指标 + 漂移告警归 [GAP-12-008](../../production/TODO_REGISTER.md#gap-12-008) 残留 / [GAP-12-011](../../production/TODO_REGISTER.md#gap-12-011)。
- TASK-12.08 灰度迁移 **enforce 代码就位（默认 observe）**：config `CapabilityConfig`（`CAPABILITY_ENFORCE_OPENAI_CHAT`/`_ANTHROPIC_MESSAGES`/`_OPENAI_RESPONSES`，全默认 false）+ router `CapabilityEnforcement` + `ChatRouteRequest.Operation` + `enforceCapability`（仅匹配表面开关 ON 时拒绝）+ 三协议客户错误渲染（OpenAI Chat/Responses → 400 `model_capability_unavailable`，Anthropic → 400 `invalid_request_error`，统一列缺失 capability key、**不暴露 channel 拓扑/凭据**，`routing.MissingCapabilities` 封装 field 提取）+ `SetCapabilityEnforcement` bootstrap 注入；测试覆盖 router enforce 决策（拒绝/放行/按表面 scope）+ chat handler 渲染。实际切 enforce + 观察期 + provisioning 物化受控 deferred 阶段 13（[GAP-12-009](../../production/TODO_REGISTER.md#gap-12-009)/[GAP-12-002](../../production/TODO_REGISTER.md#gap-12-002)/[GAP-12-007](../../production/TODO_REGISTER.md#gap-12-007)）。

## 任务表

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-12.01 capability schema | done | models 扩展 Layer 1 列 + model_capabilities / channel_capability_overrides / model_capability_sync_jobs 表 + sqlc 查询 + `internal/core/capability` 访问层 + 公开 `docs/protocol/CAPABILITY_KEYS.md` v1 注册表；DB 门控测试覆盖 upsert/list/CASCADE/CHECK。仅落事实基座，不含推断/闸门/同步逻辑。 |
| TASK-12.02 ingress capability inference | done | 推断纯函数（`core/capability.Infer` + `RequestSignals` + `Set`）+ `InferLimits`（reasoning.effort 档位值）+ 三协议 `capabilitySignals`/`RequestLimits` DTO 抽取 + 单测/fuzz；接线在 TASK-12.03 完成，档位值透传闸门（GAP-12-012 关闭）。残留 GAP-12-002（覆盖面 enforce 前复核）受控 deferred 阶段 13。 |
| TASK-12.03 routing capability filter | done | observe 闭环 + enforce 代码就位（默认 observe）：`gate.go` 纯判定 + routing observe（只记录不拒绝）+ enforce 决策（`enforceCapability`，按表面开关）+ 三协议推断/档位接线 + `capabilitygate.Checker`（metric + 审计 + fail-open）+ `request_attempts.required_capabilities` 持久化 + 三指标。GAP-12-003/12-012 关闭；实际 enforce 切换 = 运营决策（GAP-12-009）。 |
| TASK-12.04 models.dev daily cron | done | `core/modelcatalog` 同步（解析 + 纯函数合并 + sqlc upsert/删除标记 + HTTP fetcher + Syncer）+ cron worker（interval 门控/失败退避/连续失败告警）+ `sync-models --dry-run` 子命令 + config（默认关闭）。source=manual 不覆盖、新模型默认 disabled、上游删除只标记、license 指纹入 sync_job 审计；单测 + DB 门控集成全绿。运营残留见 GAP-12-011。 |
| TASK-12.05 public capability surface | in_progress | `/v1/models` cap-tags 扩展（SDK 兼容）+ `?capability=` AND 过滤已交付（handler + sqlc + 测试）。`/console/v1/models` + 缓存失效依赖 console-server，受控 deferred 阶段 13（GAP-12-006）。 |
| TASK-12.06 adapter drop 对齐 | in_progress | DeepSeek 两协议 adapter `CapabilityProfile()`（与 `dropUnsupported` 同源）+ drift 守护测试 + `core/capability` `AdapterProfile`/`MaterializeAdapterSeed` 已落地；GAP-11-010 结论已沉淀。物化进真实 model 行的 loader 待模型 provisioning，受控 deferred 阶段 13（GAP-12-007）。 |
| TASK-12.07 observability + audit | done | 三闸门指标（check/required/missing）+ 结构化审计日志 + `request_records.capability_check_result` 列（CHECK + sqlc + Store/lifecycle 写 + DB 往返测试）+ `request_attempts.required_capabilities`；cap-tag 不入账务。sync 指标归 GAP-12-008 残留/GAP-12-011。 |
| TASK-12.08 灰度迁移 | in_progress | enforce 代码就位（默认 observe）：config `CAPABILITY_ENFORCE_*` 按表面独立可控 + router `enforceCapability` + 三协议 capability error 渲染（不暴露 channel 拓扑）+ 测试。实际 observe→enforce 切换 + 观察期 + provisioning 物化受控 deferred 阶段 13（GAP-12-009/12-002/12-007）。 |

## 风险与关注点

1. capability_keys 注册表是公开稳定契约：发布即冻结、只能新增不能删除；命名前需要在 `docs/protocol/CAPABILITY_KEYS.md` review。
2. models.dev license：已确认为 MIT（© 2025 models.dev），摘要 + attribution + 公开 API 无成文 ToS 的缓解见 [MODELS_DEV_LICENSE.md](../../datasources/MODELS_DEV_LICENSE.md)；同步把 license 指纹写入 sync_job 审计（[GAP-12-005](../../production/TODO_REGISTER.md#gap-12-005) 已关闭）。后续若 API 端点出现限制性 ToS，回落到直接消费 MIT 仓库数据。
3. enforce 模式切换前必须完成观察期 + adapter 对齐（TASK-12.06），避免误拒生产请求。
4. 不引入跨 provider 拼接（DeepSeek 缺能力时不去外部 provider 拼接）；Unio 是网关不是 agent 平台。
5. 预授权兜底（[GAP-12-010](../../production/TODO_REGISTER.md#gap-12-010)）：当前客户省略输出上限时用全局 `DefaultAuthorizationMaxCompletionTokens=4096` 兜底，DeepSeek-V4（输出 384K）长输出会预冻结不足、差额走 `authorization_underfunded` 平台核销漏收；本阶段 `models.max_output_tokens`（TASK-12.01）落库后需把 authorization 兜底改为按模型上限。

## 与上下游阶段

```text
依赖：Phase 11 CAPABILITY_MATRIX 静态版本（迁入运行时）
影响：Phase 13 admin 直接基于本阶段表做 CRUD；不需要再设计能力表 schema
不影响：Phase 7 账务事实 / Phase 10 lifecycle / Phase 11 公开 API 表面
```

## 验证步骤（实现期对照）

```bash
sqlc generate
go build ./internal/... ./cmd/...
go vet ./internal/... ./cmd/...
go test ./internal/... ./cmd/...
git diff --check
```

同步 worker 启动后用 `--dry-run` 校验 conflict 列表；observe 模式上线后用 metrics 看 `unio_gateway_capability_missing_total` 分布再切 enforce。
