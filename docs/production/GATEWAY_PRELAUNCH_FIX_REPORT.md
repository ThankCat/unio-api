# 上线前 Gateway 全量修复与全量测试报告

参考审计：[GATEWAY_LIFECYCLE_AUDIT.md](GATEWAY_LIFECYCLE_AUDIT.md)
参考计划：`.cursor/plans/gateway_pre-launch_full_fix_*.plan.md`（分 P0/P1/P2 三阶段）

本报告随实施推进逐阶段追加：每条修复给出「问题 → 改法 → 涉及文件 → 测试」。真实 codex 端到端测试结果与测试中发现并当场修复的问题单列。

---

## 阶段一：P0 资金正确性

### P0-1 结算二次补扣到清空可用余额

- **问题**：旧逻辑结算时 `实收 = min(真实费用, 冻结额)`，当真实费用 > 冻结额时，差额直接进 `write_off`（平台核销）——即便用户尚有可用余额也不补扣，造成系统性漏收。
- **改法**：capture 时若 `actualAmount > authorizedAmount`，在用户余额行锁内复用 `CollectUserBalanceOverage` 二次补扣 `extra = min(actual-authorized, 剩余可用余额)`，写一条独立 overage debit（幂等键 `chat:settle:overage:<reqID>`）；`write_off` 平台金额改为 `actual-(captured+extra)`；`spent_total` 累加 `captured+extra`。余额永不为负。结算幂等重放校验同步识别 overage 分录。
- **文件**：`sql/queries/user_balances.sql`（新增 `CollectUserBalanceOverage`）、`sql/queries/ledger_billing_exceptions.sql`（`CreateLedgerWriteOffException` 加 `overage_amount`）、`internal/core/ledger/reservation.go`、`internal/core/ledger/numeric.go`、`internal/service/gateway/lifecycle/settlement.go`。
- **测试**：`internal/core/ledger/service_test.go` 新增
  - `TestCaptureCollectsOverageFromAvailableBalanceWithoutWriteOff`（超额全部由可用余额补扣，无平台核销）
  - `TestCapturePartiallyCollectsOverageThenWritesOffResidual`（补扣到清空余额，残差才核销）
  - 全绿。

### P0-2 冻结改用 models.max_output_tokens + 可配置回退默认

- **问题**：客户未传输出上限时，预冻结用全局兜底 `DefaultAuthorizationMaxCompletionTokens=4096`，对长输出模型（如 DeepSeek-V4 384K）偏小，预冻结不足、超额走平台核销漏收。
- **改法**：
  - routing `FindRouteCandidates` 带出 `models.max_output_tokens`，透传到 `ChatRouteCandidate.MaxOutputTokens`。
  - authorization 输出上限解析口径：客户显式值 > 0 优先；否则取候选池各模型 `max_output_tokens` 最大值；候选也缺失才回退进程级 `AUTHORIZATION_MAX_OUTPUT_TOKENS_FALLBACK`（默认 4096，启动校验为正）。
  - 三协议 `estimateMaxCompletionTokens`/`estimateMaxOutputTokens` 仅返回客户值（缺失返回 0），兜底统一由 authorization 决定。**不改写转发上游的请求体**，残差仍由 P0-1 兜底。
- **文件**：`sql/queries/channel_models.sql`、`internal/core/routing/router.go`、`internal/platform/config/config.go`（`GatewayConfig.MaxOutputTokensFallback`）、`internal/service/gateway/lifecycle/authorization.go`、`internal/service/gateway/lifecycle/candidates.go`（`CandidatePlan.CandidateMaxOutputTokens()`）、三协议 service 调用点、`internal/bootstrap/gateway.go`（构造函数透传 config）。
- **测试**：`internal/service/gateway/lifecycle/authorization_test.go` 新增三例（候选上限优先、回退进程默认、客户值优先），全绿。

### P0-3 原生 compact 200 缺 usage 不白嫖

- **问题**：原生 `/responses/compact` 返回 200 但缺 usage（上游很可能已计费）时，旧逻辑判为「不支持」并静默回落 synthetic 再调一次上游——一次请求双调上游、只收一次费，平台白嫖差额。
- **改法**：adapter 拆分 sentinel——
  - `ErrCompactUnsupported`：仅 404/405（上游无端点、无成本）→ 仍安全回落 synthetic。
  - `ErrCompactMissingUsage`：2xx 但缺 usage / 无法解析（可能有成本）→ 带 `server_error` 上游分类上抛，**绝不回落**。
  - AttemptRunner 新增 `UpstreamCostWithoutUsage` 分类：命中时不重试、不普通释放，而是释放冻结并记 `risk_exposure`（账务异常），杜绝静默白嫖；handler 据 server_error 映射 502。
