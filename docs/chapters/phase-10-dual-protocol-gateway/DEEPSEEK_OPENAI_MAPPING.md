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

全局原则见 [DEC-012 协议为先与 Provider 映射 Drop 策略](../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略)。

Ingress 合法 OpenAI 字段一律进入 adapter contract；本表只定义 **出站 upstream wire** 与 **入站公开响应** 策略。

状态：

| 状态 | 含义 |
| --- | --- |
| `Pass` | 字段名和语义一致，显式写入 upstream wire；响应侧显式写入 OpenAI DTO。 |
| `Adapt` | 显式转换后写入 upstream wire 或公开响应。 |
| `Drop` | ingress 可收；**不写入 upstream body**（请求）或 **不写入公开响应**（响应）。必须记 `dropped_*_fields` 内部审计。 |
| `Verify` | 文档不足，必须黑盒确认后转为其他状态。 |

Phase 10 关闭前不允许保留 `Verify`。

**不再使用** mapping 层 `Reject` / `No-op`：原 Reject / No-op 均收口为 `Drop`（出站不发送）；原「上游会 400」项同样 Drop，避免把 DeepSeek 不认识的键送进 body。

黑盒冻结记录（2026-06-01，DeepSeek OpenAI endpoint `https://api.deepseek.com`，最小请求验证）：

- `parallel_tool_calls`、`safety_identifier` 已冻结为 **Drop**（DeepSeek 接受但无效；出站不发送，防未来 upstream 报错）。
- `user` 纯 text content 数组 DeepSeek 实际接受（200）→ **Pass**。
- 含 `image_url`/`input_audio`/`file` 的 content part → **Drop** part（不进入 upstream messages；若 message 变空按 mapping 规则处理）。
- 若误传 `n>1`、`json_schema`、`custom tool` 到 DeepSeek 会 400 → Unio **Drop**，不得进入 upstream body。
- usage 形状：`prompt_tokens = prompt_cache_hit_tokens + prompt_cache_miss_tokens`；reasoning 走 `completion_tokens_details.reasoning_tokens`。
- `deepseek-chat`/`deepseek-reasoner` 响应 `model` 可能回 `deepseek-v4-flash`；公开响应 **Adapt** 为客户 catalog model，审计记 upstream model。

## 2. 请求映射（ingress OpenAI → DeepSeek wire）

| OpenAI 客户字段 | DeepSeek wire | 策略 | 说明 |
| --- | --- | --- | --- |
| `model` | `model` | `Adapt` | routing 选中的 upstream model。 |
| `messages` | `messages` | `Adapt` | 按 §3 逐条/map；只发送 DeepSeek 可接受的 wire 形态。 |
| `audio` | - | `Drop` | 不写入 upstream；黑盒：DeepSeek 200 只产文本。 |
| `frequency_penalty` | - | `Drop` | DeepSeek deprecated；出站不发送。 |
| `function_call` | `tool_choice` | `Adapt` | legacy → tool_choice；无法无损 **Adapt** 时 **Drop** `function_call`。 |
| `functions` | `tools` | `Adapt` | legacy function list → function tools；无法转换时 **Drop** `functions`。 |
| `logit_bias` | - | `Drop` | 黑盒：DeepSeek 忽略；出站不发送。 |
| `logprobs` | `logprobs` | `Pass` | 与 `top_logprobs` 配套。 |
| `max_completion_tokens` | `max_tokens` | `Adapt` | 与客户同时传 `max_tokens` 时按冲突规则 **Adapt**（优先 completion tokens）。 |
| `max_tokens` | `max_tokens` | `Pass` | 仅当未使用 `max_completion_tokens` 映射时写入。 |
| `metadata` | - | `Drop` | 出站不发送。 |
| `modalities` | - | `Drop` | 非 `["text"]` 或无法映射时整字段 Drop；不发送 upstream。 |
| `n` | - | `Drop` | 仅支持单 choice；`n>1` 不得进入 upstream。 |
| `parallel_tool_calls` | - | `Drop` | 黑盒：不生效；出站不发送。 |
| `prediction` | - | `Drop` | 出站不发送。 |
| `presence_penalty` | - | `Drop` | DeepSeek deprecated；出站不发送。 |
| `prompt_cache_key` | - | `Drop` | 出站不发送。 |
| `prompt_cache_retention` | - | `Drop` | 出站不发送。 |
| `reasoning_effort` | `reasoning_effort` | `Adapt` | `low`/`medium`→`high`，`xhigh`→`max` 等枚举映射。 |
| `response_format.type=text` | 同名 | `Pass` | |
| `response_format.type=json_object` | 同名 | `Pass` | |
| `response_format.type=json_schema` | - | `Drop` | 整段 `response_format` 或 schema 部分 Drop，不发送 upstream。 |
| `safety_identifier` | - | `Drop` | 出站不发送；不映射为 `user_id`。 |
| `seed` | - | `Drop` | 出站不发送。 |
| `service_tier` | - | `Drop` | 出站不发送。 |
| `stop` | `stop` | `Pass` | string 或数组，最多 16 条。 |
| `store` | - | `Drop` | 出站不发送。 |
| `stream` | `stream` | `Pass` | |
| `stream_options.include_usage` | 同名 | `Pass` | adapter 内部 settlement 强制启用 usage。 |
| `stream_options.include_obfuscation` | - | `Drop` | 出站不发送。 |
| `temperature` | `temperature` | `Pass` | |
| `tool_choice` | `tool_choice` | `Adapt` | none、auto、required、named function。 |
| `tools[].type=function` | 同名 | `Pass` | 最多 128 个。 |
| `tools[].type=custom` | - | `Drop` | 从 tools 数组剔除 custom 项；若 tools 变空则整键 Drop。 |
| `top_logprobs` | `top_logprobs` | `Pass` | 依赖 `logprobs=true`。 |
| `top_p` | `top_p` | `Pass` | |
| `user` | `user_id` | `Adapt` | DeepSeek 上游 wire 用顶层 `user_id`（非标准 `user`）。校验字符集 `[a-zA-Z0-9_-]`、长度 ≤512 后写入；不满足则 **Drop**（不发送，避免上游 422）。出站永不发送标准 `user`。 |
| `verbosity` | - | `Drop` | 出站不发送。 |
| `web_search_options` | - | `Drop` | 出站不发送。 |
| extension `thinking.type` | `thinking.type` | `Pass` | `enabled` / `disabled`。 |

