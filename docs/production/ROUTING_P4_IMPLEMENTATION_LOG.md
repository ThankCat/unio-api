# P4 实施日志：Endpoint 单故障域、Redis 全局熔断与客观路由观测

> 关联计划：[ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md](./ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md)
>
> 前置基线：[ROUTING_P3_IMPLEMENTATION_LOG.md](./ROUTING_P3_IMPLEMENTATION_LOG.md)
>
> 开始日期：2026-07-22

本日志按计划第 11 节 A–H 阶段逐检查点记录：工作区保护、实际改动、测试结果、偏差、临时数据和恢复结果。每完成一个检查点立即更新。

---

## 0. 工作区保护与基线

### 0.1 起始 worktree 状态（`unio-gateway`，branch `main`）

改造开始前已存在的未提交改动（P4 设计阶段产物，非本次实现引入）：

```
 M AGENTS.md
 M docs/production/DECISIONS.md
 M docs/production/ROUTING_P4_GLOBAL_BREAKER_PROVIDER_PLAN.md
```

`unio-admin`、`unio-blueprint` 为独立 git 仓库，起始 worktree 干净。三者均在 `main`。

### 0.2 构建/测试基线

- `go build ./...`：通过（exit 0）。
- `go vet ./internal/service/gateway/lifecycle/`：通过。
- 完整 `go test ./...` 需要本地 PostgreSQL + Redis（`docker-compose.yml`：Postgres :5432，Redis :6380→6379，volumes `postgres_data`/`redis_data` 为 external）。集成/E2E 基线在起对应 infra 后补记。

### 0.3 迁移与 schema 事实（Phase B 前置）

- 迁移为「一表一文件」的 squash 风格（pg_dump 形态），当前编号 `000001`–`000041`；`sqlc.yaml` 以 `migrations/*.up.sql` 为 schema，按字典序解析，因此外键被引用表必须排在引用表之前。
- 迁移文件内 `[000053_...]` 等归档注释是**旧的 pre-squash 历史编号**（provenance），不是当前文件号，Phase B 重编号时**不改**这些归档注释。
- 关键现状编号：`000005_app_settings`、`000007_providers`、`000008_channels`、`000009_request_records`、`000010_request_attempts`。
- Phase B 目标：`000008..000041` 整体 +2 → `000010..000043`；新增 `000008_provider_endpoints`、`000009_endpoint_routing_operations`、`000044_runtime_control_operations`。
- 迁移执行入口为通用目录扫描（非硬编码编号）；代码中对具体文件编号的引用集中在 `docs/` 与归档注释，Go 代码几乎不硬编码当前文件编号。
- `Channel.BaseUrl` 影响面：生成的 `sqlc/channel*.sql.go`、admin `channel`/`channelops`/`channeltest`/`provider`、`core/routing`、`core/adapter/{openai,anthropic}`、blackbox 测试等。

### 0.4 真实上游（E2E）

- StarAPI baseURL：`https://open.codex521.cc`（OpenAI + Anthropic 协议均可）。
- 已获授权的上游 key 与可用模型清单由用户提供，用于 Phase H 真实 E2E（OpenAI/Anthropic 流式+非流式、Codex/Claude CLI）。密钥不写入仓库与日志正文。

---

## A. 基线与文档治理

- [x] 建立本 P4 实施日志并记录起始 dirty worktree（见 §0.1）。
- [x] 记录现有构建基线；DB/Redis/Admin/Gateway 集成测试基线在起 infra 后补记（见 §0.2）。
- [x] 在 P3 文档标注被 P4 替代的健康度、进程内 breaker 与总耗时权重语义（见 P3 文档顶部 P4 替代提示）。
- [x] P4-D01..D20 决议已在 `DECISIONS.md`；D20 为 2026-07-23 补充的 Provider 列表 Endpoint 可观测列与行级创建入口。
- [x] 扫描 TODO_REGISTER/RELEASE_BLOCKERS：当前无 P0 release blocker；本次改造涉及公开协议/故障域/Redis 失败策略，实现中若出现契约级新歧义按计划 §17 暂停并登记。

## B. Schema 与契约

### B1. 迁移重编号与新表（完成）

- [x] `000008..000041` 整体 +2 → `000010..000043`（`git mv`，34 编号 × up/down = 68 文件，编号连续无洞）。
- [x] 新增 `000008_provider_endpoints`（唯一规范化 `base_url` + `base_url_revision`/`status_revision` + `(id,provider_id)` 复合唯一 + status 一致性）。
- [x] 新增 `000009_endpoint_routing_operations`（`preparing→prepared→db_committed→committed`、单 Endpoint / Provider 批量非终态 partial-unique、FK RESTRICT）。
- [x] 新增 `000044_runtime_control_operations`（`channel_admission_limits|app_setting|runtime_state_epoch` 三 kind、目标列/epoch 列 CHECK、per-channel / per-setting 非终态 partial-unique、FK RESTRICT）。
- [x] 修正 `settlement_test.go` 中 stale migration 路径注释。

### B2. 字段/约束/seed 与 SQL（进行中）

已完成（schema 文件层）：
- [x] `000010_channels`：删 `base_url`；加 `provider_endpoint_id` + `(provider_endpoint_id, provider_id) → provider_endpoints(id, provider_id)` 复合外键；加 `config_revision`、`admission_limits_revision`（含 CHECK / 索引 / P4 归档注释）。
- [x] `000011_request_records`：加 `final_provider_endpoint_id` + FK。
- [x] `000012_request_attempts`：加三项 upstream 时间事实 + 冻结身份/版本快照（`provider_endpoint_id`、两类 Endpoint revision、`channel_config_revision`、`routing_candidate_index`、`upstream_operation`）+ 两个 breaker disposition；含拆开的时间有序 CHECK、revision/operation/disposition 允许值 CHECK、Endpoint FK/索引。
- [x] `000019_channel_test_logs`：加三类 tested revision + `state_change_applied`，`source` CHECK 增 `credential_rotate`。
- [x] `000005_app_settings`：加逐行 `revision` + CHECK。

**偏差记录（intentional deviation）**：`request_attempts` 的冻结身份/版本/`upstream_operation` 列按计划 §4.7 最终为 `NOT NULL`，但本阶段先以 **nullable + 值存在时 CHECK** 落地，以便 B/C/D 阶段保持 `go build` 绿；在 Phase E 重写 attempt 创建、所有插入路径都提供这些值后再收紧为 `NOT NULL`。已在 todo `phase-e-lifecycle` 关联。

`sqlc generate` 已定位需改写的 `base_url` 查询点（11 处，均因 `channels.base_url` 删除）：
- `sql/queries/api/channel_models.sql`（`FindRouteCandidates`，需 JOIN `provider_endpoints` 取 `pe.base_url` 并补选四类 revision + 硬过滤 `pe.status='enabled'`）；
- `sql/queries/admin/channel.sql`（SELECT/INSERT/UPDATE/RETURNING/搜索/GROUP BY 多处）；
- `sql/queries/admin/provider.sql`、`sql/queries/admin/route.sql`、`sql/queries/worker/channels.sql`。

待办（B2 剩余，均属 ProviderEndpoint 端到端纵切，会与 C/F/G 合并推进）：
- [ ] 改写上述 `base_url` 查询为 `provider_endpoints` join；新增 ProviderEndpoint CRUD + routing/reconciler 查询；`channels` INSERT/UPDATE 改 `provider_endpoint_id` 并冻结/递增 `config_revision`；
- [ ] `sqlc generate`（当前因 `base_url` 查询未改写而报错，未写出，故生成代码未变、`go build` 仍绿）；
- [ ] 修复 `Channel.BaseUrl` 的 ~60 处 Go 引用（生成 sqlc 后）：`core/routing`、`core/adapter/{openai,anthropic}`、admin `channel/channelops/channeltest/provider`、blackbox 测试等；`channel.Runtime.BaseURL` 来源改 Endpoint；
- [ ] app_settings 关键设置值形状（删 `failure_policy`/`weight_by_remaining`，加 circuit_breaker/routing_balance 新字段）+ seed `gateway.runtime_state_epoch` + 删 `admin_backend.channel_health_thresholds`（与 D2/F2 的 runtime-control publisher 合并；限流默认后续由 H10/DEC-054 拆成两项）；
- [ ] 空库重建校验、diff 确认无 `Channel.BaseUrl`。

### B2 续：ProviderEndpoint 端到端纵切（完成，已验证）

已完成并验证（`docker compose` 起 PG+Redis；`DATABASE_URL` 指向空库 `unio_p4test`）：
- [x] 改写全部 11 处 `base_url` 查询为 `provider_endpoints` join：`FindRouteCandidates`（选 `pe.base_url` + 四类 revision + 硬过滤 `pe.status='enabled'`）、admin `channel.sql`（SELECT/INSERT/UPDATE/RETURNING/search/GROUP BY，`UpdateChannel` 用 `IS DISTINCT FROM` 条件递增 `config_revision`）、`provider.sql`、`route.sql`（`RouteRuntimePool` 带 Endpoint 四类 revision）、`worker/channels.sql`。
- [x] 新增 `sql/queries/admin/provider_endpoint.sql`：Create/Get/ListByProvider/ListPage/Count/UpdateName/UpdateBaseURL(+rev)/UpdateStatus(+rev)/ListForUpdate/CountChannelsByEndpoint。
- [x] 对齐 `request_records`/`request_attempts`/`channel_test_logs` 显式查询列表，使其重新映射回模型类型（列顺序按表定义）。
- [x] `sqlc generate` 通过；diff 确认 `sqlc.Channel` 不再有 `BaseUrl`（仅 Row/ProviderEndpoint 上保留展示用 `BaseUrl`）。
- [x] Go 生产代码：`admin/channel`（`ProviderEndpointID` + `resolveEndpointForProvider` + enrich base_url/config/admission revision）、`admin/channeltest`（从 Endpoint 取 base_url）、`admin/provider`（replacement 校验去 base_url，SQL 兜底）、adminapi channel DTO（`provider_endpoint_id` + 只读 endpoint 展示 + revision）、worker channel-test store。
- [x] 测试：`model_channel_test` 共享 helper 改为「每 channel 建一个 Endpoint」；修复 `admin_crud_test`、`channel_test`、`provider_test`、`requestlog store_test`、`lifecycle settlement_test`、`sdkfixture`（含 teardown 删 Endpoint）。
- [x] 空库应用全部 44 个 migration 成功；`go build -tags blackbox ./...` 绿；`go test ./...`（含 DB 集成）全绿。

