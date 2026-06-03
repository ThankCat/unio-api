# Phase 10 Status

状态：done（2026-06-03 acceptance sign-off）

## 阶段判断

Phase 10 已完成 ADR、范围冻结、双协议 adapter 纵向实现与共享 lifecycle 全部接线；
DEC-012 协议为先 Drop（双侧）、TASK-10.12（facts schema + 账务测试回归）、TASK-10.10B-2b（Anthropic service/handler + 共享 attempt/delivery lifecycle，含 e2e parity）、TASK-10.11（双协议错误与安全输出）、TASK-10.15（命名/冗余复核）、TASK-10.13（OpenAI SDK 黑盒验收）、TASK-10.14（Anthropic SDK 黑盒验收）、TASK-10.10 durable closeout 终局与 **TASK-10.05 架构 B 终局泛型化（thin wrapper 收口，2026-06-02）** 均已收口。Phase 10 双协议公开 Gateway 已完成全栈实现 + 共享 lifecycle + SDK 黑盒回归 + durable closeout；2026-06-03 acceptance sign-off 已通过。可选回归（DS-OAI-05/06/08、DS-ANT-15、tools 多轮 reasoning 回传等）不作为 Phase 10 关闭阻断，排到后续回归窗口。
改造计划、架构边界、协议字段矩阵、DeepSeek 双协议 mapping 和验收标准已冻结；
DeepSeek Anthropic mapping 已按 2026-06-01 官方兼容表刷新，并保存带来源日期的项目内
参考摘要。

本阶段不是局部补丁。关闭前必须完成 OpenAI Chat Completions Create 与 Anthropic Messages Create 两个公开操作的全量对话链路。
这里的“全量”指 ingress 字段识别、校验、响应翻译、usage、错误和账务事实完整；provider 无法转换的字段按 [DEC-012](../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略) **Drop**，不因 provider 能力 ingress 400。

## DEC-012 落地进度（2026-06-02）

已接受 **DEC-012 协议为先与 Provider 映射 Drop 策略**，并同步更新文档：
`DEEPSEEK_OPENAI_MAPPING.md` / `DEEPSEEK_ANTHROPIC_MAPPING.md`（Reject/No-op/Ignored → Drop）、
`PLAN.md` / `ACCEPTANCE.md` / `ARCHITECTURE.md` / 双协议字段矩阵 / `AGENTS.md`。

**生产代码——OpenAI 与 Anthropic 双侧已落地**：

OpenAI 侧：

- `adapter/openai/deepseek`：删除 `reject.go`，新增 `drop.go`（`dropUnsupported`：typed Drop + Extensions 白名单 `{thinking,logprobs,top_logprobs}` + 多模态 content part 剔除）；`adapter.go` 注入 `*slog.Logger`，调用上游前 Drop 并以脱敏 warn 记录 `dropped_request_fields`（仅字段名）；`tokenizer.go` 基于 Drop 后 wire 估算。
- `gatewayapi/openai`：移除 `chatRequestRejectError` 与 `rejectedChatCompletionFields`（`service_tier`/`store`/`web_search_options` 改为进 Extensions）；`openai_content.go` 多模态 part 放行（仅保留结构级 400）。

Anthropic 侧：

- `adapter/anthropic/deepseek`：删除 `reject.go`，新增 `drop.go`（`dropUnsupported`：`top_k` typed Drop + 不支持 content block 剔除 + 内置 server tool 剔除 + metadata 只留 user_id + Extensions 删 `container`/`service_tier`/`inference_geo`/`mcp_servers` + `output_config` 保留 effort 剔 format）；`adapter.go` 注入 logger 并脱敏 log。
- `gatewayapi/anthropic/messages`：移除 `messageRequestRejectError` 与 `mcp_servers` ingress reject；并修正现存 ingress silent drop——`service_tier`/`container`/`inference_geo`/`output_config`（known 列表但无 struct 字段，原本被静默丢弃）移出 known、改为进 Extensions，由 adapter 出站 Drop。`output_config.effort` 因此首次能正确随 Extensions merge 进 upstream wire。

公共：

