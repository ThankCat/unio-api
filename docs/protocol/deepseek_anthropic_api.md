# DeepSeek Anthropic API 兼容性参考摘要

更新时间：2026-06-01

官方来源：

- [DeepSeek Anthropic API](https://api-docs.deepseek.com/guides/anthropic_api)

本文档保存 DeepSeek 官方兼容表的项目内参考摘要。它不是 Unio 的公开契约，也不替代
`docs/chapters/phase-10-dual-protocol-gateway/DEEPSEEK_ANTHROPIC_MAPPING.md` 中的
adapter 决策。

## 1. Endpoint

```text
Base URL: https://api.deepseek.com/anthropic
Endpoint: POST /v1/messages
```

## 2. 模型映射

DeepSeek 官方当前说明：

| 客户传入的模型名 | DeepSeek 实际映射 |
| --- | --- |
| `claude-opus*` | `deepseek-v4-pro` |
| `claude-haiku*` | `deepseek-v4-flash` |
| `claude-sonnet*` | `deepseek-v4-flash` |
| 其他不支持的模型名 | `deepseek-v4-flash` |

Unio 不应依赖该隐式映射。正式 channel-model 配置仍需显式保存并校验
`upstream_model`。

## 3. Header

| 字段 | DeepSeek 官方状态 |
| --- | --- |
| `x-api-key` | fully supported |
| `anthropic-beta` | ignored |
| `anthropic-version` | ignored |

## 4. 顶层字段

| 字段 | DeepSeek 官方状态 |
| --- | --- |
| `model` | 使用 DeepSeek 模型；Claude 模型名按上表映射 |
| `max_tokens` | fully supported |
| `container` | ignored |
| `mcp_servers` | ignored |
| `metadata.user_id` | supported |
| `metadata` 其他字段 | ignored |
| `service_tier` | ignored |
| `stop_sequences` | fully supported |
| `stream` | fully supported |
| `system` | fully supported |
| `temperature` | fully supported，范围为 `[0.0, 2.0]` |
| `thinking` | supported，但 `budget_tokens` ignored |
| `output_config.effort` | supported |
| `output_config` 其他字段 | 未声明支持 |
| `top_k` | ignored |
| `top_p` | fully supported |

## 5. Tool 字段

### `tools[]`

| 字段 | DeepSeek 官方状态 |
| --- | --- |
| `name` | fully supported |
| `input_schema` | fully supported |
| `description` | fully supported |
| `cache_control` | ignored |

### `tool_choice`

| 类型 | DeepSeek 官方状态 |
| --- | --- |
| `none` | fully supported |
| `auto` | supported，但 `disable_parallel_tool_use` ignored |
| `any` | supported，但 `disable_parallel_tool_use` ignored |
| `tool` | supported，但 `disable_parallel_tool_use` ignored |

## 6. Message Content Block

| content block 或字段 | DeepSeek 官方状态 |
| --- | --- |
| string shorthand | fully supported |
| `text.text` | fully supported |
| `text.cache_control` | ignored |
| `text.citations` | ignored |
| `image` | not supported |
| `document` | not supported |
| `search_result` | not supported |
| `thinking` | supported |
| `redacted_thinking` | not supported |
| `tool_use.id` | fully supported |
| `tool_use.input` | fully supported |
| `tool_use.name` | fully supported |
| `tool_use.cache_control` | ignored |
| `tool_result.tool_use_id` | fully supported |
| `tool_result.content` | fully supported |
| `tool_result.cache_control` | ignored |
| `tool_result.is_error` | ignored |
| `server_tool_use` | supported |
| `web_search_tool_result` | supported |
| `code_execution_tool_result` | not supported |
| `mcp_tool_use` | not supported |
| `mcp_tool_result` | not supported |
| `container_upload` | not supported |

未出现在 DeepSeek 官方兼容表中的 Anthropic 高级字段，不能默认视为支持。Unio 的
DeepSeek adapter 必须明确 Reject 或经过黑盒验证后登记策略。
