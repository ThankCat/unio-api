# Phase 10 DeepSeek Anthropic Mapping

## 1. 上游信息

参考：

- [DeepSeek Quick Start](https://api-docs.deepseek.com/)
- [DeepSeek Anthropic API](https://api-docs.deepseek.com/guides/anthropic_api)
- [项目内 DeepSeek Anthropic API 兼容性参考摘要](../../protocol/deepseek_anthropic_api.md)

上游：

```text
Base URL: https://api.deepseek.com/anthropic
Endpoint: POST /v1/messages
协议族:   anthropic
Adapter:  internal/core/adapter/anthropic/deepseek
```

状态：

| 状态 | 含义 |
| --- | --- |
| `Pass` | 字段名和语义一致，显式写入 wire。 |
| `Adapt` | 显式转换后写入 wire。 |
| `Ignored` | DeepSeek 文档明确说明会忽略。Unio 必须选择公开策略并测试。 |
| `Reject` | 调用上游前返回安全 400。 |
| `Verify` | 文档不足，必须黑盒确认后转为其他状态。 |

Phase 10 关闭前不允许保留 `Verify`。

## 2. 模型映射

DeepSeek 官方会对 Anthropic 请求中的模型名做隐式映射：

| 客户传入的模型名 | DeepSeek 实际映射 |
| --- | --- |
| `claude-opus*` | `deepseek-v4-pro` |
| `claude-haiku*` | `deepseek-v4-flash` |
| `claude-sonnet*` | `deepseek-v4-flash` |
| 其他不支持的模型名 | `deepseek-v4-flash` |

Unio 不依赖该隐式行为：

1. 客户仍然请求 Unio catalog model。
2. routing 选择 channel-model，并显式给出 `upstreamModel`。
3. DeepSeek Anthropic channel-model 的 `upstream_model` 必须是已登记 DeepSeek 模型，
   不能把拼写错误或未知值交给 DeepSeek 静默降级到 `deepseek-v4-flash`。
4. adapter response map 仍将客户可见 `model` 恢复为 Unio catalog model；
   request、attempt 和成本审计记录显式 upstream model。

## 3. Header 映射

| Anthropic 客户 Header | DeepSeek upstream | 策略 | 说明 |
| --- | --- | --- | --- |
| `x-api-key: unio_sk_...` | `x-api-key: <channel credential>` | `Adapt` | |
| `anthropic-version` | 同名 | `Ignored` | DeepSeek 文档说明忽略。Unio ingress 仍需校验支持版本并记录审计事实。 |
| `anthropic-beta` | 同名或移除 | `Ignored` | DeepSeek 文档说明忽略。Unio ingress 仍只接受登记 beta；是否允许某个 beta 进入该 channel 必须明确配置。 |
| `content-type` | `application/json` | `Pass` | |

## 4. 顶层请求映射

| Anthropic 客户字段 | DeepSeek wire | 策略 | 说明 |
| --- | --- | --- | --- |
| `max_tokens` | `max_tokens` | `Pass` | 必填。 |
| `messages` | `messages` | `Adapt` | 仅允许 DeepSeek 支持的 content block。 |
| `model` | `model` | `Adapt` | 使用 routing 选中的显式 DeepSeek upstream model；不依赖上游隐式映射。 |
| `cache_control` | - | `Verify` | 需确认 DeepSeek Anthropic endpoint 的 cache control 行为。 |
| `container` | 同名或移除 | `Ignored` | DeepSeek 文档说明忽略。默认 Reject，避免客户误以为 container 生效。 |
| `inference_geo` | - | `Reject` | DeepSeek 未声明。 |
| `mcp_servers` extension | 同名或移除 | `Ignored` | DeepSeek 文档说明忽略。若 ingress 登记该扩展，必须明确公开策略。 |
| `metadata.user_id` | `metadata.user_id` | `Pass` | DeepSeek 文档声明支持，用于 rate limit 与隔离。 |
| `metadata` 其他字段 | 同名或移除 | `Ignored` | DeepSeek 文档说明忽略。默认 Reject 未登记字段。 |
| `output_config.effort` | 同名 | `Pass` | DeepSeek 文档声明仅支持 `output_config.effort`。 |
| `output_config.format` | - | `Reject` | DeepSeek 文档声明 `output_config` 仅支持 `effort`，无法保持 JSON schema 语义。 |
| `service_tier` | 同名或移除 | `Ignored` | DeepSeek 文档说明忽略。默认 Reject，避免客户误以为服务等级生效。 |
| `stop_sequences` | `stop_sequences` | `Pass` | |
| `stream` | `stream` | `Pass` | |
| `system` | `system` | `Pass` | 保持 string 或 text block 数组语义。 |
| `temperature` | `temperature` | `Pass` | DeepSeek 文档声明范围为 `[0.0, 2.0]`；thinking 模式下效果需黑盒确认。 |
| `thinking.type` | `thinking.type` | `Adapt` | DeepSeek 文档声明 thinking 支持；允许值需黑盒冻结。 |
| `thinking.budget_tokens` | 同名 | `Ignored` | DeepSeek 文档说明忽略。Unio 公开策略必须明确，不能 silent drop。 |
| `thinking.display` | 同名或 - | `Verify` | 黑盒冻结。 |
| `tool_choice` | `tool_choice` | `Pass` | `none`、`auto`、`any` 和 `tool` 均支持；嵌套 ignored 字段见下文。 |
| `tools` | `tools` | `Adapt` | 仅允许 DeepSeek 支持的 client tool。 |
| `top_k` | `top_k` | `Ignored` | DeepSeek 文档说明忽略。 |
| `top_p` | `top_p` | `Pass` | thinking 模式下效果需黑盒确认。 |

## 5. `messages[].content[]` 映射

| Anthropic content block 或字段 | DeepSeek wire | 策略 | 说明 |
| --- | --- | --- | --- |
| string shorthand | string 或 text block | `Pass` | |
| `text.text` | 同名 | `Pass` | |
| `text.cache_control` | 同名或移除 | `Ignored` | DeepSeek 文档说明忽略。 |
| `text.citations` | 同名或移除 | `Ignored` | DeepSeek 文档说明忽略。 |
| `image` | - | `Reject` | DeepSeek 文档明确不支持。 |
| `document` | - | `Reject` | DeepSeek 文档明确不支持。 |
| `search_result` | - | `Reject` | DeepSeek 文档明确不支持。 |
| `thinking` | 同名 | `Pass` | DeepSeek 文档声明支持。 |
| `redacted_thinking` | - | `Reject` | DeepSeek 文档明确不支持。 |
| `tool_use.id`、`input`、`name` | 同名 | `Pass` | |
| `tool_use.cache_control` | 同名或移除 | `Ignored` | DeepSeek 文档说明忽略。 |
| `tool_result.tool_use_id`、`content` | 同名 | `Pass` | |
| `tool_result.cache_control` | 同名或移除 | `Ignored` | DeepSeek 文档说明忽略。 |
| `tool_result.is_error` | 同名或移除 | `Ignored` | DeepSeek 文档说明忽略。默认 Reject，避免错误结果被当作普通结果。 |
| `server_tool_use` | 同名 | `Pass` | DeepSeek 文档声明支持。 |
| `web_search_tool_result` | 同名 | `Pass` | DeepSeek 文档声明支持。 |
| `web_fetch_tool_result` | - | `Reject` | DeepSeek 官方兼容表未登记为支持。 |
| `code_execution_tool_result` | - | `Reject` | DeepSeek 文档明确不支持。 |
| `mcp_tool_use` | - | `Reject` | DeepSeek 文档明确不支持。 |
| `mcp_tool_result` | - | `Reject` | DeepSeek 文档明确不支持。 |
| `bash_code_execution_tool_result` | - | `Reject` | DeepSeek 官方兼容表未登记为支持。 |
| `text_editor_code_execution_tool_result` | - | `Reject` | DeepSeek 官方兼容表未登记为支持。 |
| `tool_search_tool_result` | - | `Reject` | DeepSeek 官方兼容表未登记为支持。 |
| `container_upload` | - | `Reject` | DeepSeek 文档明确不支持。 |
| `mid_conversation_system` | - | `Reject` | DeepSeek 官方兼容表未登记为支持。 |

DeepSeek 官方文档明确说明 Anthropic API 当前不支持图片、文档、search result、
redacted thinking、code execution result、MCP tool block 和 container upload。其他未登记
高级 block 也必须 Reject 或逐项黑盒冻结，不能因为 DTO 已建模就默认上游支持。

## 6. `tools[]` 与 `tool_choice` 映射

| Anthropic tool 类型 | DeepSeek wire | 策略 |
| --- | --- | --- |
| client custom tool | 同名 | `Pass` |
| `bash_20250124` | - | `Reject` |
| `code_execution_*` | - | `Reject` |
| `memory_20250818` | - | `Reject` |
| `text_editor_*` | - | `Reject` |
| `web_search_*` | - | `Reject` |
| `web_fetch_*` | - | `Reject` |
| `tool_search_tool_*` | - | `Reject` |

client custom tool 字段：

| 字段 | DeepSeek wire | 策略 |
| --- | --- | --- |
| `name` | 同名 | `Pass` |
| `input_schema` | 同名 | `Pass` |
| `description` | 同名 | `Pass` |
| `cache_control` | 同名或移除 | `Ignored` |

`tool_choice`：

| 类型或字段 | DeepSeek wire | 策略 |
| --- | --- | --- |
| `none` | 同名 | `Pass` |
| `auto` | 同名 | `Pass` |
| `any` | 同名 | `Pass` |
| `tool` | 同名 | `Pass` |
| `disable_parallel_tool_use` | 同名或移除 | `Ignored` |

## 7. 非流式响应映射

DeepSeek Anthropic endpoint 返回 Anthropic 风格 Message。adapter 不能直接透传原始 body，必须 typed decode 后再构造客户响应与 facts。

| DeepSeek wire 字段 | Anthropic 客户字段 | 策略 |
| --- | --- | --- |
| `id` | `id` | `Pass` |
| `type=message` | 同名 | `Pass` |
| `role=assistant` | 同名 | `Pass` |
| `model` | `model` | `Adapt`：返回客户请求的 Unio catalog model。 |
| `content[].type=text` | 同名 | `Pass` |
| `content[].type=thinking` | 同名 | `Verify`：thinking/signature 必须完整。 |
| `content[].type=redacted_thinking` | - | `Reject`：DeepSeek 文档明确不支持该 block。 |
| `content[].type=tool_use` | 同名 | `Pass` |
| `content[].type=server_tool_use` | 同名 | `Pass`：DeepSeek 文档声明支持。 |
| `content[].type=web_search_tool_result` | 同名 | `Pass`：DeepSeek 文档声明支持。 |
| 其他 content block | 对应原生字段 | `Verify` 或 `Reject` |
| `stop_reason` | 同名 | `Pass` |
| `stop_sequence` | 同名 | `Pass` |
| `stop_details` | 同名 | `Verify` |
| `container` | 同名 | `Verify` |
| `usage` | 同名 + `usage.Facts` | `Adapt` |

规则：

1. 客户收到 Anthropic Message，不收到 OpenAI choices。
2. thinking block 与 text block 分离。
3. tool_use input 必须保留 JSON object，不压扁成字符串。
4. upstream 未返回的协议字段按 Anthropic JSON 省略/null 语义输出，不能伪造。

## 8. Usage 映射

DeepSeek Anthropic endpoint 的 usage 字段必须黑盒确认。实现前先冻结下表：

| DeepSeek wire 字段 | Anthropic 客户字段 | `usage.Facts` | 当前 |
| --- | --- | --- | --- |
| `input_tokens` | 同名 | `UncachedInputTokens` 或待 cache breakdown 后计算 | `Verify` |
| `cache_read_input_tokens` | 同名 | `CacheReadInputTokens` | `Verify` |
| `cache_creation_input_tokens` | 同名 | cache write 总量校验 | `Verify` |
| `cache_creation.ephemeral_5m_input_tokens` | 同名 | `CacheWrite5mInputTokens` | `Verify` |
| `cache_creation.ephemeral_1h_input_tokens` | 同名 | `CacheWrite1hInputTokens` | `Verify` |
| `output_tokens` | 同名 | `OutputTokensTotal` | `Verify` |
| `output_tokens_details.thinking_tokens` | 同名 | `ReasoningOutputTokens` | `Verify` |
| `server_tool_use.*` | 同名 | `MeteredItem` | `Verify` |
| `service_tier` | 同名 | safe metadata | `Verify` |
| `inference_geo` | 同名 | safe metadata | `Verify` |

若 DeepSeek 没有返回某个 Anthropic usage 维度：

1. adapter 标记 `not_applicable` 或 `unknown`。
2. settlement 按明确价格策略处理。
3. 不允许擅自填 0。

## 9. 流式响应映射

DeepSeek Anthropic endpoint 必须按 Anthropic SSE 事件解析并重新编码：

| DeepSeek SSE event | Anthropic 客户 event | 规则 |
| --- | --- | --- |
| `message_start` | 同名 | 解析 message 与初始 usage。 |
| `content_block_start` | 同名 | 保留 index 和 block。 |
| `content_block_delta` | 同名 | 保留 delta union。 |
| `content_block_stop` | 同名 | 保留 index。 |
| `message_delta` | 同名 | 累积 stop reason 与 usage。 |
| `message_stop` | 同名 | adapter 截留；lifecycle 持久化 facts 并 settlement 或 schedule recovery 后，由 gatewayapi/anthropic 写出。 |
| `ping` | 同名 | heartbeat。 |
| `error` | 协议安全 error event | 不透传敏感 body。 |

delta：

| delta type | 策略 |
| --- | --- |
| `text_delta` | `Pass` |
| `input_json_delta` | `Pass`，保留 partial JSON。 |
| `thinking_delta` | `Verify` |
| `signature_delta` | `Verify` |

`adapter/anthropic/deepseek/stream.go` 负责：

1. 解析 SSE。
2. 生成 Anthropic typed event。
3. 记录首个可见事件。
4. 累积最终 usage。
5. 生成 `StreamOutcome.Facts`。

## 10. Ignored 字段公开策略

DeepSeek 文档明确 ignored 的字段目前至少包括：

```text
anthropic-version
anthropic-beta
container
mcp_servers
metadata 除 user_id 外的字段
service_tier
thinking.budget_tokens
top_k
tools[].cache_control
tool_choice.disable_parallel_tool_use
text.cache_control
text.citations
tool_use.cache_control
tool_result.cache_control
tool_result.is_error
```

这些字段不能 silent drop。实现前必须逐项选择并记录策略：

| 策略 | 适用情况 |
| --- | --- |
| `AcceptIgnored` | 对 DeepSeek 行为没有误导风险，文档和 metrics 明确记录。 |
| `Reject` | 客户可能依赖该语义，忽略会产生错误预期。 |

默认偏向 `Reject`。只有确认接受 ignored 不会损害客户契约时才使用 `AcceptIgnored`。

## 11. 内部输入 tokenizer

实现位置：

```text
internal/core/adapter/anthropic/deepseek/tokenizer.go
```

实现规则：

1. 独立实现 `anthropic.MessagesInputTokenizer`。
2. 输入为 `anthropic.MessageRequest` 与 routing 选中的 `upstreamModel`。
3. 复用本 adapter 的 request map 规则，按即将发送的 DeepSeek Anthropic wire
   `system`、`messages[].content[]`、`tools` 与 framing 估算。
4. DeepSeek 不支持的 content block 必须在调用 tokenizer 或上游前明确 Reject，
   不能静默丢弃后继续估算。
5. 返回值是 authorization 使用的保守输入 token 估算，不是 settlement 使用的
   upstream usage 事实。
6. 不调用 OpenAI tokenizer，不共享 provider tokenizer facade，不通过跨协议
   中间 DTO 复用。
7. 直接在 adapter 实现与黑盒验收中校准，不新增独立 playground 前置任务。

## 12. 错误映射

`adapter/anthropic/deepseek` 负责：

1. 解析 upstream HTTP status。
2. 提取安全 request ID。
3. 解析 DeepSeek Anthropic 错误体。
4. 映射为稳定 `adapter.UpstreamError` category。
5. 不把原始 body 返回 gatewayapi。

gatewayapi/anthropic 负责渲染 Anthropic error shape。

## 13. 黑盒清单

至少覆盖：

| ID | 场景 |
| --- | --- |
| `DS-ANT-01` | 基础非流式 text。 |
| `DS-ANT-02` | 基础流式 text 事件顺序。 |
| `DS-ANT-03` | system prompt。 |
| `DS-ANT-04` | thinking 非流式 block。 |
| `DS-ANT-05` | thinking 流式 delta + signature。 |
| `DS-ANT-06` | client tool use 非流式。 |
| `DS-ANT-07` | client tool use 流式 `input_json_delta`。 |
| `DS-ANT-08` | tool result 多轮。 |
| `DS-ANT-09` | 图片、文档、redacted thinking、MCP、container upload 和未登记高级 block 明确 Reject。 |
| `DS-ANT-10` | ignored 参数公开策略。 |
| `DS-ANT-11` | usage → facts。 |
| `DS-ANT-12` | retry/fallback 每次真实调用各有 attempt。 |
| `DS-ANT-13` | stream tail error / cancel delivery audit。 |
| `DS-ANT-14` | cache control 行为冻结。 |
| `DS-ANT-15` | Anthropic tokenizer 估算与 DeepSeek Anthropic 实际 `usage.input_tokens` 校准。 |
| `DS-ANT-16` | `metadata.user_id`、`output_config.effort`、四种 `tool_choice` 与 nested ignored 字段。 |
| `DS-ANT-17` | `server_tool_use` 与 `web_search_tool_result` 非流式和流式返回。 |
| `DS-ANT-18` | 未知或拼写错误的 `upstream_model` 不允许依赖 DeepSeek 静默映射到 `deepseek-v4-flash`。 |
