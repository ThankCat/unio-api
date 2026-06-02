# Phase 10 Status

状态：partial

## 阶段判断

Phase 10 已完成 ADR、范围冻结、双协议 adapter 纵向实现与共享 lifecycle 全部接线；
DEC-012 协议为先 Drop（双侧）、TASK-10.12（facts schema + 账务测试回归）、TASK-10.10B-2b（Anthropic service/handler + 共享 attempt/delivery lifecycle，含 e2e parity）、TASK-10.11（双协议错误与安全输出）、TASK-10.15（命名/冗余复核）、TASK-10.13（OpenAI SDK 黑盒验收）、TASK-10.14（Anthropic SDK 黑盒验收）、TASK-10.10 durable closeout 终局与 **TASK-10.05 架构 B 终局泛型化（thin wrapper 收口，2026-06-02）** 均已收口。Phase 10 双协议公开 Gateway 已完成全栈实现 + 共享 lifecycle + SDK 黑盒回归 + durable closeout，剩余只有跨任务校准与可选回归（DS-OAI-05/06/08、DS-ANT-15、tools 多轮 reasoning 回传等）和阶段验收 sign-off。
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

**采用「先 A」实现**：通用 `openai.Adapter` / `anthropic.Adapter` wire builder 暂未改，靠 deepseek 调用 base 前预清理请求实现等价出站白名单；B 阶段（TASK-10.07 下沉）再把白名单改为通用 builder 的显式参数。

**待办 / 已知边界**：

- `gatewayapi/openai` handler 的 `CodeAdapterRequestUnsupported` 分支暂保留（防御性，DeepSeek 路径已不再触发）。
- `dropped_request_fields` 审计目前仅 structured log；后续评估是否进 `request_attempt`（schema 改动随 10.12B 之后）。
- nested 细粒度 Drop（如 `cache_control`/`citations`/`thinking.budget_tokens`/`tool_result.is_error`/`tool_choice.disable_parallel_tool_use`）两侧均未拆，保持与 OpenAI 侧同粒度，留待后续。

