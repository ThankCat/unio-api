# Compatibility Matrix

更新时间：2026-05-31

状态图例：

| 状态 | 含义 |
| --- | --- |
| **Done** | 已实现且测试覆盖 |
| **Partial** | 部分实现，不够 drop-in |
| **Todo** | Phase 9 必须实现 |
| **Passthrough** | 不单独建模，原样转发 upstream |
| **Reject** | 明确 400，不 silent drop |
| **N/A** | 不适用于 Unio 产品边界 |

当前代码基线：2026-05-31（C1~C4 + C5~C6 typed 化 + streamtranslate + DS-01~07 E2E）

---

## A. 请求字段

| OpenAI 字段 | Phase 9 | 当前 | DeepSeek upstream | 任务 |
| --- | --- | --- | --- | --- |
| `model` | Done | Done | 透传 upstream_model | — |
| `messages` (text) | Done | Done | 透传 | 9.03 |
| `messages` (multimodal) | Passthrough | Passthrough | 按上游 | 9.03 |
| `messages[].reasoning_content` | Done | Done | 必须回传 | 9.08 |
| `messages[].tool_calls` | Done | Done | 透传 | 9.10 |
| `tool` role | Done | Done | 透传 | 9.10 |
| `developer` role | Done | Done | 映射 system | 9.16 |
| `stream` | Done | Done | 透传 | — |
| `stream_options.include_usage` | Done | Done | 内部 always true | 9.09 |
| `stream_options.include_obfuscation` | Reject | Reject | N/A | — |
| `temperature` | Done | Done | no-op (thinking) | — |
| `top_p` | Done | Done | no-op (thinking) | — |
| `max_tokens` | Done | Done | 透传 | — |
| `max_completion_tokens` | Done | Done | 映射 max_tokens | 9.03 |
| `presence_penalty` | Done | Done | no-op (thinking) | — |
| `frequency_penalty` | Done | Done | no-op (thinking) | — |
| `stop` | Done | Done | 透传 | — |
| `user` | Done | Done | 透传 | — |
| `tools` | Done | Done | 透传 | 9.10 |
| `tool_choice` | Done | Done | 透传（RawMessage union） | 9.10 |
| `parallel_tool_calls` | Done | Done | 透传 | 9.10 |
| `response_format` | Done | Done | 透传（typed + json_schema RawMessage） | 9.11 |
| `reasoning_effort` | Done | Done | 透传 | 9.05 |
| `thinking` (extension) | Passthrough | Done | 原生字段 | 9.02, 9.05 |
| `logprobs` | Todo | Reject | 可能 400 | C8 |
| `n` | Todo | Reject | 待实测 | C8 |
| `seed` | Todo | Passthrough | 待实测 | C8 |
| `service_tier` | Reject | Reject | N/A | 9.02 |
| `store` | Reject | Reject | N/A | 9.02 |
| `web_search_options` | Reject | Reject | N/A | 9.02 |

---

## B. 非流式响应字段

| OpenAI 字段 | Phase 9 | 当前 | DeepSeek | 任务 |
| --- | --- | --- | --- | --- |
| `id` | Done | Done | 透传 | 9.06 |
| `object` | Done | Done | 固定 | — |
| `created` | Done | Done | — | — |
| `model` (Unio ID) | Done | Done | 替换为路由 ID | — |
| `choices[].message.content` | Done | Done | 透传 | — |
| `choices[].message.reasoning_content` | Done | Done | 必须输出 | 9.06 |
| `choices[].message.tool_calls` | Done | Done | 透传 | 9.10 |
| `choices[].finish_reason` | Done | Done | 透传 | 9.06 |
| `usage` 三字段 | Done | Done | 透传 | — |
| `usage.*_details` | Done | Done | 透传 | 9.06 |
| `system_fingerprint` | Passthrough | Passthrough | 有则 extension | C8 |
| `logprobs` | Todo | Missing | 待实测 | C8 |

---

## C. 流式响应字段

| OpenAI 字段 | Phase 9 | 当前 | DeepSeek | 任务 |
| --- | --- | --- | --- | --- |
| `object=chat.completion.chunk` | Done | Done | — | — |
| `choices[].delta.content` | Done | Done | 透传 | — |
| `choices[].delta.reasoning_content` | Done | Done | 分离 | 9.07 |
| `choices[].delta.tool_calls` | Done | Done | 透传（RawMessage delta） | 9.10 |
| `choices[].finish_reason` | Done | Done | 透传 | 9.07 |
| 中间 chunk `usage: null` | Done | Done | — | 9.09 |
| 尾包 usage chunk | Done | Done | settlement 数字 | 9.09 |
| `[DONE]` | Done | Done | — | — |
| internal final usage from tail | Done | Done | 非空 choices 尾包 | 9.07 |

---

## D. 全链路步骤

| 步骤 | Phase 9 | 当前 | 任务 |
| --- | --- | --- | --- |
| ① HTTP decode 不 silent drop | Done | Done | 9.02 |
| ② routing 选 DeepSeek | Done | Done | — |
| ③ adapter contract OpenAI 语义 | Done | Done | 9.04 |
| ③b gateway service DTO↔contract 映射 | Done | Done | 9.15 |
| ③c 请求校验 | Done | Done | 9.16 |
| ③d authorization token 估算输入 | Done | Done | 9.17 |
| ④ 请求翻译 → DeepSeek | Done | Done | 9.05, 9.08 |
| ⑤ 非流式响应翻译 | Done | Done | 9.06 |
| ⑤ 流式 stream translate | Done | Done | 9.07 |
| ⑥ settlement + optional usage 尾包 | Done | Done | 9.09 |
| ⑦ HTTP 写出 OpenAI 形状 | Done | Done | 9.03 |
| ⑧ DeepSeek 全链路验收 | Done | Done | 9.14 |
| ⑨ OpenAI SDK 形状黑盒 | Done | Done | 9.12 |

---

## E. Phase 9 done 判定

### C1~C4 + DeepSeek（已完成）

1. OpenAI SDK 形状请求可跑通 chat/stream/include_usage（Go 黑盒 + HTTP handler）。
2. DeepSeek DS-01~DS-07 验收用例通过。
3. 无 silent drop 回归测试。
4. stream translate 已迁入 `streamtranslate/`，不再单独定义 Normalizer 架构。

### C5~C6（已完成 typed 化）

1. `tools` / `tool_calls` / `tool` role typed DTO + upstream wire。
2. `response_format` typed（`json_object` + `json_schema` RawMessage passthrough）。
3. 流式 `delta.tool_calls` 仍用 RawMessage 保留增量语义。

### C8 待后续

`logprobs`、`n`、`seed`、multimodal 完整校验等按优先级迭代。

**注意**：流式 tool_calls 增量合并（OpenAI index 语义）未做完整 typed 建模，当前 RawMessage passthrough 对多数 SDK 场景足够。
