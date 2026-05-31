# OpenAI Chat Completions 协议参考（Phase 9）

更新时间：2026-05-31

本文档是 Unio Gateway `/v1/chat/completions` 的**公开契约事实源**。

权威参考：[OpenAI Create Chat Completion](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)

Unio 产品原则：**客户侧永远使用 OpenAI 协议形状**；上游厂商差异只在 adapter 内翻译，见 [END_TO_END_PIPELINE.md](END_TO_END_PIPELINE.md) 与 [DEEPSEEK_UPSTREAM.md](DEEPSEEK_UPSTREAM.md)。

---

## 1. 请求：`POST /v1/chat/completions`

### 1.1 顶层字段总览

| 字段 | 类型 | 含义 | Unio Phase 9 目标 |
| --- | --- | --- | --- |
| `model` | string | 客户请求的 Unio 模型 ID（路由键） | **必须** |
| `messages` | array | 对话消息列表 | **必须** |
| `stream` | boolean | 是否 SSE 流式 | **必须** |
| `stream_options` | object | 流式选项，见 1.2 | **必须**（C2） |
| `temperature` | number | 采样温度 0~2 | **必须**（C1） |
| `top_p` | number | 核采样 0~1 | **必须**（C1） |
| `max_tokens` | number | 最大输出 token（旧字段，部分模型 deprecated） | **必须**（C1） |
| `max_completion_tokens` | number | 最大输出 token（含 reasoning） | **必须**（C1） |
| `presence_penalty` | number | 存在惩罚 -2~2 | **必须**（C1） |
| `frequency_penalty` | number | 频率惩罚 -2~2 | **必须**（C1） |
| `stop` | string \| string[] | 停止序列，最多 4 个 | **必须**（C1） |
| `user` | string | 终端用户标识（OpenAI 正被 safety_identifier 取代） | **必须**（C1） |
| `tools` | array | 工具定义列表 | **必须**（C5） |
| `tool_choice` | string \| object | 工具调用策略 | **必须**（C5） |
| `parallel_tool_calls` | boolean | 是否并行 tool call | **必须**（C5） |
| `response_format` | object | JSON / JSON Schema 输出约束 | **必须**（C6） |
| `reasoning_effort` | string | reasoning 模型推理强度 | **必须**（C4） |
| `logprobs` | boolean | 是否返回 log 概率 | 按上游能力（C8） |
| `top_logprobs` | number | 每个位置返回 top-k logprobs | 按上游能力（C8） |
| `n` | number | 生成几个 choice | 按上游能力（C8） |
| `seed` | number | 确定性采样种子 | 按上游能力（C8） |
| `metadata` | object | 16 对 KV 元数据 | Passthrough 或 Reject |
| `modalities` | string[] | 输出模态 text/audio | 按上游能力（C7/C8） |
| `audio` | object | 音频输出参数 | 按上游能力（C8） |
| `prediction` | object | Predicted Outputs | 按上游能力（C8） |
| `service_tier` | string | OpenAI 服务 tier | **Reject**（Unio 自有路由） |
| `store` | boolean | OpenAI 存储开关 | **Reject** |
| `web_search_options` | object | OpenAI 内置搜索 | **Reject**（非通用上游） |
| `functions` / `function_call` | deprecated | 旧 function calling | 映射到 tools（兼容） |

规则：

- **Supported**：typed 接收、校验、全链路透传或翻译。
- **Passthrough**：保留在 extension body，由 adapter 写入 upstream。
- **Rejected**：返回明确 OpenAI-compatible 400，**禁止 silent drop**。

### 1.2 `stream_options`

| 字段 | 类型 | 含义 |
| --- | --- | --- |
| `include_usage` | boolean | 为 true 时，在 `[DONE]` 前多一条 usage chunk；中间 chunk 的 `usage` 为 `null` |
| `include_obfuscation` | boolean | OpenAI 流式混淆字段（side-channel 缓解） |

Unio 行为：

1. 客户端 `include_usage` 控制**对用户**是否写出 usage 尾包。
2. gateway 内部 settlement **始终**需要 final usage；adapter 向上游请求 `include_usage=true` 的策略与客户端请求分离。

### 1.3 `messages[]` 消息结构

| role | 关键字段 | 含义 |
| --- | --- | --- |
| `developer` | `content` string \| text parts[] | 新版 system 指令（o 系列 / gpt-5+） |
| `system` | `content` string \| text parts[] | 系统指令 |
| `user` | `content` string \| parts[] | 用户输入；parts 可含 text/image/audio/file |
| `assistant` | `content`, `tool_calls`, `refusal`, `audio` | 模型历史回复 |
| `tool` | `content`, `tool_call_id` | 工具执行结果 |
| `function` | deprecated | 旧 tool 结果，映射到 `tool` |

**OpenAI-compatible 扩展（reasoning 模型常用）：**

| 字段 | 所在 role | 含义 |
| --- | --- | --- |
| `reasoning_content` | `assistant` | 思考过程 / CoT；多轮 tool 时可能必须回传 upstream |

Phase 9 要求：

1. `content` 支持 string 与 array（multimodal passthrough）。
2. assistant 历史必须保留 `reasoning_content` 供 adapter 回传 upstream。
3. `tool_calls` / `tool_call_id` 完整保留。

### 1.4 请求示例（非流式 + reasoning + tools）

```json
{
  "model": "deepseek-v4-pro",
  "messages": [
    { "role": "user", "content": "9.11 and 9.8, which is greater?" }
  ],
  "reasoning_effort": "high",
  "stream": false,
  "max_tokens": 1024
}
```

SDK 可能通过 `extra_body` 传 vendor 扩展；Unio 必须 passthrough 到 upstream：