## 任务状态

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-10.01 | done | DEC-010 / DEC-011、双协议架构、ResponseFacts、DeepSeek 双协议 tokenizer 独立实现边界与范围冻结已完成；DeepSeek Anthropic mapping 已按 2026-06-01 官方兼容表刷新。 |
| TASK-10.02 | partial | 行为不变目录迁移：adapter 层、gatewayapi 层、service 层三层 OpenAI 协议分离已完成（`core/adapter/openai`、`app/gatewayapi/openai`、`service/gateway/openai/chatcompletions`，原 item 1/2/4）。`service/gateway/lifecycle` 已抽出 `AdapterRegistry` facade 与 `Executor.PrepareCandidates`（authorization 前候选准备，10.10B-2a）；完整 attempt/delivery/settlement 抽取仍归 TASK-10.05/10.12A。streamtranslate 收口（item 5）随 TASK-10.07。 |
| TASK-10.03 | done | `channels` 增加 `protocol`/`adapter_key`、删除 `providers.adapter` runtime、`FindRouteCandidates` 按 ingress `protocol` 过滤并改用 `channels.adapter_key`、seed 同步；已通过本地库 down→改→up 与 DB 集成测试（含协议路由过滤用例）。10.10B-1 已新增共享 `lifecycle.AdapterRegistry` facade，按 `(protocol, adapter_key)` 查询三能力并保持 SQL 顺序过滤候选；bootstrap preflight 按复合绑定存在性校验。10.10B-2a 已让 executor 消费 capability 过滤结果与候选级估算 closure。 |
| TASK-10.04 | partial | 协议无关事实契约已落地并单测：`internal/core/usage` 与 `core/adapter`（`ResponseFacts`、`FinishFacts`、`StreamOutcome`）。OpenAI 与 Anthropic adapter 均已产出 facts。10.12A 已改源 migration：`usage_records` 改为协议无关 token 维度 + `CountState`、`usage_line_items` 新表；`request_records`/`request_attempts`/`settlement_recovery_jobs` 与 prices/cost 快照表已扩展 facts 字段。生产 settlement/recovery 已改为只消费 `adapter.ResponseFacts`/`usage.Facts`；10.12B 待补 sqlc/集成/账务单测回归。 |
| TASK-10.05 | done | 共享 Lifecycle Executor 架构 B 终局：`AdapterRegistry` + `Executor.PrepareCandidates`（10.10B-2a）+ 四批叶子抽取（retry classifier / channel breaker / metrics+tracing / settlement+recovery+authorization）+ Anthropic 接线 + **2026-06-02 thin wrapper 终局收口**全部完成。**终局收口**：新建 `lifecycle/request_lifecycle.go`（`RequestLifecycle` struct + 14 个协议无关 helper），承载 channel breaker 只读判定与状态记录、authorization release（含脱离客户端取消 ctx 的 5s 补偿窗口）、5 类业务指标上报、request log 创建/推进/失败/取消（统一写入构造时注入的 `IngressProtocol` + `Operation`，错误三元 facts `FailureCodeOrFallback` × `safeMessageFor` × `InternalErrorDetail`）。协议族 ad-hoc string code 文案通过构造时注入的 `SafeMessage` 闭包提供（返回非空命中、空串 fall-through `BaseSafeRequestLogErrorMessage`）。同步 hoist `settlementOutcomeFromErr` → `lifecycle.SettlementOutcomeFromErr`。两侧 `ChatCompletionService` / `MessagesService` 增加 `lifecycle *lifecycle.RequestLifecycle` 字段，构造函数签名不变（测试零改动）；wrapper 文件 `channel_breaker.go` / `chat(message)_authorization.go` / `chat(message)_metrics.go` / `chat(message)_request_record.go` 全部改为 1-line forward，保留协议族命名（`createMessageRequestRecord` vs `createRequestRecord` / `recordMessageRequest` vs `recordChatRequest`）让编排骨架可读性 + 调用点零改动。RequestLifecycle 纯只读/指标方法支持 nil receiver（等价于「未启用熔断/不采集指标」），保留 `prepareChatCandidates` 单元测试用字面量构造 service 的灵活性。**收益**：原本两侧字面相同的 ~290 行 wrapper 实现合并为单一份共享实现 + 两侧 ~100 行 forward stub，消除「两份字面相同方法体」长期维护风险；新增协议族只需提供协议常量 + ad-hoc safeMessage 闭包即可复用。**验证**：`go vet ./...`（除既存 seed/ 双 main 与本任务无关）+ `go build` + `go test ./internal/...` 全绿 + `go test -tags=blackbox -count=1 ./internal/blackbox/...`（OpenAI SDK 16 用例 + Anthropic SDK 14 用例全 PASS）+ `git diff --check` 干净。原 `TestRequestLogErrorFactsSeparateSafeMessageAndInternalDetail` 改为通过 `lifecycle.NewRequestLifecycle` + `MarkRequestFailed` + `fakeRequestLogService` 三段断言，行为覆盖语义不变。 |
| TASK-10.06 | in_progress | OpenAI Chat Completions 全量字段契约。已完成：message content union 结构化校验（接受 text/refusal 数组，多模态 part 放行，畸形 400）。**请求侧顶层字段 typed 全量 done（Lesson 1，DEC-012 A 方案）**：矩阵 §2 剩余字段（n/seed/logprobs/top_logprobs/logit_bias/modalities/audio/prediction/metadata/store/service_tier/verbosity/prompt_cache_key/prompt_cache_retention/safety_identifier/web_search_options/function_call/functions）全部从 Extensions 提升为 ingress typed 字段并登记 `knownChatCompletionFields`；`stream_options.include_obfuscation` 补建模（修正原 silent drop）；补 `n>=1`、`top_logprobs` 0~20 且依赖 `logprobs=true` 的协议硬校验。**非流式响应侧全量字段贯通 done（Lesson 3）**：顶层 `service_tier`/`system_fingerprint`、choice `logprobs`、message `refusal`/`annotations`/`audio` 三层（wire `chatCompletionResponse`/`chatChoice`/`chatMessage` → adapter `ChatResponse` → gateway DTO + service map）端到端贯通；复杂嵌套（logprobs/annotations/audio）按 RawMessage 透传保真；并修正 `created` 一直被本地时间覆盖的 parity bug（现优先透传上游 created，缺失才回退 now）。**流式 chunk/delta 全量字段贯通 done（Lesson 4）**：chunk 顶层 `created`/`service_tier`/`system_fingerprint`、choice `index`/`logprobs`、delta `refusal`/`function_call` 三层端到端贯通；流式 `created` 同样改为优先透传上游、缺失才回退本地时间。待办：usage token 明细补全（audio/accepted/rejected prediction tokens）随 10.12 billing facts。 |
| TASK-10.07 | in_progress（核心已通） | DeepSeek OpenAI adapter 核心已对真实上游端到端验证通过。已完成：`Verify` 黑盒冻结；`adapter/openai/deepseek`（包装 `openai.Adapter`，DEC-012 改 Reject→Drop）；usage 经黑盒确认 `cached_tokens=prompt_cache_hit_tokens` 已精确；产出 `ResponseFacts`；注册键 `deepseek`、seed 同步；DS-OAI-01/02 黑盒回归（gate 在 `DEEPSEEK_BLACKBOX=1`）通过。**请求侧 typed 字段贯通 done（Lesson 1）**：新 typed 规范字段全量进入 adapter 契约 `openai.ChatRequest` + wire builder（未来真 OpenAI provider 可直接 Pass）；DeepSeek `drop.go` 按 mapping §2 对其逐字段 Drop+审计（`logprobs`/`top_logprobs` 保留 Pass）。**请求侧语义 Adapt done（Lesson 2）**：新增 `adapt.go`——`function_call`→`tool_choice`（none/auto 透传、`{"name":X}`→`{"type":"function","function":{"name":X}}`，已存在 `tool_choice` 或无法识别则 Drop）、`functions`→现代 function `tools`（已存在 `tools` 或缺 name 则 Drop）；`user`→`user_id`（校验 DeepSeek 字符集 `[a-zA-Z0-9_-]`/长度 ≤512，合法注入 wire、非法 Drop，出站永不发标准 `user`）。**响应侧 full field 透传 done（Lesson 3）**：DeepSeek 包装 base，base 响应解析已贯通 created/service_tier/system_fingerprint/refusal/annotations/audio/logprobs，DeepSeek 路径自动获得；provider 未返回的字段按上游省略。**流式收口 done（Lesson 4）**：删除 `streamtranslate` 过渡包，baseline 流式翻译内联进 `openai.Adapter`；DeepSeek 流式与基线一致（reasoning_content 为已登记扩展由 base 透传），无专属 stream 差异，未来出现再建 `deepseek/stream.go`；流式 chunk/delta 全量字段随 base 贯通。**黑盒回归补全 done（Lesson 5）**：新增 DS-OAI-03（reasoner 非流式 reasoning/content 分离 + facts reasoning tokens）、DS-OAI-04（reasoner 流式 reasoning/content 分离）、DS-OAI-07（logprobs+top_logprobs 真实上游透传，验证 Lesson 3 typed 贯通）、DS-OAI-09（塞满不支持字段仍 200，审计记录 dropped 列表——DEC-012 对真实上游的核心证明）、DS-OAI-14（tokenizer 校准）；全部 gate 在 `DEEPSEEK_BLACKBOX=1`。**DS-OAI-14 校准结论**：本地估算基于 Drop 后 wire JSON + cl100k，实测对 deepseek-chat 恒满足 `estimate ≥ 上游 prompt_tokens`（保守，不低估冻结），ratio 约 1.8（英文段落）~2.75（短句/中文，wire 结构开销与中文分词差异主导）；满足 authorization「可偏保守」目标，过冻结在大 prompt 收敛、非上线阻断。残留：DS-OAI-05（tools 多轮 reasoning 回传）/DS-OAI-06（json_object）为可选回归；DS-OAI-08（cache 命中）非确定性难稳定触发；DS-OAI-10/11（retry/fallback attempt、stream tail/cancel delivery audit）属 lifecycle，归 TASK-10.05/10.10；DS-OAI-12（parallel_tool_calls Drop）/DS-OAI-13（safety_identifier 与 user 处理）已由 drop/adapt 单测 + DS-OAI-09 覆盖。 |
| TASK-10.08 | done（ingress 契约层） | Anthropic Messages 全量字段入口已落地并单测：`app/gatewayapi/anthropic/messages`（DTO + 双轨 decode + `mcp_servers` Reject）、顶层校验（model/max_tokens/messages/role/范围）、content union 结构化校验（string shorthand + 已登记 block 类型，未知类型 400）、system/thinking/tool_choice/tools union 校验、非流式 `MessageResponse` + 强类型 `MessageUsage`、命名 SSE 事件 DTO + 帧编码器、`app/gatewayapi/anthropic` 共享 Anthropic error shape。HTTP handler 与 header 校验随 10.10 接线。 |
| TASK-10.09 | done（adapter 核心，实时黑盒通过） | DeepSeek Anthropic adapter 已对真实上游端到端验证通过。已完成：DS-ANT 全表黑盒冻结（usage 五字段、thinking+signature、流式事件序、image silent-ignore、OpenAI 风格错误体，所有 `Verify` 清零）；`core/adapter/anthropic` 契约层（`MessagesAdapter`/`StreamMessagesAdapter`/`MessagesInputTokenizer`、内部 `MessageRequest/Response/StreamEvent`、`MessageUsage.ToUsageFacts()`、`anthropicFinishClass`+`ResponseFacts`、三能力 `Registry`）；通用 `anthropic.Adapter` base（wire 编码、HTTP、响应解析、SSE 翻译、错误分类）；`adapter/anthropic/deepseek`（reject 不支持 content block / server tool / 误导 ignored 字段 + 保守 tokenizer）；单测 + DS-ANT-01/02/09 实时黑盒回归全绿（gate 在 `DEEPSEEK_BLACKBOX=1`）。待办（并入后续）：响应 content block typed 化、server_tool_use/web_search 黑盒、DS-ANT-15 tokenizer 校准。 |
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
| OpenAI 偏向的 `usage_records.prompt_tokens` 等列 | 协议无关 `usage_records` + `usage_line_items` | partial（10.12A）：schema 与 settlement 写入路径已改；测试与 sqlc helper 待 10.12B |
| `internal/core/adapter/chat.go` 的 OpenAI 语义 DTO | `internal/core/adapter/openai` | done（TASK-10.02） |
| `internal/core/adapter/openai/streamtranslate` | 基线翻译内联进 `openai.Adapter`（无 DeepSeek 专属差异，暂不建 `deepseek/stream.go`） | done（Lesson 4，过渡包已删除） |
| `providers.adapter` | 删除 runtime 职责；routing/preflight/seed 改用 `channels.adapter_key`，`routing.ChatRouteCandidate.AdapterKey` 来自 `channels.adapter_key` | done（TASK-10.03） |
| OpenAI 偏向的 `ChatUsage` | 协议无关 `usage.Facts` | partial（10.12A）：settlement/recovery 已按 `usage.Facts` 计费并持久化；`ChatUsage` 仍留 `core/adapter` 根包供 adapter/stream 翻译，待测试与公开响应路径全部切 facts 后评估移除 |
| OpenAI 偏向的 `adapter.ChatInputTokenizer` | `openai.ChatInputTokenizer` + `anthropic.MessagesInputTokenizer` | done：OpenAI 侧 `openai.ChatInputTokenizer`（10.02），DeepSeek OpenAI tokenizer 已 DS-OAI-14 实时校准（恒 ≥ 上游 prompt_tokens，ratio≈1.8~2.75，保守达标）；Anthropic 侧 `anthropic.MessagesInputTokenizer` 已定义并由 `adapter/anthropic/deepseek` 实现（10.09，保守启发式，DS-ANT-15 待校准） |
| Anthropic 协议族 adapter（不存在） | `core/adapter/anthropic` + `core/adapter/anthropic/deepseek` | done（TASK-10.09）：契约层 + 通用 base + DeepSeek 实现，实时黑盒回归通过 |

