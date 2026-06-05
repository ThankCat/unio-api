# Responses ↔ Chat Completions Bridge - 字段映射矩阵

本文档冻结 OpenAI Responses API（Codex 入口）与 Unio 内部 OpenAI Chat 契约
（`internal/core/adapter/openai.ChatRequest` / `ChatResponse` / `ChatStreamChunk`）之间的双向映射。

状态列：

```text
Pass    原样映射到 Chat 字段。
Adapt   语义等价但结构/字段名不同，需转换。
Drop    无 Chat 等价或本阶段不消费；不写 upstream，记内部审计（DEC-012）。
Reject  ingress 协议非法才 Reject；不因 provider 能力 Reject。
Verify  待真实 Codex / DeepSeek 黑盒冻结（实施 TASK-11.01 时清零）。
```

> 实现前，所有 `Verify` 必须先用真实 Codex `/responses` 抓包 + DeepSeek 黑盒冻结为
> Pass/Adapt/Drop/Reject，再开始翻译层生产代码（与 Phase 10 mapping 冻结流程一致）。

## 0. 端到端方向

```text
Codex → Responses 请求 → [responses_chat_map] → openai.ChatRequest → adapter/openai/deepseek
                                                                          → DeepSeek /chat/completions
DeepSeek → Chat 响应/SSE → adapter/openai/deepseek → openai.ChatResponse/Chunk
        → [responses_response_map / responses_stream] → Responses 响应/事件 → Codex
```

billing/审计走 adapter 同次解析产生的 `ResponseFacts`，与翻译层无关。

## 1. 请求顶层字段（Responses → Chat）

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
| `reasoning` (`{effort,summary}`) | `reasoning_effort` | Adapt | 取 `effort`；`summary` 见 §6。 |
| `text` (`{format,verbosity}`) | `response_format` + `verbosity` | Adapt | `format`→`response_format`，`verbosity`→`verbosity`。 |
| `stream` | `stream` | Pass | Codex 恒 true；流式强制 `stream_options.include_usage=true`。 |
| `store` | — | Drop | 无状态第一版（GAP-11-001）。 |
| `previous_response_id` | — | Drop/Reject | 无状态：第一版 Drop 并要求客户回传完整 input；如客户依赖会话续接则 Reject 并提示（GAP-11-001）。 |
| `include` | — | Drop | Responses 专属输出控制（GAP-11-004）。 |
| `truncation` | — | Drop | 无服务端上下文管理。 |
| `prompt_cache_key` / `prompt_cache_retention` / `safety_identifier` | 同名 Chat 字段（若有） | Adapt/Drop | 有 Chat 等价则 Pass，否则 Drop。 |
| `background` | — | Drop | 异步任务模式不支持。 |
| `service_tier` | `service_tier` | Pass | adapter 出站按能力 Drop。 |
| `prompt`（prompt template 引用） | — | Drop/Reject | server-side prompt 模板未实现。 |

## 2. input items → messages（Responses → Chat）

Responses `input` 是 item 数组，工具调用是 **顶层 item**，不像 Chat 嵌在 message 里。

| Responses input item | Chat message | 状态 | 说明 |
| --- | --- | --- | --- |
| `{type:"message", role, content:[...]}` | `{role, content}` | Adapt | content parts 见 §2.1。role: user/assistant/system/developer。 |
| `{type:"function_call", call_id, name, arguments}` | `assistant.tool_calls[{id:call_id, type:function, function:{name,arguments}}]` | Adapt | 连续 function_call 合并进 **同一条** assistant message。 |
| `{type:"function_call_output", call_id, output}` | `{role:"tool", tool_call_id:call_id, content:output}` | Adapt | 按 call_id 对齐到前一条 assistant.tool_calls。 |
| `{type:"reasoning", ...}` | `assistant.reasoning_content`（best-effort） | Adapt | 跨轮 reasoning 还原，保真度见 GAP-11-003。 |
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
| `{type:"function", name, description, parameters, strict}` | `{type:"function", function:{name,description,parameters,strict}}` | Adapt | 扁平→嵌套。`parameters` 缺 `type` 时补 `"type":"object"`。 |
| `{type:"custom", name, format:{type:"grammar",...}}` | `{type:"function", function:{name, parameters:{...string arg...}}}` 或 Drop | Adapt/Drop | **Codex apply_patch**：grammar/freeform 工具 Chat 无等价。冻结策略：convert→function（单 string 参数）保留可用性，或 Drop（GAP-11-002）。 |
| `{type:"local_shell"}` | — | Drop | Codex 内置 shell 工具，无 Chat 等价（GAP-11-002）。 |
| `{type:"web_search"/"web_search_preview"}` | `web_search_options` | Adapt/Drop | 第一版无真实执行；DeepSeek 不支持 → Drop（GAP-11-004）。 |
| `{type:"file_search"/"code_interpreter"/"computer_use"/"image_generation"}` | — | Drop | server-side 工具未实现（GAP-11-004）。 |
| `{type:"mcp", ...}` | passthrough/Drop | Drop | 本阶段不接 MCP。 |