**说明**：本纵切已把「删除 `channels.base_url`、引入 ProviderEndpoint 单故障域」端到端落地并验证。ProviderEndpoint 的 Admin CRUD HTTP API、BaseURL/status revision fence（Redis）与前端表单属 Phase D3/F1/G，随对应阶段推进。

### B2 尾项（随 D2/F2 一并完成，避免半成品）

- [ ] `app_settings` 关键设置值形状替换（旧 `rate_limit_defaults` 删 `failure_policy`；`circuit_breaker`/`routing_balance` 新字段；`concurrency_defaults`）+ seed `gateway.runtime_state_epoch` + 删 `admin_backend.channel_health_thresholds`：这些需与 runtime-control durable publisher（D2）和严格解码注册表（appsettings）一起改，否则会产生无法解码的中间态。限流默认最终按 H10/DEC-054 拆成线路与渠道两项。已在 D2/F2 关联。

### 当前构建状态

- `go build ./...` / `go build -tags blackbox ./...`：绿。
- `go test ./...`（`DATABASE_URL`=空库 `unio_p4test`，`REDIS_ADDR`=localhost:6380）：全绿。
- 44 个 migration 空库重建成功；`sqlc generate` 无漂移。

---

## C. Adapter URL、流式 TTFT 与交付事实（部分完成）

### C1. 结构化 URL 拼接 helper（完成，已验证）

- [x] 新增 [internal/core/adapter/upstream_url.go](../../internal/core/adapter/upstream_url.go)：`BuildUpstreamURL(baseRoot, operationPath)` 用 `url.JoinPath` 结构化拼接（校验 http/https + host），并定义标准 operation 路径常量（§4.6）。
- [x] 迁移 4 个官方 adapter 站点改用 helper + Endpoint root 语义：OpenAI Chat（chat.go 非流/流两处）、OpenAI Responses（adapter.go）、Responses Compact（compact.go）、Anthropic Messages（adapter.go）。删除散落的 `strings.TrimRight(base,"/")+path`。
- [x] **BaseURL 语义变更（§4.6）**：BaseURL 现为 adapter root（不含 `/v1`），adapter 追加完整标准路径 `/v1/chat/completions` 等。相应更新 adapter/service 测试的 mock BaseURL（去掉 `/v1`），最终 URL 不变、mock 路径不变。
- [x] `go test ./internal/core/adapter/...` 与三执行面 service 测试全绿。

### C2. AttemptTimingObserver / FirstTokenEligible / 非流式 nil TTFT / delivery finalizer（非流式已完成，流式逐帧待收口）

- [x] 新增协议无关 `AttemptTimingObserver`：`TransportStarted`/`FirstTokenEligible`/`TransportCompleted` 均 first-write-wins，并发安全；transport 前失败三项保持 NULL，完成后迟到 FirstToken 不回写。
- [x] `StreamChunkMeta` 增加独立 `FirstTokenEligible` 协议元数据，明确不得从 `!SuppressEmit` 推导。
- [x] 非流式 observer 即使被误调 `FirstTokenEligible` 也永远保持 `upstream_first_token_at/FirstTokenMs=nil`；时钟回拨时按调用顺序 clamp，不破坏数据库时间有序约束。
- [x] 新增 adapter context hook，lifecycle 为每个候选绑定 attempt observer；6 个真实 HTTP 发送点都在紧邻 `http.Client.Do` 前调用 `TransportStarted`：OpenAI Chat 非流/流、Responses 非流/流/Compact、Anthropic Messages 共用发送路径。adapter 返回（成功或失败）后由 lifecycle 调用 `TransportCompleted`。
- [x] 三协议 `FirstTokenEligible` 已接入实际流式元数据：OpenAI Chat 的 role/content/reasoning/tool/refusal/function-call delta；Responses 的 `response.created` 与 output/reasoning/function-call delta（chat bridge 同 Chat）；Anthropic 的 `message_start`/`content_block_delta`。finish-only、usage、ping、error/failed、completed 与空事件均不产样本。
- [x] request attempt timing 已接 `requestlog.RecordAttemptTiming` first-write-wins 存储。流中 FirstToken 快照异步写入，不阻塞客户首帧；adapter 返回后同步写入完整 start/FirstToken/completed 快照，再进入 settlement/recovery。客户取消不会取消有界审计写入。
- [x] 非流式执行面只保存 upstream start/completed，总耗时可由两者计算；`upstream_first_token_at`、`FirstTokenMs` 与 settlement 的 `ResponseStartedAt` 均为 nil，request/attempt `response_started_at` 保持 NULL。
- [x] 针对性测试覆盖 observer context、6 个 HTTP 发送点的调用顺序/恰好一次、三协议有效与无效 FirstToken 事件、共享 lifecycle 非流/流最终持久化以及非流式 `response_started_at=NULL`。验证：上述 8 个 adapter/lifecycle/service 包 focused tests 与 `-race -count=1` 全绿，`git diff --check` 通过。
- [x] 非流式 runner 删除 settlement 后提前 `MarkDeliveryCompleted`：成功返回时 delivery 仍为 `not_started`，只返回不含 request/attempt ID 的内部 `NonStreamResult` 与 opaque `DeliveryFinalizer`；公开 Chat/Responses/Compact/Anthropic DTO 未增加内部字段。
- [x] Chat、Responses Create、Responses Compact、Anthropic 四个非流式 handler 均以 `httpx.WriteJSON` 的真实结果收口：nil 时 `not_started -> completed`，write error 或 panic 时 `not_started -> interrupted`，panic 记录后原样抛出；`sync.Once` 保证并发 complete/interrupt first-terminal-wins。
- [x] `MarkRequestDeliveryCompleted/Interrupted` 已进入 `requestlog.Service` 必需契约，删除 lifecycle 的可选 marker 类型断言，测试 fake 缺实现会编译失败而不再静默漏记。recording fake 覆盖 service 返回前零终态及 handler 写后唯一终态；真实 PostgreSQL Store 测试覆盖 completed/interrupted 互斥、`response_completed_at` 约束与非流式 `response_started_at=NULL`。
- [x] 非流式 delivery focused 验证：lifecycle、三 handler 包、三 service 包与 bootstrap 普通测试全绿；四入口 success/write-error/panic 矩阵全绿；隔离 PostgreSQL 的 requestlog Store 测试与 `-race` 全绿；`git diff --check` 通过。全量 `go test ./...` 留给 AttemptPermit/RequestAdmission 并行接口收口后统一复跑。
- [ ] 仍需 Phase E 把最终 `FirstTokenMs` 交给 BreakerStore `Finish`（只允许 stream permit 更新 TTFT EWMA），并完成流式逐帧 SSE write-ack/delivery finalizer；当前 `response_started_at` 仍沿用既有流式交付路径，本检查点只把非流式 delivery finalizer 标成完成。

## D1. Redis BreakerStore 核心（原型已完成，生产契约补强中）

新增独立包 [internal/platform/breakerstore/](../../internal/platform/breakerstore/)（协议无关，三执行面共用），实现 P4 熔断核心：

- [x] 领域类型（[types.go](../../internal/platform/breakerstore/types.go)）：Scope、BreakerState、RequestMode、Outcome、Disposition、AdmissionMode/DeniedReason、Config（§4.8 形状 + 严格 valid()）、AttemptPermit、FinishOutcome/FinishResult、ScopeSnapshot。
- [x] 版本化 key（[keys.go](../../internal/platform/breakerstore/keys.go)）：`<ns>:breaker:v2:channel|endpoint|permit:*`（§5.1）。
- [x] 原子 Lua（[lua.go](../../internal/platform/breakerstore/lua.go)，均用 Redis `TIME`、先校验后写、first-terminal-wins）：`AcquireAttempt`（Endpoint+Channel breaker 门禁 + half-open 租约 + Channel 并发租约 + permit 创建 + 幂等/指纹冲突）、`Finish`（双触发状态机 + half-open 双探测恢复 + 退避 15/30/60/120/300 + 仅流式 TTFT EWMA + 资源收口 + disposition）、`Abort`、`Renew`、`Reset`、`Snapshot`。
- [x] Go 封装（[store.go](../../internal/platform/breakerstore/store.go)）：`AcquireAttempt/Finish/Abort/Renew/Reset/Snapshot`；基础设施错误统一 `ErrStoreUnavailable`（P4-D08 fail-closed）；新增 failure code `gateway_breaker_permit_conflict` / `gateway_breaker_store_unavailable`。
- [x] 429/403 运行态（与 breaker 计数独立，Reset 不清除）：`SetChannel429Cooldown`/`Channel429CooldownRemainingMs`（§2.4.1 全局共享冷却，取较大值不缩短）；`PauseChannelModelPermission`/`ClearChannelModelPermission`（§2.4.2 按 `(channel,model)` + 三类 revision CAS，config revision 前进使旧暂停 stale，不翻整 Channel credential）。AcquireAttempt 门禁顺序：429 冷却 → 403 权限 → Endpoint breaker → Channel breaker → 并发。
- [x] 真实 Redis 测试（[store_test.go](../../internal/platform/breakerstore/store_test.go)）14 例全绿：成功链路、快速触发（连续 3 次）、比例触发（≥20 样本 50%）、half-open 双探测恢复、探测失败重新 open + 退避、Abort 不计数、Finish 幂等 first-terminal-wins、TTFT 仅流式、并发上限、Reset 推进 generation 使旧 permit stale、Endpoint 作用域独立、429 冷却拒绝且不产生 breaker 样本、403 权限暂停（匹配拒绝/他模型放行/config 前进 stale/CAS 清除）。

**D1 明确的实现取舍（decision，见独立决策文档）**：
- 并发上限当前由调用方传入 `ConcurrencyLimit`（0=不限）；P4-D11 要求最终由 Redis admission control 原子解析。接入 admission control（D2）后改由 control 提供，调用方不再自报。
- Endpoint 500/首token/body-timeout 的「跨 Channel 跨模型证据」门槛（§2.4/§2.5.3）由调用方在 Finish 前决定 EndpointOutcome=EligibleFailure/Ignored（§2.5.8：Finish 前完成稳定 attribution）。BreakerStore 的 Lua 忠实应用调用方给定的 per-scope outcome，跨证据集合累积属 Phase E lifecycle 接线。
- 本核心尚未含：入口 RequestAdmission（D2）、admission-control 四维限额 + runtime-control durable publisher（D2）、Endpoint BaseURL/status 围栏 + Provider 批量 + control reconciler（D3）、完整性 epoch marker/恢复（D2/D3）。这些是同一 BreakerStore 契约的其余能力族，按 §5.3 分阶段接入。