## 进入实现前

两个协议族的 ingress 契约与 adapter 已分别就位：
- OpenAI：`core/adapter/openai` + `app/gatewayapi/openai` + `service/gateway/openai/chatcompletions`，DeepSeek OpenAI adapter 实时黑盒通过。
- Anthropic：`app/gatewayapi/anthropic`（messages ingress + 共享 error）+ `core/adapter/anthropic`（契约层 + 通用 base）+ `core/adapter/anthropic/deepseek`，DeepSeek Anthropic adapter 实时黑盒通过。

TASK-10.10A 已完成双协议 stream adapter 契约收口：

- OpenAI `StreamChatCompletions` 与 Anthropic `StreamMessages` 都返回 `adapter.StreamOutcome`。
- OpenAI adapter 截留 upstream `[DONE]`；Anthropic adapter 截留 upstream `message_stop`。
- Anthropic adapter 合并 `message_start` 初始 usage 与 `message_delta` 最终 usage，只有累计输入、
  输出 token 都可靠时才生成最终 `ResponseFacts`。
- 两侧 adapter 在可靠 usage 已到达但成功终态前断尾时返回 `facts + error`，让后续 lifecycle
  settlement 后记录 delivery interrupted，而不是把已产生成本请求当成普通失败。

TASK-10.10B-1 已完成共享 registry 与启动接线：