- **文件**：`internal/core/adapter/openai/responses/compact.go`、`internal/service/gateway/openai/responses/compact_native.go`、`compact_orchestrator.go`、`create_response.go`（strategy 透传）、`internal/service/gateway/lifecycle/attempt_runner.go`。
- **测试**：
  - adapter：`TestCompactResponseMissingUsageReturnsMissingUsage`、`TestCompactResponseUnparseableBodyReturnsMissingUsage`、`TestCompactResponseNotFoundReturnsUnsupported`（404 仍 unsupported）。
  - service：`TestCompactHistory_NativeMissingUsageRecordsRiskExposure`（不回落 synthetic、不结算、记一条 risk_exposure）。
  - 全绿。

### P0 前端

- **网关配置只读面板**：新增 `GET /admin/v1/system/config`（脱敏，只回显非敏感运维阈值：输出 token 冻结兜底、熔断、限流默认、补偿、HTTP 超时；绝不回显 DSN/密钥/token）。前端 `SystemPage` 新增「网关配置」Tab，分组卡片展示 `{标签, 值, env}`。
- **Ledger 区分 write_off/risk_exposure**：`event_type` 本地化标签 + 区分徽章（write_off=destructive，risk_exposure=outline），计费异常页新增类型 facet 筛选（复用后端已支持的 `event_type` 查询参数）；请求详情对话框同步本地化。
- **文件**：后端 `internal/app/adminapi/system_config.go`、`router.go`、`internal/bootstrap/admin_http.go`、`admin_server.go`；前端 `src/lib/api/system.ts`、`src/pages/SystemPage.tsx`、`src/pages/LedgerPage.tsx`、`src/components/ops-tables/ledger-columns.tsx`、`src/components/requests/RequestDetailDialog.tsx`。
- **校验**：`go build ./...` + `go test ./...` 全绿；`bunx tsc -b --force` + `eslint` 改动文件无误。

### P0 真实 codex 端到端测试（零真实花费 / 隔离）

**测试方式**：本地 mock 上游 + 隔离种子 + 独立 gateway 二进制，全程零真实上游花费。

- **mock 上游**（`/tmp/unio-e2e/mock`，OpenAI 兼容）：`POST /v1/chat/completions`（流式 + 非流式，usage 可控：`completion_tokens` 取请求里的 `TOKENS=N`，默认 2000，`prompt_tokens=100`）；`POST /v1/responses/compact`（**故意返回 200 但无 usage**，用于 P0-3）。
- **隔离数据**（provider `e2e-mock`）：渠道 `E2E Mock Chat`（`adapter_key=deepseek` 桥接，给 codex `/responses`）+ `E2E Mock Compact`（`adapter_key=openai` 原生 compact）；模型 `e2e-overage`(max_output=64)/`e2e-longout`(max_output=128000)/`e2e-compact`(max_output=128000)；价格 input/cached=0、output 售价 4.0 / 成本 2.0 USD/1M（令冻结额与实收额只由 output token 决定，便于精确核对）；隔离 user/balance/route/key。
- **运行**：含 P0 改动的新 gateway 二进制跑备用端口 `:8531`（不干扰运行中的 `:8521`）；真实 `codex-cli 0.142.3`（`wire_api="responses"`，临时 `CODEX_HOME` profile）指向 `:8531`。

| 场景 | 驱动 | 关键期望 | 实测（ledger）| 结论 |
| --- | --- | --- | --- | --- |
| P0-2 不传 max_tokens 长输出 | **真实 codex** `model=e2e-longout` | 冻结按模型上限（128000×4/1M=**0.512**），非 4096 兜底（=0.016384）；用户拿全量输出、按实结算 | reservation `est=auth=0.512`、`captured=0.008`(=2000×4/1M)、`released=0.504`、无 write_off、余额 100→99.992 | ✅ |
| P0-1 低余额超额（余额充足）| **真实 codex** `model=e2e-overage` `TOKENS=5000` | 冻结 0.000256，超额二次补扣到位，**无 write_off**，余额不为负 | 两条 debit：主 0.000256 + 超额 0.019744，合计=实收 0.02；无 write_off；余额 100→99.98 | ✅ |
| P0-1 超额超过可用余额 | curl（精确控余额=0.015）`model=e2e-overage` `TOKENS=5000` | 补扣到清空可用余额，仅残差核销，余额=0 不为负 | 主 0.000256 + 超额 0.014744（扣到 0）；write_off `actual=0.02, captured=0.015, platform=0.005` | ✅ |
| P0-3 原生 compact 200 缺 usage | curl `model=e2e-compact` | 不回落 synthetic（不二次调上游）、记 risk_exposure、返回 502、不扣用户钱 | mock 仅被调 1 次；日志 `recording risk exposure instead of synthetic freeloading`；reservation `released`、余额不变；billing exception `risk_exposure platform=0.512 reason=responses_compact_missing_usage`；HTTP 502 | ✅ |

#### 测试中发现并当场修复的问题

