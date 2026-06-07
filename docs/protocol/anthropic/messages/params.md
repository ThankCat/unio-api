# Anthropic Messages · 请求/响应参数说明

依据同目录 [official.md](official.md) 整理(来源:Anthropic 官方,查阅 2026-06-07)。
本文件做忠实整理与通俗说明,**不写 Unio 的适配决定**(见 [docs/providers/](../../../providers/README.md))。
完整、最新字段以 `official.md` 为准。

术语:
- **token**:模型处理文本的最小计量单位。
- **thinking**:Anthropic 的思考模式;开启后模型先产出思维链(`thinking` block)再给答案。
- **content block(内容块)**:Anthropic 用「块」表达消息内容,常见类型 text / image / thinking / tool_use / tool_result。
- **SSE**(Server-Sent Events):服务器持续推送数据的流式响应格式。

## 端点全集与 Unio 立场

| 操作 | 方法/路径 | 官方用途 | Unio 立场 |
| --- | --- | --- | --- |
| Create | `POST /v1/messages` | 发起一次消息生成 | ✅ 已实现 |
| Count tokens | `POST /v1/messages/count_tokens` | 预估输入 token 数 | 当前未对外暴露 |

下面只整理 **Create** 的参数。

## 请求参数(Create)

必填:

| 参数 | 类型 | 含义 | 备注 |
| --- | --- | --- | --- |
| `model` | string | 使用的模型 ID | |
| `messages` | array | 对话消息列表 | 每条含 `role`(user/assistant)与 `content` |
| `max_tokens` | integer | 最多生成的 token 数 | Anthropic 必填 |

常用可选:

| 参数 | 类型 | 含义 | 备注 |
| --- | --- | --- | --- |
| `system` | string/array | 系统提示 | 字符串或 text block 数组 |
| `temperature` | number | 采样温度 | 范围 0~1 |
| `top_p` | number | 核采样阈值 | |
| `top_k` | integer | 仅从概率最高的 K 个 token 采样 | |
| `stop_sequences` | array | 命中即停止的字符串 | |
| `stream` | boolean | 是否流式返回(SSE) | |
| `tools` | array | 可调用工具定义 | client 工具与内置 server 工具 |
| `tool_choice` | object | 工具选择策略 | `auto`/`any`/`tool`/`none` |
| `thinking` | object | 思考模式配置 | `{type: enabled/disabled, budget_tokens}` |
| `metadata` | object | 附加信息 | 如 `user_id` |
| `service_tier` | string | 服务层级 | |

### 消息与内容块(`messages[].content`)

| 块类型 | 说明 |
| --- | --- |
| `text` | 文本 |
| `image` | 图片(base64 或 URL) |
| `thinking` / `redacted_thinking` | 思考内容 / 脱敏思考 |
| `tool_use` | assistant 发起的工具调用(含 `id`/`name`/`input`) |
| `tool_result` | 工具结果回传(含 `tool_use_id`/`content`) |
| `document` / `search_result` 等 | 其它高级块,见 official.md |

## 响应结构(非流式)

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `id` | string | 消息 id |
| `type` | string | 固定 `message` |
| `role` | string | 固定 `assistant` |
| `model` | string | 实际使用的模型 |
| `content` | array | 内容块列表(text / thinking / tool_use 等) |
| `stop_reason` | string | 结束原因:`end_turn`/`max_tokens`/`stop_sequence`/`tool_use` |
| `stop_sequence` | string | 命中的停止串(若有) |
| `usage` | object | token 用量(见下) |

### usage 用量

| 字段 | 含义 |
| --- | --- |
| `input_tokens` | 输入 token 数(未含缓存命中部分) |
| `cache_read_input_tokens` | 命中缓存读取的输入 token 数 |
| `cache_creation_input_tokens` | 写入缓存的输入 token 数 |
| `output_tokens` | 输出 token 数 |

## 流式响应(`stream=true`)

- 以 SSE 推送具名事件:`message_start` → `content_block_start` →
  `content_block_delta`(`text_delta` / `thinking_delta` / `signature_delta` / `input_json_delta`)→
  `content_block_stop` → `message_delta`(累积 stop_reason 与 usage)→ `message_stop`,期间穿插 `ping`。
- 完整事件与字段以 [official.md](official.md) 为准。