- 新增 `service/gateway/lifecycle.AdapterRegistry` facade，按 `(protocol, adapter_key)` 查询
  non-stream / stream / input tokenizer 能力，并保持 SQL routing 顺序过滤候选。
- bootstrap 同时注册 DeepSeek OpenAI 与 Anthropic adapter；preflight 校验启用 channel 的
  复合绑定存在，不强制一个 channel 同时实现全部 operation 能力。
- routing 增加 `ProtocolAnthropic`，并在查库前拒绝空或非法 ingress protocol。

TASK-10.10B-2a 已完成 authorization 前候选准备段：

- 新增 `lifecycle.Executor.PrepareCandidates`，保持 SQL route 顺序，按 operation capability
  与只读熔断可用性过滤，并为每个可用 fallback candidate 调用协议 service 提供的估算 closure。
- OpenAI 非流式与流式链路都改为 attempt 前取候选最大输入 token 估算、冻结一次，再创建
  每个真实 attempt；熔断竞争导致全部候选失效和 attempt 写库失败都会释放 reservation。
- OpenAI tokenizer 改为消费完整 `openai.ChatRequest` wire，覆盖 messages、tools、
  response format 与 vendor extensions；DeepSeek OpenAI 入口落在独立 `deepseek/tokenizer.go`。