- **现象**：真实 codex（v0.142.3）经「Responses→Chat 桥接」流式时，日志稳定报 `ERROR codex_core::util: OutputTextDelta without active item`。
- **根因**：桥接合成的 Responses SSE 缺两类事件/字段——(1) `response.output_item.added` 的 message item 未输出 `content` 字段（Go `omitempty` 丢掉空数组），而 codex 的 `ResponseItem::Message` 把 `content` 当必填，反序列化失败导致 item 不被登记；(2) 文本增量前缺 `response.content_part.added`、收尾缺 `response.output_text.done`/`response.content_part.done`。原代码注释误判「Codex 不消费 content_part」，已被真实 codex 推翻。
- **修复**：
  - `internal/app/gatewayapi/openai/responses/dto.go`：`ResponseOutputItem` 增自定义 `MarshalJSON`，对 message item 始终输出 `content`（空时为 `[]`），其余类型仍按 `omitempty`（与上游 OpenAI 形状一致）。
  - `internal/service/gateway/openai/responses/responses_stream.go`：补齐事件序列为 `output_item.added → content_part.added → *_text.delta → *_text.done → content_part.done → output_item.done`（message 与 reasoning 对称），更新文件头说明与单测序列断言。
- **验证**：`go test ./internal/app/gatewayapi/openai/responses/... ./internal/service/gateway/openai/responses/...` 全绿；重启 gateway 后真实 codex 再跑，错误消失、输出正常渲染，资金口径不变。

> 备注：`internal/blackbox`（`-tags blackbox`）存在与本次无关的既有编译失败（`sdkfixture` 仍引用 035/036 迁移已删除的 `CreateChannelCostPrice`/`CreatePrice`），不影响生产代码与上述测试；记为遗留项，留待 blackbox 夹具按新计价模型重写。

---

## 阶段二：P1 稳定性与韧性

### P1-1 坏候选 / 坏凭据跳过而非整盘失败

- **问题**：routing 构建候选时若某渠道凭据解密失败（如密钥轮换、密文损坏），旧逻辑让整个 `PlanChat` 报错，导致同一线路下其余健康渠道也无法 fallback——单个坏渠道拖垮整批请求。
- **改法**：候选构建阶段把「凭据解析失败」降级为**跳过该候选**而非中断整盘：记 `WARN routing: skip unusable candidate`（带 `channel_id / error_code=routing_credential_resolve_failed / error_category=routing`），继续用其余健康候选。全部候选都不可用时才返回无候选错误。
- **文件**：`internal/core/routing/router.go`、`internal/platform/failure/code.go`（`CodeRoutingCredentialResolveFailed`）。
- **测试**：router 单测覆盖「坏凭据候选被跳过 + 健康候选保留」；E2E 见下表。

### P1-3 中标候选 ChannelPriceID 透传 settlement

- **问题**：路由/授权时按渠道当时的 active price 估算，但结算时再 `FindActiveChannelPrice` 重查——两步之间管理员改价/换价会造成「按 A 价授权、按 B 价结算」的竞态，金额口径漂移。
- **改法**：`ChatRouteCandidate.ChannelPriceID` 透传进 `ChatSettlementParams.ChannelPriceID`；结算用 `resolveSettlementChannelPrice` 优先**钉住**这条 price_id（校验存在且属于同渠道/模型），仅当钉价缺失时才回退 `FindActiveChannelPrice`。补偿任务（settlement_recovery_jobs）持久化 `price_id`，重放时同样钉价。
- **文件**：`internal/service/gateway/lifecycle/settlement.go`、`settlement_recovery.go`、`attempt_runner.go`、`attempt_runner_stream.go`。
- **测试**：`TestChatSettlementPinsCandidateChannelPrice`（改价后结算仍用钉住的旧价），全绿。

### P1-4 补偿窗口加宽（max_attempts / 退避上限可配）

- **问题**：结算补偿 worker 固定 `max_attempts=10` + 退避 cap 64s，总覆盖窗口仅约 4.25 分钟——上游/DB 较长抖动期间补偿会过早判死，留下永久未结算请求。
- **改法**：`WORKER_SETTLEMENT_RECOVERY_MAX_ATTEMPTS`（默认 20）与 `WORKER_SETTLEMENT_RECOVERY_BACKOFF_CAP`（默认 5m）化为可配；`settlementRecoveryBackoff` 用可配 cap 封顶（修了 `cap` 参数遮蔽 Go 内建的问题）。总覆盖窗口由约 4.25 分钟扩到约 1 小时。迁移 `000051` 把 `settlement_recovery_jobs.max_attempts` 默认值由 10 调到 20。
- **文件**：`internal/platform/config/config.go`、`internal/app/workers/settlement_recovery_worker.go`、`internal/service/gateway/lifecycle/settlement_recovery.go`、`sql/queries/settlement_recovery_jobs.sql`、`migrations/000051_widen_settlement_recovery_max_attempts_default.{up,down}.sql`、`internal/bootstrap/gateway.go`。
- **测试**：`TestSettlementRecoveryBackoffCapsAndCoversWindow`、`TestSettlementRecoveryBackoffFallsBackToDefaultCap`，全绿。

### P1-5 孤儿 authorized 预授权清扫 worker