## D2. 入口准入 + admission control + runtime-control + 完整性 epoch（Redis 原型已完成，生产契约补强中）

在 `internal/platform/breakerstore` 扩展（新增 21 个真实 Redis 测试，全绿）：

- [x] **通用 runtime-control 状态机**（[runtimecontrol.go](../../internal/platform/breakerstore/runtimecontrol.go) / [runtimecontrol_lua.go](../../internal/platform/breakerstore/runtimecontrol_lua.go)）：`PrepareControl/CommitControl/AbortControl/ReadControl/RestoreMissingControl`，供全局 rate/concurrency 默认、Channel override、`gateway.circuit_breaker`/`gateway.routing_balance` 复用（§5.3.16 同一状态机）；幂等、pending 冲突、stale 校验、recovery-only restore 全测。
- [x] **完整性 epoch marker**（[integrity.go](../../internal/platform/breakerstore/integrity.go) / [integrity_lua.go](../../internal/platform/breakerstore/integrity_lua.go)）：`StateIntegrity` 只读、`BootstrapStateEpoch`、`PrepareStateEpoch`/`CommitStateEpoch` 轮换；ready/pending 语义与 epoch/revision 强一致比对。
- [x] **入口 RequestAdmission**（[requestadmission.go](../../internal/platform/breakerstore/requestadmission.go) / requestadmission_lua.go）：`AcquireRequestAdmission`（route-user RPM/RPD + concurrency 整请求一次、all-or-nothing、`0=不限`仍写桶）、`ReserveRequestTokens`（TPM 幂等预占）、`RenewRequestAdmission`、`FinishRequestAdmission`（释放并发/保留 RPM/RPD/对账或释放 TPM）；先校验完整性 epoch marker + 两个全局控制 active/pending/revision，fail-closed 返回 `runtime_state_lost|stale_integrity_epoch|runtime_sync_required|runtime_sync_pending|stale_setting_revision`，真实超限返回 `limited`（→429）。
- [x] **Channel 四维限额接入 AcquireAttempt**（§2.12.3）：`EnforceChannelAdmission` 开关下校验 channel admission control revision，按 Redis control 有效限额强制 RPM/RPD + 预占 TPM/并发，permit 固化原始桶；`Abort` 精确归还、`Finish` 保留 RPM/RPD 并按权威 usage 对账 TPM。旧调用（enforce=0）零行为变化。
- [x] **Redis Cluster 门禁**：`VerifySingleNodeDeployment`（检测 `cluster_enabled:1` 即拒绝，P4-D19）+ `Ping`。
- [x] **PostgreSQL 操作状态机查询**：[runtime_control_operations.sql](../../sql/queries/shared/runtime_control_operations.sql) 与 [endpoint_routing_operations.sql](../../sql/queries/shared/endpoint_routing_operations.sql)（create/CAS 各状态/非终态扫描/终态清理），Admin 与 Worker 共用生成的 sqlc。

**D2 归属 E/F 的部分（应用层，非 Redis 机器）**：runtime-control **durable publisher 编排服务**（PG operation + Redis control 的多步 Prepare/Commit/Recover）随 F2/Admin 落地（其 API 形状由 settings 更新入口决定）；删除 `platform/failure` 软冷却与 `settingsApplier` 本机 fail-open 推送、`app_settings` 四设置值形状 strict-decode 随 Phase E/F 重接线（当前删除会破坏仍在用的 lifecycle/settingsApplier 构建）。已记入 OPEN_DECISIONS。

## 进度与范围说明（重要）

本改造 = 计划 §15 完成定义所列的全量多仓工程（19 类原子 Lua + 完整性 epoch 恢复 + 三执行面生命周期重写 + Admin 后端/前端 + 真实上游 E2E），属团队级、需真实 Redis 迭代验证的多周工作，**无法在单次自动会话内完整、正确、通过全部验收地交付**。因此采用「每检查点保持可编译 + 可验证纵切」原则推进，并在自主决策处记录到 [ROUTING_P4_OPEN_DECISIONS.md](./ROUTING_P4_OPEN_DECISIONS.md)。

### 已交付并验证（现有测试全绿，但 D1–D3 尚不能视为生产完成）

- **Phase A** 全部。
- **Phase B** 全部：迁移重编号 + 3 张新表；channels/request_records/request_attempts/channel_test_logs/app_settings schema；全部 `base_url` 查询改 `provider_endpoints` join；ProviderEndpoint 端到端纵切（服务/DTO/adapter/channeltest/worker/tests）；空库 44 migration 重建成功；sqlc 无漂移。
- **Phase C1**：结构化 URL 拼接 helper + 4 官方 adapter 迁移（BaseURL=root，§4.6）。
- **Phase D1 原型**：Redis BreakerStore 已有双触发状态机、half-open 双探测恢复、退避、AttemptPermit Acquire/Renew/Finish/Abort、仅流式 TTFT EWMA、Channel 并发租约、Reset、Snapshot、Channel 429 全局冷却和 Endpoint 作用域；真实 Redis 测试全绿。生产接线前仍须补齐本节下方的 P0 契约。

### D2/D3 —— BreakerStore Redis 层 + PostgreSQL 查询层（原型完成、真实 Redis 现有测试绿）

复盘代码实际状态后更正：`internal/platform/breakerstore` 已实现并测试通过 D2/D3 的 Redis 与 sqlc 查询层（此前本日志低估了进度）：
- **入口 request-admission**（`requestadmission*.go`）：`AcquireRequestAdmission/ReserveRequestTokens/RenewRequestAdmission/FinishRequestAdmission`——route-user RPM/RPD/concurrency 整请求一次、TPM 一次性幂等预占、`0=不限`仍计数、first-terminal-wins。
- **admission control**（`runtimecontrol*.go`）：route rate / channel rate / concurrency / Channel override / setting 各类 `ControlTarget` 的 `PrepareControl/CommitControl/AbortControl/ReadControl/RestoreMissingControl`（pending fence、payload hash、`next=current+1`、recovery-only restore）。
- **完整性 epoch**（`integrity*.go`）：`StateIntegrity/PrepareStateEpoch/CommitStateEpoch/BootstrapStateEpoch`（marker 真值表、pending fence）。
- **Endpoint 围栏**（`endpointfence*.go`）：`InitEndpointControl/RestoreMissingEndpointControl` + BaseURL/status 的 `Prepare/Commit/Abort`。
- **Redis Cluster 门禁**：`VerifySingleNodeDeployment`（P4-D19，检测 cluster_enabled 拒绝就绪）。
- **PostgreSQL 查询层**：`sql/queries/shared/runtime_control_operations.sql`、`endpoint_routing_operations.sql`（sqlc 已生成 `preparing→prepared→db_committed→committed` 状态机 CRUD/CAS/扫描/清理）。

### D2 续 —— 应用层 runtime-control durable publisher/reconciler（关键 setting + Channel 启动纵切完成，后台循环仍待补）

新增 [internal/core/runtimecontrol](../../internal/core/runtimecontrol)：
- `Publisher.Publish`：完整可恢复发布状态机——PostgreSQL `preparing` → Redis `PrepareControl` → CAS `prepared` → 单事务{业务行 + `db_committed`} → Redis `CommitControl` → `committed`；prepare 被拒/基础设施故障时安全 Abort；Redis commit 响应丢失返回 `runtime_sync_pending`。同 token 重试会先核对 durable operation 的不可变字段并从已有 state 续接；`db_committed|committed` 不再重复执行 `BusinessCommit`。若 Redis 已 committed、但 PostgreSQL operation 仍在业务提交前，则判为跨存储分叉并保持隔离，禁止通过“再做一次业务 CAS”猜测结果。
- `Reconciler.ReconcileWithPayload`：`db_committed` 只接受 PostgreSQL next payload/hash，原子恢复/激活 next 后置 `committed`；`preparing|prepared` 使用 PostgreSQL current payload 恢复旧 active，并用 Redis `pending_payload` 校验待撤销 hash后置 `aborted`；跳过 `runtime_state_epoch`。新增 recovery-only `RecoverCommittedControl/RecoverAbortedControl` Lua，Redis op/control 单独丢失时也能按 durable DB state 收口，冲突 revision/pending 一律不覆盖。`CleanupTerminal` 有界清理终态。
- Admin 启动顺序已改为：先扫描并收口五个关键 setting 与全部 Channel admission 的非终态 operation，再执行 `RestoreCriticalRuntimeControls`，并从 PostgreSQL 全量恢复/严格核对每个 Channel admission control；revision、canonical payload 或 pending 任一无法对账时直接启动失败。共享 Publisher/BreakerStore 已注入 `channel.Service.WithRuntimeControl`。常驻后台 reconciler 尚待补齐。
- 测试（[publisher_test.go](../../internal/core/runtimecontrol/publisher_test.go)、[admission_test.go](../../internal/platform/breakerstore/admission_test.go)）：覆盖完整发布、commit 响应丢失后 reconciler、同 token 从 `db_committed` 重试不重复业务提交、Redis 提前 committed 的分叉拒绝、pending payload 可读、Redis op/control 丢失后的 committed/aborted 恢复。Redis/core/appsettings/bootstrap focused tests 全绿；DB 集成用例仍需 Phase H 空库 44 migration 环境执行。

### D2 续 —— Gateway PostgreSQL 运行态强一致读取（完成，热路径待接线）

