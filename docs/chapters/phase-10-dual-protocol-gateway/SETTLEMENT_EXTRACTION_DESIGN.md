# Settlement / Recovery / Authorization 抽取设计（TASK-10.05 叶子第四批）

本文件是把账务子系统（settlement + recovery + authorization）从 OpenAI 专属包
`service/gateway/openai/chatcompletions` 抽到协议无关共享包 `service/gateway/lifecycle`
的迁移设计。它是 10.10B-2b「架构 B」叶子抽取的最后、也是最大的一批，落地前先冻结边界与命名，
避免在 ~1700 行账务核心移动中引入幂等/金额回归。

前三批（retry / breaker / metrics+tracing）已完成，模式见 PLAN.md TASK-10.05。

## 1. 目标

```text
让 OpenAI 与 Anthropic 两个协议族的 service 编排复用同一套账务子系统：
  authorization（预授权冻结/释放）
  settlement（成功请求的 usage + price/cost 快照 + ledger 结算事务）
  recovery（首次 settlement 失败后由 worker 幂等重放）
```

当前这三块都在 OpenAI 专属包，Anthropic service 无法复用（反向依赖 OpenAI 包不允许）。

## 2. 协议无关性论证（为什么可整体移动）

关键事实：**账务子系统已经协议无关**——10.12A 后 settlement/recovery 只消费 `adapter.ResponseFacts`
与 `usage.Facts`，不接触任何 OpenAI/Anthropic HTTP DTO。

- `ChatSettlementParams` 字段：`requestlog.RequestRecord`/`AttemptRecord`、`*auth.APIKeyPrincipal`、
  `ChatAuthorization`、`requestlog.Protocol`、`ResponseID string`、`ResponseModelID string`、
  `ModelDBID`/`FinalProviderID`/`FinalChannelID int64`、`adapter.ResponseFacts`。**全部协议无关。**
- 依赖：`billing.Calculator`、`ledger.Service`、`sqlc.Queries`、`requestlog`、`adapter`/`usage` facts。
  这些都已是协议无关核心。

因此账务子系统可以整体移到 `lifecycle`，两协议共用同一实现，不需要各写一份。

## 3. 移动清单（→ `service/gateway/lifecycle`）

| 源文件 | 目标 | 内容 |
| --- | --- | --- |
| `chat_settlement.go` | `lifecycle/settlement.go` | `ChatTxBeginner`/`ChatLedgerCapturer`/`ChatBillingCalculator` 接口、`ChatSettlementService`、`ChatSettlementParams`、`SettleSuccessfulChat`、全部 idempotency 校验（`ensureSettlement*`）、numeric helper |
| `chat_settlement_recovery.go` | `lifecycle/settlement_recovery.go` | `ChatSettlementRecoveryStore/Service`、`RecoverableChatSettlementExecutor`、job 转换、`IsChatSettlementRecoveryScheduled`、`ErrChatSettlementRecoveryScheduled` |
| `chat_authorization.go`（大部分） | `lifecycle/authorization.go` | `ChatAuthorizer`/`ChatAuthorization`/`ChatAuthorizeParams`/`ChatReleaseAuthorizationParams`/`ChatReleaseBillingExceptionParams`、`ChatAuthorizationService` + 三个 store/billing/ledger 接口、`AuthorizeChat`/`ReleaseChat`/`ReleaseChatForBillingException`、`customerPriceSnapshotFromActivePrice` |

`ChatSettlementExecutor` 接口当前在 `service.go`，随 settlement 一并移到 lifecycle。

## 4. 保留在 chatcompletions 的部分

只有 2 个绑定 OpenAI `ChatCompletionService` 的编排 helper 留下（它们调 `s.chatAuthorizer`，
是编排骨架的一部分，架构 B 下骨架暂不共享）：

```text
(s *ChatCompletionService) releaseChatAuthorization(ctx, authorization) error
(s *ChatCompletionService) releaseChatAuthorizationForBillingException(ctx, authorization, reasonCode, reason) error
```

迁移后它们改用 `lifecycle.ChatAuthorization` / `lifecycle.ChatReleaseAuthorizationParams` 等类型。

metrics/tracing 第三批留下的两个 settlement 耦合函数（`settlementOutcomeFromErr` / `endSettlementSpan`，
依赖 `IsChatSettlementRecoveryScheduled`）：本批完成后 `IsChatSettlementRecoveryScheduled` 已在 lifecycle，
这两个函数可改调 `lifecycle.IsChatSettlementRecoveryScheduled`，**保留在 chatcompletions**（它们服务于
OpenAI 编排的 metrics/tracing 收口）；Anthropic 侧将各自实现等价的 outcome/span 收口，复用 lifecycle 判定。

## 5. 命名决策

`ChatSettlementParams` / `ChatAuthorization` 等带 `Chat` 前缀是 OpenAI 单协议时代的命名；它们其实协议无关。

**决策：本批移动时保留 `Chat*` 命名，统一去前缀留到 TASK-10.15 文档/命名复核。**

理由：
- 账务子系统内部交叉引用密集（`ChatSettlementParams` 含 `ChatAuthorization`，幂等校验交叉引用）；
  移动 + 重命名同时做会放大扩散面与回归风险。