- **问题**：网关进程在「已 authorize 冻结、上游调用进行中」时崩溃，会遗留 `authorized` 预授权 + `running` 请求——用户余额被永久冻结、无人收口。
- **改法**：新增 `OrphanReservationSweeperWorker`，周期性扫描「`authorized` 且 `created_at` 早于阈值、对应 `request_record` 仍 `running`、且无在途 `settlement_recovery_job`」的孤儿预授权（`FOR UPDATE SKIP LOCKED` 分批）；逐条 `FinalizeOrphanReservation`：释放冻结、记 `risk_exposure` 账务异常（reason `gateway_request_orphan_reclaimed`）、把请求收口为 `failed`。批量大小/年龄阈值可配。
- **文件**：`sql/queries/ledger_reservations.sql`（`ListOrphanAuthorizedReservations`）、`internal/platform/store/sqlc/ledger_reservations.sql.go`、`internal/app/workers/orphan_reservation_sweeper_worker.go`、`internal/service/gateway/lifecycle/settlement.go`（`FinalizeOrphanReservation`）、`internal/bootstrap/worker_server.go`（注册）、`internal/platform/config/config.go`（`WORKER_ORPHAN_RESERVATION_SWEEP_AGE_THRESHOLD` 默认 15m、`WORKER_ORPHAN_RESERVATION_SWEEP_BATCH_SIZE` 默认 100）、`internal/platform/failure/code.go`（`CodeGatewayRequestOrphanReclaimed`）。
- **测试**：`orphan_reservation_sweeper_worker_test.go`（孤儿被收口、健康预授权不误伤），全绿。

### P1-6 非流式上游响应体大小上限（防 OOM）

- **问题**：非流式上游响应体直接全量 decode，无大小上限——上游异常/恶意返回超大 body 会撑爆网关内存。
- **改法**：新增 `adapter.ReadUpstreamBodyLimited`（读到 `limit+1` 字节即判超限），三协议非流式路径统一改用；超限返回 `CodeAdapterResponseTooLarge` 并安全释放冻结。上限由 `GATEWAY_MAX_UPSTREAM_RESPONSE_MB`（默认 8MB）配置，启动时经 `adapter.SetMaxUpstreamResponseBytes` 注入。
- **文件**：`internal/core/adapter/upstream_body_limit.go`（新增）、三协议 adapter（chatcompletions/anthropic-messages/responses + compact）、`internal/bootstrap/gateway_server.go`、`internal/platform/config/config.go`、`internal/platform/failure/code.go`（`CodeAdapterResponseTooLarge`）。删除冗余的 `responses/util.go:readAllLimited`。
- **测试**：`upstream_body_limit_test.go` + chat adapter 超大 body 单测；E2E 见下表。

### P1-7 流式 chunk 间 idle 超时

- **问题**：流式上游可能进入「半开/挂死」——已建连但久不推进任何字节，客户端被无限期挂住。
- **改法**：新增 `adapter.StreamTimeoutContext`（区分首包 header 超时与 chunk 间 idle 超时）；SSE reader 增 `OnActivity` 回调，每读到一个事件就重置 idle 计时器；超时以 `ErrStreamIdleTimeout` 取消流并映射 `CodeAdapterStreamIdleTimeout`。idle 窗口由 `GATEWAY_STREAM_IDLE_TIMEOUT`（默认 10m）配置，经 `adapter.SetStreamIdleTimeout` 注入。
- **文件**：`internal/core/adapter/stream_timeout.go`（新增）、`internal/core/adapter/sse/reader.go`（`OnActivity`）、三协议流式路径、`internal/bootstrap/gateway_server.go`、`internal/platform/config/config.go`、`internal/platform/failure/code.go`（`CodeAdapterStreamIdleTimeout`）。
- **测试**：`stream_timeout_test.go` + chat adapter idle 单测；E2E 见下表。

### P1-8 流式首字节前传输层失败可 fallback

- **问题**：流式在「尚未向客户端写出任何字节」时遇到传输层失败（建连后被对端 RST、读 body 即断），旧分类未判为可重试 `UpstreamError`，导致无法 fallback——而此时其实还能安全改投其他渠道。
- **改法**：各协议 `errors.go` 增 `newUpstreamStreamReadError` / `newUpstreamStreamIncompleteError`，把传输层读错误按语义包装为可重试 `adapter.UpstreamError`（`server_error` / `timeout` / `canceled`）。AttemptRunner 在「首字节前」据此 fallback 到下一候选；一旦已向客户端写出字节则不再 fallback（避免重复输出）。
- **文件**：`internal/core/adapter/openai/chatcompletions/errors.go`、`internal/core/adapter/anthropic/messages/errors.go`、`internal/core/adapter/openai/responses/errors.go` 及对应 adapter 流式读循环。
- **测试**：chat adapter「可重试传输失败」单测；E2E 见下表（含真实 codex 经 Responses→Chat 桥接 fallback）。

### P1 前端