- [x] `GetGatewayAdmissionControlRevisions`：单条 SQL statement snapshot 同时读取 `gateway.runtime_state_epoch` 与 route rate/channel rate/concurrency revision；任一必需行缺失返回 no rows，不回退进程缓存。
- [x] `GetGatewayRoutingControlRevisions`：单条 SQL statement snapshot 同时读取 epoch 与 circuit-breaker/routing-balance revision，避免多次查询组合出不一致版本。
- [x] 新增 `runtimecontrol.StateEpoch` 严格编解码：128-bit epoch，仅允许 `recovering|ready` 和 `bootstrap|state_loss|restore`，拒绝未知字段、非法 activation 组合与尾随 JSON。
- [x] 新增 `runtimefacts.Reader`：必需行缺失/非法 revision/非法 epoch 稳定分类为 `gateway_runtime_sync_required`，`state=recovering` 分类为 `gateway_runtime_state_lost`，PostgreSQL 读失败分类为 `dependency_postgres_unavailable`；三者都 fail-closed。
- [x] 新增维护专用 `SeedRuntimeStateEpoch` SQL 与幂等 `EnsureStateEpochSeed`：首次用 `crypto/rand` 生成 128-bit `bootstrap/recovering` 保留行，并发启动只读取并严格验证现有行；不加入普通 settings registry/API。它故意不创建 Redis ready marker，seed 后仍必须由 durable epoch operation + Redis Prepare/Commit 收口，本检查点未将完整 epoch coordinator 误标为完成。
- [x] 验证：`sqlc generate`；`go test ./internal/core/runtimecontrol ./internal/service/gateway/runtimefacts ./internal/platform/failure`。

### D3 续 —— 应用层 Endpoint 围栏 publisher（单项纵切完成，完整围栏族未完成）

[internal/core/runtimecontrol/endpoint_publisher.go](../../internal/core/runtimecontrol/endpoint_publisher.go)：`EndpointFencePublisher.Publish` 用通用引擎（prepare/commit/abort 闭包）编排 status 与 base_url 两类围栏——PostgreSQL `preparing` → Redis Prepare → `prepared` → 单事务{`provider_endpoints` 业务行 + `db_committed`} → Redis Commit → `committed`；prepare 拒绝安全 Abort，commit 丢失返回 pending。测试 1 例全绿（status enabled→disabled：Redis fence commit + DB status/status_revision +1 + op committed）。

#### 2026-07-22 生产契约复审结论（D1–D3 P0）

现有 Redis/Publisher 测试证明已实现的单项状态机可运行，但尚未覆盖计划要求的最终安全边界，故 D1–D3 不得标记 production complete，Phase E 也不得直接使用当前 API。接线前必须完成：

- `AcquireAttempt` 同时校验 request-admission token、完整性 epoch/marker、Endpoint fence 和全部当前 control；限额与 breaker 参数由 Redis active payload 原子解析，调用方不得传 effective limits 或 `Config` 绕过权威值。
- `Reserve/Renew/Finish/Abort` 按签发 token 固化的 epoch 和服务端 permit/resource token 收口；调用方只提供 permit/token 身份与业务结果，不能自行拼资源 key；所有 terminal disposition 必须显式返回。
- Lua 全部非法输入、key 类型和 revision/payload 校验先于任何计数、租约、TPM 或 breaker 写入，避免脚本错误留下部分资源变化。
- 补 `SnapshotMany`、Channel revision/Endpoint binding compare-and-rotate、跨 Channel/模型 Endpoint 证据、完整 epoch coordinator/reconciler、Endpoint control-create/combined/Provider batch fence 与 Redis operation tombstone。
- Gateway readiness 必须完成 PostgreSQL epoch、Redis marker、脚本、control 与 pending operation 对账；目前只有 Cluster/PING 启动检查。

本轮复验：`env -u LOG_FORMAT go test ./...` 通过；`go test -race -count=1 ./internal/platform/breakerstore` 通过。DB+Redis publisher 集成测试需先按 Phase H 用空库执行 44 个 migration 后复验，不能以当前未迁移的本地库失败或缓存结果宣称完成。

### F1 —— Admin ProviderEndpoint CRUD（已完成、端到端 HTTP 验证）

- service [internal/service/admin/providerendpoint](../../internal/service/admin/providerendpoint)：URL 规范化（scheme/host 小写、去默认端口/尾斜杠、path 保留；拒空/非 http(s)/userinfo/query/fragment，§4.2/§12.1）、Create（校验 Provider + 规范化 + 唯一冲突→409 + `InitEndpointControl`，Redis 缺失时标 `RuntimeSyncPending` fail-closed）、List/Get/UpdateName。
- HTTP [internal/app/adminapi/providerendpoint](../../internal/app/adminapi/providerendpoint) + router 挂载 `/admin/v1/provider-endpoints`；bootstrap 在 admin_server 构造（Redis 在则注入 `breakerstore.Store` 作 control）。
- 测试：service 单测（规范化 7 通过/7 拒绝、create、provider-not-found、control 失败→pending）；HTTP handler 集成测试（create 201 + 规范化 + 401 鉴权 + get 200）全绿。
- **BreakerStore 已首次接入 bootstrap（admin_server）**，作为 Endpoint control 初始化器。

### F1 续 + F2 部分（已完成、端到端 HTTP 验证）

- Endpoint **status/base_url 围栏热更新**：service `UpdateStatus/UpdateBaseURL` 经 `EndpointFencer`（组合 `EndpointFencePublisher` + `breakerstore.Store`）执行可恢复围栏；同值幂等不推进 revision；HTTP `POST /provider-endpoints/{id}/status`、`/base-url`；集成测试（enabled→disabled：Redis fence commit + DB `status_revision+1` + 幂等）通过。
- Endpoint **breaker reset + runtime 只读**（§8.4/§8.5）：`GET /provider-endpoints/{id}/ops/runtime`（`breakerstore.Snapshot`，客观事实 DTO，标 `ttft_sample_source=stream_only`）、`DELETE /provider-endpoints/{id}/ops/circuit-breaker`（`breakerstore.Reset` 推进 generation）；Redis 缺失时 **503**（`gateway_breaker_store_unavailable`/`dependency_redis_unavailable` → 503 映射已加）；HTTP 测试通过。
- bootstrap 注入 `breakerstore.Store` 作 Endpoint control / fencer / breaker-runtime。

### 测试回归修复（2026-07-24，用户真实 E2E 后）

用户完成 P4 全链路并做了限流/熔断/负载均衡真实测试，报了两个 bug，均已修复并验证：

1. **RPD 日计数 TTL 只有约 7.5 分钟（必修）**：`requestadmission_lua.go` 的 `AcquireRequestAdmission` 对 RPM 分钟桶与 RPD 日桶共用了 `bucket_ttl_ms = lease+terminal+120s ≈ 450s`。RPM（按分钟号分桶）长 TTL 无害，但 RPD（按 UTC 日号分桶）静默 7.5 分钟后过期清零、日限额失效。
   - 修复：新增 `rpd_bucket_ttl_ms = 86400000 + bucket_ttl_ms`，RPD 桶 `PEXPIRE` 改用它；RPM 桶不变。
   - 回归测试：`TestRequestAdmissionRPDBucketTTLCoversDay` 断言 Acquire 后 RPD 桶 `PTTL > 24h` 且 RPM 桶 `< 24h`。全绿。

2. **本地 `unio` 开发库 schema 漂移（`requestlog_store_failed`）**：源 migration `000044` 的 `ck_runtime_control_operations_target` 已把旧 `gateway.rate_limit_defaults` 拆为 `gateway.route_rate_limit_defaults` + `gateway.channel_rate_limit_defaults`（并与 registry/FK 一致）；但线上 `unio` 库因 squash 迁移创建于拆分前，CHECK 仍是旧单键，导致发布路线/渠道默认限流时 `runtime_control_operations` INSERT 违反 CHECK → `requestlog_store_failed`。
   - 排查：确认 `state_check`/epoch 函数/evidence 列均已是当前值，**唯一漂移是该 target CHECK**；`app_settings` 已含新拆分键（启动 `SeedDefaults` 建立，FK 满足）；库内有真实测试数据（6 channels/2 providers/3 endpoints/1 route）。
   - 非破坏性同步（保留全部业务数据）：事务内删除 1 条指向已废弃键的历史 op 残留 + `DROP/ADD` target CHECK 对齐源；验证新键 `gateway.route_rate_limit_defaults` INSERT 通过 CHECK（rollback）。
   - 源正确性：空库从当前 44 个 migration 重建 `unio_p4test`，target CHECK 直接为新拆分键，`go test ./...` 全绿——证明漂移仅为既有开发库未重建，源无需改动（P4-D18 可重建开发库）。

#### 2026-07-24 续：运行实例实机复验 + H8 处置决定

- 对**运行中的实例**（gateway `:8521` / admin `:8522`，库 `unio`，Redis `:6380` ns `unio:dev`；均为用户重启后的 debug 二进制）实机复验两处修复：
  - Bug2：`PUT /admin/v1/settings/gateway.route_rate_limit_defaults`（及 `channel` 版）均 **HTTP 200**（`state=active`，rev 1→2），即之前 `requestlog_store_failed` 的 durable-publish 路径已通；测试后两键还原 `{0,0,0}`。直查线上 `unio` 库确认 `ck_runtime_control_operations_target` 现含两拆分键。
  - Bug1：运行 gateway 二进制构建时间晚于 RPD 修复，故已编译进修正 Lua；`TestRequestAdmissionRPDBucketTTLCoversDay` 针对同一 live Redis 通过（RPD 桶 `PTTL>24h`、RPM 桶 `<24h`）。
  - 顺带清理死漂移：删除 `app_settings` 遗留的废弃行 `gateway.rate_limit_defaults`（0 引用、仅存在于文档、已被 DEC-054 退役），使开发库与空库全新重建一致；Redis 无对应陈旧缓存。
- **处置决定（用户 2026-07-24）**：P4 实现与核心验收视为完成；H8 剩余发布门禁（state-loss 变体、透明代理长流剩余矩阵、≥2 真实 Endpoint 容灾、§12.7 吞吐/Lua P95-P99/Redis 内存·发布回滚演练、Admin 连 live Gateway 完整浏览器矩阵）**按需再约**，本次不执行。当前保持 `go build ./...` 绿；工作区仅含 RPD 修复相关三文件（`requestadmission_lua.go`、`admission_test.go`、本日志）。H8-3 仍硬阻塞于「用户仅提供一个真实上游 BaseURL」。

### E 基础（已完成、绿）

- Gateway 启动期接入 BreakerStore 就绪门禁（§5.5/§P4-D19）：`VerifySingleNodeDeployment`（检测 Redis Cluster 即拒绝启动，多 key 原子 Lua 不拆步降级）+ `Ping`（不可达即启动失败，fail-closed）。仅 Redis 存在时执行；bootstrap 测试通过。
- 热路径 permit 接线（替换进程内 breaker/guard/concurrency 为 AcquireAttempt/Finish/Abort + request-admission）属 E 主体，见下方「未完成」。

