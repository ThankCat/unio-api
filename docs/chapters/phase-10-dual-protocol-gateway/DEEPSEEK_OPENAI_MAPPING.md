# Phase 10 DeepSeek OpenAI Mapping

## 1. 上游信息

参考：

- [DeepSeek Quick Start](https://api-docs.deepseek.com/)
- [DeepSeek Create Chat Completion](https://api-docs.deepseek.com/api/create-chat-completion)
- [DeepSeek Thinking Mode](https://api-docs.deepseek.com/guides/thinking_mode)
- [DeepSeek Tool Calls](https://api-docs.deepseek.com/guides/tool_calls)

上游：

```text
Base URL: https://api.deepseek.com
Endpoint: POST /chat/completions
协议族:   openai
Adapter:  internal/core/adapter/openai/deepseek
```

状态：

| 状态 | 含义 |
| --- | --- |
| `Pass` | 字段名和语义一致，显式写入 wire。 |
| `Adapt` | 显式转换后写入 wire。 |
| `No-op` | DeepSeek 接收但不生效；Unio 明确记录并按策略处理。 |
| `Reject` | 调用上游前返回安全 400。 |
| `Verify` | 文档不足，必须黑盒确认后转为其他状态。 |

Phase 10 关闭前不允许保留 `Verify`。

## 2. 请求映射

| OpenAI 客户字段 | DeepSeek wire | 策略 | 说明 |
| --- | --- | --- | --- |
| `model` | `model` | `Adapt` | 使用 routing 选中的 upstream model。 |
| `messages` | `messages` | `Adapt` | DeepSeek 只接受明确支持的 role 和 string content。 |
| `audio` | - | `Reject` | DeepSeek Chat Completion 未声明 audio output。 |
| `frequency_penalty` | `frequency_penalty` | `No-op` | DeepSeek 文档标记 deprecated、不再生效。 |
| `function_call` | `tool_choice` | `Adapt` | legacy function choice 转换为 tool choice；无法无损转换时 Reject。 |
| `functions` | `tools` | `Adapt` | legacy function list 转换为 function tools。 |
| `logit_bias` | - | `Reject` | 上游未声明。 |
| `logprobs` | `logprobs` | `Pass` | 与 `top_logprobs` 配套。 |
| `max_completion_tokens` | `max_tokens` | `Adapt` | DeepSeek 使用 `max_tokens`。与客户同时传 `max_tokens` 时必须校验冲突。 |
| `max_tokens` | `max_tokens` | `Pass` | deprecated OpenAI 字段仍显式支持。 |
| `metadata` | - | `Reject` | 上游未声明。 |
| `modalities` | - | `Adapt` | 仅允许省略或明确 `["text"]`；其他值 Reject。 |
| `n` | - | `Reject` | 上游未声明多 choice。 |
| `parallel_tool_calls` | - | `Verify` | 黑盒确认 DeepSeek 是否支持以及缺省语义。 |
| `prediction` | - | `Reject` | 上游未声明。 |
| `presence_penalty` | `presence_penalty` | `No-op` | DeepSeek 文档标记 deprecated、不再生效。 |
| `prompt_cache_key` | - | `Reject` | DeepSeek cache 隔离字段是 `user_id`，不能假设语义等价。 |
| `prompt_cache_retention` | - | `Reject` | 上游未声明。 |
| `reasoning_effort` | `reasoning_effort` | `Adapt` | DeepSeek 接受 `high`、`max`，并兼容映射 `low`/`medium`→`high`、`xhigh`→`max`。 |
| `response_format.type=text` | `response_format.type=text` | `Pass` | |
| `response_format.type=json_object` | 同名 | `Pass` | 客户 prompt 仍需要求 JSON。 |
| `response_format.type=json_schema` | - | `Reject` | DeepSeek 文档未声明 schema 模式。 |
| `safety_identifier` | `user_id` | `Verify` | 需要确定与 legacy `user` 并存时的优先级。 |
| `seed` | - | `Reject` | 上游未声明。 |
| `service_tier` | - | `Reject` | 上游未声明。 |
| `stop` | `stop` | `Pass` | 支持 string 或 string 数组，最多 16 条。 |
| `store` | - | `Reject` | 上游未声明。 |
| `stream` | `stream` | `Pass` | data-only SSE + `[DONE]`。 |
| `stream_options.include_usage` | 同名 | `Pass` | adapter 内部为 settlement 强制启用。 |
| `stream_options.include_obfuscation` | - | `Reject` | 上游未声明。 |
| `temperature` | `temperature` | `Pass` | thinking 模式下如无效果，矩阵与测试要明确。 |
| `tool_choice` | `tool_choice` | `Adapt` | DeepSeek 支持 none、auto、required、named function。 |
| `tools[].type=function` | 同名 | `Pass` | 最多 128 个 function。 |
| `tools[].type=custom` | - | `Reject` | DeepSeek 只声明 function tools。 |
| `top_logprobs` | `top_logprobs` | `Pass` | 依赖 `logprobs=true`。 |
| `top_p` | `top_p` | `Pass` | thinking 模式下如无效果，矩阵与测试要明确。 |
| `user` | `user_id` | `Adapt` | 校验 DeepSeek `user_id` 字符集与长度。 |
| `verbosity` | - | `Reject` | 上游未声明。 |
| `web_search_options` | - | `Reject` | 上游未声明。 |
| extension `thinking.type` | `thinking.type` | `Pass` | `enabled` 或 `disabled`。 |

## 3. `messages[]` 映射

| OpenAI 客户消息 | DeepSeek wire | 策略 |
| --- | --- | --- |
| `developer` role | `system` role | `Adapt` |
| `system` role | `system` role | `Pass` |
| `user` string content | 同名 | `Pass` |
| `user` content part array | - | `Reject` |
| `assistant.content` | 同名 | `Pass` |
| `assistant.reasoning_content` extension | 同名 | `Pass` |
| `assistant.tool_calls[].type=function` | 同名 | `Pass` |
| `assistant.tool_calls[].type=custom` | - | `Reject` |
| `assistant.audio` | - | `Reject` |
| `assistant.refusal` | - | `Reject` |
| `tool` role | 同名 | `Pass` |
| `function` role | `tool` role | `Adapt` 或 `Reject` |

`developer` → `system` 必须保持相对顺序。若多个 developer/system 消息合并会改变语义，应在实现前定义稳定转换规则并补测试。

DeepSeek 多轮 thinking + tools：

1. 收到客户回传的 `reasoning_content` 时必须原样发给 upstream。
2. 不得把 reasoning 合并进 `content`。
3. tool call 多轮必须保留 assistant `content`、`reasoning_content` 和 `tool_calls`。

## 4. 非流式响应映射

| DeepSeek wire 字段 | OpenAI 客户字段 | 策略 |
| --- | --- | --- |
| `id` | `id` | `Pass` |
| `object` | `object` | `Pass` |
| `created` | `created` | `Pass` |
| `model` | `model` | `Adapt`：返回客户请求的 Unio catalog model。 |
| `system_fingerprint` | `system_fingerprint` | `Pass` |
| `choices[].index` | 同名 | `Pass` |
| `choices[].message.role` | 同名 | `Pass` |
| `choices[].message.content` | 同名 | `Pass` |
| `choices[].message.reasoning_content` | 登记后的同名 extension | `Pass` |
| `choices[].message.tool_calls` | 同名 | `Pass` |
| `choices[].logprobs.content` | 同名 | `Pass` |
| `choices[].logprobs.reasoning_content` | 登记后的 extension | `Pass` |
| `finish_reason=stop` | 同名 | `Pass` |
| `finish_reason=length` | 同名 | `Pass` |
| `finish_reason=content_filter` | 同名 | `Pass` |
| `finish_reason=tool_calls` | 同名 | `Pass` |
| `finish_reason=insufficient_system_resource` | - | 转换为稳定 upstream failure；raw reason 进入 facts。不能向 OpenAI 客户伪造标准 finish reason。 |

## 5. Usage 映射

| DeepSeek wire 字段 | OpenAI 客户字段 | `usage.Facts` |
| --- | --- | --- |
| `usage.prompt_tokens` | `usage.prompt_tokens` | 校验总输入。 |
| `usage.prompt_cache_hit_tokens` | `usage.prompt_tokens_details.cached_tokens` | `CacheReadInputTokens` |
| `usage.prompt_cache_miss_tokens` | - | `UncachedInputTokens` |
| `usage.completion_tokens` | `usage.completion_tokens` | `OutputTokensTotal` |
| `usage.total_tokens` | `usage.total_tokens` | 校验总量。 |
| `usage.completion_tokens_details.reasoning_tokens` | 同名 | `ReasoningOutputTokens` |

必须校验：

```text
prompt_tokens = prompt_cache_hit_tokens + prompt_cache_miss_tokens
total_tokens  = prompt_tokens + completion_tokens
```

DeepSeek OpenAI endpoint 没有 cache write TTL 事实：

```text
CacheWrite5mInputTokens = not_applicable
CacheWrite1hInputTokens = not_applicable
```

## 6. 流式响应映射

DeepSeek OpenAI endpoint 使用 OpenAI 风格 SSE：

```text
data: {chunk}
data: [DONE]
```

必须处理：

| DeepSeek SSE | OpenAI 客户 SSE | 规则 |
| --- | --- | --- |
| `delta.role` | 同名 | 首包保留。 |
| `delta.reasoning_content` | 同名 extension | 与 `content` 分离。 |
| `delta.content` | 同名 | |
| `delta.tool_calls` | 同名 | 保留 index 和 arguments 增量。 |
| `logprobs.content` | 同名 | typed。 |
| `logprobs.reasoning_content` | 登记后的 extension | typed。 |
| `finish_reason` | 同名或 failure | `insufficient_system_resource` 按失败处理。 |
| final `usage` | 客户可选 usage 尾包 + internal facts | 即使 upstream usage 与非空 choices 同包，也必须提取；先完成 durable closeout，再由 gatewayapi/openai 按 `include_usage` 写客户尾包。 |
| `[DONE]` | `[DONE]` | adapter 截留；lifecycle 持久化 facts 并 settlement 或 schedule recovery 后，由 gatewayapi/openai 写出。 |

原 `adapter/openai/streamtranslate` 的职责收口到：

```text
internal/core/adapter/openai/deepseek/stream.go
```

## 7. 内部输入 tokenizer

实现位置：

```text
internal/core/adapter/openai/deepseek/tokenizer.go
```

实现规则：

1. 独立实现 `openai.ChatInputTokenizer`。
2. 输入为 `openai.ChatCompletionRequest` 与 routing 选中的 `upstreamModel`。
3. 复用本 adapter 的 request map 规则，按即将发送的 DeepSeek OpenAI wire
   `messages`、`tools` 与 framing 估算。
4. 返回值是 authorization 使用的保守输入 token 估算，不是 settlement 使用的
   upstream usage 事实。
5. 不调用 Anthropic tokenizer，不共享 provider tokenizer facade，不通过跨协议
   中间 DTO 复用。
6. 直接在 adapter 实现与黑盒验收中校准，不新增独立 playground 前置任务。

## 8. 错误映射

`adapter/openai/deepseek` 负责：

1. 解析 upstream HTTP status。
2. 提取安全 request ID。
3. 解析 DeepSeek 错误码和安全摘要。
4. 映射为稳定 `adapter.UpstreamError` category。
5. 不把原始 body 返回 gatewayapi。

gatewayapi/openai 负责渲染 OpenAI error shape。

## 9. 黑盒清单

至少覆盖：

| ID | 场景 |
| --- | --- |
| `DS-OAI-01` | 基础非流式 text。 |
| `DS-OAI-02` | 基础流式 text + `[DONE]`。 |
| `DS-OAI-03` | thinking 非流式 reasoning/content 分离。 |
| `DS-OAI-04` | thinking 流式 reasoning/content 分离。 |
| `DS-OAI-05` | tools 多轮，reasoning 回传。 |
| `DS-OAI-06` | response_format json_object。 |
| `DS-OAI-07` | logprobs + top_logprobs。 |
| `DS-OAI-08` | cache hit/miss usage → facts。 |
| `DS-OAI-09` | OpenAI 不支持字段明确 Reject。 |
| `DS-OAI-10` | retry/fallback 每次真实调用各有 attempt。 |
| `DS-OAI-11` | stream tail error / cancel delivery audit。 |
| `DS-OAI-12` | `parallel_tool_calls` 黑盒冻结策略。 |
| `DS-OAI-13` | `safety_identifier` 与 `user` 优先级冻结。 |
| `DS-OAI-14` | OpenAI tokenizer 估算与 DeepSeek OpenAI 实际 `usage.prompt_tokens` 校准。 |
