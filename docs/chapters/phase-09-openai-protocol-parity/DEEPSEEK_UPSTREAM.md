# DeepSeek 上游兼容映射

更新时间：2026-05-31

DeepSeek 官方 API 为 **OpenAI-compatible Chat Completions**（`base_url=https://api.deepseek.com`）。

Phase 9 历史实现中，DeepSeek 走 `providers.adapter=openai` + `ProviderSlug=deepseek`
的 adapter 翻译规则，**不对客户暴露 DeepSeek 协议**。

Phase 10 会把 runtime adapter 绑定迁移为 `channels.protocol` + `channels.adapter_key`，
并将 DeepSeek OpenAI 翻译收口到 `adapter/openai/deepseek`。

全链路见 [END_TO_END_PIPELINE.md](END_TO_END_PIPELINE.md)。

---

## 1. 上游基本信息

| 项 | 值 |
| --- | --- |
| Base URL | `https://api.deepseek.com` |
| 路径 | `POST /chat/completions` |
| 当前模型 | `deepseek-v4-pro`, `deepseek-v4-flash` |
| 遗留别名 | `deepseek-chat`, `deepseek-reasoner`（计划退役，Unio catalog 应优先 V4 ID） |
| 协议 | OpenAI-compatible + vendor 扩展 |

---

## 2. 请求映射：客户 OpenAI → DeepSeek wire

### 2.1 原样透传（字段名与语义一致）

| 客户 OpenAI 字段 | DeepSeek wire | 说明 |
| --- | --- | --- |
| `model` | `model` | 使用 routing 选中的 `upstream_model`，不是客户 Unio model ID |
| `messages` | `messages` | 含 role/content/tool_calls/tool_call_id |
| `stream` | `stream` | |
| `max_tokens` | `max_tokens` | DeepSeek 输出上限含 CoT |
| `max_completion_tokens` | 映射到 `max_tokens` 或两者都写 | 以实现时 DeepSeek 文档为准 |
| `stop` | `stop` | |
| `user` | `user` | |
| `tools` | `tools` | |
| `tool_choice` | `tool_choice` | |
| `response_format` | `response_format` | 支持 `json_object` |
| `reasoning_effort` | `reasoning_effort` | DeepSeek 支持 `high` / `max` |
| `stream_options.include_usage` | `stream_options.include_usage` | adapter 内部 settlement **始终 true** |

### 2.2 需要适配的字段

| 客户 OpenAI 字段 | DeepSeek wire | 适配规则 |
| --- | --- | --- |
| `messages[].reasoning_content` | `messages[].reasoning_content` | assistant 历史**必须原样回传**（tool 多轮必需） |
| OpenAI SDK `extra_body.thinking` | `thinking` | `{ "type": "enabled" \| "disabled" }` |
| 无等价 OpenAI 字段时客户直传 `thinking` | `thinking` | Passthrough |
| `developer` role | `system` 或 DeepSeek 支持的 role | 映射为 upstream 可接受 role（保持语义） |

### 2.3 DeepSeek 接受但无效果（no-op，不报错）

以下字段 DeepSeek 文档说明：传入不报错，但 **thinking 模式下不生效**：

- `temperature`
- `top_p`
- `presence_penalty`
- `frequency_penalty`

Unio 行为：**原样 passthrough** 到 upstream，不在 gateway 剥离；Compatibility Matrix 标注为 upstream no-op。

### 2.4 DeepSeek 明确不支持（Rejected 或按上游错误转发）

| 客户字段 | DeepSeek 行为 | Unio 策略 |
| --- | --- | --- |
| `logprobs` / `top_logprobs` | thinking 模式下可能 400 | 若 upstream 400，映射为安全 OpenAI error；文档标注 Rejected for DeepSeek |
| OpenAI 专有：`service_tier`, `store`, `web_search_options`, `modalities`+`audio` | 不支持 | **Reject** 400，不 silent drop |
| `n` > 1 | 未验证 | 按实测：Support 或 Reject，写入矩阵 |

### 2.5 adapter 内部附加（客户不可见）

```json
{
  "stream_options": { "include_usage": true }
}
```

即使客户未请求 `include_usage`，adapter 对流式 upstream 也应带上，以保证 settlement final usage。

---

## 3. 响应映射：DeepSeek wire → 客户 OpenAI

### 3.1 非流式