TASK-10.12A（进行中，生产代码已落地）：

- 源 migration：`usage_records` 改为协议无关 token 列 + `*_state`；新增 `usage_line_items`；
  `request_records` 增加 `ingress_protocol`；`request_attempts`、`settlement_recovery_jobs`、
  prices/cost 快照表扩展 facts 相关列。
- `chat_settlement.go` / `chat_settlement_recovery.go`：`ChatSettlementParams.Facts` 为唯一
  账务输入；`billing` 按 `usage.Facts` 计算客户扣费与成本；持久化 usage record + line items。
- `chat_stream.go` / `chat_completion.go`：流式与非流式 settlement 消费 `StreamOutcome.Facts` /
  `ChatResponse.Facts`。
- **验证现状（2026-06-02）**：10.12B 与 10.10B-2b 均已收口。`go build ./internal/... ./cmd/...`、
  `go vet ./internal/...`、带 `DATABASE_URL` 的 `go test ./internal/...`（33 包全绿，含
  ledger/sqlc/chatcompletions/messages 集成）、`git diff --check` 全绿。

**10.11 done（2026-06-02）**：双协议公开错误渲染与脱敏已统一——两侧 handler 消费 adapter
稳定上游分类，按一致 status 策略渲染各自协议原生错误，upstream 凭据问题不暴露成 401/
authentication_error，流式错误两协议都按分类映射，`anthropic-beta` 改为 DEC-013 宽进 + 出站 Drop + 脱敏审计。

**10.15 done（2026-06-02）**：阶段关闭前命名与冗余复核完成。**协议无关纯函数 hoist 到 `lifecycle/`**：`endSettlementSpan` → `tracing.EndSettlementSpan`，`failureCodeOrFallback`/`internalErrorDetail`/`routingFailureCode`/`safeRequestLogErrorMessage` 协议无关 case 与 `maxRequestLogInternalErrorDetailBytes` → 新 `lifecycle/request_log.go`；两侧 service 仅保留薄 wrapper（service-specific ad-hoc string code 例如 `chat_settlement_failed`/`messages_settlement_failed`）。**`gatewayapi/openai` 重组**：原平铺 14 文件按 operation 子包拆——`openai/chatcompletions/`（12 个 chat 文件，文件名同步简化去掉 `chat_`/`openai_` 前缀，例如 `chat_completions_handler.go` → `handler.go`，`openai_dto.go` → `dto.go`）+ `openai/models/`（2 个 models 文件）+ `openai/doc.go`（协议族包说明 + 三条硬规则）；与 `gatewayapi/anthropic/messages/` 对称。router/bootstrap/service 共 16 处 import 调整为新子包路径。`PROJECT_STRUCTURE.md` 写入"协议族 → operation 子包"长期规范。**thin wrapper 复制清单已挂到 10.05 并在 2026-06-02 终局收口**：`channel_breaker.go` / `chat(message)_authorization.go` / `chat(message)_metrics.go` / `chat(message)_request_record.go` 各对改为 1-line forward 到 `lifecycle.RequestLifecycle`，实现集中在 `lifecycle/request_lifecycle.go`。

**10.13 done（2026-06-02）**：OpenAI SDK 黑盒验收完成。openai-go v1.12.0 通过 build tag `blackbox` 完全隔离（生产二进制零残留）；共享 fixture `internal/blackbox/sdkfixture/` + `internal/blackbox/openaisdk/` 覆盖 14 个 mock 用例（非流/流式、reasoning、tools 多轮、response_format、Drop、错误映射 5 分类、settlement DB 链路、fallback）+ 2 个真实 DeepSeek smoke（gate `DEEPSEEK_BLACKBOX=1`）。