```json
{
  "thinking": { "type": "enabled" }
}
```

---

## 2. 非流式响应：`stream: false`

### 2.1 顶层结构

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion",
  "created": 1694268190,
  "model": "deepseek-v4-pro",
  "choices": [ ... ],
  "usage": { ... },
  "system_fingerprint": "optional",
  "service_tier": "optional"
}
```

| 字段 | 含义 | Unio 要求 |
| --- | --- | --- |
| `id` |  completion 唯一 ID | 来自 upstream 或 Unio 生成；需稳定 |
| `object` | 固定 `"chat.completion"` | **必须** |
| `created` | Unix 秒 | **必须** |
| `model` | **客户请求的 model ID**（Unio 路由 ID，不是 upstream model） | **必须** |
| `choices` | 候选列表 | **必须** |
| `usage` | token 统计 | **必须**，含 details |
| `system_fingerprint` | OpenAI 后端指纹 | 有则 passthrough，无则省略 |

### 2.2 `choices[]`

```json
{
  "index": 0,
  "message": {
    "role": "assistant",
    "content": "9.11 is greater than 9.8.",
    "reasoning_content": "Compare tenths first...",
    "tool_calls": [ ... ]
  },
  "finish_reason": "stop",
  "logprobs": null
}
```

| 字段 | 含义 |
| --- | --- |
| `message.content` | 给用户看的最终答案 |
| `message.reasoning_content` | 思考过程（DeepSeek 等 reasoning 模型） |
| `message.tool_calls` | 模型发起的工具调用 |
| `finish_reason` | `stop` \| `length` \| `tool_calls` \| `content_filter` |

Phase 9 **禁止**只返回 `content` 而丢弃 `reasoning_content` / `tool_calls`。

### 2.3 `usage`

```json
{
  "prompt_tokens": 12,
  "completion_tokens": 80,
  "total_tokens": 92,
  "prompt_tokens_details": {
    "cached_tokens": 0,
    "audio_tokens": 0
  },
  "completion_tokens_details": {
    "reasoning_tokens": 64,
    "accepted_prediction_tokens": 0,
    "rejected_prediction_tokens": 0,
    "audio_tokens": 0
  }
}
```

Unio settlement 使用内部 `adapter.ChatUsage`；对外必须输出 OpenAI details 结构。

---

## 3. 流式响应：`stream: true`

### 3.1 传输格式

- `Content-Type: text/event-stream`
- 每条：`data: <json>\n\n`
- 结束：`data: [DONE]\n\n`

### 3.2 Chunk 顶层结构

```json
{
  "id": "chatcmpl-xxx",
  "object": "chat.completion.chunk",
  "created": 1694268190,
  "model": "deepseek-v4-pro",
  "choices": [ ... ],
  "usage": null
}
```

| 字段 | 含义 |
| --- | --- |
| `object` | 固定 `"chat.completion.chunk"` |
| `choices[].delta` | 增量内容 |
| `choices[].finish_reason` | 仅在结束 chunk 出现 |
| `usage` | 中间 chunk 为 `null`；尾包为完整 usage（当 `include_usage=true`） |

### 3.3 典型事件顺序

**不带 include_usage：**

```text
1. data: {"choices":[{"delta":{"role":"assistant","content":""},...}]}
2. data: {"choices":[{"delta":{"content":"Hello"},...}]}
3. data: {"choices":[{"delta":{},"finish_reason":"stop"},...}]}
4. data: [DONE]
```

**带 include_usage：**

```text
1~3. 同上，且每个 content chunk 带 "usage": null
4. data: {"choices":[],"usage":{"prompt_tokens":...,"completion_tokens":...,"total_tokens":...}}
5. data: [DONE]
```

**reasoning 模型（DeepSeek thinking）：**

```text
1. delta.reasoning_content 增量（思考阶段）
2. delta.content 增量（最终答案阶段）
3. finish_reason chunk
4. usage chunk（若 include_usage）
5. [DONE]
```

Phase 9 **禁止**将 `reasoning_content` 长期合并进 `delta.content`（课 3 临时方案必须在 TASK-9.07 修正）。

### 3.4 `choices[].delta` 字段

| 字段 | 含义 |
| --- | --- |
| `role` | 首包常为 `"assistant"` |
| `content` | 最终答案增量 |
| `reasoning_content` | 思考过程增量（扩展字段） |
| `tool_calls` | 工具调用增量（含 index/id/function） |
| `refusal` | 拒绝内容 |

### 3.5 流式错误

SSE 已开始后不能改回 JSON error。Unio 写出 OpenAI-compatible data-only error chunk，且不写 `[DONE]`（Phase 4/7 已定义）。

---

## 4. 与 Unio 内部三层 DTO 的对应

| OpenAI 公开字段 | gatewayapi DTO | adapter contract | upstream wire |
| --- | --- | --- | --- |
| 客户 request 全部 supported 字段 | 接收 + 校验 | OpenAI 语义 | 厂商 JSON |
| 客户 response 全部 supported 字段 | 写出 | OpenAI 语义 | 厂商 JSON 解析后翻译 |

gateway service **不做 vendor 分支**；翻译只在 `internal/core/adapter/openai/`。

---

## 5. 参考链接

- [OpenAI Create Chat Completion](https://developers.openai.com/api/reference/resources/chat/subresources/completions/methods/create)
- [OpenAI Streaming events](https://developers.openai.com/api/reference/resources/chat/subresources/completions/streaming-events/)
- Unio 实现矩阵：[COMPATIBILITY_MATRIX.md](COMPATIBILITY_MATRIX.md)
- 全链路：[END_TO_END_PIPELINE.md](END_TO_END_PIPELINE.md)
