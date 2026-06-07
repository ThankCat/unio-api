来源（交叉印证，非官方 API reference 单一来源）：

- OpenAI Developer Community「Responses API streaming - the simple guide to events」（_j，2025-10-19 / 2026-01-21，覆盖 53 个事件 + 请求/响应/错误 shape）
- 真实 wire dump：Alibaba Cloud Model Studio「Call Qwen via OpenAI Responses API」流式样例（印证 `response.output_text.delta` / `response.reasoning_summary_text.delta` 字段）
- OpenAI 官方 streaming 指南 <https://developers.openai.com/api/docs/guides/streaming-responses?api-mode=responses>（事件并集 + `sequence_number` 语义）

> 官方 `developers.openai.com` API reference 的逐事件 schema 页在抓取时超时，未能作为单一权威来源直采。
> 本文件以上述多源交叉整理，凡 Unio 桥接真实依赖的事件（envelope / 文本 / reasoning / function_call）
> 均与真实 wire dump 对齐；标 `Verify` 的字段须在 TASK-11.01 用真实 Codex `/responses` 抓包再确认。

---

# OpenAI Responses API · Streaming Events 目录（协议快照补充）

Unio 阶段 11 [TASK-11.01](../../../chapters/phase-11-openai-responses-api/PLAN.md#task-11-01-protocol-freeze)
权威字段来源之一，提供 [openai_responses.md](openai_responses.md) 主文件之外的 **完整 streaming events 目录**。

- 主文件 [openai_responses.md](openai_responses.md)：`POST /responses` body / Returns / 示例。
- 其余 endpoint + error schema：[official-other-endpoints.md](official-other-endpoints.md)。
- 桥接消费方：[../chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md](../../../chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md) §6 流式状态机。

## 目录

- [SSE 帧格式与公共字段](#sse-帧格式与公共字段)
- [事件全集（53 种）](#事件全集53-种)
  - [A. Response envelope 生命周期](#a-response-envelope-生命周期)
  - [B. 输出项装配（message / 文本 / refusal）](#b-输出项装配message--文本--refusal)
  - [C. Reasoning summary 流式](#c-reasoning-summary-流式)
  - [D. Reasoning text 流式](#d-reasoning-text-流式)
  - [E. Function tool 调用](#e-function-tool-调用)
  - [F. Custom tool 调用](#f-custom-tool-调用)
  - [G. MCP tool 调用](#g-mcp-tool-调用)
  - [H. File Search 内置工具](#h-file-search-内置工具)
  - [I. Web Search 内置工具](#i-web-search-内置工具)
  - [J. Code Interpreter 内置工具](#j-code-interpreter-内置工具)
  - [K. Image Generation 内置工具](#k-image-generation-内置工具)
  - [L. Audio 输出与转写](#l-audio-输出与转写)
- [典型事件序列（lifecycle recipes）](#典型事件序列lifecycle-recipes)
- [不变量（实现必须遵守）](#不变量实现必须遵守)
- [Unio × DeepSeek 桥接相关性分级](#unio--deepseek-桥接相关性分级)

## SSE 帧格式与公共字段

每个事件是标准 SSE 两行（与 Chat Completions 的 data-only 不同，Responses 带 `event:` 名）：

```text
event: <type>
data: {"type":"<type>", ..., "sequence_number": <N>}
<空行>
```

公共字段：

| 字段 | 语义 |
| --- | --- |
| `type` | 事件类型（与 `event:` 行一致）。 |
| `sequence_number` | 单调递增整数，每事件 +1；用于排序、检测丢包、去重。 |
| `item_id` | 输出项唯一 ID（如 `msg_*` / `rs_*` / `fc_*`）。 |
| `output_index` | 输出项在 `response.output[]` 的位置。 |
| `content_index` | 项内 `content[]` 的位置（文本 / reasoning_text 用）。 |
| `summary_index` | reasoning summary 列表内位置（reasoning summary 用）。 |

实现要点：delta 是增量追加，`.done` 携带权威最终串；只有 `response.completed` 带 `usage`，此前 `response` 回显里 `usage=null`；未知 key 必须忽略以保持前向兼容。

## 事件全集（53 种）

### A. Response envelope 生命周期

| event | 出现时机 | data 关键字段 | 处理 |
| --- | --- | --- | --- |
| `response.queued` | 请求被接受并入队 | `response`, `sequence_number` | 标记 queued。 |
| `response.created` | 创建后立即；`status=in_progress` | `response`, `sequence_number` | 初始化状态，记 `response.id`。 |
| `response.in_progress` | 生成中（可多次） | `response`, `sequence_number` | 更新状态；参数被反复回显。 |
| `response.completed` | 成功终态 | `response`（完整 `output[]` + `usage`）, `sequence_number` | 停止；读 usage；持久化。 |
| `response.incomplete` | 提前结束（max_output_tokens / content_filter） | `response.incomplete_details.reason`, `sequence_number` | 停止；展示 incomplete 原因。 |
| `response.failed` | 生成失败终态 | `response.error`（`code`,`message`）, `sequence_number` | 停止；展示错误；重试/上报。 |
| `error` | 传输/流级错误（带外，与 `response.failed` 区分） | `code`, `message`, `param`, `sequence_number` | 视为流错误；中止并按需重试。 |

### B. 输出项装配（message / 文本 / refusal）

| event | 出现时机 | data 关键字段 | 处理 |
| --- | --- | --- | --- |
| `response.output_item.added` | 新输出项加入（如 assistant message） | `output_index`, `item`(id,type,role/status), `sequence_number` | 按 `item.id`/`output_index` 建缓冲。 |
| `response.content_part.added` | 项内新 content part（如 output_text） | `item_id`, `output_index`, `content_index`, `part`, `sequence_number` | 初始化该 part 缓冲（常为空串）。 |
| `response.output_text.delta` | assistant 文本增量 | `item_id`, `output_index`, `content_index`, `delta`, `logprobs?`, `sequence_number` | 追加文本。 |
| `response.output_text.done` | content part 最终全文 | `item_id`, `output_index`, `content_index`, `text`, `logprobs?`, `sequence_number` | 用最终文本确认缓冲。 |
| `response.output_text.annotation.added` | 文本注解（引用等） | `item_id`, `output_index`, `content_index`, `annotation_index`, `annotation`, `sequence_number` | 附加注解元数据。 |
| `response.refusal.delta` | 拒答增量（替代正常文本） | `item_id`, `output_index`, `content_index`, `delta`, `sequence_number` | 追加 refusal 文本。 |
| `response.refusal.done` | 拒答最终串 | `item_id`, `output_index`, `content_index`, `refusal`, `sequence_number` | 终结 refusal。 |
| `response.content_part.done` | content part 完成 | `item_id`, `output_index`, `content_index`, `part`, `sequence_number` | 关闭该 part。 |
| `response.output_item.done` | 输出项完成（如 message.status=completed） | `output_index`, `item`, `sequence_number` | 关闭整项，可渲染/持久化。 |

### C. Reasoning summary 流式

可分享的「reasoning summary」（区别于私有 chain-of-thought）。

| event | 出现时机 | data 关键字段 | 处理 |
| --- | --- | --- | --- |
| `response.output_item.added` | reasoning 项创建（`type=reasoning`） | `output_index`, `item`(id,type=reasoning), `sequence_number` | 初始化 summary 缓冲。 |
| `response.reasoning_summary_part.added` | 新 summary part 开始 | `item_id`, `output_index`, `summary_index`, `part`, `sequence_number` | 建 part 缓冲。 |
| `response.reasoning_summary_text.delta` | summary 文本增量 | `item_id`, `output_index`, `summary_index`, `delta`, `sequence_number` | 追加。 |
| `response.reasoning_summary_text.done` | summary part 最终文本 | `item_id`, `output_index`, `summary_index`, `text`, `sequence_number` | 终结。 |
| `response.reasoning_summary_part.done` | summary part 完成 | `item_id`, `output_index`, `summary_index`, `part`, `sequence_number` | 关闭 part。 |
| `response.output_item.done` | reasoning 项完成 | `output_index`, `item`, `sequence_number` | 终结 reasoning 预览。 |

### D. Reasoning text 流式

reasoning 正文（非 summary，GPT-OSS 类模型可流式分 part）。

| event | data 关键字段 | 处理 |
| --- | --- | --- |
| `response.content_part.added` | `item_id`, `output_index`, `content_index`, `part`, `sequence_number` | 建 reasoning_text 缓冲。 |
| `response.reasoning_text.delta` | `item_id`, `output_index`, `content_index`, `delta`, `sequence_number` | 追加（通常不展示给终端用户）。 |
| `response.reasoning_text.done` | `item_id`, `output_index`, `content_index`, `text`, `sequence_number` | 终结。 |
| `response.content_part.done` | `item_id`, `output_index`, `content_index`, `part`, `sequence_number` | 关闭 part。 |

### E. Function tool 调用

为 `function_call` 项流式发送 JSON 参数。

| event | data 关键字段 | 处理 |
| --- | --- | --- |
| `response.output_item.added` | `output_index`, `item`(id,type=function_call,call_id,name), `sequence_number` | 按 `item_id`/`call_id` 跟踪。 |
| `response.function_call_arguments.delta` | `item_id`, `output_index`, `delta`, `sequence_number` | 追加原始 JSON 串（done 前不解析）。 |
| `response.function_call_arguments.done` | `item_id`, `output_index`, `name`, `arguments`, `sequence_number` | 解析 JSON 调用函数。 |
| `response.output_item.done` | `output_index`, `item`, `sequence_number` | 调用「已发出」，等待 tool 输出。 |

### F. Custom tool 调用

模型调用 custom 工具（非 function）时流式发送其 input。

| event | data 关键字段 | 处理 |
| --- | --- | --- |
| `response.output_item.added` | `output_index`, `item`(id,type=custom_tool_call,call_id,name), `sequence_number` | 开始缓冲输入。 |
| `response.custom_tool_call_input.delta` | `item_id`, `output_index`, `delta`, `sequence_number` | 追加原始输入。 |
| `response.custom_tool_call_input.done` | `item_id`, `output_index`, `input`, `sequence_number` | 消费/分发。 |
| `response.output_item.done` | `output_index`, `item`, `sequence_number` | 进入等待 tool 输出。 |

### G. MCP tool 调用

| event | data 关键字段 | 处理 |
| --- | --- | --- |
| `response.output_item.added` | `output_index`, `item`(id,type=mcp_call,server_label,name,approval_request_id?), `sequence_number` | 跟踪 MCP 调用。 |
| `response.mcp_call_arguments.delta` | `item_id`, `output_index`, `delta`, `sequence_number` | 追加 JSON 串。 |
| `response.mcp_call_arguments.done` | `item_id`, `output_index`, `arguments`, `sequence_number` | 解析分发。 |
| `response.mcp_call.in_progress` | `item_id`, `output_index`, `sequence_number` | 状态=calling。 |
| `response.mcp_call.completed` | `item_id`, `output_index`, `sequence_number` | 收集输出。 |
| `response.mcp_call.failed` | `item_id`, `output_index`, `sequence_number` | 上报失败。 |
| `response.mcp_list_tools.in_progress` | `item_id`, `output_index`, `sequence_number` | 工具发现 loading。 |
| `response.mcp_list_tools.completed` | `item_id`, `output_index`, `sequence_number` | 读取并缓存工具表。 |
| `response.mcp_list_tools.failed` | `item_id`, `output_index`, `sequence_number` | 上报失败。 |

### H. File Search 内置工具

| event | data 关键字段 | 处理 |
| --- | --- | --- |
| `response.output_item.added` | `output_index`, `item`(id,type=file_search_call,status), `sequence_number` | 跟踪搜索会话。 |
| `response.file_search_call.in_progress` | `output_index`, `item_id`, `sequence_number` | 准备搜索。 |
| `response.file_search_call.searching` | `output_index`, `item_id`, `sequence_number` | 搜索中。 |
| `response.file_search_call.completed` | `output_index`, `item_id`, `sequence_number` | 读取 results。 |
| `response.output_item.done` | `output_index`, `item`, `sequence_number` | 终结。 |

### I. Web Search 内置工具

| event | data 关键字段 | 处理 |
| --- | --- | --- |
| `response.output_item.added` | `output_index`, `item`(id,type=web_search_call,action), `sequence_number` | 跟踪会话与 action。 |
| `response.web_search_call.in_progress` | `output_index`, `item_id`, `sequence_number` | 准备。 |
| `response.web_search_call.searching` | `output_index`, `item_id`, `sequence_number` | 搜索/访问页面。 |
| `response.web_search_call.completed` | `output_index`, `item_id`, `sequence_number` | 读取结果/引用源。 |
| `response.output_item.done` | `output_index`, `item`, `sequence_number` | 终结。 |

### J. Code Interpreter 内置工具

| event | data 关键字段 | 处理 |
| --- | --- | --- |
| `response.output_item.added` | `output_index`, `item`(id,status,container_id), `sequence_number` | 跟踪运行会话与容器。 |
| `response.code_interpreter_call.in_progress` | `output_index`, `item_id`, `sequence_number` | 启动中。 |
| `response.code_interpreter_call.interpreting` | `output_index`, `item_id`, `sequence_number` | 运行中。 |
| `response.code_interpreter_call_code.delta` | `output_index`, `item_id`, `delta`, `sequence_number` | 追加代码预览。 |
| `response.code_interpreter_call_code.done` | `output_index`, `item_id`, `code`, `sequence_number` | 终结代码缓冲。 |
| `response.code_interpreter_call.completed` | `output_index`, `item_id`, `sequence_number` | 读取输出（日志/图）。 |
| `response.output_item.done` | `output_index`, `item`, `sequence_number` | 终结。 |

### K. Image Generation 内置工具

| event | data 关键字段 | 处理 |
| --- | --- | --- |
| `response.output_item.added` | `output_index`, `item`(id,status), `sequence_number` | 跟踪图像会话。 |
| `response.image_generation_call.in_progress` | `output_index`, `item_id`, `sequence_number` | 启动。 |
| `response.image_generation_call.generating` | `output_index`, `item_id`, `sequence_number` | 生成中。 |
| `response.image_generation_call.partial_image` | `output_index`, `item_id`, `partial_image_index`, `partial_image_b64`, `sequence_number` | 渐进预览。 |
| `response.image_generation_call.completed` | `output_index`, `item_id`, `sequence_number` | 读取最终 base64。 |
| `response.output_item.done` | `output_index`, `item`, `sequence_number` | 终结。 |

### L. Audio 输出与转写

未公开发布（unreleased），列出以备前向兼容。

| event | data 关键字段 | 处理 |
| --- | --- | --- |
| `response.audio.delta` | `delta`(base64), `sequence_number` | 追加音频缓冲。 |
| `response.audio.done` | `sequence_number` | 关闭音频流。 |
| `response.audio.transcript.delta` | `delta`, `sequence_number` | 追加转写。 |
| `response.audio.transcript.done` | `sequence_number` | 终结转写。 |

## 典型事件序列（lifecycle recipes）

纯文本（Unio × DeepSeek 主路径）：

```text
response.created → response.in_progress
response.output_item.added (message) → response.content_part.added (output_text)
response.output_text.delta × N → response.output_text.done
response.content_part.done → response.output_item.done (message)
response.completed (含 usage)
```

reasoning summary + 文本（DeepSeek Reasoner 路径）：

```text
response.created → response.in_progress
response.output_item.added (reasoning)
  → response.reasoning_summary_part.added
  → response.reasoning_summary_text.delta × N → response.reasoning_summary_text.done
  → response.reasoning_summary_part.done → response.output_item.done (reasoning)
response.output_item.added (message) → response.content_part.added (output_text)
  → response.output_text.delta × N → response.output_text.done
  → response.content_part.done → response.output_item.done (message)
response.completed (含 usage)
```

function tool 调用：

```text
response.output_item.added (function_call)
response.function_call_arguments.delta × N → response.function_call_arguments.done
response.output_item.done (function_call)
```

custom tool 调用：

```text
response.output_item.added (custom_tool_call)
response.custom_tool_call_input.delta × N → response.custom_tool_call_input.done
response.output_item.done
```

## 不变量（实现必须遵守）

```text
1. sequence_number 单调递增，逐流；用于排序与去重。
2. item_id 在一条响应内唯一；output_index 是其在 response.output 的位置。
3. content_index 作用于项内 content[]；summary_index 作用于 reasoning summary 列表。
4. delta 累加，.done 携带权威最终串。
5. 只有 response.completed 带 usage；此前 response 回显 usage=null。
6. 未知 key（obfuscation、实验性 metadata）必须忽略以前向兼容。
7. 客户可在 completed/incomplete/failed 后停止读取，但通常 drain 到 EOF。
```

## Unio × DeepSeek 桥接相关性分级

桥接层（`responses_stream.go`）需要 **生成** 的事件 = DeepSeek Chat SSE 能产出的内容对应项。其余事件
属于 OpenAI 专属能力，DeepSeek 不会产生，桥接层 **不生成**（见
[CAPABILITY_MATRIX.md](../../../chapters/phase-11-openai-responses-api/CAPABILITY_MATRIX.md)）。

> **codex-rs 源码实证（main / 9a8730f3）**：Codex 的 `process_responses_event` 只消费
> `response.created` / `output_item.added` / `output_text.delta` / `reasoning_summary_text.delta` /
> `reasoning_text.delta` / `reasoning_summary_part.added` / `custom_tool_call_input.delta` /
> `output_item.done` / `response.completed` / `failed` / `incomplete`，**其余全部忽略**。
> 因此下表「必须生成」中标 ⊘ 的事件对 Codex 是可选的（仍可发以兼容标准 SDK），而 `output_item.done`
> 是 Codex 拿最终内容的**权威载体**。

| 分级 | 事件 | 桥接动作 |
| --- | --- | --- |
| **必须生成**（DeepSeek 文本） | `response.created` / `output_item.added`(message) / `output_text.delta` / **`output_item.done`** / `response.completed`；⊘可选：`in_progress` / `content_part.added` / `output_text.done` / `content_part.done` | 由 Chat chunk 翻译生成；`output_item.done` 必带完整 message item；durable closeout 后才发 `completed`。 |
| **必须生成**（DeepSeek Reasoner） | `output_item.added`(reasoning) / **`reasoning_text.delta`**(带 `content_index`) / **`output_item.done`**(reasoning, `content:[{type:"reasoning_text"}]`) | **已冻结**：`chunk.ReasoningContent` → `reasoning_text.*`（开源模型语义，codex-rs 源码确认）。兜底切 `reasoning_summary_text.*`+`summary_index`。 |
| **必须生成**（function 工具） | `output_item.added`(function_call) / **`output_item.done`**(完整 `{call_id,name,arguments}`，MCP 工具加 `namespace`)；⊘可选：`function_call_arguments.delta` / `.done`（Codex 不消费 function 工具的 args delta，只读 `output_item.done`） | 由 Chat `tool_calls` delta 累积翻译；最终 args 必须落在 `output_item.done`。 |
| **错误终态** | `response.failed` / `response.incomplete` | 上游错误 / recovery facts 未持久化 / 提前结束时生成；Codex 据此映射 ApiError。 |
| **不生成**（OpenAI 专属） | custom_tool_call_* / mcp_* / file_search_* / web_search_* / code_interpreter_* / image_generation_* / audio_* / annotation.added | DeepSeek 无对应能力；内置工具不真实执行（GAP-11-004）。 |

> Codex v0.130 实测 `apply_patch` 经 `exec_command` function 跑、**不发** `type=custom` 工具（见
> BRIDGE §3.1/§8）。故 DeepSeek 路径的工具流式表现就是 **function_call 事件族**；`custom_tool_call_*`
> 仅作旧/未来版本兜底。MCP `namespace` 工具的 function_call item 额外带 `namespace` 字段（BRIDGE §3.3）。