- `bootstrap`：`NewAdapterRegistry(client, logger)` 注入 logger，传给两侧 DeepSeek adapter。
- 测试：两侧 `reject_test`→`drop_test`；decode/content/messages 测试改为断言「放行进 Extensions / part·block 保留」；两侧黑盒 `NewAdapter` 签名同步。
- 验证：`go build ./internal/... ./cmd/...`、`go vet`、`adapter/** + gatewayapi/** + bootstrap` 包测试全绿。

**采用 provider wrapper Drop 实现**：通用 `openai.Adapter` / `anthropic.Adapter` wire builder 保持基线能力，DeepSeek adapter 在调用 base 前预清理请求，实现 provider 出站白名单与脱敏审计。该形态已通过双侧单测与 SDK 黑盒，不再作为 Phase 10 阻断。

**已知边界（不阻塞 Phase 10 关闭）**：

- `gatewayapi/openai` handler 的 `CodeAdapterRequestUnsupported` 分支暂保留（防御性，DeepSeek 路径已不再触发）。
- `dropped_request_fields` 审计目前仅 structured log；后续若产品需要后台检索，再单独评估是否进 `request_attempt`。
- nested 细粒度 Drop（如 `cache_control`/`citations`/`thinking.budget_tokens`/`tool_result.is_error`/`tool_choice.disable_parallel_tool_use`）两侧均未拆，保持与 OpenAI 侧同粒度，留待后续。