- **网关配置补全**：`system_config.go` 新增/补全只读分组——「上游响应与流式」（`MaxUpstreamResponseBytes` / `StreamIdleTimeout`）、「结算补偿 worker」（`SettlementRecoveryMaxAttempts` / `SettlementRecoveryBackoffCap`）、「孤儿预授权清扫 worker」（`OrphanReservationSweepAgeThreshold` / `OrphanReservationSweepBatchSize`）。前端 `SystemPage` 动态渲染分组，无需改前端即自动显示新增项。
- **Ledger reason_code 观测/筛选**：`ledger_billing_exceptions.sql` + 查询服务 + admin handler 增 `reason_code` 过滤；前端 `LedgerPage` 计费异常页新增 `reason_code` facet（含 `gateway_request_orphan_reclaimed`、`settlement_recovery_exhausted`、`authorization_underfunded`、`responses_compact_missing_usage` 等关键码），可直接观测孤儿清扫/补偿耗尽/白嫖拦截等风险事件。
- **文件**：后端 `internal/app/adminapi/system_config.go`、`internal/service/admin/query/ledger.go`、`internal/app/adminapi/ledger.go`、`sql/queries/ledger_billing_exceptions.sql`；前端 `src/lib/api/ledger.ts`、`src/components/ops-tables/ledger-columns.tsx`、`src/pages/LedgerPage.tsx`。
- **校验**：`go build ./...` + 相关包 `go test` 全绿；`tsc --noEmit` 无误。

### P1 真实 codex 端到端测试（零真实花费 / 隔离）

**测试方式**：沿用 P0 隔离夹具，扩展 mock 上游与种子，新 gateway 二进制跑 `:8531`（关键开关收紧：`GATEWAY_MAX_UPSTREAM_RESPONSE_MB=1`、`GATEWAY_STREAM_IDLE_TIMEOUT=2s`），全程零真实上游花费。

- **mock 扩展**：`/dropstream`（发完 header 即在首字节前关连接）、`/idle`（发一个 chunk 后挂死约 30s、不发 `[DONE]`）、`/huge`（返回约 2MB 非流式 body）。
- **种子扩展**：渠道 `E2E Bad Cred`（垃圾密文凭据，触发解密失败跳过）、`E2E Drop Stream`、`E2E Idle`、`E2E Huge`，及模型 `e2e-fallback / e2e-dropstream / e2e-idle / e2e-huge`，全部并入 `E2E Pool`。坏/中断渠道售价压低（output 3.0 < 健康 4.0），确保 `cheapest` 先选中坏渠道、再 fallback 到健康渠道。

| 场景 | 驱动 | 关键期望 | 实测 | 结论 |
| --- | --- | --- | --- | --- |
| P1-1 坏凭据跳过 | **真实 codex** `model=e2e-fallback` | 坏凭据候选被跳过而非整盘失败，落到健康渠道成功 | 日志 `skip unusable candidate channel_id=106 error_code=routing_credential_resolve_failed`；req 仅 1 个 attempt（E2E Mock Chat succeeded）；codex 拿到 `ok from mock` | ✅ |
| P1-8 流式中断 fallback | **真实 codex** `model=e2e-dropstream` | 最便宜候选首字节前断连→分类为可重试→fallback 到健康渠道，客户端拿到完整流 | mock 日志先 `dropstream` 后 `chat`；attempt0 `E2E Drop Stream failed/adapter_read_stream_failed`、attempt1 `E2E Mock Chat succeeded`；req succeeded；codex 拿到 `ok from mock` | ✅ |
| P1-7 流式 idle 超时 | curl `model=e2e-idle` `stream=true` | idle=2s 内无推进即中止，**不**等满 mock 的 30s 挂死 | 客户端收首 chunk 后约 2.08s 中止报 `upstream_timeout`；req `failed/adapter_stream_idle_timeout`；部分内容部分结算、余额释放、无挂死 | ✅ |
| P1-6 大响应不 OOM | curl `model=e2e-huge`（非流式）| 超 1MB 上限即拒收，网关不 OOM、安全释放冻结 | HTTP 500（内部错误对外归一）；req `failed/adapter_response_too_large`；reservation 全额释放、`captured=0`、网关存活 | ✅ |

**资金收口核对**：四场景跑完后用户余额 `99.992`、`reserved_balance=0`、未结清/挂起预授权 `=0`——无冻结泄漏、无负余额。

#### 测试中发现并当场修复的问题

- **现象**：首轮 P1-8 用 curl 打 `e2e-dropstream`，客户端直接拿到健康流，但 mock 日志**没有** dropstream 命中——坏渠道根本没被先选中。
- **根因**：`cheapest` 排序口径是**客户售价**（`output_price` 优先，其次 `uncached_input_price`），不是平台成本。初版种子把坏渠道与健康渠道售价都设为 4.0 → 平手 → 稳定排序回落到 SQL priority 基序，健康渠道（id 更小）反而先被选，fallback 路径未被真正触发（假阴性）。
- **修复**：把坏/中断渠道（`E2E Bad Cred` / `E2E Drop Stream`）的 `output_price` 压到 3.0（< 健康 4.0），令 `cheapest` 必先选中坏渠道。重跑后 mock 日志先 `dropstream` 后 `chat`、DB 两条 attempt（坏失败→健康成功）齐备，fallback 被真实触发并通过。这是测试夹具的口径修正，非生产代码缺陷；同时确认了 `cheapest=按客户售价` 这一行为符合预期。

