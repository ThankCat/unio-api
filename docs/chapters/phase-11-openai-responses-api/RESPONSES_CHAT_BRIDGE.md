# Responses ↔ Chat Completions Bridge - 字段映射矩阵

本文档冻结 OpenAI Responses API（Codex 入口）与 Unio 内部 OpenAI Chat 契约
（`internal/core/adapter/openai.ChatRequest` / `ChatResponse` / `ChatStreamChunk`）之间的双向映射。

权威字段来源：
- [docs/protocol/openai/responses/official.md](../../protocol/openai/responses/official.md)（`POST /responses` 完整 body + Returns + 示例）
- [docs/protocol/openai/responses/official-streaming-events.md](../../protocol/openai/responses/official-streaming-events.md)（53 种 streaming events）
- [docs/protocol/openai/responses/official-other-endpoints.md](../../protocol/openai/responses/official-other-endpoints.md)（其余 endpoint + error schema）

> 仅覆盖 `POST /v1/responses` 主路径。其他 Responses endpoint（`compact` / `input_tokens` /
> retrieve / delete / input_items / cancel）以及公开能力边界见 [CAPABILITY_MATRIX.md](CAPABILITY_MATRIX.md)。
> compact 端的字段处理见 [PLAN.md TASK-11.11](PLAN.md#task-11-11-compact)。

状态列：

```text
Pass    原样映射到 Chat 字段。
Adapt   语义等价但结构/字段名不同，需转换。
Drop    无 Chat 等价或本阶段不消费；不写 upstream，记内部审计（DEC-012）。
Reject  ingress 协议非法才 Reject；不因 provider 能力 Reject。
```

> 结构性字段已全部冻结为 Pass/Adapt/Drop/Reject（真实 Codex v0.130 抓包 + codex-rs 源码交叉确认，
> 见 §9）。`reasoning_effort` 在 DeepSeek 上的行为已据官方 API 文档定调（possible values `high`/`max`，
> low/medium→high、xhigh→max）并在 DeepSeek adapter 出站归一（TASK-11.09 / GAP-11-010 已关闭）；
> reasoning 缺省/null → `ReasoningDisabled` → DeepSeek `thinking:disabled`（DEC-016）。真实 key 端到端
> smoke 归 TASK-11.15。

## 0. 端到端方向

```text
Codex → Responses 请求 → [responses_chat_map] → openai.ChatRequest → adapter/openai/deepseek
                                                                          → DeepSeek /chat/completions
DeepSeek → Chat 响应/SSE → adapter/openai/deepseek → openai.ChatResponse/Chunk
        → [responses_response_map / responses_stream] → Responses 响应/事件 → Codex
```

billing/审计走 adapter 同次解析产生的 `ResponseFacts`，与翻译层无关。

## 1. 请求顶层字段（Responses → Chat）

> 职责划分：桥接层只做协议结构翻译，能映射进 `openai.ChatRequest` 契约的字段一律 Adapt 进契约；
> provider（DeepSeek）能力裁剪由 `adapter/openai/deepseek` 出站 `dropUnsupported` 负责。因此
> `store` / `prompt_cache_*` / `verbosity` / `response_format(json_schema)` / `parallel_tool_calls`
> 等在本表是 Adapt（进契约），真正的 Drop 发生在 DeepSeek adapter 出站，桥接层不重复硬 Drop。
> 本表 “Drop” 仅用于 **契约里无承载字段** 的 Responses 专属字段。

| Responses 字段 | Chat 目标 | 状态 | 说明 |
| --- | --- | --- | --- |
| `model` | `model` | Pass | routing 用客户模型名，adapter 用 `upstreamModel`。 |
| `input` (string) | `messages:[{role:user,content}]` | Adapt | 单条 user message。 |
| `input` (items[]) | `messages[]` | Adapt | 见 §2。 |
| `instructions` | 顶部 `system`（或 `developer`）message | Adapt | LiteLLM/codex-relay 一致：作为首条 system。 |
| `max_output_tokens` | `max_tokens`（或 `max_completion_tokens`） | Adapt | 字段改名。 |
| `temperature` | `temperature` | Pass | |
| `top_p` | `top_p` | Pass | |
| `user` | `user` | Pass | |
| `parallel_tool_calls` | `parallel_tool_calls` | Pass | |
| `metadata` | `metadata` | Pass | adapter 出站按 DeepSeek 能力可能 Drop。 |
| `tools` | `tools` | Adapt | 扁平→嵌套，见 §3。 |
| `tool_choice` | `tool_choice` | Adapt | 归一，见 §3。 |
| `reasoning` (`{effort,summary}`) | `reasoning_effort` / `ReasoningDisabled` | Adapt | 有 `effort`→`reasoning_effort`（DeepSeek adapter 归一 high/max）；`reasoning` 缺省/null→置 `ReasoningDisabled`（opt-in 语义），DeepSeek 出站注入 `thinking:disabled`。`summary` 见 §6。 |
| `text` (`{format,verbosity}`) | `response_format` + `verbosity` | Adapt | `format`→`response_format`，`verbosity`→`verbosity`。 |
| `stream` | `stream` | Pass | Codex 恒 true；流式强制 `stream_options.include_usage=true`。 |
| `store` | `ChatRequest.Store` | Adapt | 进契约；DeepSeek adapter 出站 Drop（无状态，GAP-11-001）。 |
| `previous_response_id` | — | Drop/Reject | 契约无承载字段；无状态：第一版 Drop 并要求客户回传完整 input；如客户依赖会话续接则 Reject 并提示（GAP-11-001）。 |
| `include` | — | Drop | 契约无承载字段，Responses 专属输出控制（GAP-11-004）。 |
| `truncation` | — | Drop | 契约无承载字段，无服务端上下文管理。 |
| `prompt_cache_key` / `prompt_cache_retention` / `safety_identifier` | `ChatRequest.PromptCacheKey` / `PromptCacheRetention` / `SafetyIdentifier` | Adapt | 进契约；DeepSeek adapter 出站 Drop。 |
| `background` | — | Drop | 异步任务模式不支持。 |
| `service_tier` | `service_tier` | Pass | adapter 出站按能力 Drop。 |
| `prompt`（prompt template 引用） | — | Drop/Reject | server-side prompt 模板未实现。 |
| `client_metadata`（Codex 专属） | — | Drop | **真实抓包发现**：Codex 发 `{x-codex-installation-id}`，OpenAI 规范无、契约无承载字段，静默 Drop（不计入审计敏感字段）。 |

## 2. input items → messages（Responses → Chat）

Responses `input` 是 item 数组，工具调用是 **顶层 item**，不像 Chat 嵌在 message 里。

| Responses input item | Chat message | 状态 | 说明 |
| --- | --- | --- | --- |
| `{type:"message", role, content:[...]}` | `{role, content}` | Adapt | content parts 见 §2.1。role: user/assistant/system/developer。 |
| `{type:"function_call", call_id, name, arguments}` | `assistant.tool_calls[{id:call_id, type:function, function:{name,arguments}}]` | Adapt | 连续 function_call 合并进 **同一条** assistant message。 |
| `{type:"function_call_output", call_id, output}` | `{role:"tool", tool_call_id:call_id, content:output}` | Adapt | 按 call_id 对齐到前一条 assistant.tool_calls。 |
| `{type:"reasoning", ...}` | `assistant.reasoning_content`（仅工具调用轮） | Adapt | U1 已回灌：紧邻 `function_call` 前的 reasoning 翻回该轮 `assistant.reasoning_content`，还原优先级 `encrypted_content`(Unio 载体)→`content.reasoning_text`→`summary.summary_text`；非工具轮丢弃。残留真实 Codex 回传形态确认见 GAP-11-003。 |
| `{type:"item_reference", id}` | — | Drop/Reject | 引用 server-side 历史 item，无状态不支持（GAP-11-001）。 |

### 2.1 message content parts

| Responses content part | Chat content | 状态 | 说明 |
| --- | --- | --- | --- |
| `{type:"input_text", text}` | 文本（string 或 content part `{type:"text"}`） | Adapt | 多 part 合并/保留。 |
| `{type:"output_text", text}` | assistant 文本 | Adapt | 历史 assistant 文本。 |
| `{type:"input_image", image_url/file_id}` | `{type:"image_url"}` content part | Adapt | 协议面识别；DeepSeek 不支持时 adapter 出站 Drop。 |
| `{type:"input_file", file_data/file_id}` | `{type:"file"}` content part | Adapt | 同上，能力不支持 Drop。 |
| `{type:"input_audio", ...}` | `{type:"input_audio"}` | Adapt | 同上。 |
| `{type:"refusal", refusal}` | assistant.refusal | Adapt | 历史拒答。 |

合并规则（对齐 LiteLLM / codex-relay）：

```text
1. 连续 function_call item 合并进单条 assistant.tool_calls（避免 back-to-back assistant）。
2. function_call_output 必须紧跟其 call_id 对应的 assistant.tool_calls，保持顺序。
3. 找不到匹配 call_id 的 function_call_output：保守降级为 user/tool 文本，不丢内容。
```

## 3. tools 与 tool_choice

### 3.1 tools

| Responses tool | Chat tool | 状态 | 说明 |
| --- | --- | --- | --- |
| `{type:"function", name, description, parameters, strict}` | `{type:"function", function:{name,description,parameters,strict}}` | Adapt | 扁平→嵌套。`parameters` 缺 `type` 时补 `"type":"object"`。**真实抓包确认 v0.130 全部工具都是此形状**（`exec_command`/`write_stdin`/`view_image`/`update_plan`/`request_user_input`/`spawn_agent`/`close_agent` 等，`strict:false`）。 |
| `{type:"namespace", name:"mcp__xxx__", tools:[{type:"function",...}]}` | 拍平为多个 `{type:"function", function:{...}}` | Adapt | **真实抓包新发现**：Codex 用 `namespace` 分组 MCP 工具（如 `mcp__node_repl__`、`mcp__openaiDeveloperDocs__`），内层是 `type:function`。OpenAI 规范无此类型。冻结策略：拍平内层 function 工具到 Chat 顶层 tools（name 保留），名称回译方案见 §3.3（GAP-11-002）。 |
| `{type:"custom", name, format:{type:"grammar"/"text",...}}` | `{type:"function", function:{name, parameters:{...string arg...}}}` 或 Drop | Adapt/Drop | **v0.130 实测未发送**：apply_patch 经 `exec_command` function 跑 shell（instructions 内说明），非 grammar 工具。保留兜底：若旧/未来版本发 custom/grammar，convert→function（单 string 参数）或 Drop（GAP-11-002）。 |
| `{type:"local_shell"}` | — | Drop | **v0.130 实测未发送**（改用 `exec_command` function）。保留兜底：若出现则 Drop（GAP-11-002）。 |
| `{type:"web_search", external_web_access}` / `web_search_preview` | `web_search_options` | Adapt/Drop | **真实抓包确认 v0.130 发送**（`external_web_access:false`，无 name/parameters）。第一版无真实执行；DeepSeek 不支持 → Drop（GAP-11-004）。 |
| `{type:"image_generation", output_format}` | — | Drop | **真实抓包确认 v0.130 发送**（`output_format:"png"`，无 name/parameters）。server-side 工具未实现 → Drop（GAP-11-004）。 |
| `{type:"file_search"/"code_interpreter"/"computer_use"}` | — | Drop | server-side 工具未实现（GAP-11-004）。 |
| `{type:"mcp", server_label,...}`（OpenAI 规范形态） | passthrough/Drop | Drop | 本阶段不接 server-side MCP；注意与上面 Codex `namespace` 形态区分。 |

### 3.2 tool_choice（归一，对齐 LiteLLM `_transform_tool_choice`）

| Responses tool_choice | Chat tool_choice | 状态 |
| --- | --- | --- |
| `"auto"` / `"none"` / `"required"` | 原样 | Pass |
| `{type:"auto"}` | `"auto"` | Adapt |
| `{type:"none"}` | `"none"` | Adapt |
| `{type:"required"}` / `{type:"any"}` / `{type:"tool"}`（无具体 name） | `"required"` | Adapt |
| `{type:"function", name}` / `{type:"function", function:{name}}` | `{type:"function", function:{name}}` | Adapt |
| `{type:"allowed_tools", ...}` | `"auto"` 或具名 | Adapt |

### 3.3 namespace 工具拍平与名称回译（真实抓包新增）

Codex v0.130 把 MCP 工具按 `type:"namespace"` 分组下发，内层是标准 `type:"function"`：

```text
{ "type":"namespace", "name":"mcp__node_repl__", "tools":[ {"type":"function","name":"js",...}, ... ] }
```

DeepSeek（Chat Completions）无 namespace 概念，冻结策略：

```text
出站（请求）：拍平内层 function 工具到 Chat 顶层 tools[]。
  名称方案（候选，需输出侧回归确认）：
    A. 直接用内层 name（如 "js"）——名称简洁但可能与其它 namespace 撞名。
    B. 拼接 "<namespace>__<name>"（如 "mcp__node_repl__js"）——保证唯一、可逆。
  当前默认 B：唯一可逆，便于回译。
入站（响应 function_call）：codex-rs 的 function_call ResponseItem 支持可选 `namespace` 字段
  （源码 `ev_function_call_with_namespace` 确认）。因此回译时拆出 namespace：
    Chat function name "mcp__node_repl__js"（方案 B）
      → Responses output item {type:"function_call", call_id, namespace:"mcp__node_repl__", name:"js", arguments}
  Codex 据此路由到对应 namespace 工具。
```

> 用 codex-rs 原生 `namespace` 字段回译比"靠名字前缀猜"更稳；最终以一次"模型真的调用了 namespace
> 工具 + Codex 成功路由"的回归定稿（TASK-11.08/11.15，GAP-11-002）。

## 4. 非流式响应（Chat → Responses）

`openai.ChatResponse` → Responses `response` 对象：

```text
response = {
  id: "resp_<gen>",            # 新生成（chat id 可记入审计）
  object: "response",
  created_at: ChatResponse.Created,
  model: 请求 model,
  status: <见 §4.1>,
  output: [ ...见下... ],
  usage: <见 §5>,
  // 透传/默认: parallel_tool_calls, temperature, top_p, tool_choice, tools, max_output_tokens
}
```

output items（顺序：reasoning → message → function_call）：

| Chat 来源 | Responses output item | 状态 |
| --- | --- | --- |
| `reasoning_content` | `{type:"reasoning", id:"rs_*", summary:[{type:"summary_text",text}]}`（或 content output_text） | Adapt |
| `content` | `{type:"message", id:"msg_*", role:"assistant", status, content:[{type:"output_text", text, annotations:[]}]}` | Adapt |
| `tool_calls[i]` | `{type:"function_call", id:"fc_*", call_id, name, arguments, status:"completed"}` | Adapt |
| `refusal` | message content `{type:"refusal", refusal}` | Adapt |

### 4.1 finish_reason → status

| Chat finish_reason | Responses status | incomplete_details |
| --- | --- | --- |
| `stop` / `tool_calls` / `function_call` | `completed` | — |
| `length` | `incomplete` | `{reason:"max_output_tokens"}` |
| `content_filter` | `incomplete` | `{reason:"content_filter"}` |
| 上游错误 | `failed` | error 对象 |

## 5. usage 映射（Chat → Responses）

| Chat usage | Responses usage | 状态 |
| --- | --- | --- |
| `prompt_tokens` | `input_tokens` | Adapt |
| `completion_tokens` | `output_tokens` | Adapt |
| `total_tokens` | `total_tokens` | Pass |
| `prompt_tokens_details.cached_tokens` | `input_tokens_details.cached_tokens` | Adapt |
| `completion_tokens_details.reasoning_tokens` | `output_tokens_details.reasoning_tokens` | Adapt |

> 账务不依赖此公开 usage：billing 用 adapter 同次解析的 `ResponseFacts`/`usage.Facts`。
> 这里只是把公开 usage 渲染成 Responses 形状供 Codex/SDK 读取。

## 6. 流式事件状态机（Chat SSE → Responses 命名事件）

Chat 是 data-only chunk + `[DONE]`；Responses 是命名事件 + 单调 `sequence_number`。
对照 LiteLLM `streaming_iterator.py` 与 codex-relay 的事件排序。

> **Codex 实际消费的事件子集（codex-rs `process_responses_event` 源码确认）**
> Codex 的 SSE 解析器只 match 下列事件，其余一律 `trace!("unhandled")` 忽略：
> `response.created`(读 `response`)、`response.output_item.added`、`response.output_text.delta`(只读 `delta`)、
> `response.reasoning_summary_text.delta`(`delta`+`summary_index`)、`response.reasoning_text.delta`(`delta`+`content_index`)、
> `response.reasoning_summary_part.added`、`response.custom_tool_call_input.delta`(custom 工具，DeepSeek 不触发)、
> `response.output_item.done`、`response.completed`、`response.failed`、`response.incomplete`。
>
> 关键推论：
> - **`response.output_item.done` 是权威载体**：Codex 从中反序列化完整 `ResponseItem` 拿最终
>   message / function_call / reasoning，delta 仅用于实时打字 UX。每个输出 item 必须发一条携带
>   **完整内容**的 `output_item.done`。
> - Codex **忽略** `response.in_progress` / `content_part.added|done` / `output_text.done` /
>   `reasoning_*.done` / `function_call_arguments.delta|done`。**实现现状**：`responses_stream.go`
>   当前只 emit Codex 消费子集（见下方 recipe），上述规范完整事件**暂未发出**（标准 Responses SDK
>   完整性补齐留 GAP-11-011），Codex 不依赖，故不影响本阶段验收。
> - `response.completed` 必须能反序列化为 `ResponseCompleted`（至少 `id`；`usage`/`end_turn` 可选），
>   否则 Codex 视为 stream error。
> - function_call item 形如 `{type:"function_call", call_id, name, arguments}`（无需 `id`/`status`）；
>   MCP namespace 工具额外带 `namespace` 字段（见 §3.3）。

典型一次成功流（单文本输出）：

```text
response.created                      # status=in_progress，空 output
response.output_item.added           # item:{type:"message", id:"msg_*", role:"assistant"}
response.output_text.delta           # 每个 content delta 一条（delta=chunk.Content）
...
response.output_item.done            # 权威载体：完整 message（Codex 反序列化取最终文本）
response.completed                   # 完整 response 对象 + usage（durable closeout 后才发）
# 规范完整事件 in_progress / content_part.added|done / output_text.done 当前未发（GAP-11-011）
```

reasoning（DeepSeek `reasoning_content` 增量）——**已按 codex-rs 源码冻结为 `reasoning_text`**：

```text
response.output_item.added           # item:{type:"reasoning", id:"rs_*", summary:[]}
response.reasoning_text.delta        # {delta, content_index}（DeepSeek reasoning_content 增量）
response.output_item.done            # item:{type:"reasoning", id, summary:[], content:[{type:"reasoning_text", text}]}
```

> 依据 codex-rs `core_test_support::responses`：`reasoning_text.delta` 携带 `content_index`、
> 落入 reasoning item 的 `content:[{type:"reasoning_text"}]`；`reasoning_summary_text.delta` 携带
> `summary_index`、落入 `summary:[{type:"summary_text"}]`。app-server 文档明确：raw `content`/
> `reasoning_text` 适用于开源模型，`summary` 适用于 OpenAI 托管模型。DeepSeek `reasoning_content`
> 是原始 CoT（开源模型语义）→ 选 `reasoning_text`。`encrypted_content` 为 Option，DeepSeek 省略。

tool_calls（增量累积成 function_call）：

```text
response.output_item.added           # item:{type:"function_call", id:"fc_*", call_id, name}
response.function_call_arguments.delta  # 每段 arguments 增量
response.output_item.done            # 权威载体：完整 function_call（含 arguments）
# 规范完整事件 function_call_arguments.done 当前未发（GAP-11-011）
```

状态机规则：

| 规则 | 说明 | 状态 |
| --- | --- | --- |
| sequence_number | 每个事件单调递增，从 0 开始，每事件 +1。 | Adapt（协议不变量：无条件单调；客户用于排序/去重，故 Unio 始终正确发出，与 Codex 是否强校验无关） |
| item/part index | content_part / output_index 稳定，不同 item 不串。 | Adapt |
| 文本 delta | `chunk.Content` → `response.output_text.delta`。 | Adapt |
| reasoning delta | `chunk.ReasoningContent` → `response.reasoning_text.delta`（带 `content_index`）。 | **Adapt（已冻结）** |

> reasoning 事件名已按 codex-rs 源码冻结为 `reasoning_text.*`（见上方 recipe 依据）。
> 兜底：若真实回归显示 Codex 对我们的 model 名只渲染 summary，则切 `reasoning_summary_text.*`
> + `summary_index`（仅改 `responses_stream.go` 事件名常量与 index 字段，约 2~3 行）。
| tool delta | `chunk.ToolCalls` 增量累积；首段发 `output_item.added`，每段发 `arguments.delta`。 | Adapt |
| 终态 | `response.completed` 必须带完整 `output` + `usage`，且只在 durable closeout 后写。 | Adapt |
| 错误 | tail error / recovery facts 未持久化：写 `response.failed` 或 `error` 事件，记 delivery interrupted，不写 `response.completed`。 | Adapt |
| fallback | 首个客户可见事件（`response.created` 之后第一个内容事件）前允许；之后禁止。 | Adapt |

## 7. 错误（Chat/upstream → Responses error）

复用 `adapter.UpstreamCategoryOf`，HTTP status 策略与 chat/messages 一致：

| 上游分类 | HTTP | Responses error.type | 说明 |
| --- | --- | --- | --- |
| rate_limit | 429 | `rate_limit_error` | |
| timeout | 504 | `api_error` | |
| bad_request | 400 | `invalid_request_error` | |
| auth/permission/server/unknown | 502 | `api_error` | 上游凭据问题 **绝不**渲染成客户 401。 |
| ledger 余额不足 | 402 | `insufficient_quota` | 与 chat 一致；用 402 与限流 429 区分。 |

非法 Responses 协议结构 → 400 `invalid_request_error`（带 `param`）。
原始上游 body / credential / prompt / 完整正文不透传、不进默认审计。

## 8. Codex 专属坑（v0.130.0 真实抓包已确认，标注 ✅）

> 抓包来源：`internal/blackbox/fixtures/codex/20260605_151709.845_POST_v1_responses.json`
> （Codex CLI v0.130.0，`codex exec` 单轮 `read-only`、`reasoning effort: none`，76 KB）。

| 现象 | 处理 | 关联 |
| --- | --- | --- |
| ✅ Codex 恒 `stream:true` | 流式是主路径，必须强制 `include_usage` 拿 final usage 结算。 | TASK-11.07 |
| ✅ `store:false` + `parallel_tool_calls:false` + `tool_choice:"auto"` | 无状态可行：不需要 server-side 会话。 | GAP-11-001 |
| ✅ 本次 `reasoning:null`、`include:[]`、`prompt_cache_key`=session UUID、无 `previous_response_id`/`text`/`max_output_tokens` | `reasoning`/`include` 显式 null/空数组需安全处理；`previous_response_id` 出现时 Drop+提示。 | GAP-11-001 |
| ✅ 多数工具是 `type:"function"`（`exec_command`/`write_stdin`/`view_image`/`update_plan`/`request_user_input`/`spawn_agent`/`close_agent` 等，`strict:false`） | 扁平→嵌套 function 工具，§3.1。 | TASK-11.08 |
| ✅ **新发现** 内置工具 `{type:"web_search", external_web_access:false}` 与 `{type:"image_generation", output_format:"png"}`（无 `name`/`parameters`） | ingress 不得因缺 name 而 Reject（已落地 validation default 放行）；DeepSeek 不支持 → adapter 出站 Drop。 | GAP-11-004 |
| ✅ `apply_patch` 经 `exec_command` function 跑 shell（**未发** `type:custom`/grammar） | v0.130 无需 custom/grammar 转换；保留兜底分支应对旧/未来版本。 | GAP-11-002 |
| ✅ **未发** `type:"local_shell"`（改用 `exec_command`） | 保留兜底 Drop。 | GAP-11-002 |
| ✅ **新发现** MCP 工具用 `type:"namespace"`（`mcp__node_repl__`/`mcp__openaiDeveloperDocs__`，内层 `type:function`） | 拍平内层 function 工具到 Chat 顶层 tools，名称回译见 §3.3。 | GAP-11-002 |
| ✅ **新发现** 顶层 `client_metadata`（`x-codex-installation-id`，OpenAI 规范无） | 静默 Drop，不进敏感审计。 | §1 |
| ✅ `input[]` 用 `role:"developer"` 承载大段 instructions（permissions/memory/skills/plugins），content 是多个 `input_text` part | developer→system；多 part 合并；tokenizer 估算计入。 | TASK-11.05 |
| `reasoning:{effort,summary}`（reasoning run，本次为 null） | `effort`→`reasoning_effort`；`summary` 影响是否发 reasoning 事件，按 Codex 期望冻结。 | TASK-11.08 |
| `text:{format,verbosity}`（本次未发） | `format`→`response_format`，`verbosity`→`verbosity`。 | TASK-11.08 |
| 多 function_call 并行 | 合并进单条 assistant.tool_calls；流式分项发 `output_item`。 | §2 / §6 |
| `instructions` 很大（顶层 Codex system prompt） | 注入首条 system，tokenizer 估算需计入。 | TASK-11.05 |
| ⚠️ `GET /v1/models` 形状：Codex v0.130 期望 `{"models":[...]}`，标准 OpenAI/Unio 返回 `{"object":"list","data":[...]}` | Codex 报 ERROR 但继续；drop-in 体验需补 Codex 兼容形状或单独 endpoint。 | TASK-11.14 |

## 9. 冻结结果（TASK-11.01）

```text
协议快照：完整（POST /responses + streaming events 53 种 + 其余 endpoint + error schema）
  主文件 docs/protocol/openai/responses/official.md
  子文件 docs/protocol/openai/responses/official-streaming-events.md
  子文件 docs/protocol/openai/responses/official-other-endpoints.md
结构可定字段：已冻结（§1~§7 全部 Pass/Adapt/Drop/Reject；§6 sequence_number 已冻结为 Adapt）
真实 DeepSeek 黑盒 gate：DEEPSEEK_BLACKBOX=1 + DEEPSEEK_API_KEY（TASK-11.09 reasoning_effort 验证用）
```

真实 Codex `/responses` 抓包残留项 —— **全部已清零（抓包 + codex-rs 源码）**：

```text
1. ✅ §6 reasoning 事件名：冻结为 response.reasoning_text.delta（content_index）+ reasoning item
      content:[{type:"reasoning_text"}]。依据 codex-rs process_responses_event + core_test_support
      （reasoning_summary_text→summary_index/summary_text；reasoning_text→content_index/reasoning_text）
      + app-server 文档（raw content 适用于开源模型）。兜底切 summary 仅 2~3 行（见 §6）。
2. ✅ /responses/compact：请求体 CompactionInput {model, input:[ResponseItem], instructions,
      tools:[], parallel_tool_calls, reasoning?, text?}；响应 {"output":[ResponseItem,...]}
      （CompactHistoryResponse，非完整 response 对象）。依据 codex-api common.rs / endpoint/compact.rs。
3. ✅ /responses/input_tokens：响应 {"input_tokens":<int>, "object":"response.input_tokens"}
      （两字段必填）。依据 openai-* SDK InputTokenCountResponse + openai-python 类型。
4. ✅ Codex 请求字段子集（v0.130.0 真实抓包，见 §8）。
      新发现并已纳入矩阵：client_metadata（Drop）、tools type:"namespace"（拍平 + namespace 回译）、
      内置工具 type:"web_search"/"image_generation"（无 name/parameters，ingress 放行→adapter Drop）、
      多数工具 type:function（无 custom/grammar/local_shell）、reasoning:null/include:[] 显式空值。
      ✅ TASK-11.04 ingress（decode + validation）已用该真实 fixture 回归验证通过
      （internal/app/gatewayapi/openai/responses/codex_fixture_test.go）。
```

```text
首份真实抓包冻结日期：2026-06-05
Codex 版本：v0.130.0（codex exec, read-only, reasoning effort=none）
fixture：internal/blackbox/fixtures/codex/20260605_151709.845_POST_v1_responses.json
⚠️ 该 raw fixture 含个人信息（MEMORY_SUMMARY、/Users/chenhao 路径、x-codex-installation-id），
   作为提交进 repo 的黑盒 fixture 前必须脱敏（TASK-11.15）。
源码冻结依据（codex-rs，main / 9a8730f3）：
  codex-api/src/sse/responses.rs（process_responses_event 消费事件子集）
  core/tests/common/responses.rs（ev_* 事件 JSON 形状：reasoning/message/function_call/completed）
  codex-api/src/common.rs（ResponsesApiRequest / CompactionInput 字段）
  codex-api/src/endpoint/compact.rs（responses/compact 路径 + CompactHistoryResponse{output}）
  protocol/src/openai_models.rs（GET /models 期望 ModelsResponse{models:[ModelInfo]}）
```