| DeepSeek 字段 | 客户 OpenAI 字段 | 规则 |
| --- | --- | --- |
| `choices[0].message.content` | `choices[0].message.content` | 原样 |
| `choices[0].message.reasoning_content` | `choices[0].message.reasoning_content` | **必须输出**，禁止合并进 content |
| `choices[0].message.tool_calls` | `choices[0].message.tool_calls` | 原样 |
| `choices[0].finish_reason` | `choices[0].finish_reason` | 原样 |
| `usage.prompt_tokens` | `usage.prompt_tokens` | 原样 |
| `usage.completion_tokens` | `usage.completion_tokens` | 原样 |
| `usage.total_tokens` | `usage.total_tokens` | 原样 |
| `usage.prompt_tokens_details.cached_tokens` | `usage.prompt_tokens_details.cached_tokens` | 原样 |
| `usage.completion_tokens_details.reasoning_tokens` | `usage.completion_tokens_details.reasoning_tokens` | 原样 |
| `id`, `created`, `model` | 顶层同名字段 | 响应 `model` 用**客户 Unio model ID** |

### 3.2 流式

| DeepSeek SSE delta | 客户 OpenAI delta | 规则 |
| --- | --- | --- |
| `delta.reasoning_content` | `delta.reasoning_content` | 思考阶段增量 |
| `delta.content` | `delta.content` | 答案阶段增量 |
| `delta.tool_calls` | `delta.tool_calls` | 工具调用增量 |
| `delta.role` | `delta.role` | 首包 |
| `finish_reason` | `choices[].finish_reason` | 结束包 |
| 尾包 `usage`（可能在 choices 非空 event 内） | 内部 final usage | adapter 提取给 settlement |
| 客户端 `include_usage` 尾包 | `choices:[]` + `usage` | gateway 在 settlement 后写出 |

### 3.3 stream translate 特殊规则（TASK-9.07）

| DeepSeek 行为 | 翻译规则 |
| --- | --- |
| usage 出现在带 content/reasoning 的最后一包 | 必须识别 usage，不能只等 `choices:[]` |
| 空 heartbeat event | 跳过，不 emit 给客户 |
| 仅 `reasoning_content` 无 `content` | emit `delta.reasoning_content`，**不**写入 `delta.content` |
| 同一 event 同时有 content 和 reasoning | 两个 delta 字段分别 emit |

---

## 4. 多轮对话与 tool calls

DeepSeek thinking + tools 规则（必须实现 TASK-9.08）：

| 场景 | 规则 |
| --- | --- |
| 普通多轮（无 tool） | 后续请求**不要**把历史 `reasoning_content` 发回 upstream |
| tool call 多轮 | assistant 消息必须包含 `content` + `reasoning_content` + `tool_calls` |
| 客户请求含 `reasoning_content` | adapter 原样写入 upstream messages |
| 客户请求不含但 Unio 曾返回过 | 客户端/SDK 负责带回；Unio 不能 silent 丢弃收到的字段 |

---

## 5. thinking 模式控制（客户视角）

客户可通过 OpenAI SDK 这样调用 Unio：

```python
client.chat.completions.create(
    model="deepseek-v4-pro",
    messages=[{"role": "user", "content": "..."}],
    reasoning_effort="high",
    stream=True,
    stream_options={"include_usage": True},
    extra_body={"thinking": {"type": "enabled"}},
)
```

Unio 必须：

1. 接收 `reasoning_effort`、`stream_options`、`extra_body.thinking`。
2. 翻译后发给 DeepSeek。
3. 返回分离的 `reasoning_content` 与 `content`。

关闭 thinking：

```python
extra_body={"thinking": {"type": "disabled"}}
```

---

## 6. 验收用例（TASK-9.14）

| # | 用例 | 通过标准 |
| --- | --- | --- |
| DS-01 | 非流式思考题 | 响应含 `message.reasoning_content` + `message.content` |
| DS-02 | 流式思考题 | SSE 先出现 `delta.reasoning_content`，再出现 `delta.content` |
| DS-03 | stream + include_usage | 尾包 usage + `[DONE]` |
| DS-04 | thinking disabled | 无 reasoning_content，只有 content |
| DS-05 | tools 多轮 | 第二轮不 400；assistant 历史含 reasoning_content |
| DS-06 | passthrough | 请求带 `extra_body.thinking`，upstream 收到等价 body |
| DS-07 | settlement | request_records succeeded；usage 含 reasoning_tokens |

---

## 7. 与代码位置的对应

| 能力 | 目标代码位置 |
| --- | --- |
| DeepSeek 请求映射 | `internal/core/adapter/openai/` request map（按 `ProviderSlug`） |
| DeepSeek 非流式响应 | `internal/core/adapter/openai/chat.go` ChatCompletions |
| DeepSeek 流式响应 | `internal/core/adapter/openai/` stream translate（吸收 `normalizer/deepseek.go`） |
| 客户 HTTP | `internal/app/gatewayapi/openai_dto.go` |
| 编排 | `internal/service/gateway/chat_*.go` |

---

## 8. 参考

- [DeepSeek Thinking Mode](https://api-docs.deepseek.com/guides/thinking_mode)
- [DeepSeek Create Chat Completion](https://api-docs.deepseek.com/api/create-chat-completion)
- [OPENAI_PROTOCOL.md](OPENAI_PROTOCOL.md)