> 备注：`internal/blackbox`（`-tags blackbox`）既有编译失败（`sdkfixture` 引用已删除迁移的旧计价 API）与本阶段无关，仍为遗留项。

---

## 阶段三：P2 限流 · 观测 · 体验

### P2-8 两层限流（令牌 Key + 渠道 Channel，各含 RPM/TPM/RPD）

- **问题**：网关只有一条粗粒度全局限流，缺少按「令牌（API Key）」与「渠道（Channel）」分层的 RPM/TPM/RPD 精细配额，无法防单 Key 滥用，也无法保护单个上游渠道被打爆。
- **改法**（new-api 风格，RPM=每分钟请求、TPM=每分钟 token、RPD=每日请求）：
  - 迁移：`api_keys`（`000052`）与 `channels`（`000053`）各加 `rpm_limit/tpm_limit/rpd_limit`（nullable，`NULL`=继承全局默认、`0`=显式不限、`>0`=具体上限）。
  - 计数器：`internal/platform/ratelimit/sliding.go` 用 Redis 滑动窗口实现 RPM/TPM/RPD 三维原子计数 + 预检；`guard.go` 封装「先按估算预占、命中即拒」的判定。
  - 令牌层：`internal/app/gatewayapi/middleware/rate_limit.go` 在鉴权后、进入业务前对该 Key 的 RPM/TPM/RPD 判定，命中返回 HTTP 429 + `X-RateLimit-*` 头。TPM 用「请求前按估算 token 预检阻断 + 结算后回填实际」口径。
  - 渠道层：候选循环里调上游前对中标渠道做 RPM/TPM/RPD 判定，命中则**跳过该渠道并 fallback** 到下一候选（而非整盘失败）。
  - 全局默认入 `RateLimitConfig.DefaultRPM/DefaultTPM/DefaultRPD`（env `RATE_LIMIT_DEFAULT_RPM/TPM/RPD`，0=不限）；`FailurePolicy`（`fail_closed`/`fail_open`）决定 Redis 故障时拒绝还是放行。
  - admin：`api_keys.go`/`channels.go` 的 create/update 接受并校验 `rate_limits`（非负），DTO 回显三维上限。
  - 观测：`unio_ratelimit_decisions_total{decision}`（allowed/limited/redis_failure_fail_open/redis_failure_fail_closed）。
- **文件**：`migrations/000052_*`、`migrations/000053_*`、`internal/platform/ratelimit/{sliding,guard}.go`、`internal/app/gatewayapi/middleware/rate_limit.go`、候选循环（`internal/service/gateway/lifecycle/attempt_runner*.go`、`request_lifecycle.go`）、`internal/platform/config/config.go`、`internal/app/adminapi/{api_keys,channels}.go`、`internal/platform/observability/metrics/metrics.go`。
- **测试**：`ratelimit/sliding_test.go`、`middleware/rate_limit_test.go`、`config_test.go`，全绿；真实 codex / curl E2E 见下表。

### P2-7 429 读 Retry-After + 渠道 cooldown

- **问题**：上游 429（限流/过载）时若无脑立刻重试或继续选同一渠道，会加剧上游过载、放大故障。
- **改法**：`internal/core/adapter/retry_after.go` 解析上游 `Retry-After`（秒数或 HTTP date）；三协议 `errors.go` 把 429 归类为带 `RetryAfter` 的可重试上游错误。`internal/service/gateway/lifecycle/cooldown.go` 对返回 429 的渠道登记**短时 cooldown**（有 `Retry-After` 用其值、封顶 `GATEWAY_CHANNEL_RATELIMIT_COOLDOWN_CAP`；无则用默认 `GATEWAY_CHANNEL_RATELIMIT_COOLDOWN`）；cooldown 窗口内候选循环跳过该渠道，直接 fallback，窗口过后自动恢复。
- **文件**：`internal/core/adapter/retry_after.go`、`internal/core/adapter/openai/{chatcompletions,responses}/errors.go`、`internal/core/adapter/anthropic/messages/errors.go`、`internal/service/gateway/lifecycle/cooldown.go`、`internal/platform/config/config.go`（`GatewayConfig.ChannelRateLimitCooldown/Cooldown Cap`）。
- **测试**：`adapter/retry_after_test.go`、`lifecycle/cooldown_test.go`，全绿；E2E 见下表（Test D）。

### P2-1 预算软上限可视化 + 告警

- **问题**：`spend_limit` 是软上限（高并发下可能轻微超），需在前端清晰展示并对「接近上限」告警，而非加硬闸门。
- **改法**：保持软上限语义（命中后端自动停用 Key），前端 API Key 运维表「已用」列在设了上限时展示 `已用金额 (使用率%)`，并按阈值高亮——`≥80%` 琥珀色预警、`≥100%` 红色告警。
- **文件**：前端 `src/components/ops-tables/api-keys-columns.tsx`（`budgetUsagePercent` + 分级着色）。