### F2 检查点：删除主观健康分桶（完成、绿）

- 删除 Admin Dashboard、Channel Ops、Provider Ops 中的 `HealthBucket`、`health`、`health_bucket` 与健康阈值读取；对应 service/bootstrap 不再依赖 `SettingsStore`。
- Dashboard 保留成功率、失败数等客观事实；失败渠道排行新增 `attempt_failed`，只统计真实上游失败并按该值排序，不把平台自身错误算作渠道健康失败。
- 删除 Dashboard 里的主观渠道操作建议，避免用人为阈值给 Channel 打“健康/不健康”标签。
- 更新 `sql/queries/admin/overview.sql` 并重新执行 `sqlc generate`。
- 验证：`env -u LOG_FORMAT go test ./...`、`git diff --check` 均通过。直接执行 `go test ./...` 会受本机环境变量 `LOG_FORMAT=json` 污染，`TestLoadLogLevelDebug` 期望默认 `console`；这不是 P4 回归。

### F2 检查点：Channel 四维限额 durable publisher（service/SQL 完成，bootstrap 接线并行收口）

- [x] Channel 创建把 RPM/TPM/RPD/concurrency 与业务行一次 INSERT，初始 `admission_limits_revision=1`；随后用 recovery-only `RestoreMissingControl` 安装并严格读取核对 revision=1 control。Redis control 未确认时返回 `runtime_sync_pending=true`，执行面继续 fail-closed。
- [x] 导出唯一 canonical helper `CanonicalAdmissionLimitsPayloadFromChannel`，固定 payload 为 `{"rpm":int|null,"rpd":int|null,"tpm":int|null,"concurrency":int|null}`；字段始终存在，`null=继承` 与 `0=不限` 不混淆。启动 reconciler 与 Admin 发布共用该 helper，不各自猜 schema。
- [x] Channel 更新先比较完整 canonical payload；四维语义同值时不创建 operation、不推进 revision。真变化时强制经 `runtimecontrol.Publisher` 执行 `preparing -> prepared -> {业务 CAS + db_committed 同事务} -> committed`；publisher/Redis 不可用时普通 Update 不得直接覆盖限额。
- [x] 删除无 revision 的 `SetChannelRateLimits` 查询和 service Store 能力；替换为仅供 Publisher `BusinessCommit` 使用的 `CommitChannelAdmissionLimitsAtRevision`，同时校验 `current_revision`、`next=current+1` 和四维真变化，避免同值或并发写重复推进。
- [x] Redis commit 未确认时保留 PostgreSQL 已提交的 next revision/四维值并返回 `runtime_sync_pending`；事务 callback 直接保留已提交 Channel 行，避免客户 context 在提交后取消导致补查失败而误报未保存。Channel Create/Update/Get/List DTO 均返回一致的 `runtime_sync_pending`。
- [x] 验证：`sqlc generate`；Channel service、sqlc、Admin API、bootstrap focused tests全绿；另建隔离临时 PostgreSQL，空库成功执行 44 个 migration 后真实运行 `TestChannelCRUDQueries`，验证限额真变化只 `+1`、同值 CAS 返回 no rows，随后已删除临时库；focused `git diff --check` 通过。
- [x] Admin 启动共享 Publisher/BreakerStore 已注入 `channel.Service.WithRuntimeControl`；启动 reconciler 的 target/payload resolver 同时支持五个关键 setting 与 Channel operation，随后只补 absent control 并逐 Channel 严格核对 active revision/canonical payload/pending。focused bootstrap/channel/runtimecontrol/breakerstore tests 全绿。

### F2/G 后端检查点：全局 runtime DTO 与旧 health fan-out 收口（完成，绿）

- [x] ProviderEndpoint list/get 返回 `active|runtime_sync_pending|runtime_sync_required|stale|store_unavailable`，并带 Redis active/pending BaseURL/status revisions；Redis 故障时仍保留 PostgreSQL 业务行，不伪造 active。
- [x] Channel runtime/reset 同时展示 PostgreSQL Endpoint/config/admission revisions 与 Redis binding/control 事实；stale/pending/required 时不暴露旧 breaker/TTFT，缺 breaker state 则保留 `active + exists=false` 客观语义。
- [x] `/channels/ops` 删除 `health/health_score`、Gateway 实例 fan-out 与旧 `BreakerClient` 接线；旧 client 仅保留 Route runtime 仍在使用的边界。
- [x] `ScopeSnapshot` 保留 Endpoint pending revisions；TTFT 样本来源固定标记为 `stream_only`。
- [x] 验证：Admin API、breakerstore、channel、bootstrap focused tests，相关包 race tests 与 `git diff --check` 全绿。

### E1 检查点：非流式 AttemptPermit 执行边界（代码路径完成，生产接线未完成）

- [x] `RunNonStream` 已按候选执行 `ResolveAdapter -> AcquireAttempt -> CreateAttempt -> Invoke`；业务 denied 不创建 attempt、不进入 transport，并可继续下一候选；Store/运行态读取错误立即终止整次 fallback。
- [x] permit 成功后 attempt 创建失败或 transport 未开始统一 `Abort`；transport 已开始（含错误和 panic）统一 `Finish`，终态前停止并等待 permit renewer 退出。`Finish/Abort` 使用脱离客户取消的有界 context，同一终态最多重试两次。
- [x] 非流式 `FirstTokenMs` 固定为 nil；成功写 Endpoint/Channel `eligible_success`，4xx/取消/平台本地错误 ignored，Channel 5xx/timeout/协议失败为 `eligible_failure`。Endpoint 失败暂时保守 ignored，等待跨 Channel/模型 ambiguous evidence 收集器后再启用。
- [x] `request_attempts` 新增 Endpoint/Channel breaker disposition first-write-wins 审计；`Finish` 两次仍无法确认时记录双 `result_unknown`，不改写客户业务结果。
- [x] `BreakerStore.AcquireAttempt` 新增 integrity epoch/revision 与已 Reserve request-admission token 校验；`Finish` 不再接受调用方 breaker config/TTFT alpha，breaker 与流式 TTFT alpha 只读取 Redis committed control。测试基线已补 ready integrity marker、reserved request token 与 routing-balance control。
- [x] focused 验证：`REDIS_ADDR=127.0.0.1:6380 env -u LOG_FORMAT go test ./internal/platform/breakerstore`、`env -u LOG_FORMAT go test ./internal/core/requestlog ./internal/service/gateway/lifecycle`、`env -u LOG_FORMAT go test -race ./internal/service/gateway/lifecycle`、focused `git diff --check` 均通过。

本检查点**尚未激活生产流量**：三协议 service/bootstrap 仍未注入共享 `AttemptPermitManager`，非流式候选准备仍需按 stream/non-stream 拆除旧本机 breaker/软冷却/容量评分。另有一个 P0 契约缺口：既有 permit 的 `Renew/Finish/Abort` 还未在每次操作前强读 PostgreSQL epoch，也未在对应 Lua 中校验 Redis integrity marker；完成前不得宣称完整性 epoch 端到端完成。流式 runner 与 Responses Compact 双 permit 本检查点未改。

### 尚未完成（真实剩余工作，需专门推进）

- **F2 剩余**：常驻 runtime-control reconciler、Provider 批量 Endpoint fence、前端删除残留主观健康展示、流式 TTFT/总耗时查询。
- **Phase E/E2（money path，最大）**：把 request-admission + AttemptPermit + BreakerStore 接进 gateway 热路径（三执行面），删进程内 breaker/guard/软冷却/fail-open，permit renewer、终态矩阵、BreakerStore timing 消费/delivery finalizer、Compact 双 permit、balanced 三因子、安全外部错误聚合、readiness/epoch 门禁、trace/metrics/logs。
- **app_settings 四设置值形状 + seed epoch**（与 E 的热路径消费耦合）。
- **G（unio-admin 前端）**、**H（真实 StarAPI/Codex/Claude E2E + Playwright）**。
- **app_settings 四设置值形状 + seed epoch + 删健康阈值**（D-D）。
- **删 `platform/failure` 软冷却 + settingsApplier 本机 fail-open 推送**（与 E 一起）。
- **Phase C2 剩余**：BreakerStore `Finish` 消费流式 FirstTokenMs，以及逐帧 SSE write-ack/delivery finalizer（与 E 强耦合）；observer、三协议元数据、requestlog timing 与非流式 nil TTFT 已接入热路径。
- **Phase E/E2**：Gateway 生命周期重写——删进程内 breaker、接线 request-admission wrapper + AttemptPermit/renewer + 终态矩阵 + Compact 双 permit + balanced 三因子 + 安全外部错误聚合 + readiness/epoch 门禁 + trace/metrics/logs。这是最大、风险最高（资金/结算路径）的阶段。
- **Phase F1/F2**：Admin 后端（ProviderEndpoint CRUD + fence、RotateCredentialAndTest 五态、runtime-control 发布、runtime API 改读 Redis、删主观健康、TTFT/总耗时查询）。
- **Phase G**：unio-admin 前端。
- **Phase H**：两 Gateway / 真实 StarAPI E2E / Codex·Claude CLI / Playwright 全量验收。

---

## H. 生产接线与验收收口（2026-07-23，部分完成）

> 本节是当前最新状态，覆盖并替代上方“E1 生产接线未完成”和“尚未完成”这两个历史快照。
> 历史记录保留用于说明实施顺序，不再代表当前剩余工作。

### H1. Gateway money path 与安全边界（完成）

- [x] 三个生成执行面已注入共享 request-admission、`AttemptPermitManager` 与 Redis BreakerStore：OpenAI Chat、OpenAI Responses/Compact、Anthropic Messages 的流式/非流式均走新生命周期；旧进程内 breaker、候选 guard/concurrency/fail-open 生产接线已移除。
- [x] request-admission `Renew/Finalize` 每次只强读 PostgreSQL integrity epoch；Redis Lua 同时校验签发 epoch/marker/control，运行态不可信时 fail-closed。
- [x] attempt permit 在 transport 前 `Abort`、transport 后 `Finish`；终态使用同一 token 最多重试两次，仍无法确认只记录 `result_unknown`，不改写客户业务结果或账务事实。
- [x] permit 取得后、attempt 创建前发生 panic 时也会停止 renewer 并释放资源；非流、流式和 Compact 第二张 permit 均有回归测试。
- [x] Gateway 缺少 Redis/BreakerStore 时启动直接失败，不再以空组件静默放行。
- [x] 共享上游 HTTP client 禁止自动跟随 307/308，避免 POST 和计费请求被透明重放。
- [x] `cmd/runtime-state-maintenance` 已提供 `begin/commit/release` 命令、显式人工确认、绑定 recovery identity/revision 与有界 evidence 文件校验；状态机单元/数据库集成测试已通过，完整 `FLUSHDB -> begin -> commit -> 六协议 smoke -> release` 隔离 E2E 也已通过。更广的 state-loss 发布门禁见 H8，仍未全部完成。