**10.14 done（2026-06-02）**：Anthropic SDK 黑盒验收完成。anthropic-sdk-go v1.46.0 同样通过 build tag 隔离，`internal/blackbox/anthropicsdk/` 覆盖 12 mock + 2 真实 DeepSeek Anthropic endpoint smoke。

**10.10 durable closeout 终局 done（2026-06-02）**：黑盒实证 + fixture 收口：复核 `RecoverableChatSettlementExecutor` 双轨语义；item 10（recovery facts 不可持久化不输出成功终态）+ item 11（客户端断开后 `WithoutCancel(ctx)+5s` 收口账务）已实现；修 `AddFallbackChannel` 同步插入 `channel_cost_prices` 以匹配生产 admin 配置硬约定；OAI-SDK-Mock-10 收紧为同步终态推进契约——`request_records.status=succeeded` + `final_channel_id=secondary` + `cost_snapshots` 1 条且属 secondary + `settlement_recovery_jobs` 1 条且 status=succeeded。任何 running 终态或 pending recovery job 视为 durable closeout 回归。

**10.05 done（2026-06-02）**：架构 B 终局泛型化完成——抽出 `lifecycle.RequestLifecycle` 集中承载协议无关 channel breaker / authorization release / metrics / request log 全部实现；两侧 service thin wrapper 改为 1-line forward；`SettlementOutcomeFromErr` hoist 到 lifecycle。章节工程主线已全部收口。

下一步：**阶段 10 ACCEPTANCE sign-off** + 可选回归（DS-OAI-05/06/08、DS-ANT-15）+ 阶段 11 启动复核。

DeepSeek 两个 endpoint 的 mapping `Verify` 均已黑盒清零（OpenAI: DEEPSEEK_OPENAI_MAPPING.md；Anthropic: DEEPSEEK_ANTHROPIC_MAPPING.md §8/§14）。

## 交接说明（TASK-10.08 + DS-ANT 冻结 + TASK-10.09 实施轮）

本轮完成 Anthropic 协议族从 ingress 到 adapter 的纵向打通，全程 `go build ./internal/... ./cmd/...`、`go vet`、`go test ./internal/... ./cmd/...`（32 包全绿）、`git diff --check` 全绿；DeepSeek Anthropic adapter 另跑实时黑盒回归全绿。

1. **TASK-10.08 Anthropic ingress 契约层**：`app/gatewayapi/anthropic/messages` 已有 DTO + 双轨 decode（`mcp_servers` Reject）；本轮补齐 content union 结构化校验（`content.go`：string shorthand + 已登记 block 类型，未知类型/缺 type/核心必填缺失 → 400）、system/thinking/tool_choice/tools union 校验（`options.go`：枚举 + 内置 tool 类型集 + custom tool 必填）、非流式 `MessageResponse` + 强类型 `MessageUsage`（`response.go`）、命名 SSE 事件 DTO + `EncodeStreamEvent` 帧编码器（`stream.go`）、`app/gatewayapi/anthropic/response.go` 共享 Anthropic error shape。ingress 只做协议级结构校验，provider 能力级 Reject 留给 adapter。

2. **DS-ANT 黑盒冻结**：对 `https://api.deepseek.com/anthropic/v1/messages` 实测冻结，DEEPSEEK_ANTHROPIC_MAPPING.md 中所有 `Verify` 清零（§8 usage 表、§7 thinking 响应、§9 delta、§4 cache_control/thinking.display、§12 错误体），并新增 §14 黑盒冻结结果。关键发现：usage 固定五字段（无 TTL 拆分/无 thinking 分解/无 inference_geo）；thinking `signature`=message id；流式事件序 `message_start→content_block_start→ping→delta*→content_block_stop→message_delta→message_stop`；**image 被 HTTP 200 静默忽略并伪造文字回复**（必须 pre-flight Reject）；错误体是 **OpenAI 风格信封**（按 HTTP status 分类，网关再渲染 Anthropic shape）；任意 model 名都回 `deepseek-v4-flash`（必须用显式 upstream_model 并恢复 catalog model）。