- 移动本身已是 ~1700 行账务核心的高风险动作，单步只改「包位置 + 引用路径」，行为与符号名不变，
  最易 diff review 与回归比对。
- 10.15 已有「命名与冗余复核」任务，统一去 `Chat` 前缀（`lifecycle.SettlementParams` 等）放在那里，
  一次重命名 + 一次回归。

迁移后短期可见 `lifecycle.ChatSettlementParams` 这类命名，属已知过渡态，已在 10.15 登记收口。

## 6. worker 依赖反转

`worker_server.go` 当前 `import gateway "…/openai/chatcompletions"` 来构造
`NewChatSettlementService` + `NewChatSettlementRecoveryService` 跑 recovery job。

这是个**不合理的依赖方向**：worker 不该依赖 OpenAI 协议包。settlement/recovery 移到 lifecycle 后，
`worker_server.go` 改 `import lifecycle`，依赖方向修正为 worker → lifecycle（协议无关）。

`gateway.ChatTxBeginner`（被 gateway_server.go / worker_server.go 当类型用）随之改 `lifecycle.ChatTxBeginner`。

## 7. 测试拆分

| 测试 | 去向 |
| --- | --- |
| `chat_settlement_test.go`（DB 集成：usage/price/cost 快照、idempotency、write-off、release/capture） | → `lifecycle/settlement_test.go`（协议无关，整体移动） |
| `chat_settlement_recovery_test.go` | → `lifecycle/settlement_recovery_test.go` |
| `chat_authorization_test.go` | → `lifecycle/authorization_test.go` |
| `chat_settlement_recovery_behavior_test.go` | 若用 `ChatCompletionService` 编排（stream tail/cancel 行为）→ 留 `chatcompletions`；否则移 lifecycle |
| `service_test.go` 的 `fakeChatSettlementExecutor` / `fakeChatAuthorizer` 替身 | 留 `chatcompletions`（编排测试用），改实现 `lifecycle.ChatSettlementExecutor` / `lifecycle.ChatAuthorizer` 接口 |

注意：settlement 测试是 DB 集成测试，移到 lifecycle 包后需复用 lifecycle 包内（或现有 sqlc test helper）的
建表/seed helper；若依赖 chatcompletions 包内 helper，需一并迁移或改为 lifecycle 包内 helper。**这是本批最易卡住的点**，执行前要先确认 settlement 测试的 helper 依赖。

## 8. 分步执行计划（每步独立全绿）

```text
Step 1  authorization → lifecycle/authorization.go（保留 Chat 命名）
        - 移类型/接口/ChatAuthorizationService + customerPriceSnapshotFromActivePrice
        - 2 个 (s *ChatCompletionService) release helper 留 chatcompletions，改引用 lifecycle 类型
        - chat_completion/chat_stream/service.go/settlement 引用改 lifecycle.ChatAuthorization*
        - bootstrap gateway.NewChatAuthorizationService → lifecycle
        - 移 chat_authorization_test.go → lifecycle/authorization_test.go
        - 全量 go test（带 DB）+ vet + diff --check

Step 2  settlement → lifecycle/settlement.go（保留命名）
        - 移 ChatSettlementService/Params/接口/校验/numeric helper + ChatSettlementExecutor 接口
        - chat_completion/chat_stream/service.go 引用改 lifecycle.ChatSettlement*
        - bootstrap gateway.NewChatSettlementService → lifecycle
        - 移 settlement 测试（先确认 helper 依赖，见 §7）
        - 全量全绿

Step 3  recovery → lifecycle/settlement_recovery.go
        - 移 store/service/executor + IsChatSettlementRecoveryScheduled
        - worker_server.go import 改 lifecycle，依赖方向修正
        - chatcompletions 的 settlementOutcomeFromErr/endSettlementSpan 改调 lifecycle.IsChatSettlementRecoveryScheduled
        - 移 recovery 测试
        - 全量全绿

Step 4  收尾：确认 chatcompletions 仅余「OpenAI 编排骨架 + DTO map + 2 个 release helper」
        - 更新 STATUS/PLAN：叶子抽取全部完成，lifecycle 持有完整共享账务子系统
```

完成后，`lifecycle` 已持有 Anthropic service 编排所需的全部共享能力：
candidates / registry / retry / breaker / metrics / tracing / authorization / settlement / recovery。
随后即可进入 **Anthropic messages service**（骨架镜像 OpenAI + 复用 lifecycle）→ handler → router → bootstrap。

## 9. 风险与回归重点

1. **账务幂等**：`ensureSettlement*` 系列校验（request/price/cost/reservation/usage/usage-line-item/write-off）
   是 settlement replay 安全的核心；移动后必须跑 `chat_settlement_test`（idempotency、different usage/cost、
   write-off、release/capture、recovery scheduled）全绿，确认行为零变化。
2. **numeric helper**：`chatSettlement*Numeric` 系列涉及金额比较；移动不得改实现。
3. **DB 集成测试 helper 依赖**（§7）：settlement 测试需要建表/seed，迁移包时确认 helper 可用。
4. **每步独立全绿**：authorization → settlement → recovery 三步分别提交验证，禁止三步混在一次大改后才编译。
5. **命名过渡态**：本批保留 `Chat*` 命名，去前缀在 10.15；不要在本批夹带重命名。
```
