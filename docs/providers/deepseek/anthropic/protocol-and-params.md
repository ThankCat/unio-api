# DeepSeek · Anthropic 格式 协议与参数映射

> 本文件是 DeepSeek **Anthropic 兼容格式**(`POST /anthropic/v1/messages`)的**权威逐字段映射**
> (由 Phase 10 映射表迁入,出现分歧以此为准)。适配决策与转换逻辑见同目录
> [adaptation.md](adaptation.md);计费见 [../billing.md](../billing.md);
> 待办见 [../upgrade-plan.md](../upgrade-plan.md)。
> 标准 Anthropic 协议本身见 [docs/protocol/anthropic/messages/](../../../protocol/anthropic/messages/official.md);
> DeepSeek 官方 Anthropic 兼容性摘要见 [../anthropic-api-reference.md](../anthropic-api-reference.md)。

## 1. 上游信息

参考：

- [DeepSeek Quick Start](https://api-docs.deepseek.com/)
- [DeepSeek Anthropic API](https://api-docs.deepseek.com/guides/anthropic_api)
- [项目内 DeepSeek Anthropic API 兼容性参考摘要](../anthropic-api-reference.md)

上游：

```text
Base URL: https://api.deepseek.com/anthropic
Endpoint: POST /v1/messages
协议族:   anthropic
Adapter:  internal/core/adapter/anthropic/deepseek/messages
```

状态：

| 状态 | 含义 |
| --- | --- |
| `Pass` | 字段名和语义一致，显式写入 upstream wire；响应侧写入 Anthropic DTO。 |
| `Adapt` | 显式转换后写入 upstream wire 或公开响应。 |
| `Drop` | ingress 可收；**不写入 upstream body** 或 **不写入公开响应**；记内部 `dropped_*_fields`。 |
| `Verify` | 文档不足，必须黑盒确认后转为其他状态。 |

全局原则见 [DEC-012](../../../production/DECISIONS.md#dec-012-协议为先与-provider-映射-drop-策略)。

Phase 10 关闭前不允许保留 `Verify`。

**不再使用** mapping 层 `Reject` / `Ignored`：原「上游会忽略」或「不支持」项统一 **Drop**（出站不发送对应键或 block）。

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
| `anthropic-version` | - | `Drop` | DeepSeek 忽略；出站不发送。ingress 仍校验支持版本。 |
| `anthropic-beta` | - | `Drop` | DeepSeek 忽略；出站不发送。ingress 接受任意 beta（DEC-013），按 Drop + 脱敏审计处理。 |
| `content-type` | `application/json` | `Pass` | |

## 4. 顶层请求映射

| Anthropic 客户字段 | DeepSeek wire | 策略 | 说明 |
| --- | --- | --- | --- |
| `max_tokens` | `max_tokens` | `Pass` | 必填。 |
| `messages` | `messages` | `Adapt` | 仅允许 DeepSeek 支持的 content block。 |
| `model` | `model` | `Adapt` | 使用 routing 选中的显式 DeepSeek upstream model；不依赖上游隐式映射。 |
| `cache_control` | - | `Drop` | 出站不发送；成本按上游 usage 结算。 |
| `container` | - | `Drop` | 出站不发送。 |
| `inference_geo` | - | `Drop` | 出站不发送。 |
| `mcp_servers` extension | - | `Drop` | ingress 可登记；出站不发送。 |
| `metadata.user_id` | `metadata.user_id` | `Pass` | DeepSeek 文档声明支持。 |
| `metadata` 其他字段 | - | `Drop` | 仅 `user_id` Pass；其余 Drop。 |
| `output_config.effort` | 同名 | `Pass` | |
| `output_config.format` | - | `Drop` | 无法保持 schema 语义；整段 format Drop。 |
| `service_tier` | - | `Drop` | 出站不发送。 |
| `stop_sequences` | `stop_sequences` | `Pass` | |
| `stream` | `stream` | `Pass` | |
| `system` | `system` | `Pass` | 保持 string 或 text block 数组语义。 |
| `temperature` | `temperature` | `Pass` | DeepSeek 文档声明范围为 `[0.0, 2.0]`；thinking 模式下效果需黑盒确认。 |
| `thinking.type` | `thinking.type` | `Adapt` | DeepSeek 文档声明 thinking 支持；允许值需黑盒冻结。 |
| `thinking.budget_tokens` | - | `Drop` | 出站不发送。 |
| `thinking.display` | - | `Drop` | 出站不发送。 |
| `tool_choice` | `tool_choice` | `Pass` | `none`、`auto`、`any` 和 `tool` 均支持；嵌套 ignored 字段见下文。 |
| `tools` | `tools` | `Adapt` | 仅允许 DeepSeek 支持的 client tool。 |
| `top_k` | - | `Drop` | 出站不发送。 |
| `top_p` | `top_p` | `Pass` | thinking 模式下效果需黑盒确认。 |

## 5. `messages[].content[]` 映射

| Anthropic content block 或字段 | DeepSeek wire | 策略 | 说明 |
| --- | --- | --- | --- |
| string shorthand | string 或 text block | `Pass` | |
| `text.text` | 同名 | `Pass` | |
| `text.cache_control` | - | `Drop` | |
| `text.citations` | - | `Drop` | |
| `image` | - | `Drop` | 不写入 upstream content；黑盒：若误发 upstream 会 200 静默忽略。 |
| `document` | - | `Drop` | |
| `search_result` | - | `Drop` | |
| `thinking` | 同名 | `Pass` | |
| `redacted_thinking` | - | `Drop` | |
| `tool_use.id`、`input`、`name` | 同名 | `Pass` | |
| `tool_use.cache_control` | - | `Drop` | |
| `tool_result.tool_use_id`、`content` | 同名 | `Pass` | |
| `tool_result.cache_control` | - | `Drop` | |
| `tool_result.is_error` | - | `Drop` | |
| `server_tool_use` | 同名 | `Pass` | |
| `web_search_tool_result` | 同名 | `Pass` | |
| `web_fetch_tool_result` | - | `Drop` | |
| `code_execution_tool_result` | - | `Drop` | |
| `mcp_tool_use` | - | `Drop` | |
| `mcp_tool_result` | - | `Drop` | |
| `bash_code_execution_tool_result` | - | `Drop` | |
| `text_editor_code_execution_tool_result` | - | `Drop` | |
| `tool_search_tool_result` | - | `Drop` | |
| `container_upload` | - | `Drop` | |
| `mid_conversation_system` | - | `Drop` | |

DeepSeek 官方文档说明 Anthropic API 当前不支持多种高级 block。Unio **Drop** 这些 block/content，
不写入 upstream wire；ingress 仍允许合法 Anthropic 协议字段进入 DTO。

## 6. `tools[]` 与 `tool_choice` 映射

| Anthropic tool 类型 | DeepSeek wire | 策略 |
| --- | --- | --- |
| client custom tool | 同名 | `Pass` |
| `bash_20250124` | - | `Drop` |
| `code_execution_*` | - | `Drop` |
| `memory_20250818` | - | `Drop` |
| `text_editor_*` | - | `Drop` |
| `web_search_*` | - | `Drop` |
| `web_fetch_*` | - | `Drop` |
| `tool_search_tool_*` | - | `Drop` |

client custom tool 字段：

| 字段 | DeepSeek wire | 策略 |
| --- | --- | --- |
| `name` | 同名 | `Pass` |
| `input_schema` | 同名 | `Pass` |
| `description` | 同名 | `Pass` |
| `cache_control` | - | `Drop` |

`tool_choice`：

| 类型或字段 | DeepSeek wire | 策略 |
| --- | --- | --- |
| `none` | 同名 | `Pass` |
| `auto` | 同名 | `Pass` |
| `any` | 同名 | `Pass` |
| `tool` | 同名 | `Pass` |
| `disable_parallel_tool_use` | - | `Drop` |

## 7. 非流式响应映射

DeepSeek Anthropic endpoint 返回 Anthropic 风格 Message。adapter 不能直接透传原始 body，必须 typed decode 后再构造客户响应与 facts。

| DeepSeek wire 字段 | Anthropic 客户字段 | 策略 |
| --- | --- | --- |
| `id` | `id` | `Pass` |
| `type=message` | 同名 | `Pass` |
| `role=assistant` | 同名 | `Pass` |
| `model` | `model` | `Adapt`：返回客户请求的 Unio catalog model。 |
| `content[].type=text` | 同名 | `Pass` |
| `content[].type=thinking` | 同名 | `Pass`：黑盒冻结，返回 `thinking` + `signature`（当前 signature 为 message id），thinking 与 text 分属不同 block。 |
| `content[].type=redacted_thinking` | - | `Drop` | 不进公开响应；facts 如需可记 raw。 |
| `content[].type=tool_use` | 同名 | `Pass` | |
| `content[].type=server_tool_use` | 同名 | `Pass` | |
| `content[].type=web_search_tool_result` | 同名 | `Pass` | |
| 其他 content block | - | `Drop` | 未登记 block 不进公开 Anthropic DTO。 |
| `stop_reason` | 同名 | `Pass` | |
| `stop_sequence` | 同名 | `Pass` | |
| `stop_details` | - | `Drop` | |
| `container` | - | `Drop` | |
| `usage` | 同名 + `usage.Facts` | `Adapt`（见 §8 冻结表）。 |

规则：

1. 客户收到 Anthropic Message，不收到 OpenAI choices。
2. thinking block 与 text block 分离。
3. tool_use input 必须保留 JSON object，不压扁成字符串。
4. upstream 未返回的协议字段按 Anthropic JSON 省略/null 语义输出，不能伪造。

## 8. Usage 映射

DeepSeek Anthropic endpoint 的 usage 已黑盒冻结（2026-06-01）。非流式 `usage` 与流式
`message_delta.usage` 字段集合一致，固定返回以下五个字段：

| DeepSeek wire 字段 | Anthropic 客户字段 | `usage.Facts` | 冻结状态 |
| --- | --- | --- | --- |
| `input_tokens` | `input_tokens` | `UncachedInputTokens` | `Pass`：始终返回；为未命中缓存的输入量（与 `cache_read` 分离，观测 `cache_read=256` 时 `input_tokens=24`）。 |
| `cache_read_input_tokens` | `cache_read_input_tokens` | `CacheReadInputTokens` | `Pass`：始终返回（可为 0），DeepSeek 自动缓存命中。 |
| `cache_creation_input_tokens` | `cache_creation_input_tokens` | cache write 总量 | `Pass`：始终返回（可为 0）。 |
| `cache_creation.ephemeral_5m_input_tokens` | — | `CacheWrite5mInputTokens` | `not_applicable`：DeepSeek 不返回 TTL 拆分。 |
| `cache_creation.ephemeral_1h_input_tokens` | — | `CacheWrite1hInputTokens` | `not_applicable`：同上。 |
| `output_tokens` | `output_tokens` | `OutputTokensTotal` | `Pass`：始终返回；流式以 `message_delta.usage` 为最终值。 |
| `output_tokens_details.thinking_tokens` | — | `ReasoningOutputTokens` | `not_applicable`：thinking 模式下也不单独返回，`output_tokens` 已含思考输出。 |
| `server_tool_use.*` | `server_tool_use.*` | `MeteredItem` | 未黑盒触发；返回即映射，缺失为 `not_applicable`，不补 0。 |
| `service_tier` | `service_tier` | safe metadata | `Pass`：始终返回 `"standard"`。 |
| `inference_geo` | — | safe metadata | `not_applicable`：黑盒未返回。 |

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
| `text_delta` | `Pass`，黑盒冻结。 |
| `input_json_delta` | `Pass`，保留 partial JSON。 |
| `thinking_delta` | `Pass`，黑盒冻结（thinking block 在 index 0，先于 text block）。 |
| `signature_delta` | `Pass`，黑盒冻结（在 thinking block 末尾，`signature` 当前为 message id）。 |

`adapter/anthropic/deepseek/stream.go` 负责：

1. 解析 SSE。
2. 生成 Anthropic typed event。
3. 记录首个可见事件。
4. 累积最终 usage。
5. 生成 `StreamOutcome.Facts`。

## 10. Drop 字段清单（出站不发送）

DeepSeek 文档明确 ignored 或 Unio 判定无法转换的字段，出站统一 **Drop**：

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

这些字段在出站 mapping 中统一 **Drop**，并写入 `dropped_request_fields` 内部审计。

## 11. 内部输入 tokenizer

实现位置：

```text
internal/core/adapter/anthropic/deepseek/messages/tokenizer.go
```

实现规则：

1. 独立实现 `anthropic.MessagesInputTokenizer`。
2. 输入为 `anthropic.MessageRequest` 与 routing 选中的 `upstreamModel`。
3. 复用本 adapter 的 request map 规则，按 **Drop 后** 的 DeepSeek Anthropic wire 估算。
4. DeepSeek 不支持的 content block 在 `buildUpstreamWire` 中 **Drop**，tokenizer 与 upstream 共用同一 wire。
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

**黑盒冻结**：DeepSeek Anthropic endpoint 的错误体不是 Anthropic error shape，而是 OpenAI
风格错误信封：

```json
{"error":{"message":"...","type":"authentication_error","param":null,"code":"invalid_request_error"}}
```

因此 adapter 必须按 OpenAI 风格信封 typed decode（`error.type` / `error.code` / `error.message`），
并以 HTTP status 为主信号映射 category（401 → auth，等）。gatewayapi/anthropic 再渲染为
原生 Anthropic error shape（`{"type":"error","error":{"type","message"}}`），不透传上游 body。

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
| `DS-ANT-09` | 图片、文档、MCP、container upload 等：ingress 接受，upstream body **不含** dropped block，内部审计有 drop 列表。 |
| `DS-ANT-10` | ignored 参数 Drop 策略与 `dropped_request_fields`。 |
| `DS-ANT-11` | usage → facts。 |
| `DS-ANT-12` | retry/fallback 每次真实调用各有 attempt。 |
| `DS-ANT-13` | stream tail error / cancel delivery audit。 |
| `DS-ANT-14` | cache control 行为冻结。 |
| `DS-ANT-15` | Anthropic tokenizer 估算与 DeepSeek Anthropic 实际 `usage.input_tokens` 校准。 |
| `DS-ANT-16` | `metadata.user_id`、`output_config.effort`、四种 `tool_choice` 与 nested ignored 字段。 |
| `DS-ANT-17` | `server_tool_use` 与 `web_search_tool_result` 非流式和流式返回。 |
| `DS-ANT-18` | 未知或拼写错误的 `upstream_model` 不允许依赖 DeepSeek 静默映射到 `deepseek-v4-flash`。 |

## 14. 黑盒冻结结果（2026-06-01）

针对 `https://api.deepseek.com/anthropic/v1/messages` 的实测冻结，证据保留在结论中（密钥不入库）：

### 14.1 响应与 usage 形状

非流式与流式 `message_delta.usage` 字段集合一致，固定五字段：
`input_tokens`、`cache_creation_input_tokens`、`cache_read_input_tokens`、`output_tokens`、
`service_tier`（`"standard"`）。未返回 `cache_creation` TTL 拆分、`output_tokens_details`、
`inference_geo`。详见 §8。

- 非流式 text：`content=[{type:text,text}]`，`stop_reason=end_turn`，`stop_sequence=null`。
- thinking：`content=[{type:thinking,thinking,signature},{type:text,text}]`；`signature` 当前等于 message `id`。
- tool use：`content=[{type:tool_use,id:"call_...",name,input:{...}}]`，`stop_reason=tool_use`。
- `model` 字段无论客户传 `deepseek-chat` 或任意名，响应一律回 `deepseek-v4-flash`。
  Unio 必须用显式 `upstream_model` 调用，并在响应中恢复客户 catalog `model`（DS-ANT-18）。

### 14.2 流式事件序

```text
message_start → content_block_start → ping → content_block_delta* → content_block_stop → message_delta → message_stop
```

thinking 流：thinking block 为 index 0（`thinking_delta`* 后跟一个 `signature_delta`），
text block 为 index 1。最终 usage 在 `message_delta.usage`。

### 14.3 image block 与 Drop 策略

黑盒：若 `image` block 进入 DeepSeek upstream，会 HTTP 200 **静默忽略**并返回纯文本。
Unio 在出站 mapping **Drop** `image`/`document` 等 block，不写入 upstream body；
客户仍走 Anthropic 协议 200 路径，但不会误以为图片已被上游处理。Drop 必须记内部审计。

### 14.4 协议严格度差异

DeepSeek 对缺失 `max_tokens` 仍返回 200（不强制必填）。Unio ingress 维持 Anthropic 契约，
`max_tokens` 缺失返回 400（更严格），不受上游宽松行为影响。

### 14.5 错误体形状

DeepSeek Anthropic endpoint 返回 **OpenAI 风格错误信封**（见 §12），不是 Anthropic error shape。
adapter 按 OpenAI 信封 typed decode 并以 HTTP status 为主映射 category，gatewayapi/anthropic 渲染
Anthropic error shape。