### H2. 隔离双 Gateway 故障 E2E（已完成当前套件，6/6）

新增 `internal/blackbox/p4fault`，每次自建并清理随机 PostgreSQL/Redis 容器、volume、两个真实 Gateway 和可计数 mock upstream，不读取开发 `.env`。2026-07-23 实跑约 27 秒，以下六组全部通过：

1. marker 与 Redis operation 丢失后由 immutable PostgreSQL transition 重建同一 pending fence，完成 smoke/release，冲突 marker 不被覆盖；
2. OpenAI Chat、OpenAI Responses、Anthropic Messages 的流式/非流式六种入口基线；
3. 删除一个关键 control 后，两个 Gateway 共享拒绝新请求、零上游调用，并由 reconciler 自动修复；
4. Redis stop/restart 保留数据时 fail-closed，epoch/marker/control 对账后恢复；
5. Gateway A 触发全局 breaker 后，Gateway B 下一请求立即拒绝；
6. 未执行 maintenance 的 `FLUSHDB` 即使 control 可重建也持续 not-ready，所有新请求零上游调用。

2026-07-23 显式复验标准 6/6 用时 26.95 秒。完整 `FLUSHDB -> begin -> commit -> 六协议 smoke -> release` 和后续长流、Prepare crash、AOF/RDB 回档均是单独 opt-in 用例，不计入上述编号，见 H7。标准 6/6 本身不能据此宣称计划第 12.6 节 50 个场景全部完成；24h RPD/permission 实际时间门禁、active-owner 完整恢复闭环及其它 H8 场景仍未完成。Redis Cluster/CROSSSLOT 由独立 `internal/blackbox/p4cluster` 套件覆盖，见 H7。

### H3. 真实 StarAPI 与 CLI（基础 smoke 8/8 完成）

隔离方式：新建空 PostgreSQL 数据库并顺序应用 44 个 migration，使用唯一 Redis namespace；渠道密钥仅从用户附件读取到进程环境，未写入仓库、文档或测试输出。真实上游为 `https://open.codex521.cc`：

- [x] OpenAI Chat 非流式，`gpt-5.4-mini`；
- [x] OpenAI Chat 流式，`gpt-5.4-mini`；
- [x] OpenAI Responses 非流式，`gpt-5.4-mini`；
- [x] OpenAI Responses 流式，`gpt-5.4-mini`；
- [x] Anthropic Messages 非流式，`claude-sonnet-4-6`；
- [x] Anthropic Messages 流式，`claude-sonnet-4-6`；
- [x] Codex CLI，经 Gateway Responses 流式返回确定性结果；
- [x] Claude Code `2.1.104`，经 Gateway Anthropic Messages 流式返回确定性结果。

最终一次性运行 8/8 全绿，约 42 秒。共享 facts 断言覆盖 request/attempt 成功、上游 transport 时间边界、流式 TTFT 非空、非流式 TTFT 为 NULL、usage、ledger debit、price/cost snapshot、routing trace 与 Admin request query。

真实上游预跑曾出现一次公开 502 和一次明确 429，随后单项、六模式与最终 8/8 均通过；直连同模型也可立即返回合法 SSE。由于没有稳定复现的 adapter/lifecycle 缺陷，本轮不基于瞬时上游波动修改生产流链路。为保留下一次失败证据，blackbox fixture 新增清理前脱敏诊断，只输出稳定 `error_code`、上游状态码、失败阶段、transport/FirstToken 边界、fault party 与 breaker disposition；不输出密钥、正文、上游错误正文或 `internal_error_detail`。

Claude CLI 首次卡住 3 分钟的根因是 `--bare` 仍读取个人 settings 的 `env`，旧 BaseURL 覆盖 fixture 地址，Gateway 实际收到零请求。夹具现为每次运行分配独立 `CLAUDE_CONFIG_DIR` 并设置 `CLAUDE_CODE_DISABLE_NONESSENTIAL_TRAFFIC=1`；隔离回归测试和真实 CLI 均通过。

### H4. 后端最终回归（完成）

- [x] `go test -count=1 ./...`；
- [x] `go test -race -count=1 ./...`，全仓通过且无 data race；
- [x] `go vet ./...`；
- [x] `go build ./cmd/...`；
- [x] `sqlc generate`，无生成漂移；
- [x] `git diff --check`；
- [x] 空库 44 migration 重建；
- [x] P4 fault E2E 6/6；
- [x] opt-in 完整 FLUSHDB maintenance E2E；
- [x] StarAPI/CLI blackbox 8/8。

2026-07-23 在新增 Compact 部署态、在途长流、Prepare crash 与真实 AOF 回档后再次执行回归：`env -u LOG_FORMAT go test -count=1 ./...`、`env -u LOG_FORMAT go test -race -count=1 ./...`、`go vet ./...`、`go build ./cmd/...` 全部通过；`sqlc generate` 前后生成差异哈希一致，`git diff --check` 通过。当时已存在的 p4fault gate 包级实跑为 96.977 秒；该数字已被 H7 的最新全量复验替代。独立 Cluster 套件当时 3/3 为 3.150 秒。测试后 `unio-p4-fault-*` / `unio-p4-cluster-*` 容器和 volume 均为零残留。

### H5. Admin 最终回归（当前自动化门禁完成）

- [x] lint：0 错误；
- [x] production build：3927 modules；
- [x] Vitest：11 files / 26 tests 全部通过；
- [x] P4 Playwright：3/3，通过 ProviderEndpoint breaker + stream-only TTFT、基础设施故障 denied admission，以及 Provider 列表零/单/多 Endpoint 展示、多项点击展开、归档 Provider 无创建入口与“更多 → 新建端点 → 当前行刷新”；
- [x] 完整 Playwright：3 passed / 1 skipped。唯一跳过项是旧 P3 live 用例，因未提供 `E2E_ADMIN_TOKEN` 与 `E2E_ROUTE_ID` 按既定条件跳过；
- [x] P4-D20：Provider Ops 分页主查询一次聚合 Endpoint 业务摘要；前端增加“端点”列和非归档 Provider 行级“新建端点”入口，可见文案不暴露技术类型名；旧后端暂缺 `endpoints` 字段时降级为空列表而不白屏。桌面与 768px 窄屏截图检查无重叠，窄屏由表格容器横向滚动；
- [x] P4-D20 后端新增 SQL/service/DTO 映射测试，`env -u LOG_FORMAT go test ./...`、`sqlc generate` 与 focused `git diff --check` 通过；
- [x] `git diff --check`。

### H6. 临时资源清理（完成）

- [x] 本轮创建的 `unio_p4_starapi_diag_20260722`、`unio_p4_starapi_final_20260722`、`unio_p4_fullrace_20260722` 已删除；
- [x] 对应 `unio:blackbox:starapi-*` 与 `unio:test:p4-fullrace-20260722` Redis namespace 已清空并复核为 0 key；
- [x] 渠道密钥未落盘到仓库、实施日志或测试日志。

### H7. 2026-07-23 新增聚焦验收（完成）

- [x] 独立 opt-in 完整 `FLUSHDB` maintenance E2E 于 2026-07-23 显式复验用时 15.87 秒。首次运行暴露 post-commit smoke 与 infrastructure fault latch 互锁；现由 PostgreSQL readiness snapshot 提供精确的 `runtime_maintenance_smoke_allowed`，只允许绑定当前 ready epoch 的唯一 `awaiting_release` operation 在后台严格对账后清除 latch 并执行内部 smoke。对外 `/readyz` 在 release 前仍保持 `503/runtime_operation_pending`。
- [x] Redis Cluster 套件 3/3 通过：BreakerStore 明确拒绝 Cluster；Gateway bootstrap 在访问 PostgreSQL 或上游前失败；跨 slot 多 key Lua 返回 CROSSSLOT 且两个 key 均无部分写入。真实产品语义是“启动拒绝、没有 Handler”，不是“启动成功后返回 503”。
- [x] 长流聚焦测试覆盖 transport 跨多个 permit TTL 持续续租、Finish 前停止 renewer；真实 Redis status fence 测试覆盖 pending 后旧 permit 仍续租和释放 Channel concurrency、新 permit 零资源 fail-closed、旧 Finish 不污染新运行态。新增完整隔离部署 E2E 进一步证明客户收到 SSE 首帧后 Redis 停止超过一个 renew 周期时既有流不中断、新请求 503 且零上游；数据保留恢复后同一 permit 继续续租，usage、账务、delivery 与两级 concurrency 完整收口，实跑用时 26.84 秒。
- [x] Compact Native 404/405 -> Synthetic 完整隔离部署 E2E 使用真实 Gateway + PostgreSQL + Redis + 本地 HTTP fault upstream，两个分支均证明同一入口请求只有一张 request token，但产生两张 permit、两条连续 attempt 和两次真实 transport；入口计数一次、Channel 计数两次，原生 404/405 breaker ignored，最终 usage、单次 debit、snapshot 与 settlement recovery audit 一致。该项不等于外部 Provider/StarAPI Compact 验收。
- [x] maintenance Prepare crash E2E 在 Redis pending 已建立、PostgreSQL operation 仍为 `preparing` 的精确窗口 kill 进程并终止后端事务；同一 immutable operation 在 marker/Redis operation 丢失后恢复到 `db_committed`，不 Abort、不换 epoch，随后完成六协议 smoke/release，已连续实跑通过。
- [x] 五类 marker 真值表、非 state-loss smoke 失败后同 epoch 重跑，以及再次确认 state loss 后 revision+1 新 operation 已由完整 FLUSHDB maintenance E2E 实跑覆盖。
- [x] 真实 Redis 7 AOF 文件回档 E2E 已通过，wall time 15.02 秒：旧 ready epoch 的 manifest/AOF segment 从隔离 volume 归档；PostgreSQL durable state 进入 recovering 后清空 Redis volume 并恢复旧文件，旧 marker/control 确实回归，但两 Gateway 仍 503 且六模式零上游；同一 operation 重建 pending 后完成 commit、六协议 smoke 和 release。active-owner AOF 的 recovering 安全边界见下一项，真实排空后的完整闭环仍属 H8。
- [x] 真实 Redis 7 RDB 独立文件回档 E2E 已通过：`SAVE`、`redis-check-rdb`、checksum、清空原 volume、只恢复 `dump.rdb` 并用 RDB-only Redis 启动；旧 marker/control 回归后仍被新 recovering epoch 隔离，随后同一 operation 完成 commit、六协议 smoke 和 release。最终合并复验用时 12.54 秒。
- [x] active-owner AOF 旧 epoch 回档安全边界已通过：旧流首帧后归档 active request token/permit，PostgreSQL 进入新 recovering epoch 后回档旧 ready AOF；新 Acquire、旧 Renew/Finish 均为 `runtime_state_lost`，目标 owner/control/counter/concurrency 状态零污染，客户流、usage、debit 与 delivery 正常收口。测试刻意停在 recovering，不伪造排空证据或执行 commit/release；最终合并复验用时 19.86 秒。
- [x] BaseURL 与 credential revision 长流围栏已通过：新请求只使用新地址/新凭据，两条旧流均完成客户交付和账务；BaseURL 旧 Finish 对两级均为 `stale_revision`，credential 旧 Finish 的 Endpoint success 合法应用，但当前 Channel breaker、TTFT、credential/config revision 不变；最终合并复验用时 6.31 秒。
- [x] 场景 26/29 的 Channel half-open 部署态已通过：permit、request token、Channel/route-user concurrency 的逻辑 lease、ZSET score 和物理 TTL 跨多个短 TTL 刷新；持有 probe 的 Gateway 被 `SIGKILL` 后旧 owner 到期，另一 Gateway 用两张不同 permit 完成恢复；最终合并复验用时 10.21 秒。
- [x] 场景 27 已通过：旧流在途时 Reset，当前 Channel generation 明确经历 `closed -> open -> half-open -> closed` 且使用两张不同 permit；旧流随后断尾并以双 `stale_generation` 收口，当前 Endpoint/Channel breaker 语义快照不变，partial usage、reservation capture、debit、余额、request token 和两级 concurrency 全部闭合；最终合并复验用时 7.13 秒。
- [x] 2026-07-23 最终一次性开启全部现有 p4fault gate，包级实跑 151.736 秒全部通过；独立 Cluster 3/3 包级 6.359 秒。全仓 normal/race、vet、command build 与 `sqlc generate` 一致性通过；附件中识别出的敏感候选在仓库内零匹配。

