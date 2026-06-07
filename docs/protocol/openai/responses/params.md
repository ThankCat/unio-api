# OpenAI Responses · 请求/响应参数说明

依据同目录 [official.md](official.md)、[official-streaming-events.md](official-streaming-events.md)、
[official-other-endpoints.md](official-other-endpoints.md) 整理(来源:OpenAI 官方,查阅 2026-06-07)。
本文件做忠实整理与通俗说明,**不写 Unio 的适配决定**(见 [docs/providers/](../../../providers/README.md))。
完整、最新字段以 official 文档为准。

术语:
- **Responses API**:OpenAI 比 Chat Completions 更新的对话接口,Codex 默认走这个协议。
- **item(项)**:Responses 用「项」表达输入/输出,常见类型有 message(消息)、reasoning(思考)、function_call(工具调用)。
- **reasoning**:模型思考过程;Responses 用独立的 reasoning item 表达。
- **SSE**(Server-Sent Events):服务器持续推送数据的流式响应格式。

## 端点全集与 Unio 立场

| 操作 | 方法/路径 | 官方用途 | Unio 立场 |
| --- | --- | --- | --- |
| Create | `POST /v1/responses` | 发起一次响应生成 | ✅ 已实现 |
| Compact | `POST /v1/responses/compact` | 压缩历史上下文 | ✅ 已实现(无状态降级) |
| Input tokens | `POST /v1/responses/input_tokens` | 预估输入 token 数 | ✅ 已实现(本地估算) |
| Get | `GET /v1/responses/{id}` | 取回已存储响应 | 不支持 → 501(无状态) |
| Delete | `DELETE /v1/responses/{id}` | 删除已存储响应 | 不支持 → 501(无状态) |
| Cancel | `POST /v1/responses/{id}/cancel` | 取消后台任务 | 不支持 → 501(无状态) |
| Input items | `GET /v1/responses/{id}/input_items` | 列出输入项 | 不支持 → 501(无状态) |

> compact / input_tokens 的请求与响应形状见 [official-other-endpoints.md](official-other-endpoints.md)。
> 下面只整理 **Create** 的参数。

## 请求参数(Create)

| 参数 | 类型 | 必填 | 含义 | 备注 |
| --- | --- | --- | --- | --- |
| `model` | string | 是 | 使用的模型 ID | |
| `input` | string/array | 是* | 输入内容 | 字符串,或 item 数组(message/function_call/function_call_output/reasoning 等) |
| `instructions` | string | 否 | 系统级指令 | 相当于 system 提示 |
| `max_output_tokens` | integer | 否 | 最多生成的输出 token 数(含 reasoning) | |
| `tools` | array | 否 | 可调用工具定义 | function 与内置工具 |
| `tool_choice` | string/object | 否 | 工具选择策略 | `none`/`auto`/`required`/指定 |
| `parallel_tool_calls` | boolean | 否 | 是否允许并行工具调用 | |
| `reasoning` | object | 否 | 思考配置 | `{effort: minimal/low/medium/high, summary}` |
| `text` | object | 否 | 文本输出配置 | `{format: text/json_object/json_schema, verbosity}` |
| `stream` | boolean | 否 | 是否流式返回(SSE) | |
| `store` | boolean | 否 | 是否在服务端存储本次响应 | 有状态特性 |
| `previous_response_id` | string | 否 | 接续某条已存储响应 | 依赖服务端存储 |
| `include` | array | 否 | 额外返回的内容项 | 如 reasoning 加密内容 |
| `background` | boolean | 否 | 是否后台异步执行 | |
| `metadata` | map | 否 | 附加键值 | |
| `temperature` / `top_p` | number | 否 | 采样参数 | |
| `truncation` | string | 否 | 超长上下文截断策略 | `auto`/`disabled` |
| `prompt_cache_key` | string | 否 | prompt 缓存键 | |
| `service_tier` | string | 否 | 服务层级 | |
| `user` / `safety_identifier` | string | 否 | 终端用户标识 | |

\* `input` 与 `prompt`/`previous_response_id` 等组合规则以 official.md 为准。

## 响应结构(非流式)

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `id` | string | 响应 id |
| `object` | string | 固定 `response` |
| `created_at` | integer | 创建时间戳 |
| `status` | string | `completed`/`incomplete`/`failed`/`in_progress` 等 |
| `model` | string | 实际使用的模型 |
| `output` | array | 输出项列表(message / reasoning / function_call 等) |
| `output_text` | string | 便捷字段:所有文本输出拼接 |
| `usage` | object | token 用量(见下) |
| `incomplete_details` | object | 未完成原因(如 max tokens) |
| `error` | object | 失败时的错误 |
| `reasoning` / `text` / `tools` 等 | object | 回显请求配置 |

### usage 用量

| 字段 | 含义 |
| --- | --- |
| `input_tokens` | 输入 token 数 |
| `input_tokens_details.cached_tokens` | 命中缓存的输入 token 数 |
| `output_tokens` | 输出 token 数(含 reasoning) |
| `output_tokens_details.reasoning_tokens` | 输出中 reasoning 的 token 数 |
| `total_tokens` | 总和 |

## 流式响应(`stream=true`)

- 以 SSE 推送一系列具名事件:`response.created` → `response.in_progress` →
  `response.output_item.added` → 各类 `*.delta`(`output_text.delta` / `reasoning_text.delta` /
  `function_call_arguments.delta`)→ `response.output_item.done` → `response.completed`。
- `sequence_number` 单调递增;usage 在 `response.completed`。
- 完整 53 种事件与字段见 [official-streaming-events.md](official-streaming-events.md)。
