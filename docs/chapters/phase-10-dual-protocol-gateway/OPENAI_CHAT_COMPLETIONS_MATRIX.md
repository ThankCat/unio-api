# Phase 10 OpenAI Chat Completions Matrix

## 1. 参考与范围

原始参考：

- [docs/protocol/openai_chat_completion.md](../../protocol/openai_chat_completion.md)
- [OpenAI Chat Completions Create](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)

本阶段范围：

```text
POST /v1/chat/completions
```

本文件不是“当前已支持字段”列表，而是 Phase 10 必须逐项实现、透传、Reject
或验证的完整字段清单。字段进入 `Typed` 只代表 Unio 能识别、校验并交给 adapter
做显式策略，不代表当前 provider 或选中模型已经支持该能力。

Phase 10 只实现 Chat Completions 对话链路，不扩展图片、视频、音频、文件等
模型能力。相关字段仍必须全量建模；若 DeepSeek 或后续选中模型不能保持语义，
必须在调用上游前明确 Reject。模型能力判断后续应参考
[models.dev API](https://models.dev/api.json)、provider 官方文档和黑盒验收。

状态定义：

| 状态 | 含义 |
| --- | --- |
| `Typed` | Unio DTO 显式建模、校验并进入 adapter contract。 |
| `Passthrough` | 已登记扩展字段，保留原 JSON 并显式透传。 |
| `Reject` | 当前 DeepSeek upstream 无法保持语义，调用上游前返回安全 400。 |
| `Verify` | 必须用 DeepSeek 黑盒确认后转为 `Typed`、`Passthrough` 或 `Reject`。 |

规则：

1. Phase 10 关闭前不允许保留 `Verify`。
2. 顶层字段与 nested 字段都禁止 silent drop。
3. DeepSeek 不支持某字段，不代表 Unio HTTP DTO 可以不认识该字段。
4. 多模态、audio、file、web search 等字段 typed 化不等于本阶段支持对应模型能力。

## 2. 请求顶层字段

| OpenAI 字段 | Phase 10 | 说明 |
| --- | --- | --- |
| `messages` | `Typed` | 完整 message union，见下一节。 |
| `model` | `Typed` | 客户发送 Unio catalog model；adapter 使用 routing 后的 upstream model。 |
| `audio` | `Typed` | 输出 audio 配置；DeepSeek 是否支持见 mapping。 |
| `frequency_penalty` | `Typed` | 显式保留零值。 |
| `function_call` | `Typed` | deprecated，但公开协议仍存在。 |
| `functions` | `Typed` | deprecated，但公开协议仍存在。 |
| `logit_bias` | `Typed` | token ID → bias。 |
| `logprobs` | `Typed` | 与 response choice logprobs 对齐。 |
| `max_completion_tokens` | `Typed` | 新字段，provider adapter 决定是否映射为 `max_tokens`。 |
| `max_tokens` | `Typed` | deprecated，但公开协议仍存在。 |
| `metadata` | `Typed` | 公开协议 metadata，不等于内部 observability metadata。 |
| `modalities` | `Typed` | 例如 text、audio。 |
| `n` | `Typed` | 多 choice 输出。 |
| `parallel_tool_calls` | `Typed` | 工具并发控制。 |
| `prediction` | `Typed` | Predicted Outputs。 |
| `presence_penalty` | `Typed` | 显式保留零值。 |
| `prompt_cache_key` | `Typed` | OpenAI prompt cache 路由字段。 |
| `prompt_cache_retention` | `Typed` | OpenAI prompt cache retention。 |
| `reasoning_effort` | `Typed` | provider adapter 显式转换。 |
| `response_format` | `Typed` | `text`、`json_object`、`json_schema` union。 |
| `safety_identifier` | `Typed` | 安全标识。 |
| `seed` | `Typed` | best-effort determinism。 |
| `service_tier` | `Typed` | 服务等级。 |
| `stop` | `Typed` | `string` 或 `[]string`，不能只支持数组。 |
| `store` | `Typed` | 是否存储输出。 |
| `stream` | `Typed` | 非流式或流式。 |
| `stream_options` | `Typed` | `include_usage`、`include_obfuscation`。 |
| `temperature` | `Typed` | 显式保留零值。 |
| `tool_choice` | `Typed` | none、auto、required、function、custom union。 |
| `tools` | `Typed` | function 与 custom tool union。 |
| `top_logprobs` | `Typed` | 与 `logprobs` 配套。 |
| `top_p` | `Typed` | 显式保留零值。 |
| `user` | `Typed` | legacy user identifier。 |
| `verbosity` | `Typed` | 输出详细度。 |
| `web_search_options` | `Typed` | Chat Completions web search 配置。 |

## 3. `messages[]`

### Role union

| role | Phase 10 | 关键字段 |
| --- | --- | --- |
| `developer` | `Typed` | `content`、`name` |
| `system` | `Typed` | `content`、`name` |
| `user` | `Typed` | `content`、`name` |
| `assistant` | `Typed` | `content`、`refusal`、`name`、`audio`、`function_call`、`tool_calls`、登记后的 `reasoning_content` extension |
| `tool` | `Typed` | `content`、`tool_call_id` |
| `function` | `Typed` | deprecated；`content`、`name` |

### Content union

| content part | Phase 10 | 关键字段 |
| --- | --- | --- |
| string shorthand | `Typed` | 保留原字符串语义。 |
| `text` | `Typed` | `text` |
| `image_url` | `Typed` | `image_url.url`、`image_url.detail` |
| `input_audio` | `Typed` | `input_audio.data`、`input_audio.format` |
| `file` | `Typed` | `file.file_data`、`file.file_id`、`file.filename` |
| refusal part | `Typed` | assistant refusal content。 |

### Tool calls

| 字段 | Phase 10 | 说明 |
| --- | --- | --- |
| `tool_calls[].id` | `Typed` | 调用 ID。 |
| `tool_calls[].type=function` | `Typed` | `function.name`、`function.arguments`。 |
| `tool_calls[].type=custom` | `Typed` | `custom.name`、`custom.input`。 |
| `tool_call_id` | `Typed` | tool result 与调用关联。 |
| `function_call` | `Typed` | deprecated legacy function call。 |

## 4. 复杂请求字段

### `audio`

| 字段 | Phase 10 |
| --- | --- |
| `audio.format` | `Typed` |
| `audio.voice` | `Typed` |

### `prediction`

| 字段 | Phase 10 |
| --- | --- |
| `prediction.type` | `Typed` |
| `prediction.content` string | `Typed` |
| `prediction.content[]` text part | `Typed` |

### `response_format`

| 类型 | Phase 10 | 字段 |
| --- | --- | --- |
| `text` | `Typed` | `type` |
| `json_object` | `Typed` | `type` |
| `json_schema` | `Typed` | `json_schema.name`、`description`、`schema`、`strict` |

### `stream_options`

| 字段 | Phase 10 | 说明 |
| --- | --- | --- |
| `include_usage` | `Typed` | 客户是否希望看到 usage 尾包。内部 settlement 是否抓 usage 不依赖此值。 |
| `include_obfuscation` | `Typed` | provider 不支持时明确 Reject。 |

### `tools`

| 类型 | Phase 10 | 字段 |
| --- | --- | --- |
| `function` | `Typed` | `function.name`、`description`、`parameters`、`strict` |
| `custom` | `Typed` | `custom.name`、`description`、`format` |

### `tool_choice`

| 类型 | Phase 10 |
| --- | --- |
| `none` | `Typed` |
| `auto` | `Typed` |
| `required` | `Typed` |
| named `function` | `Typed` |
| named `custom` | `Typed` |

### `web_search_options`

| 字段 | Phase 10 |
| --- | --- |
| `search_context_size` | `Typed` |
| `user_location` | `Typed` |
| `user_location.type` | `Typed` |
| `user_location.approximate` | `Typed` |

## 5. 非流式响应

### 顶层

| 字段 | Phase 10 |
| --- | --- |
| `id` | `Typed` |
| `choices` | `Typed` |
| `created` | `Typed` |
| `model` | `Typed` |
| `object=chat.completion` | `Typed` |
| `service_tier` | `Typed` |
| `system_fingerprint` | `Typed` |
| `usage` | `Typed` |

### `choices[]`

| 字段 | Phase 10 |
| --- | --- |
| `finish_reason` | `Typed` |
| `index` | `Typed` |
| `logprobs.content[]` | `Typed` |
| `logprobs.refusal[]` | `Typed` |
| `message` | `Typed` |

### `message`

| 字段 | Phase 10 |
| --- | --- |
| `content` | `Typed` |
| `refusal` | `Typed` |
| `role=assistant` | `Typed` |
| `annotations[]` | `Typed` |
| `annotations[].url_citation` | `Typed` |
| `audio` | `Typed` |
| `function_call` | `Typed` |
| `tool_calls[]` function/custom union | `Typed` |
| 登记后的 `reasoning_content` extension | `Typed` |

### `usage`

| 字段 | Phase 10 |
| --- | --- |
| `prompt_tokens` | `Typed` |
| `completion_tokens` | `Typed` |
| `total_tokens` | `Typed` |
| `prompt_tokens_details.audio_tokens` | `Typed` |
| `prompt_tokens_details.cached_tokens` | `Typed` |
| `completion_tokens_details.accepted_prediction_tokens` | `Typed` |
| `completion_tokens_details.audio_tokens` | `Typed` |
| `completion_tokens_details.reasoning_tokens` | `Typed` |
| `completion_tokens_details.rejected_prediction_tokens` | `Typed` |

## 6. 流式响应

OpenAI stream 仍使用 data-only SSE：

```text
data: {ChatCompletionChunk JSON}

data: [DONE]
```

chunk 必须完整建模：

| 字段 | Phase 10 |
| --- | --- |
| `id` | `Typed` |
| `choices[]` | `Typed` |
| `created` | `Typed` |
| `model` | `Typed` |
| `object=chat.completion.chunk` | `Typed` |
| `service_tier` | `Typed` |
| `system_fingerprint` | `Typed` |
| `usage` | `Typed` |

delta 必须完整建模：

| 字段 | Phase 10 |
| --- | --- |
| `choices[].delta.role` | `Typed` |
| `choices[].delta.content` | `Typed` |
| `choices[].delta.refusal` | `Typed` |
| `choices[].delta.function_call` | `Typed` |
| `choices[].delta.tool_calls` | `Typed` |
| 登记后的 `choices[].delta.reasoning_content` extension | `Typed` |
| `choices[].finish_reason` | `Typed` |
| `choices[].logprobs` | `Typed` |

上游 final usage chunk 先转成内部 facts。只有 immutable recovery facts 已持久化，
并且 settlement 已完成或 durable recovery job 已接管后，`gatewayapi/openai` 才按
`include_usage` 输出客户可选 usage 尾包，最后写出由 adapter 截留的 `[DONE]`。

## 7. 错误响应

OpenAI ingress 对外保持 OpenAI error shape：

```json
{
  "error": {
    "message": "safe message",
    "type": "invalid_request_error",
    "param": "messages",
    "code": "invalid_request"
  }
}
```

规则：

1. `message` 只使用安全公开文案。
2. provider 原始 body 不透传。
3. nested 字段 Reject 必须返回可定位 `param`。
4. SSE 开始后使用 OpenAI data-only stream error，不能改回普通 JSON。

## 8. DeepSeek 实现要求

本矩阵只定义公开 OpenAI 协议。DeepSeek 的逐字段处理必须在 [DEEPSEEK_OPENAI_MAPPING.md](DEEPSEEK_OPENAI_MAPPING.md) 中给出：

```text
Pass
Adapt
No-op
Reject
Verify
```

Phase 10 关闭前不允许保留 `Verify`。