### H8. 仍未完成的发布门禁（阻止 Phase H/P4 标记完成）

1. state-loss 剩余部署验收：24h RPD/permission 实际时间门禁、active-owner 真实排空后的 commit/smoke/release、active-owner RDB 回档变体，以及未申报同 epoch 回档的部署权限拒绝；AOF/RDB 独立文件回档、active-owner AOF recovering 安全边界、Prepare crash、五类 marker、同 epoch smoke 重试和 revision+1 已由 H7 覆盖；
2. 透明代理长流剩余矩阵：旧 credential 返回 401 后的迟到回写、Endpoint half-open 对应部署态，以及计划第 12.6 节其它未执行的长流/并发组合；Reset 新 half-open generation、BaseURL revision、有效 credential/config revision 成功迟到 Finish、Channel half-open Gateway kill/TTL 回收、status pending、普通续租和数据保留型 Redis 故障恢复已由 H7 覆盖；
3. 至少两个独立真实 Endpoint/BaseURL 的容灾验收；当前用户只提供一个 StarAPI BaseURL，不能宣称真实 Endpoint 级容灾。外部 Provider/StarAPI Compact 透明代理矩阵已由 H13 覆盖；
4. Admin 连接 live Gateway 的完整浏览器矩阵，以及计划第 12.7 节吞吐、Lua P95/P99、Redis 内存/续租写放大、Admin 轮询热 key 与发布/回滚演练；
5. 计划第 12.6 节其它未执行场景。真实 8/8 smoke 证明基础协议主链路，不等于 50 场景全量验收；真实 smoke 的共享 facts 尚未直接断言 non-stream delivery/`response_started_at`，这两项当前由 handler/lifecycle 单元与集成测试覆盖。

### H9. 限流代码默认调整（完成，key 拆分由 H10 修订）

- [x] 按 DEC-053 将默认限流从 `60/0/0` 改为 `0/0/0`，三维均不限；该检查点当时仍使用共享 key，H10/DEC-054 已将其替换为线路与渠道两套设置。
- [x] P4 计划、限流设计文档和默认值单测同步更新；不限期间继续执行 DEC-044 的稳定窗口用量记录。
- [x] 当前开发运行态通过 Admin durable runtime-control publisher 发布 `{"rpm":0,"tpm":0,"rpd":0}`；H10 已改为分别发布并核验两套 revision/control。

### H10. 线路默认限流与渠道默认限流拆分（完成）

- [x] 按 DEC-054 删除共享注册项 `gateway.rate_limit_defaults`，新增
  `gateway.route_rate_limit_defaults` 与 `gateway.channel_rate_limit_defaults`；两套默认均为
  `{"rpm":0,"tpm":0,"rpd":0}`，严格解码、独立 revision、独立 durable publisher/reconciler，系统设置可热更新。
- [x] Redis control 拆为 `admission:v1:route-rate-limits` 与 `admission:v1:channel-rate-limits`；
  `gateway.concurrency_defaults` 保持一套，入口继续使用 `key_limit`，候选继续使用 `channel_limit`。
- [x] request admission 只读取线路 rate revision；request token 固化 `route_rate_limits_revision`。
  `SnapshotMany/AcquireAttempt` 只读取渠道 rate revision；AttemptPermit 固化
  `channel_rate_limits_revision`。线路命中返回 429 且不创建 AttemptPermit；渠道命中只拒绝当前候选并继续 fallback。
- [x] PostgreSQL admission/readiness snapshot、runtime facts、readiness、fault proof/clear、运行态 API 与 routing trace
  全部扩展为两套 rate revision。readiness 当前校验五个关键 control；fault proof 返回两套 rate payload/hash，
  Redis/BreakerStore 或 PostgreSQL 不可信时继续 fail-closed。
- [x] Admin 系统设置新增“线路默认限流”和“渠道默认限流”两张独立卡；路由运行态显示
  “默认限流 线路 rX · 渠道 rY”和“控制 并发 rX · 熔断 rX · 均衡 rX”。TypeScript DTO、组件测试与
  Playwright fixture 已同步，Admin 旧 key/旧 revision 字段搜索为 0。线路/渠道表单与列表摘要也明确显示
  “继承线路默认限流”或“继承渠道默认限流”，不再使用含糊的“继承全局默认”；并发默认语义保持不变。
- [x] 补齐两类直接回归：Channel 显式 `rpm=0` 可覆盖非零渠道默认，连续两次 `AcquireAttempt` 均放行且
  `SnapshotMany` 返回 `used=2, limit=0`；共享 lifecycle 覆盖 Chat/Responses/Messages × 流式/非流式，
  当前候选 `rate_limited` 时不创建 attempt、不调用其 transport，并继续下一候选。
- [x] 后端聚焦验证通过：breakerstore/readiness 真实 Redis 测试，appsettings/runtimecontrol/bootstrap/sqlc、
  Gateway/blackbox 全包测试；隔离临时 PostgreSQL 完成 44 migration 的 up/down 后已删除；Admin 全量
  Vitest 29/29、ESLint、生产 build、P4 Playwright 3/3 与 `git diff --check` 通过。需要
  `P4_FAULT_E2E=1` 的完整容器故障演练仍属于 H8 发布门禁，本检查点未执行。
- [x] `DECISIONS.md` 新增 DEC-054，P4 计划同步 P4-D22；DEC-043 的共享 rate control 表述被修订，
  DEC-053 的两套默认均为 `0/0/0` 结论继续有效。

### H11. Balanced 受限成本因子与过期错误窗口修复（代码完成，开发运行态已激活）

- [x] 按 DEC-055 / P4-D23 为 `balanced` 增加真实成本因子。成本使用已通过毛利门禁的
  `ChannelCost` 与本线路客户售价，取七个归一化计价分项中最大的 `channel_cost / customer_sale`；
  `final_weight = capacity_score * routing_factor * cost_factor`，只在已经通过 Endpoint/Channel breaker、
  revision、权限、限流、并发和负毛利等硬门禁的候选间调整概率。
- [x] `gateway.routing_balance` 增加 `cost_weight`，新环境代码默认值为 `0.5`，系统设置支持 durable
  runtime-control 热更新。旧四字段 payload 严格按 `cost_weight=0` 兼容，避免只升级二进制就无 revision
  改变流量；`fixed` 不参与成本排序，已有 sticky 不主动迁移。
- [x] `SnapshotMany` 修复过期 eligible 错误窗口仍长期参与评分的问题：超过当前 breaker window 后，本次
  只读评分把错误样本归零并恢复中性错误因子，但保留 stream-only TTFT EWMA，不推进或重置 breaker 状态机。
- [x] routing trace 算法版本升级为 `balanced_v3_cost`，保存 `cost_ratio/cost_weight/cost_factor/final_weight`；
  实际尝试链改为由 transport 调用方逐次记录，准入阶段跳过的候选不再伪装成已调用，Compact 同渠道的
  Native/Synthetic 两次 transport 按真实 `upstream_operation` 分别记录。
- [x] Admin 线路实时运行态展示成本占比、成本权重、成本系数和最终权重；系统设置新增“成本权重”控件。
  价格币种、计价单位或数值不可解析时显示“价格配置无效”并摘除候选；价格事实改为批量查询，避免按渠道 N+1。
  新前端连接尚未升级、缺少 `cost_*` 字段的旧 Admin API 时使用中性显示，不白屏。
- [x] Redis Lua 对 routing-balance 的四个浮点输入显式拒绝 `NaN`；后端覆盖旧 payload 中性、新 payload 严格
  解码、成本公式、绝对价格/倍率成本、硬门禁、fixed/sticky、过期窗口与 TTFT 保留、价格配置无效和真实
  transport 链。最终复验 `go test -count=1 ./...`、`go vet ./...`、`go build ./cmd/...`、`sqlc generate`
  全部通过；Admin Vitest 13 files / 34 tests、ESLint、production build（3927 modules）全部通过。