出站实现必须使用 **allowlist builder**：仅上表 Pass/Adapt 产物进入 `json.Marshal` 的 upstream body；Dropped 键不得出现。

## 3. `messages[]` 映射

| OpenAI 客户消息 | DeepSeek wire | 策略 |
| --- | --- | --- |
| `developer` role | `system` role | `Adapt` |
| `system` role | `system` role | `Pass` |
| `user` string content | 同名 | `Pass` |
| `user` content part array（纯 text part） | 同名 | `Pass` |
| `user` content part（image_url/input_audio/file） | - | `Drop` | 剔除 part；剩余 text/refusal 可 Pass。 |
| `assistant.content` | 同名 | `Pass` |
| `assistant.reasoning_content` extension | 同名 | `Pass` |
| `assistant.tool_calls[].type=function` | 同名 | `Pass` |
| `assistant.tool_calls[].type=custom` | - | `Drop` | 剔除该 tool_call。 |
| `assistant.audio` | - | `Drop` | 不写入 upstream message。 |
| `assistant.refusal` | - | `Drop` | 不写入 upstream message。 |
| `tool` role | 同名 | `Pass` |
| `function` role | `tool` role | `Adapt` | 无法无损转换时 **Drop** 该条 message。 |

`developer` → `system` 必须保持相对顺序。

DeepSeek 多轮 thinking + tools：

1. 客户回传的 `reasoning_content` 必须原样进入 upstream wire（**Pass**）。
2. 不得把 reasoning 合并进 `content`。
3. tool 多轮保留 assistant `content`、`reasoning_content`、`tool_calls`（经 Drop 清理后）。

## 4. 非流式响应映射（DeepSeek wire → 公开 OpenAI）

| DeepSeek wire 字段 | OpenAI 客户字段 | 策略 |
| --- | --- | --- |
| `id` | `id` | `Pass` |
| `object` | `object` | `Pass` |
| `created` | `created` | `Pass` |
| `model` | `model` | `Adapt` | 返回 Unio catalog model。 |
| `system_fingerprint` | `system_fingerprint` | `Pass` |
| `choices[].index` | 同名 | `Pass` |
| `choices[].message.role` | 同名 | `Pass` |
| `choices[].message.content` | 同名 | `Pass` |
| `choices[].message.reasoning_content` | 登记 extension | `Pass` |
| `choices[].message.tool_calls` | 同名 | `Pass` |
| `choices[].logprobs.content` | 同名 | `Pass` |
| `choices[].logprobs.reasoning_content` | 登记 extension | `Pass` |
| `finish_reason=stop/length/content_filter/tool_calls` | 同名 | `Pass` |
| `finish_reason=insufficient_system_resource` | - | `Adapt` | 稳定 upstream failure；raw reason 进 facts；不伪造 OpenAI finish_reason。 |
| 其他 upstream 字段 | - | `Drop` | 不进公开 DTO；需要的进 ResponseFacts。 |

## 5. Usage 映射