### 3.2 tool_choice（归一，对齐 LiteLLM `_transform_tool_choice`）

| Responses tool_choice | Chat tool_choice | 状态 |
| --- | --- | --- |
| `"auto"` / `"none"` / `"required"` | 原样 | Pass |
| `{type:"auto"}` | `"auto"` | Adapt |
| `{type:"none"}` | `"none"` | Adapt |
| `{type:"required"}` / `{type:"any"}` / `{type:"tool"}`（无具体 name） | `"required"` | Adapt |
| `{type:"function", name}` / `{type:"function", function:{name}}` | `{type:"function", function:{name}}` | Adapt |
| `{type:"allowed_tools", ...}` | `"auto"` 或具名 | Adapt |

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

典型一次成功流（单文本输出）：

```text
response.created                      # status=in_progress，空 output
response.in_progress
response.output_item.added           # item:{type:"message", id:"msg_*", role:"assistant"}
response.content_part.added          # part:{type:"output_text"}
response.output_text.delta           # 每个 content delta 一条（delta=chunk.Content）
...
response.output_text.done            # 累积全文
response.content_part.done
response.output_item.done
response.completed                   # 完整 response 对象 + usage（durable closeout 后才发）
```

reasoning（DeepSeek `reasoning_content` 增量）：

```text
response.output_item.added           # item:{type:"reasoning", id:"rs_*"}
response.reasoning_summary_text.delta  # 或 response.reasoning_text.delta（按 Codex 期望冻结）
response.reasoning_summary_text.done
response.output_item.done
```

tool_calls（增量累积成 function_call）：

```text
response.output_item.added           # item:{type:"function_call", id:"fc_*", call_id, name}
response.function_call_arguments.delta  # 每段 arguments 增量
response.function_call_arguments.done   # 完整 arguments
response.output_item.done
```

状态机规则：

| 规则 | 说明 | 状态 |
| --- | --- | --- |
| sequence_number | 每个事件单调递增，从 0 开始。 | Verify（Codex 是否强校验顺序） |
| item/part index | content_part / output_index 稳定，不同 item 不串。 | Adapt |
| 文本 delta | `chunk.Content` → `response.output_text.delta`。 | Adapt |
| reasoning delta | `chunk.ReasoningContent` → `reasoning_*` 事件。 | Verify（事件名以 Codex 实际期望为准） |
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
| ledger 余额不足 | 429 | `insufficient_quota` | 与 chat 一致。 |

非法 Responses 协议结构 → 400 `invalid_request_error`（带 `param`）。
原始上游 body / credential / prompt / 完整正文不透传、不进默认审计。

## 8. Codex 专属坑（实施前必须用真实抓包确认）

| 现象 | 处理 | 关联 |
| --- | --- | --- |
| Codex 恒 `stream:true` | 流式是主路径，必须强制 `include_usage` 拿 final usage 结算。 | TASK-11.07 |
| Codex `store:false` + 每轮回传完整 input | 无状态可行：不需要 server-side 会话。 | GAP-11-001 |
| `previous_response_id` 出现 | 第一版 Drop 并要求完整 input；若客户真用会话续接则 Reject 提示。 | GAP-11-001 |
| `apply_patch` = `{type:"custom",format:{type:"grammar"}}` | convert→function（单 string 参数）或 Drop；决定 Codex 编辑能力是否可用。 | GAP-11-002 |
| `local_shell` 内置工具 | Drop（DeepSeek 无此能力）。 | GAP-11-002 |
| `reasoning:{effort,summary}` | `effort`→`reasoning_effort`；`summary` 仅影响是否发 reasoning 事件，按 Codex 期望冻结。 | TASK-11.08 |
| `text:{format,verbosity}` | `format`→`response_format`，`verbosity`→`verbosity`。 | TASK-11.08 |
| 多 function_call 并行 | 合并进单条 assistant.tool_calls；流式分项发 `output_item`。 | §2 / §6 |
| `instructions` 很大（Codex system prompt） | 注入首条 system，tokenizer 估算需计入。 | TASK-11.05 |

## 9. 冻结结果（TASK-11.01 填写）

```text
冻结日期：<待填>
真实 Codex 抓包 fixture：<路径>
真实 DeepSeek 黑盒 gate：DEEPSEEK_BLACKBOX=1 + DEEPSEEK_API_KEY
本表 Verify 全部清零：<是/否>
```
