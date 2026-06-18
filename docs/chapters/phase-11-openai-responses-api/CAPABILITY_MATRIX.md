# Unio Responses API 公开能力声明

本文档是 **Unio 商业网关对客户的能力契约**：声明 Unio `/v1/responses*` 在第三方上游（首版
DeepSeek）下能做什么、做到什么程度、不做什么。

定位：

- 与 [PLAN.md](PLAN.md) 接口范围表、[DEC-014](../../production/DECISIONS.md#dec-014-openai-responses-ingress-下转-chat-completions-桥接)（桥接）、[DEC-018](../../production/DECISIONS.md#dec-018-上游-responses-直传--第三方桥接分流dec-014-补充)（上游直传分流，见 §7）一致。
- 字段级映射细节见 [RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md)（桥接路径；直传路径零转换，不适用字段映射表）。
- OpenAI Responses 官方协议字段树与 SSE 事件示例见 [docs/protocol/openai/responses/official.md](../../protocol/openai/responses/official.md)。
- 公开声明，可作为 Unio 文档站 "Responses API Capability" 页面的事实来源。

> **两条上游路径（DEC-018，按 channel `adapter_key` 分流）**：本矩阵的「桥接路径」适用于
> chat-only 第三方上游（如 DeepSeek，`adapter_key=deepseek`）——Responses 在 gateway 内部下转到
> Chat Completions，下表所有 ⚠️/❌ 桥接保真度限制均按此口径。当 channel 绑定原生支持 `/responses`
> 的上游（OpenAI 官方 / Codex 中转，`adapter_key=openai`，该 key 含 responses 直传槽）时走「直传路径」——请求/响应/SSE
> 零结构转换原文透传、仅改写 model 回显，能力与保真度**等于该上游本身**（含 namespace 工具、加密
> reasoning、内置工具等：上游支持则保真，上游不支持则不补偿），不适用下表的桥接降级口径（GAP-11-012）。
> routing / authorization / settlement / 账务对两条路径完全一致。

## 0. 核心承诺

```text
✅ Codex CLI 改 base_url / api_key 指向 Unio 即可用 DeepSeek 驱动主流程
✅ /v1/responses 主路径（非流式、流式、function tools、reasoning 文本透传、错误安全）商业级实现
✅ 共享 Phase 10 已验收的认证、限流、routing、authorization、fallback、settlement、recovery、metrics
⚠️ 无状态商业承诺：每轮回传完整 input，与 Codex 默认行为一致
❌ 不模拟 OpenAI 服务端专属能力（加密 reasoning、内置工具真实执行、服务端会话存储）
```

## 1. Endpoint 支持矩阵

| Endpoint | 状态 | 说明 |
| --- | --- | --- |
| `POST /v1/responses` | ✅ Full | 主路径，含流式/非流式、function tools、reasoning 文本、错误安全 |
| `POST /v1/responses/compact`（Native 路径） | ✅ Passthrough | `adapter_key=openai`（含原生 compact 槽）：原文透传上游 `/responses/compact`，保留 `compaction` + `encrypted_content`，能力等于上游（GAP-11-014）。上游不支持（404/405/无 usage）时按配置回落 Synthetic（默认开，DEC-019 / 整改 Q2） |
| `POST /v1/responses/compact`（Synthetic 路径） | ⚠️ Degraded | chat-only 第三方（DeepSeek 等）：无状态 chat 摘要降级，单条 assistant message 承载摘要，**不签发**加密 `compaction` item（Synthetic 永久限制，GAP-11-007 / GAP-11-014） |
| `POST /v1/responses/input_tokens` | ⚠️ Approximate | 本地 tokenizer 估算，与 OpenAI 服务端精确计数有偏差（GAP-11-008） |
| `GET /v1/responses/{id}` | ❌ 501 | 无服务端持久化，返回 `unsupported_endpoint_stateless` |
| `DELETE /v1/responses/{id}` | ❌ 501 | 同上 |
| `GET /v1/responses/{id}/input_items` | ❌ 501 | 同上 |
| `POST /v1/responses/{id}/cancel` | ❌ 501 | 仅适用于 `background:true`，本阶段不支持 |
| `GET /v1/models` | ✅ Full | 复用既有实现，列出运营配置的可用模型 |

## 2. 请求字段支持矩阵

仅列对客户行为有影响的字段。完整字段映射见 [RESPONSES_CHAT_BRIDGE.md](RESPONSES_CHAT_BRIDGE.md)。

| 字段 | 状态 | 说明 |
| --- | --- | --- |
| `model` | ✅ | 必须填 Unio 模型目录的 model_id（如 `deepseek-chat` / `deepseek-reasoner`）；Codex 默认模型名（如 `gpt-5-codex`）需运营配置别名（GAP-11-006）或客户改 Codex 配置 |
| `input`（string / items[]） | ✅ | message / function_call / function_call_output 完整支持 |
| `instructions` | ✅ | 作为首条 system message 注入 |
| `temperature` / `top_p` / `max_output_tokens` / `user` / `stream` | ✅ | 标准透传 / 字段改名 |
| `tools[{type:"function"}]` | ✅ | 扁平→嵌套，function-calling 闭环可用 |
| `tools[{type:"custom", format:{type:"grammar"}}]`（Codex `apply_patch`） | ⚠️ | convert→function 单 string 参数；DeepSeek 工具调用质量受 prompt 影响（GAP-11-002） |
| `tools[{type:"local_shell"}]` | ❌ | Drop，DeepSeek 无等价能力（GAP-11-002） |
| `tools[{type:"web_search"\|"file_search"\|"code_interpreter"\|"computer_use"\|"image_generation"\|"mcp"}]` | ❌ | Drop，**不真实执行**（GAP-11-004） |
| `tool_choice` | ✅ | 归一为 `auto`/`none`/`required`/具名 function |
| `reasoning.effort` | ✅ | 进 `ChatRequest.ReasoningEffort`；DeepSeek 上行为以 TASK-11.09 黑盒为准 |
| `reasoning.summary` | ⚠️ | best-effort，与 OpenAI 原生 summary 行为可能有差异（GAP-11-003） |
| `text.format`（含 `json_schema` / `json_object`） | ⚠️ | 进 `ChatRequest.ResponseFormat`；DeepSeek 对 `json_schema` 出站 Drop |
| `text.verbosity` | ⚠️ | 进 `ChatRequest.Verbosity`；DeepSeek 出站 Drop |
| `parallel_tool_calls` | ⚠️ | 进 `ChatRequest.ParallelToolCalls`；DeepSeek 出站 Drop |
| `store` / `prompt_cache_key` / `prompt_cache_retention` / `safety_identifier` | ⚠️ | 进契约由 DeepSeek adapter 出站 Drop；客户行为上等价于"不生效"（GAP-11-001） |
| `previous_response_id` | ❌ | 第一版 Drop 并要求完整 input；如客户依赖会话续接则 Reject（GAP-11-001） |
| `include`（含 `reasoning.encrypted_content` 等） | ❌ | Drop（GAP-11-004） |
| `truncation` | ❌ | Drop，无服务端上下文管理 |
| `background` | ❌ | Reject 为 400 `unsupported_background`（GAP-11-009） |
| `prompt`（server-side prompt template 引用） | ❌ | Drop / Reject，无服务端 prompt 模板（GAP-11-004） |
| `context_management.compact_threshold` 服务端自动压缩 | ❌ | 不支持；客户需显式调用 `/v1/responses/compact`（GAP-11-007） |

## 3. 响应/输出能力

| 项 | 状态 | 说明 |
| --- | --- | --- |
| `output[{type:"message",...}]`（普通文本） | ✅ | 标准映射 |
| `output[{type:"reasoning",...}]`（文本 reasoning） | ✅ | 从 DeepSeek `reasoning_content` 还原 |
| `output[{type:"reasoning", encrypted_content:...}]`（加密 reasoning） | ⚠️ | 非 OpenAI 加密；U1 在 `include:["reasoning.encrypted_content"]` 或无状态时附带 Unio 可逆载体 `unio-rsn-v1:`+base64，供无状态跨轮回灌（GAP-11-003） |
| `output[{type:"function_call",...}]` | ✅ | 从 Chat tool_calls 还原 |
| `output[{type:"web_search_call"\|"file_search_call"\|...}]` 内置工具调用项 | ❌ | 不生成（GAP-11-004） |
| `usage`（input_tokens / output_tokens / 字段名转换 / details） | ✅ | 完整 |
| `status` / `incomplete_details` | ✅ | 由 finish_reason 映射 |

## 4. 流式事件支持

| 事件 | 状态 |
| --- | --- |
| `response.created` / `response.in_progress` / `response.completed` | ✅ |
| `response.output_item.added` / `.done` | ✅ |
| `response.content_part.added` / `.done` | ✅ |
| `response.output_text.delta` / `.done` | ✅ |
| `response.reasoning_summary_text.delta` / `.done` | ✅（文本 reasoning） |
| `response.function_call_arguments.delta` / `.done` | ✅ |
| `response.web_search_call.*` / `response.file_search_call.*` 等内置工具事件 | ❌ |
| `response.failed` / `response.error` | ✅ |
| `sequence_number` 单调、`item_index` / `content_index` 稳定 | ✅ |
| `usage` 在 `response.completed` 中 | ✅ |

## 5. 商业承诺

| 维度 | 承诺 |
| --- | --- |
| 计费 | Responses 请求与等价 chat 请求的 `usage_records` / `ledger_entries` / `price_snapshots` / `cost_snapshots` 一致 |
| 审计 | Responses 请求在 `request_records.operation = "responses"` 可与 chat / messages 区分 |
| 安全 | 上游原始 body、credential、prompt、完整响应正文不进日志/审计；上游 auth/permission 绝不渲染为客户 401 |
| 流式可靠交付 | 首个客户可见事件前允许 fallback；之后禁止；recovery facts 未持久化不写 `response.completed` |
| HTTP 错误码 | 与 chat/messages 一致；非法 Responses 协议结构返回 400；不因 provider 能力 400 |
| 无状态承诺 | 不持久化客户对话；服务端不保留 response/input_items；每轮回传完整 input |

## 6. 与 OpenAI 官方 Responses 的差距对照

下列差距是 **结构性不可桥接**（任何第三方 provider 都做不到，社区开源项目均不实现），不是 Unio
的实现疏漏：

| OpenAI 原生能力 | 不可桥接原因 | Unio 处理 |
| --- | --- | --- |
| `encrypted_content` reasoning 跨轮加密透传 | 需 OpenAI 服务端密钥 | 不生成，Drop ingress |
| `web_search` 等内置工具真实执行 | 需 OpenAI 服务端 + 第三方集成 | Drop，不真实执行 |
| 服务端 `store=true` 持久化 + retrieve/delete/input_items | 需服务端会话存储 | 501 / Drop |
| `background:true` 异步任务 | 需服务端任务队列 | Reject |
| `prompt` server-side prompt template | 需服务端模板存储 | Drop / Reject |
| `/v1/responses/compact` 加密压缩 | 需 OpenAI 密钥 | 用 DeepSeek 摘要降级（语义可用，加密性质不可比） |
| `/v1/responses/input_tokens` 精确计数 | 需上游 tokenizer 精确分词 | 本地估算（精度可比 Codex 本地估算） |

社区参照：LiteLLM / codex-relay / codex-deepseek / codex-bridge 在第三方 provider 上**全部都不实现**
上述能力。Unio 的差异是把这些边界做成**显式商业声明 + 协议原生错误**，而不是隐性缺陷或静默退化。

## 7. 上游 Responses 直传分流（DEC-018）

上文 §1~§6 描述的是 **chat-only 第三方（首版 DeepSeek）经 responses→chat 桥接（DEC-014）** 的能力。
当上游**原生支持 `POST /responses`**（OpenAI 官方，或 Codex 标准中转）时，Unio 走第二条路径：
**直连上游 `/responses`、零结构转换原文透传**（[DEC-018](../../production/DECISIONS.md#dec-018-上游-responses-直传--第三方桥接分流dec-014-补充)）。

| 维度 | 桥接路径（adapter_key=`deepseek` 等 chat-only） | 直传路径（adapter_key=`openai`，含 responses 直传槽） |
| --- | --- | --- |
| 上游端点 | `POST <base>/chat/completions` | `POST <base>/responses` |
| 请求转换 | responses→chat 压平（§2 字段映射 / Drop） | 零转换，仅改写 `model`→upstream model、`stream` |
| 响应/SSE | chat→responses 翻译（streamEncoder 重建命名事件 + 补发 `response.completed`） | 上游命名事件**原文透传**，仅改写 `model` 回显；`response.completed`/`incomplete` 由上游下发，不二次补发 |
| 保真度 | 受 chat 压平上限约束（§6 差距） | **等于上游本身的保真度**：上游支持即保真，上游不支持即不补偿 |
| 分流依据 | 候选 adapter 未注册 Responses 直传能力 | 候选 adapter 注册了 `HasResponses`/`HasStreamResponses` |
| 账务/审计/fallback/recovery | 统一 AttemptRunner + settlement（DEC-010） | **完全一致**：同一份 `adapter.ResponseFacts`、同一条流式 fallback 循环、`operation="responses"` |
| channel 配置 | 绑定 chat adapter_key | 绑定 `adapter_key=openai`（该 key 同时含 chat+responses 能力），**零新字段** |

要点：

```text
✅ 两条路径在同一 model 的候选池可共存（分档卖档，DEC-017）：routing 不感知协议差异，
   service 层按候选 adapter 能力分流；混合候选池在「首字节前」仍可互相 fallback。
✅ 直传账务与桥接逐字一致：usage 由上游 responses input_tokens/output_tokens/*_details 归一到
   ChatUsage（与桥接侧 mapResponsesUsage 反向），落同一 usage_records / ledger / 快照口径。
⚠️ 直传只对响应/事件做 model 回显改写（顶层 model + 嵌套 response.model），不解析重排上游其它字段。
   桥接侧的 namespace 工具回译（GAP-11-002）、encrypted_content 回灌（GAP-11-003）等保真补偿在
   直传路径上「按上游原样透传」——保真差异登记 GAP-11-012，公开能力声明需按 adapter_key 区分。
```

## 8. 未来演进

不属于阶段 11 范围，但本表预留观察位：

- 引入支持加密 reasoning 的上游（如 Anthropic Computer Use、未来某 OpenAI 兼容上游）后，
  `encrypted_content` / 内置工具 / `previous_response_id` 可重新评估。
- 能力架构（阶段 12 Capability Architecture，DEC-015）落地后，本文档静态约定迁入运行时 `model_capabilities` 表，ingress 加 capability 闸门，阶段 11 公开 API 表面不变。
- 后台管理（阶段 13）落地后，模型别名、可见性、project policy 与 capability 编辑进入运营 CRUD。
- 如客户对 compact 精度有要求，可评估服务端会话存储 + 真实压缩实现路径。
