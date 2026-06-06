来源（交叉印证，非官方 API reference 单一来源）：

- OpenAI Developer Community「Responses API streaming - the simple guide to events」（_j，2026-01-21）——响应对象 / error 对象 / `compaction` item / status 枚举
- OpenAI 官方 API reference 方法页 <https://developers.openai.com/api/reference/resources/responses>（抓取超时，方法签名按公开惯例整理）
- Unio 阶段 11 [PLAN.md](../chapters/phase-11-openai-responses-api/PLAN.md) 接口范围决策

> 官方逐方法 schema 页抓取超时。本文件中 `compact` / `input_tokens` 两个较新接口的 **请求体** 官方
> 细节未能从权威来源完整确认，标 `Verify`，须在 TASK-11.01 用真实 Codex 抓包冻结。其余有状态接口
> Unio 本阶段一律返回 501（见 [PLAN.md 接口范围](../chapters/phase-11-openai-responses-api/PLAN.md#responses-api-接口范围)），
> 故只记录 Unio 需要识别的 URL 形态与官方返回大形状，不逐字段冻结。

---

# OpenAI Responses API · 其余 endpoint 与 Error schema（协议快照补充）

提供 [openai_responses.md](openai_responses.md) 主文件之外的 **5 个非创建 endpoint + error schema**。
streaming 事件目录见 [openai_responses_streaming_events.md](openai_responses_streaming_events.md)。

## 目录

- [Endpoint 全集与 Unio 处理](#endpoint-全集与-unio-处理)
- [POST /responses/compact](#post-responsescompact)
- [POST /responses/input_tokens](#post-responsesinput_tokens)
- [GET /responses/{id}（retrieve）](#get-responsesid-retrieve)
- [DELETE /responses/{id}](#delete-responsesid)
- [POST /responses/{id}/cancel](#post-responsesidcancel)
- [GET /responses/{id}/input_items](#get-responsesidinput_items)
- [Error schema](#error-schema)
  - [非流式 error 对象](#非流式-error-对象)
  - [流式 error 事件](#流式-error-事件)
  - [error code 枚举](#error-code-枚举)
- [compaction output item](#compaction-output-item)

## Endpoint 全集与 Unio 处理

| Endpoint | 官方语义 | Unio 本阶段处理 | 任务 / GAP |
| --- | --- | --- | --- |
| `POST /v1/responses` | 创建响应 | **完整实现** | TASK-11.04~11.10（见 [openai_responses.md](openai_responses.md)） |
| `POST /v1/responses/compact` | 上下文压缩 | **降级**：DeepSeek 摘要 + Unio 自编码 `compaction` item | TASK-11.11 / GAP-11-007 |
| `POST /v1/responses/input_tokens` | 预检 input token 数 | **本地估算**：`ChatInputTokenizer` | TASK-11.12 / GAP-11-008 |
| `GET /v1/responses/{id}` | 取回已存响应 | **501** `unsupported_endpoint_stateless` | TASK-11.13 / GAP-11-009 |
| `DELETE /v1/responses/{id}` | 删除已存响应 | **501** | TASK-11.13 / GAP-11-009 |
| `POST /v1/responses/{id}/cancel` | 取消 background 任务 | **501** | TASK-11.13 / GAP-11-009 |
| `GET /v1/responses/{id}/input_items` | 列出输入项 | **501** | TASK-11.13 / GAP-11-009 |

## POST /responses/compact

**官方（已由 codex-rs 源码确认）**：长会话上下文压缩，**非流式 unary** 调用。

请求体 `CompactionInput`（`codex-api/src/common.rs`，Codex 客户端构造）：

```text
{
  model: <string>,
  input: [ResponseItem, ...],          # 待压缩的历史
  instructions: <string>,              # Codex 解析好的压缩指令（如「总结会话」），空串则不发
  tools: [],
  parallel_tool_calls: <bool>,
  reasoning?: {effort, summary},
  text?: {format, verbosity}
}
```

响应 `CompactHistoryResponse`（`codex-api/src/endpoint/compact.rs`）：

```text
{ "output": [ResponseItem, ...] }      # 压缩后的新历史；非完整 response 对象、非 SSE
```

> 纠正：早期文档以为返回完整 response 且必含 `{type:"compaction"}` item。源码实证是
> `{output:[ResponseItem]}` 包裹，Codex 把 `output` 直接当作压缩后的会话历史。`{type:"compaction"}`
> 只是其中一种合法 ResponseItem。

**Unio 降级实现**（TASK-11.11 / GAP-11-007）：

```text
请求：解析 CompactionInput（input[] + instructions）。
处理：拼成单条「会话压缩助手」Chat 请求（system=instructions），调既有 OpenAI ChatAdapter，
      走完整计费/lifecycle。
返回：{"output":[ 压缩后的 ResponseItem ]}。第一版可返回单条 message item 承载摘要：
      {"output":[{type:"message", role:"assistant"|"user", content:[{type:"output_text"|"input_text", text:<摘要>}]}]}
      或自编码 {type:"compaction", id:"cmp_<gen>", encrypted_content:"<unio-opaque-token>"}。
约束：encrypted_content（若用）由 Unio 自编码，仅 Unio 可解，不模拟 OpenAI 密文。
回传：下一轮 /responses 若带回 type:"compaction" item，桥接层在 responses_chat_map 还原为
      一条 summary system message（透明往返）。
```

## POST /responses/input_tokens

**官方（已由 openai-* SDK 类型确认）**：预检请求会消耗的 input token 数（用于 `auto_compact_limit`
时机判断）。请求体是 `/responses` 的子集（`input`/`model`/`instructions`/`tools`/`tool_choice`/
`reasoning`/`text`/`truncation`/`parallel_tool_calls`/`previous_response_id`/`conversation`，均可选）。
响应**确定**为：

```text
{ "input_tokens": <int>, "object": "response.input_tokens" }   # 两字段必填
```

> 依据 openai-node `InputTokenCountResponse`、openai-python `input_token_count_response.py`、
> openai-go `InputTokenCountResponse`。`object` 字段名已定，不再 `Verify`。

**Unio 本地估算实现**（TASK-11.12 / GAP-11-008）：

```text
请求：复用 /responses 的 input[]。
处理：responses_chat_map 翻译为 openai.ChatRequest → 取对应 channel 的 ChatInputTokenizer 估算。
返回：{"input_tokens":<N>, "object":"response.input_tokens"}
约束：不走 routing 候选选择、不调上游、不计费；与 OpenAI 服务端精确计数有偏差，不反映 prompt cache 折扣。
回退：如改判返回 501，仅 1 处 handler 改动（Codex 会回退本地估算）。
```

## GET /responses/{id}（retrieve）

**官方**：取回先前 `store=true` 创建的响应。支持 `?include[]`、`?stream=true`（回放事件）、
`?starting_after=<seq>`。返回一个完整 Response 对象（[openai_responses.md](openai_responses.md) Returns）。

**Unio**：无服务端存储（无状态商业承诺）。返回 **HTTP 501** + Responses 原生 error
`code:"unsupported_endpoint_stateless"`，提示客户每轮回传完整 `input`。

## DELETE /responses/{id}

**官方**：删除已存响应，返回 `{id, object:"response.deleted", deleted:true}`。

**Unio**：**HTTP 501** `unsupported_endpoint_stateless`。

## POST /responses/{id}/cancel

**官方**：取消 `background:true` 的异步任务，返回该 Response 对象（`status:"cancelled"`）。

**Unio**：`background` 本阶段被 Reject（见下），无异步任务可取消 → **HTTP 501**
`unsupported_endpoint_stateless`。

## GET /responses/{id}/input_items

**官方**：列出某响应的输入项，分页（`?limit`、`?order`、`?after`、`?before`），返回
`{object:"list", data:[...InputItem], first_id, last_id, has_more}`。

**Unio**：无服务端存储 → **HTTP 501** `unsupported_endpoint_stateless`。

> 另：`POST /v1/responses` 带 `background:true` → Unio **HTTP 400** `unsupported_background`
> （明确报错，不静默转同步）。见 [PLAN.md TASK-11.13](../chapters/phase-11-openai-responses-api/PLAN.md#task-11-13-unsupported-endpoints)。

## Error schema

### 非流式 error 对象

Response 对象上的 `error`（生成失败时）与顶层 HTTP error 共用结构核心：

```text
error: object | null
├▸ code: string      # required（枚举见下）
└▸ message: string   # required
```

HTTP 层错误（鉴权/参数/限流等）OpenAI 通常返回：

```json
{ "error": { "type": "<type>", "code": "<code|null>", "message": "...", "param": "<field|null>" } }
```

> Unio 渲染策略以 [RESPONSES_CHAT_BRIDGE.md §7](../chapters/phase-11-openai-responses-api/RESPONSES_CHAT_BRIDGE.md) 为准：
> 复用 `adapter.UpstreamCategoryOf`，上游 auth/permission **绝不**渲染成客户 401；原始上游 body /
> credential / prompt 不透传。

### 流式 error 事件

带外流错误（与 `response.failed` 区分）：

```text
event: error
data: {"type":"error","code":"<code>","message":"...","param":<string|null>,"sequence_number":<N>}
```

### error code 枚举

Response 对象 `error.code` 已确认枚举（社区源，对应 image/input 相关失败）：

```text
server_error
rate_limit_exceeded
invalid_prompt
vector_store_timeout
invalid_image
invalid_image_format
invalid_base64_image
invalid_image_url
image_too_large
image_too_small
image_parse_error
image_content_policy_violation
invalid_image_mode
image_file_too_large
unsupported_image_media_type
empty_image_file
failed_to_download_image
image_file_not_found
```

Unio 自有（非 OpenAI）扩展 code，用于无状态边界（本阶段商业承诺）：

```text
unsupported_endpoint_stateless   # HTTP 501，有状态 endpoint（retrieve/delete/cancel/input_items）
unsupported_background           # HTTP 400，/responses 带 background:true
```

> Response 对象 `status` 枚举（含取消态）：`completed | failed | in_progress | cancelled | queued | incomplete`。

## compaction output item

由社区源确认的输出项形状（出现在 `compact` 返回与可回传 input 中）：

```text
{type:"compaction"}:
├▸ type: "compaction"           # required
├▸ id: string | null            # optional
└▸ encrypted_content: string    # required（OpenAI 为服务端密文；Unio 为自编码 opaque token）
```

桥接处理：

```text
出（compact 返回）：Unio 生成 {type:"compaction", id:"cmp_*", encrypted_content:"<unio-opaque>"}。
入（下一轮 /responses 的 input[] 带回）：responses_chat_map 解码为一条 summary system message。
红线：Unio 不解析也不伪造 OpenAI 密文；只认自己签发的 opaque token，无法识别的 compaction
      item 按无状态降级处理（提示回传完整 input）。
```