3. **TASK-10.09 DeepSeek Anthropic adapter**：
   - 契约层 `core/adapter/anthropic`：`MessagesAdapter`/`StreamMessagesAdapter`/`MessagesInputTokenizer` 接口、内部 `MessageRequest/MessageResponse/MessageStreamEvent`、Anthropic 形状 `MessageUsage` + `ToUsageFacts()`（flat cache write 归默认 5m 档、thinking/inference_geo 缺失标 not_applicable）、`anthropicFinishClass` + `ResponseFacts(NonStream/Stream)`、三能力 `Registry`。
   - 通用 base `anthropic.Adapter`：`/v1/messages` 调用（`x-api-key`+`anthropic-version`）、wire 编码（Extensions merge）、非流式响应 typed decode → `MessageResponse`+facts、SSE 解析按命名事件回调（message_delta 附终态 usage+upstream）、错误按 HTTP status 分类（不解析上游 body）。
   - `adapter/anthropic/deepseek`：`reject.go`（不支持 content block / 内置 server tool / metadata 非 user_id / container/service_tier/inference_geo/output_config.format 前置 Reject）+ 保守 `tokenizer.go`（字符启发式高估，DS-ANT-15 待校准）+ 组合 base 的 `adapter.go`。
   - 复用既有 `failure.CodeAdapter*` 协议无关错误码；reject 用 `CodeAdapterRequestUnsupported` + `param` field（与 OpenAI 侧一致，gatewayapi 映射 400）。

4. **TASK-10.10A stream adapter 契约**：OpenAI / Anthropic 流式接口已统一返回 `StreamOutcome`。OpenAI 截留 `[DONE]`；Anthropic 截留 `message_stop`，合并 `message_start` 与 `message_delta` usage 后生成最终 facts；terminal 前断尾仍保留可靠 facts。定向单测覆盖成功终态截留和 tail error。

5. **TASK-10.10B-1 registry / preflight / routing**：共享 `lifecycle.AdapterRegistry` facade 已落地；DeepSeek 双协议 adapter 已同时进入 bootstrap；preflight 校验复合绑定；routing 增加 Anthropic 协议常量与非法协议防护。

6. **TASK-10.10B-2a 候选准备 executor**：共享 executor 已消费 operation capability filter 与候选级估算 closure；OpenAI 非流式/流式统一在 attempt 前生成保守 fallback plan 并冻结一次。OpenAI tokenizer 改为完整 wire 估算，新增 tools 计数、熔断 half-open 只读检查、熔断竞争 release、attempt 创建失败 release 等高风险回归。

7. **10.12A / lifecycle（用户实施轮，见下节）**：facts schema 与 OpenAI settlement/recovery 生产路径已迁移；lifecycle 候选准备已接线。Anthropic HTTP handler 与 10.12B 测试回归仍待办。工作区改动尚未提交。

---

## 交接说明（10.10B-2a + 10.12A 用户实施轮）

本轮在双协议 adapter 已就绪基础上，推进共享 lifecycle 首段与 facts 账务迁移：

1. **`service/gateway/lifecycle`**：`AdapterRegistry` facade（OpenAI + Anthropic 双 registry，按 `(protocol, adapter_key)` 查三能力）；`Executor.PrepareCandidates`（capability 过滤 + 只读熔断 + 候选级 tokenizer closure + 保守最大输入估算）。
2. **Bootstrap / preflight / routing**：`bootstrap.NewAdapterRegistry` 同时注册 DeepSeek 双协议 adapter；`gateway.go` 注入 `lifecycle.NewExecutor(registry)`；preflight 按 channel 复合绑定 `HasAny` 校验；routing 已有 `ProtocolAnthropic` 与非法 protocol 拒绝。
3. **OpenAI 接线**：`chat_candidates.go` + `service` 构造注入；非流式/流式在 authorization 前生成 `CandidatePlan` 并冻结一次估算；`openai/deepseek/tokenizer.go` 复用完整 `ChatRequest` wire 估算（含 tools）。
4. **10.12A schema**：`usage_records` facts 化、`usage_line_items`、request/attempt/recovery/prices/cost 相关 migration 扩展、`request_records.ingress_protocol` NOT NULL。
5. **10.12A settlement**：`chat_settlement.go` / `chat_settlement_recovery.go` 只消费 `ResponseFacts`/`usage.Facts`；`billing` 计算器接口已切 facts。
6. **10.10A（若本轮一并合入）**：OpenAI/Anthropic `StreamMessages`/`StreamChatCompletions` 返回 `StreamOutcome`；Anthropic 截留 `message_stop` 并合并 usage 生成终态 facts。

**当前阻断（10.12B）**：生产代码可编译，但 `go test ./internal/...` 未全绿——需更新 settlement/service/sqlc/ledger 测试与 DB helper 以匹配新 schema。

