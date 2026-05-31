# 全链路：OpenAI 请求 → 上游 → OpenAI 响应

更新时间：2026-05-31

本文档定义 Unio Gateway 的**完整翻译链路**。实现 Phase 9 时，每一层都必须对齐本文档，不允许在 gateway 业务层写 vendor 分支。

---

## 1. 总览

```text
┌─────────────┐
│  客户程序    │  OpenAI SDK / LangChain / curl
│  (OpenAI)   │
└──────┬──────┘
       │ POST /v1/chat/completions
       │ Authorization: Bearer unio_sk_...
       │ Body: OpenAI ChatCompletionRequest
       ▼
┌─────────────────────────────────────────────────────────────┐
│ ① HTTP 入口 gatewayapi                                       │
│    decode / validate / 禁止 silent drop                      │
│    ChatCompletionRequest（OpenAI 公开契约）                   │
└──────┬──────────────────────────────────────────────────────┘
       ▼
┌─────────────────────────────────────────────────────────────┐
│ ② Gateway Service                                            │
│    auth / rate limit / routing / authorization / settlement  │
│    只做编排，不做 vendor 翻译                                  │
└──────┬──────────────────────────────────────────────────────┘
       │ 选中 candidate: provider=deepseek, channel, upstream_model
       ▼
┌─────────────────────────────────────────────────────────────┐
│ ③ Adapter Contract                                           │
│    adapter.ChatRequest / ChatResponse / ChatStreamChunk    │
│    （OpenAI 语义内部模型，不是 vendor 模型）                    │
└──────┬──────────────────────────────────────────────────────┘
       ▼
┌─────────────────────────────────────────────────────────────┐
│ ④ Adapter Request Translate                                  │
│    OpenAI 语义 request → upstream wire JSON                  │
│    providers.slug=deepseek 时使用 DeepSeek 映射规则            │
└──────┬──────────────────────────────────────────────────────┘
       │ POST https://api.deepseek.com/chat/completions
       ▼
┌─────────────┐
│  DeepSeek   │  上游响应（OpenAI-compatible wire，含 vendor 差异）
└──────┬──────┘
       ▼
┌─────────────────────────────────────────────────────────────┐
│ ⑤ Adapter Response Translate                                 │
│    非流式：wire JSON → adapter.ChatResponse                   │
│    流式：SSE event → stream translate → adapter chunk         │
│    （吸收原 normalizer/ 过渡代码，不再单独定义 Normalizer）     │
└──────┬──────────────────────────────────────────────────────┘
       ▼
┌─────────────────────────────────────────────────────────────┐
│ ⑥ Gateway Service                                            │
│    settlement 消费 adapter.ChatUsage（内部账务事实）           │
│    可选：按客户端 include_usage 写出 usage 尾包               │
└──────┬──────────────────────────────────────────────────────┘
       ▼
┌─────────────────────────────────────────────────────────────┐
│ ⑦ HTTP 出口 gatewayapi                                       │
│    JSON 或 SSE（OpenAI ChatCompletionResponse / chunk）       │
└──────┬──────────────────────────────────────────────────────┘
       ▼
┌─────────────┐
│  客户程序    │  看到的始终是 OpenAI 结构
└─────────────┘
```

---

## 2. 各层职责（必须遵守）

| 步骤 | 模块 | 输入 | 输出 | 禁止 |
| --- | --- | --- | --- | --- |
| ① | `gatewayapi` handler | HTTP JSON | `ChatCompletionRequest` | 选 channel；调 upstream |
| ② | `gateway service` | OpenAI request + auth ctx | route + billing | vendor if/else；改 message 语义 |
| ③ | adapter contract | OpenAI 语义 | OpenAI 语义 | 暴露 vendor 字段给 gateway |
| ④ | adapter request map | OpenAI 语义 + `channel.Runtime` | upstream wire body | 读 DB/env |
| ⑤ | adapter response map | upstream wire / SSE | OpenAI 语义 response/chunk | 把 vendor 字段直接写给客户 |
| ⑥ | gateway stream/chat | adapter 结果 | emit OpenAI DTO | 合并 reasoning 到 content |
| ⑦ | handler | OpenAI DTO | HTTP JSON/SSE | 透传 upstream 原始 body |

---

## 3. 模型选择（步骤 ②）

客户请求：

```json
{ "model": "deepseek-v4-pro", ... }
```

routing 输出：

```text
AdapterKey:     openai
ProviderSlug:   deepseek
UpstreamModel:  deepseek-v4-pro
Channel:        channel.Runtime{ BaseURL, APIKey, ProviderSlug, Timeout }
```