### P2-2 / P2-3 观测指标

- **P2-2 partial 结算占比**：`unio_gateway_partial_settlement_total{reason}`（流式按已吐内容保守估算收费的发生次数，监控偏少收/滥用）。
- **P2-3 可重试 fallback 前序成本**：`unio_gateway_retryable_fallback_total{error_category}`（因可重试上游错误切换候选的次数，监控前序候选可能已产生但未计费的成本）。
- 二者均在 `request_lifecycle.go` 热路径调用（`IncPartialSettlement`/`IncRetryableFallback`），非仅声明。
- **文件**：`internal/platform/observability/metrics/metrics.go`、`internal/service/gateway/lifecycle/request_lifecycle.go`、`metrics.go`（recorder 接口）。

### P2-4 零价渠道告警

- **改法**：以零售价（客户侧 $0）成功结算的请求计入 `unio_gateway_zero_price_served_total{provider,channel,model}`（`IncZeroPriceServed`，结算成功路径调用），用于零价渠道误配的运营告警。
- **文件**：`internal/platform/observability/metrics/metrics.go`、`internal/service/gateway/lifecycle/request_lifecycle.go`。

### P2-5 补偿 worker 批量 claim

- **改法**：`SettlementRecoveryWorker` 单轮按 `batchSize`（默认 16，`SetBatchSize` 可调、`WORKER_SETTLEMENT_RECOVERY_BATCH_SIZE` 配置）批量 claim 处理积压，避免逐条低吞吐。
- **文件**：`internal/app/workers/settlement_recovery_worker.go`、`internal/platform/config/config.go`。

### P2-6 故障注入隔离（生产二进制不读）

- **问题**：`BILLING_E2E_INJECT_SETTLEMENT_FAIL` 若被生产进程读取，误设即每次结算都失败。
- **改法**：用 build tag 物理隔离——`fault_inject_e2e.go`（`//go:build billing_e2e`）才读该 env；`fault_inject_prod.go`（`//go:build !billing_e2e`，默认）恒返回 false，并在检测到该 env 被设置时打 WARN「已忽略，需 -tags billing_e2e」。生产构建结算热路径不读任何注入 env。
- **文件**：`internal/service/gateway/lifecycle/fault_inject_{e2e,prod}.go`、`settlement.go`、`settlement_recovery.go`。

### P2-10 Responses inline 错误脱敏

- **问题**：上游 Responses inline 错误（`response.failed`/`error` 事件）的 message 可能含 `base_url`、内部 request id 等基础设施细节，直接透传给客户端即信息泄露。
- **改法**：`inline_error_sanitize.go` 在透传前把错误事件重建为「最小且脱敏」的同形状信封——保留 Codex 所需的 `type`/`error.code`/`message`，但对 message 去 URL（`[redacted]`）、压缩空白并截断。`stream.go` 在 inline 透传处改用 `sanitizedResponsesFailedEvent`/`sanitizedResponsesErrorEvent`。
- **文件**：`internal/core/adapter/openai/responses/inline_error_sanitize.go`、`stream.go`。
- **测试**：`inline_error_sanitize_test.go`（URL 脱敏、截断、信封形状），全绿。

### P2 前端

- **限流字段表单**：渠道表单 `ChannelFormDialog`（创建 + 编辑）与 API Key 创建弹窗 `CreateApiKeyDialog` 均加 RPM/TPM/RPD 三列输入（留空=继承全局默认、0=不限、>0=上限），含非负整数校验，并映射后端 `rate_limits`（创建/更新原子替换三维）。
- **预算可视化**：API Key 运维表「已用」列加使用率与分级告警高亮（见 P2-1）。
- **网关配置（只读）补限流默认**：`system_config.go` 已含「限流全局默认（两层 RPM/TPM/RPD）」分组（`RATE_LIMIT_DEFAULT_RPM/TPM/RPD` + `RATE_LIMIT_FAILURE_POLICY`）与「上游响应与流式」组里的渠道 429 cooldown（默认 + 上限）；`SystemPage` 动态渲染分组，无需改前端即自动显示。
- **限流命中观测**：限流判定以 Prometheus `unio_ratelimit_decisions_total{decision}` 暴露（标准可观测面），命中即 HTTP 429 对客户端可见、网关访问日志可查。
- **文件**：`src/components/channels/ChannelFormDialog.tsx`、`src/components/customer/CreateApiKeyDialog.tsx`、`src/components/ops-tables/api-keys-columns.tsx`、`src/lib/api/{channels,apiKeys}.ts`、后端 `internal/app/adminapi/system_config.go`。
- **校验**：`tsc -b --force` + `eslint` 改动文件无误；`go build ./...` + `go test ./...` 全绿。

### P2 真实 codex / curl 端到端测试（零真实花费 / 隔离）