| DeepSeek wire 字段 | OpenAI 客户字段 | `usage.Facts` |
| --- | --- | --- |
| `usage.prompt_tokens` | `usage.prompt_tokens` | 校验总输入。 |
| `usage.prompt_cache_hit_tokens` | `usage.prompt_tokens_details.cached_tokens` | `CacheReadInputTokens` |
| `usage.prompt_cache_miss_tokens` | - | `UncachedInputTokens` |
| `usage.completion_tokens` | `usage.completion_tokens` | `OutputTokensTotal` |
| `usage.total_tokens` | `usage.total_tokens` | 校验总量。 |
| `usage.completion_tokens_details.reasoning_tokens` | 同名 | `ReasoningOutputTokens` |
| 其他 usage 维度 | - | `Drop`（公开） | 仅 facts 需要的维度写入 Facts。 |

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

| DeepSeek SSE | OpenAI 客户 SSE | 策略 |
| --- | --- | --- |
| `delta.role` | 同名 | `Pass` |
| `delta.reasoning_content` | extension | `Pass` |
| `delta.content` | 同名 | `Pass` |
| `delta.tool_calls` | 同名 | `Pass` |
| `logprobs.*` | 同名 | `Pass` |
| `finish_reason`（常规） | 同名 | `Pass` |
| `finish_reason=insufficient_system_resource` | failure | `Adapt` |
| final `usage` | 可选尾包 + facts | `Adapt` |
| `[DONE]` | `[DONE]` | `Adapt` | adapter 截留；lifecycle 后再写出。 |
| 其他 SSE 字段 | - | `Drop` | 不进公开 chunk。 |

## 7. 内部输入 tokenizer

实现位置：

```text
internal/core/adapter/openai/deepseek/tokenizer.go
```

规则：

1. 独立实现 `openai.ChatInputTokenizer`。
2. **必须基于 Drop 后的 upstream wire** 估算（与 `buildUpstreamWire` 同一 allowlist），不得按客户原始全量参数估算。
3. 返回值用于 authorization 保守冻结，不是 settlement 事实。

## 8. 错误映射

`adapter/openai/deepseek` 负责 upstream HTTP 错误分类；**不因 mapping Drop 而返回 `CodeAdapterRequestUnsupported`**。

gatewayapi/openai 负责渲染 OpenAI error shape。

## 9. 黑盒清单

实现位置：`internal/core/adapter/openai/deepseek/blackbox_test.go`（gate 在 `DEEPSEEK_BLACKBOX=1` + `DEEPSEEK_API_KEY`）。

| ID | 场景 | 状态 |
| --- | --- | --- |
| `DS-OAI-01` | 基础非流式 text。 | done（实时通过） |
| `DS-OAI-02` | 基础流式 text + `[DONE]`。 | done（实时通过） |
| `DS-OAI-03` | thinking 非流式 reasoning/content 分离。 | done（reasoner，含 facts reasoning tokens） |
| `DS-OAI-04` | thinking 流式 reasoning/content 分离。 | done（reasoner 流式 + 尾包 usage） |
| `DS-OAI-05` | tools 多轮，reasoning 回传。 | 可选回归（tools/reasoning 透传由 typed/单测覆盖） |
| `DS-OAI-06` | response_format json_object。 | 可选回归（json_object 为 Pass） |
| `DS-OAI-07` | logprobs + top_logprobs。 | done（实时透传 choice.logprobs.top_logprobs） |
| `DS-OAI-08` | cache hit/miss usage → facts。 | 非确定性（难稳定触发缓存命中；usage 字段已黑盒冻结） |
| `DS-OAI-09` | 客户传 DeepSeek 不支持字段：ingress 200 路径，**upstream body 不含 dropped 键**，内部审计含 drop 列表。 | done（实时 200 + 审计 dropped 列表；upstream body 不含 dropped 键由 `drop_test` wire 单测断言） |
| `DS-OAI-10` | retry/fallback 每次真实调用各有 attempt。 | lifecycle 范畴，归 TASK-10.05/10.10（mock 单测覆盖） |
| `DS-OAI-11` | stream tail error / cancel delivery audit。 | lifecycle 范畴，归 TASK-10.05/10.10 |
| `DS-OAI-12` | `parallel_tool_calls` Drop 策略。 | done（`drop_test` 单测 + DS-OAI-09 实时 dropped 列表含 `parallel_tool_calls`） |
| `DS-OAI-13` | `safety_identifier` 与 `user` 优先级。 | done（`safety_identifier` Drop、`user`→`user_id` Adapt，由 drop/adapt 单测 + DS-OAI-09 覆盖） |
| `DS-OAI-14` | tokenizer 与 DeepSeek 实际 `usage.prompt_tokens` 校准（基于 dropped wire）。 | done（恒 ≥ 上游 prompt_tokens；ratio≈1.8 英文段落 ~2.75 短句/中文；保守达标） |