要点：

1. 客户 `model` 是 Unio catalog ID，响应里的 `model` 也必须是这个 ID。
2. `ProviderSlug` 只传给 adapter，用于选择 request/response 翻译规则。
3. gateway 不因为选了 DeepSeek 而改变对外 JSON 形状。

---

## 4. 请求翻译（步骤 ④）

原则：**客户发 OpenAI 形状，adapter 负责变成 upstream 能接受的形状**。

通用规则：

| 情况 | 处理 |
| --- | --- |
| 字段名相同、语义相同 | 原样写入 upstream wire |
| 客户 OpenAI 字段，upstream 也认 | 原样写入 |
| 客户 OpenAI 字段，upstream 不认但可忽略 | 写入；document 标注 no-op |
| 客户 OpenAI 字段，upstream 明确报错 | Reject 或 strip 并 document |
| 客户 vendor extension（如 `thinking`） | Passthrough 到 upstream |
| Unio 不支持且无法 passthrough | **400**，不能 silent drop |

DeepSeek 细则见 [DEEPSEEK_UPSTREAM.md](DEEPSEEK_UPSTREAM.md)。

内部 settlement 附加：

```text
adapter 向上游 stream 请求始终带 stream_options.include_usage=true
（与客户端是否请求 include_usage 无关）
```

---

## 5. 响应翻译（步骤 ⑤）

### 5.1 非流式

```text
upstream choices[0].message.{content, reasoning_content, tool_calls}
  → adapter.ChatResponse
  → gatewayapi.ChatCompletionResponse
```

### 5.2 流式

```text
upstream SSE data: {...}
  → wire decode
  → stream translate（按 ProviderSlug）
  → adapter.ChatStreamChunk 序列
  → gatewayapi.ChatCompletionStreamResponse 序列
```

stream translate 职责（原 normalizer 吸收至此）：

1. 解析每个 SSE event 的 choices / usage。
2. 输出 OpenAI 语义 delta：`content` 与 `reasoning_content` **分离**。
3. usage 尾包在非空 choices 时也要识别（DeepSeek 常见）。
4. 跳过空 heartbeat；保留 finish_reason 位置。

---

## 6. 返回给用户（步骤 ⑥⑦）

| 场景 | 行为 |
| --- | --- |
| 非流式成功 | 一次 JSON，`choices` + `usage` 完整 |
| 流式成功 | SSE chunks + `[DONE]` |
| 流式 + `include_usage` | 尾包 `choices:[]` + `usage`，数字来自 settlement final usage |
| 失败（SSE 未开始） | OpenAI JSON error |
| 失败（SSE 已开始） | data-only stream error，无 `[DONE]` |

---

## 7. 适配原则（你提出的规则）

```text
1. 能完整适配 OpenAI 协议的，必须完整实现（Supported）。
2. 上游字段名不同但语义等价，必须映射到 OpenAI 字段（Adapted）。
3. 上游扩展字段若客户/SDK 会通过 extra_body 传入，必须 passthrough（Passthrough）。
4. 确实无法适配且无等价语义，明确 Reject 并写入 Compatibility Matrix（Rejected）。
5. 禁止 silent drop。
```

---

## 8. Phase 9 实现顺序与链路关系

```text
TASK-9.01 ADR
  → TASK-9.02 请求不 silent drop
  → TASK-9.03/10.04 公开 DTO + adapter contract
  → TASK-9.05 请求翻译（通用 + DeepSeek）
  → TASK-9.06 非流式响应翻译
  → TASK-9.07 流式响应翻译（吸收 normalizer/）
  → TASK-9.08 DeepSeek 多轮 reasoning 回传
  → TASK-9.09 stream usage 完整语义
  → TASK-9.10~11 tools / response_format
  → TASK-9.14 DeepSeek 上游全链路验收
  → TASK-9.12 SDK 黑盒
```

**DeepSeek 上游兼容是链路最后验收，不是最先写 special case**；先有 OpenAI 形状的全链路，再填 DeepSeek 映射表。

---

## 9. 相关文档

- [OPENAI_PROTOCOL.md](OPENAI_PROTOCOL.md) — 字段解释与响应结构
- [DEEPSEEK_UPSTREAM.md](DEEPSEEK_UPSTREAM.md) — DeepSeek 请求/响应映射表
- [COMPATIBILITY_MATRIX.md](COMPATIBILITY_MATRIX.md) — 逐字段实现状态
- [PLAN.md](PLAN.md) — 任务锚点