**测试方式**：沿用 P0/P1 隔离夹具，扩展 429-capable mock（可控 429 + `Retry-After`）与种子（`rl-chanA`/`rl-chanB`/`rl-429-trigger` 渠道、`e2e-429` 模型，cooldown 设 2s 便于观测），新 gateway 二进制跑 `:8531`，全程零真实上游花费。

| 场景 | 驱动 | 关键期望 | 实测 | 结论 |
| --- | --- | --- | --- | --- |
| Test A — 令牌级 RPM | curl（Key RPM=3，连发 6） | 前 3 成功（余额头 2/1/0），后 3 → HTTP 429 + `X-RateLimit-*` | 前 3 成功、后 3 返回 429 且头正确 | ✅ |
| Test B — 渠道级 RPM + fallback | curl（Chan A RPM=1，发 2） | req1 命中 A，req2 A 超限→跳过→fallback B | mock 日志 req1→`rl-chanA`、req2→`rl-chanB`（A 被跳过） | ✅ |
| Test C — 令牌级 TPM | curl（Key TPM=5 + 长 prompt） | 估算 token 超限即 429 `rate_limit_error`；调高 TPM→200 | 长 prompt → 429；TPM 调高 → 200 | ✅ |
| Test D — 429 cooldown（P2-7）| curl（`model=e2e-429`，cooldown=2s） | req1 429→fallback B 并登记 cooldown；req2(<2s) 只走 B（跳过 429-chan）；req3(>2s) 重试 429-chan→fallback B | mock 时序完全符合：req1 `rl-429-trigger`→`rl-chanB`；req2 仅 `rl-chanB`；req3 `rl-429-trigger`→`rl-chanB` | ✅ |
| P2-8 真实 codex 命中 Key RPM | **真实 codex**（Key RPM=1，先用 curl 耗尽配额）| codex 请求被拒 429、退非零 | 网关日志记 codex 请求 429（correlation_id `1833a384…`）；codex 日志 `http.response.status_code=429` → `Turn error: exceeded retry limit, last status: 429`（同 id）退非零 | ✅ |
| 集成回归（P0+P1+P2 同一二进制）| **真实 codex** `model=e2e-longout` | 桥接 SSE 正常、资金口径不变、无 P0 的 `OutputTextDelta` 错误 | SSE `response.created→output_item.added→content_part.added→output_text.delta→…→response.completed`，输出 `ok from mock`，tokens 100/2000，无报错 | ✅ |

> 说明：A/B/C/D 用 curl 精确驱动限流边界（codex 难以稳定连发触发具体阈值）；P2-8 用真实 codex 验证「耗尽配额后被网关 429 拒绝」的端到端闭环。限流中间件作用在 codex 实际打的同一 `/v1/responses` 入口，curl 与 codex 命中同一条判定链路。

#### 测试中发现并当场修复的问题

- **现象**：首轮 Test B 渠道级限流的 fallback 未被触发（坏/受限渠道没有先被选中）。
- **根因**：与 P1-8 同源——`cheapest` 排序按客户售价，受限渠道与健康渠道售价持平导致回落 SQL priority 基序，健康渠道反被先选（假阴性）。
- **修复**：把受限渠道（`rl-chanA`/`rl-429-trigger`）售价压低于健康渠道，令 `cheapest` 必先选中受限渠道，fallback / cooldown 路径方被真实触发。属测试夹具口径修正，非生产代码缺陷。

> 备注：`internal/blackbox`（`-tags blackbox`）既有编译失败（`sdkfixture` 引用已删除迁移的旧计价 API）与本阶段无关，仍为遗留项，留待按新计价模型重写夹具。

---

## 收尾交付总览

- **三阶段全部完成**：P0 资金正确性、P1 稳定性与韧性、P2 限流·观测·体验，逐条「问题 → 改法 → 文件 → 测试」如上。
- **真实 codex 端到端**：P0（长输出冻结、超额二次补扣、compact 不白嫖）、P1（坏凭据跳过、流式中断 fallback、idle 超时、大响应不 OOM）、P2（两层限流、429 cooldown、真实 codex 命中 RPM）全部通过；测试中发现的问题已当场修复并复测。
- **零真实花费**：全程本地 mock 上游 + 隔离种子（provider `e2e-mock`）+ 备用端口 `:8531`，不触达任何真实上游、不动生产 `:8521`。
- **无后台静默**：所有新增可变设置在前端可编辑（API Key/Channel 的 RPM/TPM/RPD、spend_limit），所有进程级 env 阈值在「网关配置(只读)」面板可见（冻结兜底、上游体上限、流式 idle、渠道 429 cooldown、熔断、限流全局默认、补偿/孤儿清扫 worker、HTTP 超时）。
- **资金安全**：资金类改动（二次补扣、limit 计数、cooldown）均在事务/行锁或原子计数内，保证幂等与不透支；E2E 复核用户余额永不为负、无冻结泄漏。
- **遗留项**：`internal/blackbox`（`-tags blackbox`）夹具引用已删除的旧计价 API，需按 `channel_prices` 新模型重写（与本次修复无关，不影响生产代码与默认 `go test ./...`）。