## 任务状态

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-10.01 | done | DEC-010 / DEC-011、双协议架构、ResponseFacts、DeepSeek 双协议 tokenizer 独立实现边界与范围冻结已完成；DeepSeek Anthropic mapping 已按 2026-06-01 官方兼容表刷新。 |
| TASK-10.02 | done | 行为不变目录迁移已完成：adapter 层、gatewayapi 层、service 层三层 OpenAI 协议分离已落到 `core/adapter/openai`、`app/gatewayapi/openai/{chatcompletions,models}`、`service/gateway/openai/chatcompletions`；协议无关 lifecycle 已在 TASK-10.05 收口，`streamtranslate` 已随 TASK-10.07 删除。 |
| TASK-10.03 | done | `channels` 增加 `protocol`/`adapter_key`、删除 `providers.adapter` runtime、`FindRouteCandidates` 按 ingress `protocol` 过滤并改用 `channels.adapter_key`、seed 同步；已通过本地库 down→改→up 与 DB 集成测试（含协议路由过滤用例）。10.10B-1 已新增共享 `lifecycle.AdapterRegistry` facade，按 `(protocol, adapter_key)` 查询三能力并保持 SQL 顺序过滤候选；bootstrap preflight 按复合绑定存在性校验。10.10B-2a 已让 executor 消费 capability 过滤结果与候选级估算 closure。 |
| TASK-10.04 | done | 协议无关事实契约、schema、sqlc 与账务回归均已完成：`ResponseFacts`、`FinishFacts`、`StreamOutcome`、`usage.Facts` 已落地；OpenAI 与 Anthropic adapter 均产出 facts；`usage_records` 协议无关化并新增 `usage_line_items`；request/attempt/recovery/price/cost schema 已扩展；settlement/recovery 只消费 immutable `ResponseFacts`/`usage.Facts`；10.12B 已补齐 sqlc/集成/账务测试并全绿。 |
| TASK-10.05 | done | 共享 Lifecycle Executor 架构 B 终局：`AdapterRegistry` + `Executor.PrepareCandidates`（10.10B-2a）+ 四批叶子抽取（retry classifier / channel breaker / metrics+tracing / settlement+recovery+authorization）+ Anthropic 接线 + **2026-06-02 thin wrapper 终局收口**全部完成。**终局收口**：新建 `lifecycle/request_lifecycle.go`（`RequestLifecycle` struct + 14 个协议无关 helper），承载 channel breaker 只读判定与状态记录、authorization release（含脱离客户端取消 ctx 的 5s 补偿窗口）、5 类业务指标上报、request log 创建/推进/失败/取消（统一写入构造时注入的 `IngressProtocol` + `Operation`，错误三元 facts `FailureCodeOrFallback` × `safeMessageFor` × `InternalErrorDetail`）。协议族 ad-hoc string code 文案通过构造时注入的 `SafeMessage` 闭包提供（返回非空命中、空串 fall-through `BaseSafeRequestLogErrorMessage`）。同步 hoist `settlementOutcomeFromErr` → `lifecycle.SettlementOutcomeFromErr`。两侧 `ChatCompletionService` / `MessagesService` 增加 `lifecycle *lifecycle.RequestLifecycle` 字段，构造函数签名不变（测试零改动）；wrapper 文件 `channel_breaker.go` / `chat(message)_authorization.go` / `chat(message)_metrics.go` / `chat(message)_request_record.go` 全部改为 1-line forward，保留协议族命名（`createMessageRequestRecord` vs `createRequestRecord` / `recordMessageRequest` vs `recordChatRequest`）让编排骨架可读性 + 调用点零改动。RequestLifecycle 纯只读/指标方法支持 nil receiver（等价于「未启用熔断/不采集指标」），保留 `prepareChatCandidates` 单元测试用字面量构造 service 的灵活性。**收益**：原本两侧字面相同的 ~290 行 wrapper 实现合并为单一份共享实现 + 两侧 ~100 行 forward stub，消除「两份字面相同方法体」长期维护风险；新增协议族只需提供协议常量 + ad-hoc safeMessage 闭包即可复用。**验证**：`go vet ./...`（除既存 seed/ 双 main 与本任务无关）+ `go build` + `go test ./internal/...` 全绿 + `go test -tags=blackbox -count=1 ./internal/blackbox/...`（OpenAI SDK 16 用例 + Anthropic SDK 14 用例全 PASS）+ `git diff --check` 干净。原 `TestRequestLogErrorFactsSeparateSafeMessageAndInternalDetail` 改为通过 `lifecycle.NewRequestLifecycle` + `MarkRequestFailed` + `fakeRequestLogService` 三段断言，行为覆盖语义不变。 |
| TASK-10.06 | done | OpenAI Chat Completions 全量字段契约已完成：message content union 结构化校验、请求顶层字段 typed 全量建模、`stream_options.include_obfuscation` 补建模、`n`/`top_logprobs` 等协议硬校验、非流式响应全字段贯通、流式 chunk/delta 全字段贯通、`created` 透传优先级修复均已落地。provider 不支持的合法协议字段按 DEC-012 在 adapter 出站 Drop，不作为 ingress 400。usage 细节已经由 10.12 facts schema 与 usage line item 收口。 |
| TASK-10.07 | done | DeepSeek OpenAI adapter 全量转换已完成并对真实上游验证：请求 typed 字段贯通、`function_call`/`functions`/`user` 语义 Adapt、provider 出站 Drop+审计、非流式/流式响应全字段贯通、usage→`ResponseFacts`、错误分类、独立 tokenizer 与 DS-OAI-14 校准均已完成。DS-OAI-03/04/07/09/14 已补全黑盒；DS-OAI-05/06/08 仅作为可选回归，不阻塞 Phase 10 关闭。 |
| TASK-10.08 | done（ingress 契约层） | Anthropic Messages 全量字段入口已落地并单测：`app/gatewayapi/anthropic/messages`（DTO + 双轨 decode + `mcp_servers` Reject）、顶层校验（model/max_tokens/messages/role/范围）、content union 结构化校验（string shorthand + 已登记 block 类型，未知类型 400）、system/thinking/tool_choice/tools union 校验、非流式 `MessageResponse` + 强类型 `MessageUsage`、命名 SSE 事件 DTO + 帧编码器、`app/gatewayapi/anthropic` 共享 Anthropic error shape。HTTP handler 与 header 校验随 10.10 接线。 |
| TASK-10.09 | done（adapter 核心，实时黑盒通过） | DeepSeek Anthropic adapter 已对真实上游端到端验证通过。已完成：DS-ANT 全表黑盒冻结（usage 五字段、thinking+signature、流式事件序、image silent-ignore、OpenAI 风格错误体，所有 `Verify` 清零）；`core/adapter/anthropic` 契约层（`MessagesAdapter`/`StreamMessagesAdapter`/`MessagesInputTokenizer`、内部 `MessageRequest/Response/StreamEvent`、`MessageUsage.ToUsageFacts()`、`anthropicFinishClass`+`ResponseFacts`、三能力 `Registry`）；通用 `anthropic.Adapter` base（wire 编码、HTTP、响应解析、SSE 翻译、错误分类）；`adapter/anthropic/deepseek`（reject 不支持 content block / server tool / 误导 ignored 字段 + 保守 tokenizer）；单测 + DS-ANT-01/02/09 实时黑盒回归全绿（gate 在 `DEEPSEEK_BLACKBOX=1`）。响应 content block typed 化、server_tool_use/web_search 黑盒、DS-ANT-15 tokenizer 校准列为后续非阻断回归。 |
| TASK-10.10 | done | 10.10A stream adapter 契约：OpenAI / Anthropic 流式接口统一返回 `StreamOutcome`；OpenAI 截留 `[DONE]`，Anthropic 截留 `message_stop`、合并 `message_start` + `message_delta` usage 并生成最终 `ResponseFacts`；有可靠 usage 的 terminal 前断尾仍返回 facts + 稳定错误。10.10B-1：共享 registry facade、DeepSeek 双协议 bootstrap 注册、复合绑定 preflight 和 routing 非法协议防护。10.10B-2a：共享候选准备 executor 与 OpenAI 接线。**10.10B-2b done**：Anthropic Messages service (`service/gateway/anthropic/messages`：非流式 `messages.go` + 流式 `message_stream.go`) 完整编排共享 lifecycle，app handler (`app/gatewayapi/anthropic/messages/handler.go`) + `/v1/messages` 路由 + bootstrap `NewMessagesGateway` 全接线；service 编排单测 + handler 集成测试 + `messages_parity_e2e_test.go` 全绿。**Durable closeout 终局（2026-06-02）done**：复核 `RecoverableChatSettlementExecutor` 双轨语义（成功路径同步推 succeeded、失败路径 `ErrChatSettlementRecoveryScheduled` 返回客户成功响应但留 running 等 worker 重放）；item 10（recovery facts 不可持久化时不输出成功终态）和 item 11（客户端断开后账务收口用 `WithTimeout(WithoutCancel(ctx), 5s)`）已实现；黑盒实证：修复 `AddFallbackChannel` fixture 同步插入 `channel_cost_prices`（与生产 admin 配置硬约定一致），OAI-SDK-Mock-10 fallback 收紧断言为「`request_records.status=succeeded` + `final_channel_id=secondary` + 1 条 `cost_snapshots` + 1 条 `settlement_recovery_jobs` 且 status=succeeded」，任何 running 终态或 pending recovery job 视为 durable closeout 回归。`go vet`、`go build ./cmd/...`、`git diff --check`、`go test -tags=blackbox -count=1 ./internal/blackbox/...` 全绿。 |
| TASK-10.11 | done | 双协议错误与安全输出。两侧 handler 已消费 adapter 稳定上游分类 `UpstreamErrorCategory`（不再把上游错误压成 500），按一致 HTTP status 策略渲染各自协议原生错误：rate_limit→429、timeout→504、bad_request→400、auth/permission/server/unknown→502。**安全语义**：upstream auth/permission 是平台 channel 凭据问题，绝不渲染成 401/authentication_error，避免客户误判自己 key 失效。Anthropic 流式 `event: error` 改为复用非流式分类映射（不再写死 `api_error`），与 OpenAI data-only error chunk 对齐。原始上游 body 不直接返回客户（公开 message 全为固定安全文案）。安全验收 4（按 **DEC-013** 修订）：`anthropic-beta` 改为宽进——一律接受不再 400，按 DEC-012 在 provider 映射层 Drop（DeepSeek 忽略且出站不发送），handler 注入 logger 并脱敏 Debug 审计 `dropped_beta_headers`，绝不假装 beta 生效。理由：drop-in 兼容（真实 Anthropic SDK/Claude Code 默认带 beta）+ 与 DEC-012 一致 + Anthropic beta 列表更新快；对照 OpenRouter（`x-anthropic-beta` pass-through supported / strip unsupported，同样不 400）。测试：`openai/chat_upstream_error_test.go` + `anthropic/messages/error_security_test.go`（各含非流式 7 分类表 + 流式 tail error 映射；Anthropic 另含 beta 任意值/逗号列表均接受）。`go vet`、相关包测试、`git diff --check` 全绿。 |
| TASK-10.12 | done | **10.12A**：源 migration 已扩展 `usage_records`（协议无关 token + state）、`usage_line_items`、`request_records.ingress_protocol`/`operation`、attempt/recovery/prices/cost 的 facts 列；`chat_settlement.go`/`chat_settlement_recovery.go` 只消费 `ResponseFacts`/`usage.Facts`；`billing` 计算器接 `usage.Facts`。**10.12B（2026-06-02 完成）**：本地库 `drop`→`up` 重建到新 schema、`sqlc generate` 无 drift；对齐全部测试——`chat_settlement_test`（usage facts 列断言、`ensureSettlementUsageMatches(row,facts)`、cost snapshot tamper 列名）、`service_test`/`openai_parity_e2e_test`（`fakeChatAdapter` 合成 `StreamOutcome.Facts`、`chatResponse` 产出 `ResponseFacts`、断言改 `Facts.Usage.*`/`Facts.Metadata`）、`ledger`（request_records insert 补 `ingress_protocol`+`operation`）、`sqlc`（usage line item 用例拆为独立事务避免 25P02）。带 `DATABASE_URL` 的 `go test ./internal/...`、`go vet`、`git diff --check` 全绿。 |
| TASK-10.13 | done | OpenAI SDK 黑盒验收（2026-06-02）。`openai-go v1.12.0` 通过 `//go:build blackbox` 完全隔离，生产二进制零残留（`go build ./cmd/...` 不引入 SDK）。共享 fixture `internal/blackbox/sdkfixture/` 装完整 unio gateway httptest server + 真实 PostgreSQL + Redis + 一份可用 user/project/api_key/channel/model/price/cost/balance/credential。`internal/blackbox/openaisdk/` 覆盖 **16 个用例**：14 个 mock 上游（非流式/流式 text、reasoning 非流/流、tools 多轮、`response_format=json_object`、DEC-012 不支持字段 Drop、错误映射 5 分类含 upstream 401→502 不渲染成客户 401、settlement DB 事实链路 6 表、fallback durable closeout 现状校验）+ 2 个真实 DeepSeek smoke（gate `DEEPSEEK_BLACKBOX=1` + `DEEPSEEK_API_KEY`）。fallback 用例发现 settle 在多 attempt 场景下走 `RecoverableChatSettlementExecutor` recovery 路径（这是 GAP-7-007 设计行为，不阻塞客户响应），客户视角的终态推进缺口归 **TASK-10.10 durable closeout** 收口。`go vet ./cmd/... ./internal/...`、`go build ./cmd/...`、`git diff --check` 全绿；`go test -tags=blackbox -count=1 ./internal/blackbox/openaisdk/...` 全 16 用例 PASS。 |
| TASK-10.14 | done | Anthropic SDK 黑盒验收（2026-06-02）。`anthropic-sdk-go v1.46.0` 通过 `//go:build blackbox` 完全隔离，生产二进制零残留。共享 fixture 已扩展 `Protocol="anthropic"` + `AdapterKey="deepseek"` 模式与 `AnthropicBaseURL = Server.URL + "/"`（匹配 anthropic-sdk-go 的 base 期望，SDK 内部自己拼 `v1/messages`），channel.base_url 真实模式指向 `https://api.deepseek.com/anthropic`。`internal/blackbox/anthropicsdk/` 覆盖 **14 个用例**：12 个 mock 上游 + 2 个真实 DeepSeek smoke。**12 mock**：非流式（nonstream_test）、流式 named-event 序（stream_test）、thinking content block（thinking_test，验 SDK `ContentBlockUnion.AsThinking()`）、tool_use 多轮（tools_test，验 tool_use_id 双向贯通）、**DEC-012 Drop + DEC-013 anthropic-beta 出站 Drop**（drop_test，验 `service_tier/mcp_servers/top_k/output_config.format` 在上游 body 不存在但 `output_config.effort` 保留、upstream headers 不含 `anthropic-beta` 但含 adapter 强制写入的 `anthropic-version`/`x-api-key`）、错误映射 5 分类（errors_test：上游 429→客户 429 rate_limit_error；上游 400→客户 400 invalid_request_error；**上游 401→客户 502 api_error**+原文不透传；上游 500→客户 502 api_error；上游 timeout→客户 504 api_error）、settlement DB 事实链路 6 表（settlement_test：`request_records.ingress_protocol=anthropic`/`operation=messages`、`request_attempts.upstream_protocol=anthropic`、`usage_records.input_tokens→uncached_input_tokens`/`output_tokens→output_tokens_total`、`ledger_entries` debit、`price_snapshots`/`cost_snapshots` 各一条）。**2 real**：真实 DeepSeek Anthropic endpoint 非流/流式 smoke，发现 DeepSeek 默认开 thinking mode 需 `MaxTokens >= 256` 才能让 final text 完整输出。fallback 走共享 lifecycle（行为协议无关），不在 Anthropic 侧重复建第二 channel；fallback 路径 durable closeout 归 **TASK-10.10** 收口。`go vet`、`go build ./cmd/...`、`git diff --check` 全绿；`go test -tags=blackbox -count=1 ./internal/blackbox/anthropicsdk/...` 全 14 用例 + OpenAI SDK 16 用例回归 PASS。 |
| TASK-10.15 | done | 文档、命名与冗余复核（2026-06-02）。**9 项检查 7 项干净**：协议族包名层级（openai/+anthropic/ 各自 deepseek/ 子包）、streamtranslate 移除（仅留死注释引用）、common/util/helper 反模式（零命中）、矩阵 vs 代码一致（OpenAI §2 typed 全量、Anthropic §4 走 typed+Extensions+adapter Drop）、DeepSeek mapping vs 黑盒（DS-OAI/DS-ANT Verify 清零）、DTO 不跨层泄漏（core/adapter 不引 gatewayapi/sqlc，gatewayapi 不引 sqlc，deepseek/* 不外泄）、build/vet/diff-check + 非 DB 测试全绿。**修复 2 项**：①protocol-agnostic helpers hoist：`endSettlementSpan` → `lifecycle/tracing.go::EndSettlementSpan`；request-log 协议无关函数（`FailureCodeOrFallback`/`InternalErrorDetail`/`RoutingFailureCode`/`BaseSafeRequestLogErrorMessage` + `MaxRequestLogInternalErrorDetailBytes`）→ 新 `lifecycle/request_log.go`，两侧 service 保留薄 wrapper（service-specific ad-hoc string code），fall-through 到 lifecycle；纯协议无关测试迁到 `lifecycle/request_log_test.go`。②**gatewayapi/openai 目录对称重组**：原 `app/gatewayapi/openai/` 平铺 14 个文件，与 `app/gatewayapi/anthropic/messages/` 按 operation 子包的结构不对称；本次拆为 `openai/chatcompletions/`（12 个 chat 文件，文件名同步去 `openai_`/`chat_` 前缀）+ `openai/models/`（2 个 models 文件）+ `openai/doc.go`（协议族包说明，明确"协议族根包不允许平铺单 operation"硬规则）；router/bootstrap/service 共 16 处 import 调整为新子包路径，router 用 `gatewaychat`/`gatewaymodels` 双别名同时引用。**剩余复制清单挂到 10.05 架构 B 终局**：channel_breaker/chat(message)_authorization/chat(message)_metrics 各对仍是 thin wrapper，受 service 字段制约不能 hoist，等架构 B 泛型化时一并收。**文档同步**：PROJECT_STRUCTURE.md 写入"协议族 → operation 子包"长期规范与三条硬规则。 |

## 已识别的现有迁移点

| 当前实现 | Phase 10 目标 | 状态 |
| --- | --- | --- |
| `internal/app/gatewayapi` OpenAI 文件平铺 | `internal/app/gatewayapi/openai` | done（TASK-10.02） |
| `internal/service/gateway/chat_*` | `lifecycle` + `openai/chatcompletions` + `anthropic/messages` | done（编排分列）：双协议编排骨架已分列；`lifecycle` 持有完整共享账务子系统；messages 编排单测 + `/v1/messages` router/handler 集成测试 + 真实 adapter↔mock 上游 parity e2e 已补；DeepSeek Anthropic 真实上游回归由 adapter 级 blackbox 覆盖。 |
| OpenAI 偏向的 `usage_records.prompt_tokens` 等列 | 协议无关 `usage_records` + `usage_line_items` | done（10.12B）：schema、sqlc、settlement/recovery、账务集成测试均已对齐。 |
| `internal/core/adapter/chat.go` 的 OpenAI 语义 DTO | `internal/core/adapter/openai` | done（TASK-10.02） |
| `internal/core/adapter/openai/streamtranslate` | 基线翻译内联进 `openai.Adapter`（无 DeepSeek 专属差异，暂不建 `deepseek/stream.go`） | done（Lesson 4，过渡包已删除） |
| `providers.adapter` | 删除 runtime 职责；routing/preflight/seed 改用 `channels.adapter_key`，`routing.ChatRouteCandidate.AdapterKey` 来自 `channels.adapter_key` | done（TASK-10.03） |
| OpenAI 偏向的 `ChatUsage` | 协议无关 `usage.Facts` | done（Phase 10 边界）：settlement/recovery 已按 `usage.Facts` 计费并持久化；`ChatUsage` 仅作为 OpenAI 协议族内部响应解析、公开 usage DTO 映射与测试 helper 使用，不再作为账务事实输入。若未来要彻底移出根包，属于命名整理，不阻塞 Phase 10。 |
| OpenAI 偏向的 `adapter.ChatInputTokenizer` | `openai.ChatInputTokenizer` + `anthropic.MessagesInputTokenizer` | done：OpenAI 侧 `openai.ChatInputTokenizer`（10.02），DeepSeek OpenAI tokenizer 已 DS-OAI-14 实时校准（恒 ≥ 上游 prompt_tokens，ratio≈1.8~2.75，保守达标）；Anthropic 侧 `anthropic.MessagesInputTokenizer` 已定义并由 `adapter/anthropic/deepseek` 实现（10.09，保守启发式，DS-ANT-15 待校准） |
| Anthropic 协议族 adapter（不存在） | `core/adapter/anthropic` + `core/adapter/anthropic/deepseek` | done（TASK-10.09）：契约层 + 通用 base + DeepSeek 实现，实时黑盒回归通过 |

## 验收签核结果

Phase 10 已于 2026-06-03 完成 acceptance sign-off；本次未修改生产代码。已确认：

1. `docs/production/RELEASE_BLOCKERS.md` 仍无 P0 release blocker。
2. `TODO_REGISTER.md` 中没有 Phase 10 相关 P0/P1 open GAP；可选回归不登记为 Phase 10 阻断。
3. DeepSeek OpenAI / Anthropic mapping 表格里没有状态列为 `Verify` 的残留。
4. `internal/core/adapter/openai/streamtranslate` 目录不存在。
5. `providers.adapter` 不再参与 runtime schema、routing query、registry、preflight 或 seed；代码注释中允许保留历史迁移说明。
6. `adapter.ChatRequest` / `adapter.ChatResponse` 不再作为根包契约使用；`adapter.ChatUsage` 仅允许作为 OpenAI 协议族内部响应解析、公开 usage DTO 映射与测试 helper，不作为账务事实输入。
7. 最终验收命令通过，本文件状态与 `docs/PROJECT_STATUS.md` 阶段 10 状态已改为 `done`。

本次验收命令：

```bash
sqlc generate
go build ./internal/... ./cmd/...
go vet ./internal/... ./cmd/...
go test ./internal/... ./cmd/...
git diff --check
go test -tags=blackbox -count=1 ./internal/blackbox/openaisdk/...
go test -tags=blackbox -count=1 ./internal/blackbox/anthropicsdk/...
```

说明：本地沙箱禁止 `httptest` 监听端口，`go test ./internal/... ./cmd/...` 与两套 SDK blackbox 已在非沙箱环境重跑并通过。真实 DeepSeek smoke 今天未额外打开 `DEEPSEEK_BLACKBOX` / `DEEPSEEK_API_KEY` gate。

## 下一步

下一节课进入 **Phase 11 后台管理启动前复核**：

1. 复核 `GAP-6-001` credential resolver 与后台安全轮换边界。
2. 复核 admin auth、audit log、provider/channel/model/price CRUD 的业务事实边界。
3. 进入 Phase 11 实现前重新扫描全局 TODO/GAP 和 release blocker。