---

## 交接说明（TASK-10.02 实施轮）

本轮按行为不变原则完成 TASK-10.02 的两处协议分离，全程 `go build ./internal/... ./cmd/...`、`go vet`、`go test ./internal/... ./cmd/...`、`git diff --check` 全绿（含 DATABASE_URL 跳过的集成测试以外的全部用例）：

1. adapter 层：`ChatRequest/ChatResponse/ChatStreamChunk/ChatMessage`、`ChatAdapter`/`StreamChatAdapter` 接口、tools、`ChatInputTokenizer`、单键 `Registry` 从 `core/adapter` 根包迁入 `core/adapter/openai`。`ChatUsage` 与协议无关的 `UpstreamError`/`UpstreamMetadata`（`upstream_error.go`）留在根包，以避免 `core/adapter` 与 `core/adapter/openai` 循环依赖，并为 TASK-10.04 的 `usage.Facts` 改造留出过渡点。
2. gatewayapi 层：OpenAI 的 handler/dto/decode/validation/stream/models 迁入 `app/gatewayapi/openai`（包名 `openai`），新增 `NewChatCompletionsHandler`/`NewModelsHandler` 导出构造函数；`router.go` 与 `middleware/` 留在 `gatewayapi` 根包并改用构造函数装配。
3. consumer 适配：service 层与 bootstrap 引用 OpenAI HTTP DTO 时用别名 `gatewayapi "…/app/gatewayapi/openai"`，避免与 `core/adapter/openai` 同名 `openai` 冲突，且现有 `gatewayapi.X` 引用零改动。

实施中发现的耦合与重新排序（已征得同意）：

1. 原 item 2「OpenAI 编排迁入 `service/gateway/openai/chatcompletions`」与 item 3「lifecycle 抽取」强耦合——当前 `service/gateway` 把 OpenAI 专属编排与协议无关 lifecycle（settlement/recovery/metrics/tracing/breaker/retry/request record）混在一个包。干净拆分的前提是先抽 lifecycle，而 lifecycle 的协议无关边界依赖 TASK-10.04 的 `ResponseFacts`/`usage.Facts`。为避免"改名后立刻被 10.05 返工"的 churn，item 2 + item 3 合并到 **TASK-10.05** 一起做。
2. 原 item 5「streamtranslate 收口到 `adapter/openai/deepseek/stream.go`」已在 TASK-10.07 Lesson 4 收口：实测 DeepSeek 流式与 OpenAI 基线无差异（旧 `DeepSeek` translator 是 `Default` 的纯透传），因此把 baseline 翻译内联进 `openai.Adapter` 并**删除整个 `streamtranslate` 包**，而不是新建空壳 `deepseek/stream.go`；如未来出现 DeepSeek 专属流式 framing 再按 provider adapter 收口。
3. 因此 TASK-10.02 标记为 partial：两处干净的协议分离已交付，service 层迁移与 lifecycle 抽取在 10.05 收口。

---

上一轮（规划自检）记录：

1. 已明确 Phase 10 的“全量”是双协议对话 endpoint 的字段识别、校验、响应翻译、
   usage、错误和账务事实完整，不表示扩展图片、视频、音频、文件等模型能力。
2. 已把 DeepSeek OpenAI / Anthropic mapping 的 `Verify` 清理要求提前为 adapter
   编码前置条件；未黑盒冻结前不得写对应 provider adapter 生产代码。
3. 已明确 `routing candidate` 只表示 SQL 同协议数据库候选；registry capability
   过滤发生在 lifecycle，并产出最终 fallback plan / attempt plan。
4. 已定死 `providers.adapter` 迁移策略：Phase 10 删除其 runtime 职责，
   `channel.Runtime.AdapterKey` 只来自 `channels.adapter_key`。
5. 已明确 `server_tool_usage` 只是 ResponseFacts / usage line item 的受控账务事实，
   不是提前实现模型能力系统；未登记 key 不能自动入账。
6. 已修正 DeepSeek OpenAI mapping 表格列数问题，并把生产代码历史注释中的
   admin 阶段编号同步为阶段 11。

TASK-10.02 的 adapter 层与 gatewayapi 层迁移完成后，下一步从 [TASK-10.03](PLAN.md#task-10-03-channel-protocol-routing)
开始。进入 `TASK-10.07` 和 `TASK-10.09` 前，必须先完成各自 DeepSeek mapping
中所有 `Verify` 的最小黑盒冻结。
