# Stream Partial Settlement（流式部分结算）

本文档定义 **Partial bill / Partial settlement** 的产品语义、扣费路线与实现整改方案。

关联决策：[DEC-025](../../production/DECISIONS.md#dec-025-stream-partial-settlement)（修订 [DEC-003](../../production/DECISIONS.md#dec-003-stream-无-final-usage-暂不扣费) 的部分条款）

关联任务：[TASK-7.23](PLAN.md#task-7-23-stream-partial-settlement)

登记欠账：[GAP-7-015](../../production/TODO_REGISTER.md#gap-7-015)

---

## 0. 决策记录（2026-06-25 定稿）

| 编号 | 决策 |
| --- | --- |
| **A1** | partial 结算后 attempt/request 都 `succeeded`，用 attempt 列 **`final_usage_received=FALSE`** 承载「没拿到 usage」这一事实。`MarkAttemptSucceeded` 的 `final_usage_received` 由写死 `TRUE` 改为**入参**。 |
| **A2** | 合成 facts 在 lifecycle 层补齐所有字段以通过现有 `ValidateChatSettlementFacts`，**settlement 与校验零改动**（A2-i）。 |
| **B1** | output token 复用各 adapter 已有 tokenizer 数「已 emit 可见文本」（OpenAI 走 tiktoken，Anthropic 用其估算器）；`StreamChunkMeta` 增加可见文本字段；估算**偏保守**。 |
| **B2** | partial 涉及三处分支，均按 `emitted` 分流；interrupt 分支需**重排** `MarkAttemptFailed`，让 partial 进入时 attempt 仍 `running`；抽统一私有方法。 |
| **B3** | B/D 的 request 终态都 `succeeded`（复用 settlement）；取消靠指标 `Outcome=Canceled` + `usage_source` 区分；D 返回 `nil`、B 返回 `canceled` err；**partial 永不向客户端发合成 usage 尾帧**。 |
| **B4** | full bill / partial **只看 `streamFacts == nil`**；`finalUsage` 永不参与计费。 |
| **范围** | partial 要在 **两处独立循环**实现：`RunStreamGeneric`（OpenAI chat / Responses 直传 / Responses→chat 桥接）与 `message_stream.go`（Anthropic Messages，独立实现）。 |

---

## 1. 背景与问题

### 1.1 现状（DEC-003 + TASK-7.07）

流式请求在以下路径会 **释放用户冻结、不 capture、写入 `ledger_billing_exceptions.risk_exposure`**：

| reason_code | 场景 |
| --- | --- |
| `stream_client_canceled_without_final_usage` | 客户端取消，且 adapter 未返回 final usage |
| `stream_interrupted_without_final_usage` | 已向客户端 emit 后流中断，且无 final usage |
| `stream_final_usage_missing` | adapter 正常结束，但上游未给 final usage |

`risk_exposure.platform_amount` 记为 **`authorized_amount`（预授权冻结额）**，是审计上界，不是上游真实发票。

### 1.2 业务问题

当用户 **已收到部分/完整可见内容** 后取消、断连，或上游正常结束却没给 usage：

- 上游可能已产生 token 成本；
- 网关 ctx 同步取消上游读流，往往 **读不到 final usage chunk**；
- 用户 **0 扣费**，平台承担 margin 风险；
- Admin「结算异常 / 平台承担」被 `risk_exposure` 放大（冻结上界 ≠ 真实上游成本）。

### 1.3 设计目标

1. **用户已消费可见内容 → 应 partial 扣费**，不再把整笔 `authorized_amount` 记为 `risk_exposure`。
2. **仍只认 settlement 管道**：不引入 quota 倍率式旁路；与 full bill 共用 `SettleSuccessfulChat → Capture`。
3. **首 token 前结束 → 0 扣费**：无用户价值，不写 `risk_exposure`。
4. **有 final usage → 永远 full bill**（已有，不变）。
5. **客户端 ctx 仍绑定上游 HTTP**：用户取消后上游停止，不采用「断客户端但 drain 上游」方案。

### 1.4 本方案范围外

| 项 | 状态 |
| --- | --- |
| new-api 式「上游不绑 ctx 继续跑」 | 不考虑；Unio 已 ctx 传播 |
| 客户端断开后 drain 上游抢 usage | 不做（与 ctx 取消语义冲突） |

---

## 2. 术语

| 术语 | 含义 |
| --- | --- |
| **Full bill** | 有 adapter `ResponseFacts`（可靠 final usage），`usage_source = upstream_stream` |
| **Partial bill / Partial settlement** | 无 final usage 但已 emit；合成估算 `ResponseFacts`，`usage_source = partial_stream_estimate` |
| **Release** | 普通释放冻结，`captured_amount = 0`，不写 billing exception |
| **risk_exposure** | 释放冻结 + 平台风险敞口审计；**已 emit 的无 usage 流（B/D）落地后不再写入** |
| **渠道异常（路线 D）** | 不新增专用指标：用 attempt 的 `final_usage_received=FALSE` + `upstream_finish_reason='stream_final_usage_missing'` 复用现有渠道错误/健康口径 |

---

## 3. 扣费路线（决策表）

流结束时的 **四条路线**：

| 路线 | 条件 | 账务动作 | 用户扣费 | request 终态 | attempt 标记 |
| --- | --- | --- | --- | --- | --- |
| **A Full bill** | `streamFacts != nil` | `SettleSuccessfulChat → Capture` | `min(actual, authorized)` | succeeded | succeeded，`final_usage_received=TRUE` |
| **B Partial bill** | `emitted && streamFacts == nil && (cancel \|\| interrupt)` | 合成 facts → 同 A 管道 | `min(estimated, authorized)` | succeeded（指标 `Canceled`） | succeeded，`final_usage_received=FALSE` |
| **C Release** | `!emitted` | `ReleaseAuthorization` | 0 | failed/canceled | failed/canceled（现状） |
| **D Partial bill + 渠道异常** | `emitted && streamFacts == nil && adapter 正常结束` | 合成 facts → 同 A 管道 | `min(estimated, authorized)` | succeeded | succeeded，`final_usage_received=FALSE`，`upstream_finish_reason=stream_final_usage_missing` |

**B 与 D 对用户扣费完全相同**；差别只在结束原因（落到 `upstream_finish_reason`）与 D 是渠道侧问题。

### 3.1 路线 B / D 共同触发条件

1. `emitted == true`（用户已看到至少一帧可见内容）。
2. **`streamFacts == nil`**（账务唯一真源缺失；**只看这一个信号**，见 §3.5）。

### 3.2 路线 B — 结束原因（用户/网络侧）

- `context.Canceled` / `ctx.Err()==Canceled` → 合成 `RawReason = stream_client_canceled_without_final_usage`
- emit 后非 cancel 的流错误 → 合成 `RawReason = stream_interrupted_without_final_usage`

### 3.3 路线 D — 一句话

**adapter 正常读完流，用户已看到内容，上游没给 final usage。**

- 合成 `RawReason = stream_final_usage_missing`
- 账务：与 B 相同 → partial settlement
- 渠道异常：靠 `final_usage_received=FALSE` + 上述 finish reason 暴露（§6）

### 3.4 不走路线 B/D

- 首 token 前结束（`!emitted`）→ **路线 C**（0 扣费，不写 risk_exposure）。
- `streamFacts != nil` → **路线 A**（full bill）。

### 3.5 B4：full vs partial 只看 `streamFacts`

runner 内有两个 usage 来源：`streamFacts`（adapter 交回的账务事实，**结算唯一真源**）与 `finalUsage`（onChunk 抓取，**只给协议写客户端尾帧**）。二者正常同生同灭。

判定一律以 **`streamFacts == nil`** 为准：

- `streamFacts != nil` → full bill（即使 `finalUsage == nil`，顶多客户端少一个 usage 帧，与计费无关）；
- `streamFacts == nil` → partial。

**绝不**用 `finalUsage` 反推全量 facts（否则等于在 lifecycle 层重写 adapter 的 usage→facts 映射，破坏「账务只认 adapter facts」）。现网 `streamFacts == nil || finalUsage == nil` 判定收敛为以 `streamFacts == nil` 为准。

---

## 4. Partial 用量事实（ResponseFacts 合成）

### 4.1 新增 UsageSource

在 `internal/core/usage/facts.go` 登记：

```text
partial_stream_estimate   // 已 emit 但无 final usage 时，gateway 合成的估算事实
```

与 `upstream_response` / `upstream_stream` 区分，便于 Admin、对账与幂等校验。

### 4.2 各维度来源与合成默认值（A2-i）

cancel / 缺 usage 时 adapter 返回**空 StreamOutcome**（无 Facts、无 metadata），runner 必须在本地把 facts 补齐到通过 `ValidateChatSettlementFacts`（要求 `ResponseProtocol/ResponseID/UpstreamProtocol/UpstreamResponseID/UpstreamModel/UsageMappingVersion` 非空、`StatusCode∈[100,599]`、finish class 合法）。

| 字段 | 合成来源 | 性质 |
| --- | --- | --- |
| `UncachedInputTokens` | 预授权阶段 `ConservativeInputTokens` | 真实（与 freeze 同源） |
| `CacheReadInputTokens` 等 | `not_applicable` | 无上游事实不猜测 |
| `OutputTokensTotal` | 对已 emit **可见内容**增量 tokenizer（§5.1、B1-i） | 估算，偏保守 |
| `ReasoningOutputTokens` | `not_applicable`（P0） | 不可见 reasoning 可能估少；P2 补 |
| `UpstreamModel` | `candidate.UpstreamModel` | 真实 |
| `UpstreamProtocol` | `candidate.Protocol` | 真实 |
| `Metadata.StatusCode` | 固定 **200** | 真实（`emitted` 意味着上游已回 200 头并开始流） |
| `UpstreamResponseID` / `ResponseID` | `streamResponseID`（chunk 收集）→ 空则 `partial-<request_id>` | 真实 / 兜底（前缀便于审计） |
| `UsageMappingVersion` | 常量 `partial.v1` | 标记 |
| `Finish.Class` | `other` | 合成 |
| `Finish.RawReason` | §3.2 / §3.3 的 reason | 合成（区分 B/D，落到 `upstream_finish_reason`）|

> `upstream_finish_reason` 正常承载上游真实 finish（stop/length…）；B/D 上游没有干净 finish，这里**有意**写入内部 reason 作为 B/D 判别与渠道异常依据。

幂等键为 `chat:settle:<request_id>`，与 response id 无关，兜底 id 不影响幂等。

### 4.3 估算性质（产品声明）

- Partial settlement 是 **capped 估算结算**，不是 provider 发票级精确计费。
- output 估算**偏保守**（宁可少算偏向用户），叠加 `captured_amount = min(estimated_charge, authorized_amount)`（DEC-006）双重保护。
- `estimated_charge > authorized_amount` → `write_off` 记录平台差额核销。
- `estimated_charge == 0` → 走 settlement 内 **Release**（与现有零金额逻辑一致）。

### 4.4 已知局限（P0 接受）

1. 不可见 reasoning / tool delta 可能导致 **output 估少**，平台仍可能小贴。
2. input 用预授权估算可能 **略高于** 上游实际 input（偏保护平台）。
3. 按协议细化（reasoning、tool、Anthropic cache）列为 **P2**。

---

## 5. 执行流程

### 5.1 请求生命周期（含 output 计数管线）

```text
AuthorizeChat → PreAuthorize(authorized_amount)
  → RunStreamGeneric / StreamMessage
  → onChunk:
       首次可见 emit → response_started_at、emitted=true
       partialOutputMeter += count(StreamChunkMeta.VisibleText)   // O(1) 增量，不 buffer 全文
```

管线（B1）：

1. `StreamChunkMeta` 增加 `VisibleText string`（或每 chunk token 增量）。
2. 各载体 `ChunkMeta` 填充可见文本：chat 用 `chunk.Content`；Responses 用 output_text delta；Anthropic 用 content_block_delta 文本。
3. 输出计数复用 adapter 已有 tokenizer：OpenAI 族走已依赖的 tiktoken `encode(text)`；Anthropic 用其估算器套输出文本；无 tokenizer 时回退 ~4 字符/token 启发式。增量编码避免 buffer 全文。

### 5.2 流结束 — 三处落点（`RunStreamGeneric`，Anthropic `message_stream.go` 同构镜像）

| # | 位置（`attempt_runner_stream.go`） | 现状 | 改法 |
| --- | --- | --- | --- |
| ① cancel | `:301-316`（`err!=nil` 内） | release+risk_exposure，**未判 emitted** | `emitted`→B partial；`!emitted`→`ReleaseAuthorization`(C) |
| ② interrupt-after-emit | `:320-335`（已判 `emitted`） | release+risk_exposure | 改 B partial；**重排** `MarkAttemptFailed`（见下） |
| ③ normal-end 缺 usage | `:352-373`（`err==nil`） | release + `MarkAttemptFailed` + `MarkRequestFailed` | `emitted`→D partial；`!emitted`→release(C) |

**② 的重排**：第 318 行 `MarkAttemptFailed(stream_adapter_error)` 当前**无条件**先执行，会让 attempt 变 `failed`，与 partial 需要的 `MarkAttemptSucceeded`（要求 `running`）冲突。须把它从「无条件」改为**只在非 partial 的 fallback/retry 路径（337-349）执行**。① 在 318 之前、③ 在 `err==nil` 块，attempt 都仍 `running`，不受影响。

统一私有方法（两处循环各自实现一份，逻辑一致）：

```text
settlePartialOrRelease(emitted, reason):
    if !emitted:
        ReleaseAuthorization(); 终态 release; return        // 路线 C
    facts := BuildPartialStreamFacts(reason, candidate, streamResponseID, partialOutputMeter)  // A2-i
    streamFacts = &facts
    settleStreamFacts(finalUsageReceived=false)             // A1：MarkAttemptSucceeded(final_usage_received=FALSE) + MarkRequestSucceeded
    reason == stream_final_usage_missing ? Outcome=Success : Outcome=Canceled
```

### 5.3 Settlement 管道（复用，不 fork）

Partial 与 Full 共用 `SettleSuccessfulChat`、`usage_records` + `price_snapshots` + `cost_snapshots`、`ledger.CaptureWithQueries`、settlement recovery。唯一改动：`MarkAttemptSucceeded` 的 `final_usage_received` 改入参（A1）。

**终态与返回（B3）：**

| | request 状态 | metrics Outcome | 返回 HTTP | 客户端 usage 尾帧 |
| --- | --- | --- | --- | --- |
| B（cancel/interrupt） | succeeded | `Canceled` | 返回原 `err`（canceled/interrupt） | 不发（客户端多已断） |
| D（缺 usage） | succeeded | `Success` | 返回 `nil` | 不发 |

- **partial 不调用 `params.Finish`**：永不把估算 usage 当真 usage 发给客户端。`[DONE]` / 流收尾由 handler 照常处理，客户端仅少一个 usage chunk。
- B 的 request 记为 `succeeded`（复用 settlement，不动资金主干）；取消行为不丢——靠 metrics `Canceled` + `usage_source=partial_stream_estimate` 可筛。**代价**：DB 按 `status` 统计的成功率会把「取消但已部分计费」算进成功（这类少，且用户确得了内容+被计费）。

---

## 6. 路线 D：渠道异常（不新增 Admin 指标）

用户扣费走 partial（与 B 相同）；渠道异常是**运维事实**，复用现有口径，不做专用卡片/指标。

### 6.1 信号

| 层 | 落地后 | 现有 Admin 可见 |
| --- | --- | --- |
| **Attempt** | `status=succeeded` + **`final_usage_received=FALSE`** + `upstream_finish_reason=stream_final_usage_missing` | 渠道抽屉错误明细 / 渠道健康可据此筛 |
| **Request** | `succeeded`（已交付、已 partial 计费） | 不进「失败原因 Top」（仅统计 `status=failed`，符合预期） |
| **Usage** | `usage_source=partial_stream_estimate` | 请求详情可见结算事实 |

### 6.2 渠道异常统计口径（排除取消）

「succeeded 且 `final_usage_received=FALSE`」会**同时包含 B（取消）和 D（渠道）**。统计**渠道故障**时必须再约束 `upstream_finish_reason='stream_final_usage_missing'`，否则用户取消会被误算成渠道问题。

### 6.3 P0 实现

1. `MarkAttemptSucceeded` 的 `final_usage_received` 改入参；partial 传 `FALSE`，full bill 传 `TRUE`。
2. **不**写 `risk_exposure`（用户已 capture，与 captured=0 语义冲突）。
3. **不**新增 `RecordChannelMissingFinalUsage` 专用 metrics / Admin 聚合。

### 6.4 与 risk_exposure 的关系

| 场景 | 现在 | 落地后 |
| --- | --- | --- |
| emit + cancel/interrupt，无 usage | `risk_exposure` | partial capture；**无** risk_exposure |
| emit + 正常结束，无 usage（D） | `risk_exposure` | partial capture + 渠道异常信号 |
| `!emit` | release | 不变（0 扣，无 exception） |
| full usage | capture | 不变 |

`risk_exposure` 在 **已 emit 的无 usage 流** 上应 **归零**。

---

## 7. 实现任务拆分（TASK-7.23）

### P0 — 核心账务

| # | 项 | 文件/范围 |
| --- | --- | --- |
| 1 | 登记 `usage.SourcePartialStreamEstimate` | `internal/core/usage/facts.go` |
| 2 | `MarkAttemptSucceeded` 的 `final_usage_received` 改入参 | `sql/queries/request_attempts.sql` + `requestlog/store.go` + sqlc 重生成 |
| 3 | `StreamChunkMeta` 加 `VisibleText`，各载体 ChunkMeta 填充 | `attempt_runner_stream.go`、responses/直传 carrier、Anthropic onChunk |
| 4 | 输出文本 token 计数（B1-i：复用 tokenizer，偏保守，回退启发式） | adapter tokenizer 暴露「数文本」能力 + lifecycle meter |
| 5 | `BuildPartialStreamFacts`（A2-i 合成）+ incremental meter | `lifecycle/partial_stream.go`（新） |
| 6 | `RunStreamGeneric` 三处 emitted 分流 + ② 重排 + 统一 helper | `attempt_runner_stream.go` |
| 7 | **Anthropic `message_stream.go` 同构实现**（独立循环，三处镜像） | `service/gateway/anthropic/messages/message_stream.go` |
| 8 | 单测（见 §8） | 两处循环 |
| 9 | 同步 DEC-025 / PLAN | docs |

### P1 — 可观测与 Admin

| # | 项 |
| --- | --- |
| 1 | Admin 请求详情展示 `usage_source`（含 partial） |
| 2 | 文档：对用户说明 partial 计费语义 |

### P2 — 估算精度

| # | 项 |
| --- | --- |
| 1 | reasoning / tool call delta 计入 output |
| 2 | Anthropic cache 维度（若有 emit 侧事实） |
| 3 | 与 provider 账单抽样对账告警 |

---

## 8. 测试与验收

### 8.1 必测用例（OpenAI + Anthropic 各一套）

1. **B partial（cancel after emit）** → `succeeded` + `final_usage_received=FALSE` + `captured>0` + 无 `risk_exposure`。
2. **B partial（interrupt after emit）** → 同上；并验证 attempt **未被 318 预先标 failed**（partial 能正常 `MarkAttemptSucceeded`）。
3. **D partial（normal end missing usage, emitted）** → `succeeded` + `final_usage_received=FALSE` + `upstream_finish_reason=stream_final_usage_missing` + `captured>0`。
4. **Zero emit（cancel / 缺 usage 前未 emit）** → `ReleaseAuthorization`，0 扣，无 settlement、无 exception。
5. **Full bill 优先（B4）** → `streamFacts!=nil` 一律 full settlement，即便 `finalUsage==nil`。
6. **Cap** → partial estimated > authorized → `captured==authorized` + `write_off`。

### 8.2 验收标准

- [ ] 已 emit 且无 final usage（B/D）**不再** 产生 `risk_exposure`。
- [ ] 上述 request 有 `usage_records.usage_source=partial_stream_estimate` 且 `captured_amount>0`（估算非零时）。
- [ ] 上述 attempt 为 `succeeded` 且 `final_usage_received=FALSE`。
- [ ] 路线 D：attempt `upstream_finish_reason=stream_final_usage_missing`；渠道故障统计排除取消（仅算该 reason）。
- [ ] 首 token 前结束仍 `captured_amount=0`。
- [ ] 现有 full stream settlement 测试全绿（OpenAI chat / Responses / Anthropic）。

---

## 9. 参考（不采纳实现）

| 项目 | 借鉴点 | 不采纳 |
| --- | --- | --- |
| LiteLLM #30630 | cancel 且有 chunk → partial 计费 | 其 chunk builder 与 SpendLogs 模型不同；我们走 ledger settlement |
| LiteLLM #30522 | 「有 emit 不应全额退」的语义方向 | 其跨请求 budget 计数器我们没有，也不引入；我们用单请求 reservation 的 partial capture |
| new-api | — | 上游不绑 ctx、全文 buffer 本地计数 |

---

## 10. 路线 D 定稿摘要

**情况：** adapter 正常结束，用户已看到内容，上游没有 final usage。

**决策：** 与路线 B **同一套 partial settlement**（succeeded + `final_usage_received=FALSE`）；用 `upstream_finish_reason=stream_final_usage_missing` 标识渠道异常、复用现有渠道错误/健康统计；**不写 `risk_exposure`、不新增 Admin 指标**。

---

## 11. 改造收尾：僵尸代码清理

**前置条件：** P0 全部落地、两处循环（`RunStreamGeneric` + `message_stream.go`）partial 生效、单测全绿后再做，避免删早了留下空窗。

### 11.1 应删除（被 partial 取代）

两处循环里「**已 emit 且无 final usage**」的旧分支逻辑：

| 旧调用（reason_code） | 位置 | 处置 |
| --- | --- | --- |
| `stream_client_canceled_without_final_usage` | OpenAI `attempt_runner_stream.go` cancel 分支 / Anthropic 对应分支 | 删除该 release+risk_exposure 调用，改走 partial |
| `stream_interrupted_without_final_usage` | interrupt-after-emit 分支 | 同上；并完成 ② 的 `MarkAttemptFailed` 重排 |
| `stream_final_usage_missing`（已 emit 时） | normal-end 缺 usage 分支 | 删除 release+risk_exposure + 旧 `MarkAttemptFailed`/`MarkRequestFailed`，改走 partial |

附带清理：
- 这三处对应的旧终态写入（`MarkRequestCanceled` / `MarkRequestFailed` 等）与 `result.Outcome` 旧赋值，按 §5.3 新终态收敛。
- 断言旧行为的测试（如断言 `releaseBillingExceptionParams[0].ReasonCode == "stream_client_canceled_without_final_usage"` 之类）改写为 partial 断言或删除。
- 现网 `streamFacts == nil || finalUsage == nil` 判定收敛为 `streamFacts == nil` 后，`|| finalUsage == nil` 这一支变为死代码，移除（B4）。

### 11.2 必须保留（仍在用，勿删）

- `ReleaseAuthorizationForBillingException` / `releaseMessageAuthorizationForBillingException` **函数本体**：`stream_settlement_failed_after_upstream_success`（上游成功但 settlement 永久失败且无 recovery 接管）仍调用它。
- `risk_exposure` 事件类型与 `ReleaseWithBillingException`：上述 settlement-failed 路径与 `FinalizeDeadChatSettlement`（dead-finalize）仍写 `risk_exposure`。
- `!emitted` 路径的普通 `ReleaseAuthorization`（路线 C）。

### 11.3 验证

- `rg "stream_client_canceled_without_final_usage|stream_interrupted_without_final_usage"` 应无残留调用（字符串可保留在测试/审计映射）。
- `rg "risk_exposure"` 仅剩 settlement-failed / dead-finalize 相关。
- full-bill 与 partial 测试、settlement-failed recovery 测试全绿。
