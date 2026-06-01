# Phase 10 Status

状态：partial

## 阶段判断

Phase 10 已完成 ADR、范围冻结、双协议 adapter 纵向实现与共享 lifecycle 首段接线；
当前进入 TASK-10.12A（facts schema + settlement/recovery 迁移）收口，测试与 sqlc 回归待补齐。
改造计划、架构边界、协议字段矩阵、DeepSeek 双协议 mapping 和验收标准已冻结；
DeepSeek Anthropic mapping 已按 2026-06-01 官方兼容表刷新，并保存带来源日期的项目内
参考摘要。

本阶段不是局部补丁。关闭前必须完成 OpenAI Chat Completions Create 与 Anthropic Messages Create 两个公开操作的全量对话链路。
这里的“全量”指字段识别、校验、响应翻译、usage、错误和账务事实完整，不表示本阶段扩展图片、视频、音频、文件等模型能力；相关字段必须显式 Reject 或按 mapping 转换。

## 任务状态

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-10.01 | done | DEC-010 / DEC-011、双协议架构、ResponseFacts、DeepSeek 双协议 tokenizer 独立实现边界与范围冻结已完成；DeepSeek Anthropic mapping 已按 2026-06-01 官方兼容表刷新。 |
| TASK-10.02 | partial | 行为不变目录迁移：adapter 层、gatewayapi 层、service 层三层 OpenAI 协议分离已完成（`core/adapter/openai`、`app/gatewayapi/openai`、`service/gateway/openai/chatcompletions`，原 item 1/2/4）。`service/gateway/lifecycle` 已抽出 `AdapterRegistry` facade 与 `Executor.PrepareCandidates`（authorization 前候选准备，10.10B-2a）；完整 attempt/delivery/settlement 抽取仍归 TASK-10.05/10.12A。streamtranslate 收口（item 5）随 TASK-10.07。 |
| TASK-10.03 | done | `channels` 增加 `protocol`/`adapter_key`、删除 `providers.adapter` runtime、`FindRouteCandidates` 按 ingress `protocol` 过滤并改用 `channels.adapter_key`、seed 同步；已通过本地库 down→改→up 与 DB 集成测试（含协议路由过滤用例）。10.10B-1 已新增共享 `lifecycle.AdapterRegistry` facade，按 `(protocol, adapter_key)` 查询三能力并保持 SQL 顺序过滤候选；bootstrap preflight 按复合绑定存在性校验。10.10B-2a 已让 executor 消费 capability 过滤结果与候选级估算 closure。 |
| TASK-10.04 | partial | 协议无关事实契约已落地并单测：`internal/core/usage` 与 `core/adapter`（`ResponseFacts`、`FinishFacts`、`StreamOutcome`）。OpenAI 与 Anthropic adapter 均已产出 facts。10.12A 已改源 migration：`usage_records` 改为协议无关 token 维度 + `CountState`、`usage_line_items` 新表；`request_records`/`request_attempts`/`settlement_recovery_jobs` 与 prices/cost 快照表已扩展 facts 字段。生产 settlement/recovery 已改为只消费 `adapter.ResponseFacts`/`usage.Facts`；10.12B 待补 sqlc/集成/账务单测回归。 |
| TASK-10.05 | in_progress | 共享 Lifecycle Executor：`lifecycle.AdapterRegistry` + `Executor.PrepareCandidates` 已落地并由 OpenAI 非流式/流式接入（10.10B-2a）。待办：抽出共享 attempt/delivery/settlement 到 `service/gateway/lifecycle`（当前 settlement 仍在 `chatcompletions` 但已 facts 化）、接 Anthropic service/handler（10.10B-2b）。 |
| TASK-10.06 | in_progress | OpenAI Chat Completions 全量字段契约。已完成：message content union 结构化校验（接受 text/refusal 数组，多模态 part 前置 Reject，畸形 400）。待办：剩余高级顶层字段 typed（audio/modalities/n/seed/logit_bias/logprobs/top_logprobs/metadata/prediction/prompt_cache_*/verbosity/function_call/functions）——多数与 10.07 DeepSeek Reject 强耦合，需一起推进。 |
| TASK-10.07 | in_progress（核心已通） | DeepSeek OpenAI adapter 核心已对真实上游端到端验证通过。已完成：`Verify` 黑盒冻结；`adapter/openai/deepseek`（包装 `openai.Adapter`，请求 Reject 经黑盒收敛为 `n>1`/`custom tool`/`json_schema`/`audio`/`modalities`含非text，其余被忽略字段 No-op 透传）；usage 经黑盒确认 `cached_tokens=prompt_cache_hit_tokens` 已精确；产出 `ResponseFacts`；注册键 `deepseek`、seed 同步、`failure.CodeAdapterRequestUnsupported`→gatewayapi 400；DS-OAI-01/02 黑盒回归（非流式/流式，gate 在 `DEEPSEEK_BLACKBOX=1`）通过。待办：响应侧 full field 映射（logprobs/annotations 等）、`streamtranslate` 收口到本包（item 5，可并入 10.15）、DS-OAI 其余黑盒用例（tools/reasoning/cache/reject）。 |
| TASK-10.08 | done（ingress 契约层） | Anthropic Messages 全量字段入口已落地并单测：`app/gatewayapi/anthropic/messages`（DTO + 双轨 decode + `mcp_servers` Reject）、顶层校验（model/max_tokens/messages/role/范围）、content union 结构化校验（string shorthand + 已登记 block 类型，未知类型 400）、system/thinking/tool_choice/tools union 校验、非流式 `MessageResponse` + 强类型 `MessageUsage`、命名 SSE 事件 DTO + 帧编码器、`app/gatewayapi/anthropic` 共享 Anthropic error shape。HTTP handler 与 header 校验随 10.10 接线。 |
| TASK-10.09 | done（adapter 核心，实时黑盒通过） | DeepSeek Anthropic adapter 已对真实上游端到端验证通过。已完成：DS-ANT 全表黑盒冻结（usage 五字段、thinking+signature、流式事件序、image silent-ignore、OpenAI 风格错误体，所有 `Verify` 清零）；`core/adapter/anthropic` 契约层（`MessagesAdapter`/`StreamMessagesAdapter`/`MessagesInputTokenizer`、内部 `MessageRequest/Response/StreamEvent`、`MessageUsage.ToUsageFacts()`、`anthropicFinishClass`+`ResponseFacts`、三能力 `Registry`）；通用 `anthropic.Adapter` base（wire 编码、HTTP、响应解析、SSE 翻译、错误分类）；`adapter/anthropic/deepseek`（reject 不支持 content block / server tool / 误导 ignored 字段 + 保守 tokenizer）；单测 + DS-ANT-01/02/09 实时黑盒回归全绿（gate 在 `DEEPSEEK_BLACKBOX=1`）。待办（并入后续）：响应 content block typed 化、server_tool_use/web_search 黑盒、DS-ANT-15 tokenizer 校准。 |
| TASK-10.10 | in_progress | 10.10A stream adapter 契约已完成：OpenAI / Anthropic 流式接口统一返回 `StreamOutcome`；OpenAI 截留 `[DONE]`，Anthropic 截留 `message_stop`、合并 `message_start` + `message_delta` usage 并生成最终 `ResponseFacts`；有可靠 usage 的 terminal 前断尾仍返回 facts + 稳定错误。10.10B-1 已完成共享 registry facade、DeepSeek 双协议 bootstrap 注册、复合绑定 preflight 和 routing 非法协议防护。10.10B-2a 已完成共享候选准备 executor 与 OpenAI 接线。待办：10.12A facts settlement/recovery、10.10B-2b Anthropic service / handler、durable closeout 后由 gatewayapi 写客户终态。 |
| TASK-10.11 | planned | 双协议错误与安全输出。 |
| TASK-10.12 | in_progress（10.12A 生产路径已改，10.12B 测试待补） | **10.12A（进行中）**：源 migration 已扩展 `usage_records`（协议无关 token + state）、`usage_line_items`、`request_records.ingress_protocol`、attempt/recovery/prices/cost 的 facts 列；`chat_settlement.go`/`chat_settlement_recovery.go` 已只消费 `ResponseFacts`/`usage.Facts` 计费；`billing` 计算器已接 `usage.Facts`。`go build ./internal/... ./cmd/...` 通过。**10.12B（待办）**：对齐 `chat_settlement_test`/`service_test`/`sqlc`/`ledger` 等测试与 helper（旧 `PromptTokens` 列已移除）；本地 down→up + `sqlc generate` + 账务回归全绿后再标 done。 |
| TASK-10.13 | planned | OpenAI SDK 黑盒验收。 |
| TASK-10.14 | planned | Anthropic SDK 黑盒验收。 |
| TASK-10.15 | planned | 文档、命名与冗余复核。 |

