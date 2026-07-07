# Phase 10 Anthropic Messages Matrix

## 1. 参考与范围

原始参考：

- [docs/protocol/anthropic/messages/official.md](../../protocol/anthropic/messages/official.md)
- [Anthropic Messages Create](https://platform.claude.com/docs/en/api/messages/create)
- [Anthropic Streaming Messages](https://platform.claude.com/docs/en/build-with-claude/streaming)

本阶段范围：

```text
POST /v1/messages
```

本文件不是“先支持 text-only”清单。Phase 10 必须逐项处理 Messages Create 的公开字段、
content block、response block、usage 和 stream event。字段进入 `Typed` 只代表
Unio ingress 能识别、校验并进入 adapter contract。

Phase 10 只实现 Messages 对话链路，不扩展图片、视频、音频、文件等模型能力。
image、document、container upload 等 block 仍必须全量 ingress 建模；provider 无法
转换时在 [DEEPSEEK_ANTHROPIC_MAPPING.md](DEEPSEEK_ANTHROPIC_MAPPING.md) 登记 **Drop**
（见 [DEC-012](../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略)）。

状态定义（ingress 层）：

| 状态 | 含义 |
| --- | --- |
| `Typed` | Unio DTO 显式建模、校验并进入 adapter contract。 |
| `Passthrough` | 已登记 extension 字段，ingress 保留原 JSON。 |
| `IngressReject` | 客户协议 JSON 结构/类型/union 非法；gatewayapi 返回协议原生 400。 |

Provider 出站 **Drop** 不在本矩阵定义，见 DeepSeek mapping。

规则：

1. Phase 10 关闭前 mapping 不允许保留 `Verify`。
2. ingress 禁止 decode 丢字段；provider 层 Drop 必须登记 mapping 并写内部审计。
3. Anthropic ingress 返回 Anthropic 原生结构，不转换成 OpenAI response。
4. image、document、container upload 等字段 typed 化不等于本阶段支持对应模型能力。

## 2. HTTP 边界

Anthropic SDK 需要原生 header：

| Header | Phase 10 | 规则 |
| --- | --- | --- |
| `x-api-key` | `Typed` | 解析为 Unio opaque API key 身份。 |
| `anthropic-version` | `Typed` | 校验并记录 ingress version。 |
| `anthropic-beta` | `Passthrough` | ingress 接受任意 beta（含未登记 / 逗号多值），不 400。**出站(官方 adapter)透传 + 小黑名单**（`beta.go`：默认转发，仅拦截有计费/解析缺口的 `code-execution` / `context-1m`）+ 脱敏审计，见 [passthrough-audit.md](../../providers/anthropic/passthrough-audit.md)。DeepSeek adapter 仍全 Drop。 |
| `mcp_servers` extension | `Passthrough` | ingress 可登记；provider **Drop**，见 mapping。 |
| `content-type` | `Typed` | 必须为 JSON。 |

公开错误使用 Anthropic error shape，不复用 OpenAI error body。

## 3. 请求顶层字段

| Anthropic 字段 | Phase 10 | 说明 |
| --- | --- | --- |
| `max_tokens` | `Typed` | 必填；允许协议文档定义的 `0` cache warm 场景。 |
| `messages` | `Typed` | 完整 message/content block union。 |
| `model` | `Typed` | 客户发送 Unio catalog model；adapter 使用 routing 后的 upstream model。 |
| `cache_control` | `Passthrough` | request 级 cache control。**注**:非顶层 typed 字段（不在 `decode.go: knownMessageFields`），经 `Extensions` 透传到上游 body。 |
| `container` | `Passthrough` | code execution container ID。经 `Extensions` 透传（非 Typed）。 |
| `inference_geo` | `Passthrough` | 推理区域偏好。经 `Extensions` 透传（非 Typed）。 |
| `metadata` | `Typed` | 包含 `user_id`。 |
| `output_config` | `Passthrough` | effort 与 structured output format。经 `Extensions` 透传（非 Typed）。 |
| `service_tier` | `Passthrough` | `auto` 或 `standard_only`。经 `Extensions` 透传（非 Typed）。 |
| `stop_sequences` | `Typed` | 字符串数组。 |
| `stream` | `Typed` | 非流式或流式。 |
| `system` | `Typed` | string 或 text block 数组；不能塞入 messages system role 代替。 |
| `temperature` | `Typed` | 显式保留零值。 |
| `thinking` | `Typed` | enabled、disabled、adaptive union。 |
| `tool_choice` | `Typed` | auto、any、tool、none union。 |
| `tools` | `Typed` | client tool 与 server tool union。 |
| `top_k` | `Typed` | sampling。 |
| `top_p` | `Typed` | sampling。 |

## 4. `messages[]`

### Role

| role | Phase 10 | 说明 |
| --- | --- | --- |
| `user` | `Typed` | 客户消息、tool result 等。 |
| `assistant` | `Typed` | 历史 assistant content、thinking、tool use 等。 |
| `system` | `Typed` | 仅用于文档登记的 mid-conversation system block 场景；普通 system prompt 仍使用顶层 `system`。 |

### Content

`content` 支持 string shorthand 或 content block 数组。

| content block | Phase 10 | 关键字段 |
| --- | --- | --- |
| string shorthand | `Typed` | 等价于单个 text block。 |
| `text` | `Typed` | `text`、`cache_control`、`citations` |
| `image` | `Typed` | base64 或 URL source、`cache_control` |
| `document` | `Typed` | base64 PDF、plain text、content、URL PDF source；citations、context、title |
| `search_result` | `Typed` | `content`、`source`、`title`、`cache_control`、citations |
| `thinking` | `Typed` | `thinking`、`signature` |
| `redacted_thinking` | `Typed` | `data` |
| `tool_use` | `Typed` | `id`、`name`、`input`、`caller`、`cache_control` |
| `tool_result` | `Typed` | `tool_use_id`、`content`、`is_error`、`cache_control` |
| `server_tool_use` | `Typed` | `id`、`name`、`input`、`caller` |
| `web_search_tool_result` | `Typed` | `tool_use_id`、result 或 error、`caller` |
| `web_fetch_tool_result` | `Typed` | `tool_use_id`、result 或 error、`caller` |
| `code_execution_tool_result` | `Typed` | `tool_use_id`、stdout/stderr/result 或 error |
| `bash_code_execution_tool_result` | `Typed` | `tool_use_id`、result 或 error |
| `text_editor_code_execution_tool_result` | `Typed` | `tool_use_id`、result 或 error |
| `tool_search_tool_result` | `Typed` | `tool_use_id`、result 或 error |
| `container_upload` | `Typed` | `file_id`、`cache_control` |
| `mid_conversation_system` | `Typed` | `content`、`cache_control` |

### `cache_control`

| 字段 | Phase 10 |
| --- | --- |
| `type=ephemeral` | `Typed` |
| `ttl=5m` | `Typed` |
| `ttl=1h` | `Typed` |

### Citations

| citation | Phase 10 |
| --- | --- |
| `char_location` | `Typed` |
| `page_location` | `Typed` |
| `content_block_location` | `Typed` |
| `web_search_result_location` | `Typed` |
| `search_result_location` | `Typed` |

## 5. `thinking`

| 类型 | Phase 10 | 字段 |
| --- | --- | --- |
| `enabled` | `Typed` | `budget_tokens`、`display` |
| `disabled` | `Typed` | `type` |
| `adaptive` | `Typed` | `display` |

规则：

1. thinking block 与普通 text block 分离。
2. signature 必须原样保存并允许客户在多轮对话中回传。
3. provider 忽略 `budget_tokens` 时，mapping 登记 **Drop** 并写内部审计。

## 6. `output_config`

| 字段 | Phase 10 |
| --- | --- |
| `effort` | `Typed` |
| `format.type=json_schema` | `Typed` |
| `format.schema` | `Typed` |

## 7. `tool_choice`

| 类型 | Phase 10 | 字段 |
| --- | --- | --- |
| `auto` | `Typed` | `disable_parallel_tool_use` |
| `any` | `Typed` | `disable_parallel_tool_use` |
| `tool` | `Typed` | `name`、`disable_parallel_tool_use` |
| `none` | `Typed` | `type` |

## 8. `tools[]`

Anthropic tools 是 union，不能只实现普通 function tool 后静默吞掉其他类型。

| tool 类型 | Phase 10 |
| --- | --- |
| client `custom` tool | `Typed` |
| `bash_20250124` | `Typed` |
| `code_execution_20250522` | `Typed` |
| `code_execution_20250825` | `Typed` |
| `code_execution_20260120` | `Typed` |
| `memory_20250818` | `Typed` |
| `text_editor_20250124` | `Typed` |
| `text_editor_20250429` | `Typed` |
| `text_editor_20250728` | `Typed` |
| `web_search_20250305` | `Typed` |
| `web_search_20260209` | `Typed` |
| `web_fetch_20250910` | `Typed` |
| `web_fetch_20260209` | `Typed` |
| `web_fetch_20260309` | `Typed` |
| `tool_search_tool_bm25_20251119` | `Typed` |
| `tool_search_tool_regex_20251119` | `Typed` |

DeepSeek 对 server tool definition 未逐项声明支持，mapping 登记 **Drop**（出站不发送）。
注意 `server_tool_use` 与 `web_search_tool_result` content block 已由 DeepSeek
文档声明支持，两者不能混淆。

## 9. 非流式响应

### 顶层

| 字段 | Phase 10 |
| --- | --- |
| `id` | `Typed` |
| `container` | `Typed` |
| `content[]` | `Typed` |
| `model` | `Typed` |
| `role=assistant` | `Typed` |
| `stop_details` | `Typed` |
| `stop_reason` | `Typed` |
| `stop_sequence` | `Typed` |
| `type=message` | `Typed` |
| `usage` | `Typed` |

### Response content block

| content block | Phase 10 |
| --- | --- |
| `text` | `Typed` |
| `thinking` | `Typed` |
| `redacted_thinking` | `Typed` |
| `tool_use` | `Typed` |
| `server_tool_use` | `Typed` |
| `web_search_tool_result` | `Typed` |
| `web_fetch_tool_result` | `Typed` |
| `code_execution_tool_result` | `Typed` |
| `bash_code_execution_tool_result` | `Typed` |
| `text_editor_code_execution_tool_result` | `Typed` |
| `tool_search_tool_result` | `Typed` |
| `container_upload` | `Typed` |

### Stop reason

| 值 | Phase 10 |
| --- | --- |
| `end_turn` | `Typed` |
| `max_tokens` | `Typed` |
| `stop_sequence` | `Typed` |
| `tool_use` | `Typed` |
| `pause_turn` | `Typed` |
| `refusal` | `Typed` |

### Usage

| 字段 | Phase 10 |
| --- | --- |
| `input_tokens` | `Typed` |
| `cache_creation_input_tokens` | `Typed` |
| `cache_read_input_tokens` | `Typed` |
| `cache_creation.ephemeral_5m_input_tokens` | `Typed` |
| `cache_creation.ephemeral_1h_input_tokens` | `Typed` |
| `output_tokens` | `Typed` |
| `output_tokens_details.thinking_tokens` | `Typed` |
| `server_tool_use.web_search_requests` | `Typed` |
| `server_tool_use.web_fetch_requests` | `Typed` |
| `service_tier` | `Typed` |
| `inference_geo` | `Typed` |

统一账务映射见 [RESPONSE_FACTS.md](RESPONSE_FACTS.md)。

## 10. 流式响应

Anthropic stream 使用 named SSE event：

| SSE event | Phase 10 | 说明 |
| --- | --- | --- |
| `message_start` | `Typed` | 消息元信息与初始 usage。 |
| `content_block_start` | `Typed` | 新 content block。 |
| `content_block_delta` | `Typed` | block 增量。 |
| `content_block_stop` | `Typed` | block 结束。 |
| `message_delta` | `Typed` | stop reason 与 usage 更新。 |
| `message_stop` | `Typed` | 流结束。 |
| `ping` | `Typed` | heartbeat。 |
| `error` | `Typed` | stream 内错误。 |

delta union：

| delta type | Phase 10 |
| --- | --- |
| `text_delta` | `Typed` |
| `input_json_delta` | `Typed` |
| `thinking_delta` | `Typed` |
| `signature_delta` | `Typed` |

规则：

1. 保留 content block index。
2. `input_json_delta.partial_json` 不能假设每包都是完整 JSON。
3. usage 由 adapter 累积，不要求 lifecycle 理解事件顺序。
4. upstream `message_stop` 由 adapter 截留；只有 immutable recovery facts 已持久化，
   并且 settlement 已完成或 durable recovery job 已接管后，`gatewayapi/anthropic`
   才写出客户可见 `message_stop`。
5. Anthropic stream 不写 OpenAI `[DONE]`。

## 11. 错误响应

Anthropic ingress 对外保持 Anthropic error shape：

```json
{
  "type": "error",
  "error": {
    "type": "invalid_request_error",
    "message": "safe message"
  },
  "request_id": "..."
}
```

规则：

1. provider 原始 body 不透传。
2. `request_id` 使用安全可公开 ID。
3. SSE 开始后写 Anthropic `error` event，不改回普通 JSON。

## 12. DeepSeek 实现要求

DeepSeek 的逐字段处理必须在 [DEEPSEEK_ANTHROPIC_MAPPING.md](DEEPSEEK_ANTHROPIC_MAPPING.md) 中给出：

```text
Pass
Adapt
Drop
```

Phase 10 关闭前不允许保留 `Verify`。