- [x] 浏览器完成桌面及 390px 窄屏检查：系统设置控件无文档级横向溢出，线路运行态两张宽表使用各自
  `overflow-x:auto` 容器（当前实测 345px 容器承载 1739px / 706px 表格），页面文字和操作区无重叠。
- [x] 当前开发 Gateway/Admin/Worker 已使用 2026-07-23 19:31 新 debug 二进制运行；`/healthz`、Gateway
  `/readyz` 均通过。Admin durable publisher 已保存五字段 `gateway.routing_balance`，revision 从 `1`
  推进到 `2`，Redis active revision 同步为 `2`，`cost_weight=0.5` 已在当前开发流量生效。线路
  `VIP-Codex` / `gpt-5.6-sol` 实时运行态显示四个候选均 eligible 且 `routing_balance_revision=2`：
  `openai_0.06` 的 `cost_ratio=0.3,cost_factor=0.85,final_weight=0.85`，
  `openai_0.09` 的 `cost_ratio=0.45,cost_factor=0.775,final_weight≈0.623`，
  `openai_0.085` 的 `cost_ratio=0.425,cost_factor=0.7875,final_weight=0.7875`，
  `openai_0.16` 的 `cost_ratio=0.8,cost_factor=0.6,final_weight≈0.469`。已有 sticky 不清除，
  默认 1 小时绝对过期，到期前同会话仍可能保持原渠道。

### H12. Admin P4 live smoke 与窄屏溢出修复（聚焦完成，完整矩阵仍属 H8）

- [x] 修复 `RouteDetailPage` 顶部操作区在 390px 视口下的页面级横向溢出：通用
  `DetailPageHeader` 的 actions 容器在窄屏改为满宽换行，`sm` 以上保持自适应靠右。复验真实前端
  `127.0.0.1:5173` 连接真实 Admin `127.0.0.1:8522`，`/routes/1` 在 390px 下
  `document.scrollWidth - innerWidth = 0`，控制台无 error。
- [x] P4 Playwright 聚焦矩阵复验通过：`e2e/p4-routing.spec.ts` 与
  `e2e/providers-endpoints.spec.ts` 共 4/4 通过；同步修正成本权重上线后的 UI 文案断言
  `权重` -> `最终权重`。Admin `npm run lint`、`npm run test`（13 files / 34 tests）与
  `npm run build`（3927 modules）均通过。
- [ ] 这不等同于 H8 的完整 live Gateway 浏览器全矩阵：Provider/Endpoint pending/recovery、Channel
  credential 轮换全状态、breaker 注入 3 秒刷新、Redis/BreakerStore 故障恢复与桌面/移动全路径仍需按
  第 12.5 节逐项执行。

### H13. 外部 StarAPI Compact 透明代理矩阵（完成）

- [x] 新增 gated blackbox：`STARAPI_COMPACT_BLACKBOX=1` 时覆盖 StarAPI OpenAI Compact 三条真实链路。
  Fixture 改为使用本地透明代理 URL + 真实 StarAPI 上游，避免与开发库已有正式 `ProviderEndpoint`
  BaseURL 唯一约束冲突；客户侧 `model_id` 使用唯一临时值，`upstream_model` 仍使用真实
  `gpt-5.6-sol`，测试后按 fixture 前缀清理临时数据。
- [x] StarAPI 原生 `/v1/responses/compact` Native 200 通过；同一个 OpenAI key 的 chat-only
  `deepseek` adapter 直接 Synthetic 通过；透明代理稳定注入 Native 404 后回落 Synthetic 通过。三条链路均验证
  非流式 compact `response_started_at/upstream_first_token_at` 为 NULL、usage/账务/snapshot/trace 收口，
  且尝试链分别为 `responses_compact`、`chat_completions`、`responses_compact -> chat_completions`。
- [x] 修复真实 StarAPI Native Compact 暴露的 adapter 缺口：上游 compact 200 响应可能有 `usage` 但没有顶层
  `model`；adapter 现在用已经冻结到请求体里的 upstream model 补齐审计 facts，Raw 响应仍原文透传。
  回归覆盖 `TestCompactResponseUsesRequestModelWhenUpstreamOmitsModel`。
- [x] 真实执行结果：`STARAPI_BLACKBOX=1 STARAPI_COMPACT_BLACKBOX=1 ... go test -tags=blackbox
  -count=1 -v ./internal/blackbox/starapi -run '^TestStarAPIOpenAIResponsesCompact'`，3/3 通过，用时
  22.232 秒。随后同一代理 fixture 复跑 OpenAI Chat/Responses 四个基础 smoke 与三条 Compact 均通过；
  Anthropic 复验未计入本项，因为临时选择的 Claude 模型在当前 Anthropic key 下返回上游 404/403。

### H14. 2026-07-23 成本调度收口复验（完成）

- [x] Gateway 最终收口复验通过：`go vet ./...`、`go build ./cmd/...`、`sqlc generate` 与
  `git diff --check` 均通过；Admin `git diff --check` 也通过。该复验未改变 H8 未完成发布门禁的结论。
- [x] 实时运行态复查 `VIP-Codex` / `gpt-5.6-sol`：`gateway.routing_balance.cost_weight=0.5`
  已在 active runtime 生效，四个候选均 eligible；当前最终权重排序为
  `openai_0.06` > `openai_0.085` > `openai_0.09` > `openai_0.16`。其中
  `openai_0.16` 的 `cost_ratio=0.8,cost_factor=0.6`，在成本因子下已排到最后；短时间内仍看到它有
  `selected_1m/selected_5m` 历史命中，属于既有 sticky 或时间窗口尾巴，不代表当前 balanced 评分仍偏向高价渠道。

### H15. H8-3 真实两 Endpoint 容灾实机演练 + `final_provider_endpoint_id` 修复（2026-07-24）

用户解锁 H8-3（本地开发库已含两个真实 provider）。对**运行中的开发 gateway/admin**做真实容灾演练——route `VIP-Codex`（balanced），候选跨 **StarAPI Endpoint 1 `open.codex521.cc`**（channel 4）与 **ZZAPI Endpoint 24 `zz1cc.cc.cd`**（channel 24/25），共享 `gpt-5.6-sol`。全程用 `request_attempts.provider_endpoint_id` 逐尝试观测，演练后完全还原：

- **基线（10 笔）**：流量在两故障域间分布；record 1841 天然发生跨域 failover（ch4/ep1 transport 失败 → ch24/ep24 成功）。
- **计划内 failover（status 围栏）**：`POST /provider-endpoints/24/status disabled` 后 8 笔全部成功且**全部落在 StarAPI ep1、ZZAPI ep24 零尝试**——status 围栏把故障域移出候选池。
- **故障触发 failover（blackhole base_url）**：把 ep24 base_url 改到死地址 `http://127.0.0.1:1`（`routing` 围栏，base_url_revision→6），ZZAPI 通道 **2–5ms 瞬时 connection-refused** → 请求内回退到 StarAPI；累计约 5 次失败后 ep24/ch24/ch25 breaker open，后续请求**直接跳过 ZZAPI**；ZZAPI 故障域 disposition `applied`，StarAPI 单通道 `invalid_response` 的 endpoint disposition `not_applicable`（需跨通道证据，符合 D1 设计）；故障域隔离成立。演练期两真实上游均有真实抖动（StarAPI 一度 30–40s 慢响应→`invalid_response`，ZZAPI 503/502），属外部波动、非网关缺陷，恢复后 6/6 成功。
- **完全还原**：ep24 base_url 复位 `https://zz1cc.cc.cd`、ep1/ep24 status enabled、五个 breaker（ep1/ep24/ch4/ch24/ch25）reset；末笔确认 3/3 成功，record 1869 ZZAPI 直接成功、1868/1870 跨域 failover，两故障域恢复正常。

**演练中发现并修复的真实缺陷**：`request_records.final_provider_endpoint_id`（P4 schema `000011` 为记录"最终服务的故障域/Endpoint"而加）在**成功/已结算失败/取消三个终态从未写入**（`final_provider_id`/`final_channel_id` 均有写、endpoint 列长期 NULL；且全仓无任何读端消费——proactively 加了列却未接线）。
- **最小安全修复**：在 `MarkRequestSucceeded`/`MarkSettledRequestFailed`/`MarkSettledRequestCanceled` 三个 UPDATE 内，由已绑定的 `final_channel_id` 派生 `final_provider_endpoint_id = (SELECT provider_endpoint_id FROM channels WHERE id = final_channel_id)`——**零 Go/param/结算逻辑/recovery-job 改动**（channel→endpoint 为稳定 1:1 FK），同时覆盖主结算与 settlement recovery 重放路径。`sqlc generate` 仅改嵌入 SQL 文本、param 结构不变，`go build ./...` 绿。
- **回归**：`TestStoreRequestLifecycleMapsNullableFields` 新增断言——结算后该列由 final channel 派生为对应 endpoint id；`internal/core/requestlog` 与 `internal/service/gateway/lifecycle` 全包测试（真实 DB+Redis）绿。
- 读端（domain struct / Admin request 查询 / DTO / 前端）暴露该列属可选后续增强，本次未做（数据现已正确落库；`request_attempts` 亦已有逐尝试 endpoint 归因）。运行中的开发 gateway 为旧 debug 二进制，需重建才会在实时流量上写该列；本修复已由 store 集成测试对真实 schema 验证。

**当前结论**：P4 生产主链路、基础 fail-closed、安全收口、Admin 自动化、真实协议/CLI smoke、完整 FLUSHDB maintenance 闭环、真实 AOF/RDB 独立文件回档、active-owner AOF recovering 安全边界、Prepare crash、BaseURL/credential/Reset 长流围栏、Channel half-open TTL/SIGKILL 接管、数据保留型 Redis 在途长流、Compact 本地 fault upstream 完整部署态、Redis Cluster 启动门禁与 **H8-3 真实两 Endpoint 容灾实机演练**已通过；Phase H 仍是"部分完成"，H8 剩余项（H8-1 state-loss 变体、H8-2 长流剩余矩阵、H8-4/5 性能+live 浏览器）在全部收口前不得把 P4 标记为发布完成。
