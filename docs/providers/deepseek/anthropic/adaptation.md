# DeepSeek · Anthropic 格式 适配与转换逻辑

本文件记录 Unio 在 **Anthropic 兼容格式**(`POST /anthropic/v1/messages`)上接 DeepSeek 的关键转换
逻辑与行为决策(思考、工具、usage、模型名、错误、流式)。逐字段映射表见同目录
[protocol-and-params.md](protocol-and-params.md);DeepSeek 官方兼容性摘要见
[../anthropic-api-reference.md](../anthropic-api-reference.md);计费金额口径见 [../billing.md](../billing.md)。

## 1. 思考模式(reasoning / thinking)

DeepSeek v4 默认**思考开启**。Anthropic 格式下的控制:

| 控制 | 字段 | 说明 |
| --- | --- | --- |
| 开关 | `thinking: {type: enabled/disabled}` | 默认 enabled;`thinking.budget_tokens`/`thinking.display` 被忽略 |
| 强度 | `output_config: {effort: high/max}` | 对应 OpenAI 的 `reasoning_effort` |

来源:[官方·思考模式](https://api-docs.deepseek.com/zh-cn/guides/thinking_mode)、
[官方·Anthropic API](https://api-docs.deepseek.com/zh-cn/guides/anthropic_api)(查阅 2026-06-07)。

强度归一:adapter 出站**显式归一** `output_config.effort` 为 `high`/`max`(minimal/low/medium/high→high,
xhigh/max→max),未知值 Drop 让上游回退默认,不依赖上游隐式兼容映射。
代码:`internal/core/adapter/anthropic/deepseek/drop.go`(`adaptOutputConfig`、`normalizeOutputConfigEffort`)。

> 思考模式下 `temperature`/`top_p` 等不报错但不生效(官方·思考模式)。

## 2. thinking block 跨轮回传

Anthropic 格式下思维链以 `thinking` content block 表达。与 OpenAI 格式一致的原则:进行了工具调用的轮次,
其 assistant thinking 内容需在后续请求中保留,否则思考 + 工具循环可能被上游拒绝。
入站 content block 中 `thinking` 为 supported、`redacted_thinking` not supported(出站剔除)。
依据见 [../anthropic-api-reference.md](../anthropic-api-reference.md) §6 与 protocol-and-params。

## 3. 工具调用(Function Calling)

- 仅支持 **client custom tool**(`tools[].name/input_schema/description`);`tools[].cache_control` 被忽略。
- `tool_choice` 支持 none/auto/any/tool;`disable_parallel_tool_use` 被忽略。
- 内置 **server tool 定义**(web_search/code_execution 等)出站 **Drop**;但入站响应里的
  `server_tool_use`/`web_search_tool_result` block 为 supported,予以保留。
  是否放行 web_search 工具定义待黑盒确认(见 [../upgrade-plan.md](../upgrade-plan.md) U6)。

来源:[官方·Anthropic API](https://api-docs.deepseek.com/zh-cn/guides/anthropic_api)(查阅 2026-06-07)。

## 4. 模型名解耦

- DeepSeek 官方对 Anthropic 格式有隐式模型映射(`claude-opus*→v4-pro`、`claude-haiku*/sonnet*→v4-flash`、
  其它→v4-flash),详见 [../anthropic-api-reference.md](../anthropic-api-reference.md) §2。
- Unio **不依赖**该隐式映射:routing 选中 channel-model 后用显式 `upstream_model` 发上游;
  响应里把 `model` 恢复为客户的 Unio catalog model,审计记录真实 upstream model。

代码:`internal/core/adapter/anthropic` response map。

## 5. usage 映射(上游 → 内部计费事实)

| DeepSeek usage 字段 | 内部含义 |
| --- | --- |
| `input_tokens` | 未命中输入 |
| `cache_read_input_tokens` | 缓存命中输入 |
| `cache_creation_input_tokens` | 缓存写入输入 |
| `output_tokens` | 输出总量(思考 token 不单独返回,已含在 output) |

来源:[官方·Anthropic API](https://api-docs.deepseek.com/zh-cn/guides/anthropic_api)(查阅 2026-06-07);
代码 `internal/core/adapter/anthropic`。计费金额口径见 [../billing.md](../billing.md)。

## 6. 错误映射(重要)

DeepSeek 的 **Anthropic endpoint 错误体是 OpenAI 风格信封**(`{"error":{type,code,message}}`),
**不是** Anthropic error shape。adapter 按 OpenAI 信封解析、以 HTTP status 为主映射 category,
gatewayapi 再渲染成原生 **Anthropic** error 返回客户。上游 auth/permission 绝不渲染成客户 401
(对客户屏蔽上游凭据问题)。代码:`internal/core/adapter/anthropic/deepseek`。

错误码:400 / 401 / 402 / 422 / 429 / 500 / 503,同 OpenAI 格式。
来源:[官方·错误码](https://api-docs.deepseek.com/zh-cn/quick_start/error_codes)(查阅 2026-06-07)。

## 7. 流式

DeepSeek Anthropic endpoint 按 **Anthropic SSE 事件**(`message_start`/`content_block_delta`/...)输出,
adapter 解析后重编码为 Unio 对客户的 Anthropic SSE。adapter 截留上游终态,由 lifecycle 结算后
再由 gatewayapi 写出客户可见终态。