## 已识别的现有迁移点

| 当前实现 | Phase 10 目标 | 状态 |
| --- | --- | --- |
| `internal/app/gatewayapi` OpenAI 文件平铺 | `internal/app/gatewayapi/openai` | done（TASK-10.02） |
| `internal/service/gateway/chat_*` | `service/gateway/lifecycle` + `service/gateway/openai/chatcompletions` | partial：整包在 `chatcompletions`；`lifecycle` 已有 `AdapterRegistry` + `PrepareCandidates` 并由 OpenAI 使用。settlement/recovery 已在 `chatcompletions` 改为消费 `ResponseFacts`（代码未迁入 lifecycle 包）。待办：共享 attempt/delivery lifecycle、Anthropic service/handler |
| OpenAI 偏向的 `usage_records.prompt_tokens` 等列 | 协议无关 `usage_records` + `usage_line_items` | partial（10.12A）：schema 与 settlement 写入路径已改；测试与 sqlc helper 待 10.12B |
| `internal/core/adapter/chat.go` 的 OpenAI 语义 DTO | `internal/core/adapter/openai` | done（TASK-10.02） |
| `internal/core/adapter/openai/streamtranslate` | `internal/core/adapter/openai/deepseek/stream.go` | 顺延 TASK-10.07 |
| `providers.adapter` | 删除 runtime 职责；routing/preflight/seed 改用 `channels.adapter_key`，`routing.ChatRouteCandidate.AdapterKey` 来自 `channels.adapter_key` | done（TASK-10.03） |
| OpenAI 偏向的 `ChatUsage` | 协议无关 `usage.Facts` | partial（10.12A）：settlement/recovery 已按 `usage.Facts` 计费并持久化；`ChatUsage` 仍留 `core/adapter` 根包供 adapter/stream 翻译，待测试与公开响应路径全部切 facts 后评估移除 |
| OpenAI 偏向的 `adapter.ChatInputTokenizer` | `openai.ChatInputTokenizer` + `anthropic.MessagesInputTokenizer` | done：OpenAI 侧 `openai.ChatInputTokenizer`（10.02）；Anthropic 侧 `anthropic.MessagesInputTokenizer` 已定义并由 `adapter/anthropic/deepseek` 实现（10.09，保守启发式，DS-ANT-15 待校准） |
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
- **验证现状**：`go build ./internal/... ./cmd/...` 通过；`go test ./internal/...` 当前
  3 处失败待 10.12B——`chatcompletions`（测试仍引用旧 `UsageRecord.PromptTokens` 等列）、
  `platform/store/sqlc`（usage_line_items / ingress_protocol helper）、`core/ledger`（测试
  insert 缺 `ingress_protocol`）。

下一步 **10.12B**：本地 down→up、`sqlc generate`、对齐全部测试 helper 与账务回归全绿后，
再继续 **10.10B-2b**（Anthropic service/handler + 共享 attempt/delivery lifecycle）。

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
2. 原 item 5「streamtranslate 收口到 `adapter/openai/deepseek/stream.go`」依赖 TASK-10.07 的 DeepSeek provider adapter 落点；当前 `openai.Adapter` 仍是通用 OpenAI-compatible 适配器，提前建 `deepseek` 子包是空壳。item 5 合并到 **TASK-10.07**。
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
