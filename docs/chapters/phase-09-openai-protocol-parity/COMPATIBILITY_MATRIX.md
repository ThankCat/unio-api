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

当前代码基线：2026-05-31（课 2~4 + normalizer 过渡代码）

---

## A. 请求字段

| OpenAI 字段 | Phase 9 | 当前 | DeepSeek upstream | 任务 |
| --- | --- | --- | --- | --- |
| `model` | Todo | Done | 透传 upstream_model | — |
| `messages` (text) | Todo | Partial | 透传 | 9.03 |
| `messages` (multimodal) | Passthrough | Reject | 按上游 | 9.03 |
| `messages[].reasoning_content` | Todo | Reject | 必须回传 | 9.03, 9.08 |
| `messages[].tool_calls` | Todo | Reject | 透传 | 9.10 |
| `tool` role | Todo | Reject | 透传 | 9.10 |
| `developer` role | Todo | Reject | 映射 system | 9.03 |
| `stream` | Todo | Done | 透传 | — |
| `stream_options.include_usage` | Todo | Partial | 内部 always true | 9.09 |
| `stream_options.include_obfuscation` | Reject | Reject | N/A | — |
| `temperature` | Todo | Done | no-op (thinking) | — |
| `top_p` | Todo | Done | no-op (thinking) | — |
| `max_tokens` | Todo | Done | 透传 | — |
| `max_completion_tokens` | Todo | Reject | 映射 max_tokens | 9.03 |
| `presence_penalty` | Todo | Done | no-op (thinking) | — |
| `frequency_penalty` | Todo | Done | no-op (thinking) | — |
| `stop` | Todo | Done | 透传 | — |
| `user` | Todo | Done | 透传 | — |
| `tools` | Todo | Silent drop | 透传 | 9.02, 9.10 |
| `tool_choice` | Todo | Silent drop | 透传 | 9.02, 9.10 |
| `parallel_tool_calls` | Todo | Silent drop | 透传 | 9.10 |
| `response_format` | Todo | Silent drop | 透传 | 9.11 |
| `reasoning_effort` | Todo | Silent drop | 透传 | 9.05, 9.08 |
| `thinking` (extension) | Passthrough | Silent drop | 原生字段 | 9.02, 9.05 |
| `logprobs` | Todo | Reject | 可能 400 | 9.13 矩阵 |
| `n` | Todo | Reject | 待实测 | 9.13 |
| `seed` | Todo | Silent drop | 待实测 | C8 |
| `service_tier` | Reject | Silent drop | N/A | 9.02 |
| `store` | Reject | Silent drop | N/A | 9.02 |
| `web_search_options` | Reject | Silent drop | N/A | 9.02 |

---

## B. 非流式响应字段

| OpenAI 字段 | Phase 9 | 当前 | DeepSeek | 任务 |
| --- | --- | --- | --- | --- |
| `id` | Todo | Partial | 透传 | 9.06 |
| `object` | Todo | Done | 固定 | — |
| `created` | Todo | Done | — | — |
| `model` (Unio ID) | Todo | Done | 替换为路由 ID | — |
| `choices[].message.content` | Todo | Done | 透传 | — |
| `choices[].message.reasoning_content` | Todo | **Missing** | 必须输出 | 9.06 |
| `choices[].message.tool_calls` | Todo | **Missing** | 透传 | 9.06, 9.10 |
| `choices[].finish_reason` | Todo | Partial | 透传 | 9.06 |
| `usage` 三字段 | Todo | Done | 透传 | — |
| `usage.*_details` | Todo | **Missing** | 透传 | 9.06 |
| `system_fingerprint` | Passthrough | Missing | 有则透传 | 9.06 |
| `logprobs` | Todo | Missing | 待实测 | C8 |

---

## C. 流式响应字段

| OpenAI 字段 | Phase 9 | 当前 | DeepSeek | 任务 |
| --- | --- | --- | --- | --- |
| `object=chat.completion.chunk` | Todo | Done | — | — |
| `choices[].delta.content` | Todo | Done | 透传 | — |
| `choices[].delta.reasoning_content` | Todo | **Merged into content** | 必须分离 | 9.07 |
| `choices[].delta.tool_calls` | Todo | Missing | 透传 | 9.07, 9.10 |
| `choices[].finish_reason` | Todo | Partial | 透传 | 9.07 |
| 中间 chunk `usage: null` | Todo | Missing | — | 9.09 |
| 尾包 usage chunk | Todo | Partial | settlement 数字 | 9.09 |
| `[DONE]` | Todo | Done | — | — |
| internal final usage from tail | Todo | Done | 非空 choices 尾包 | 9.07 |

---

## D. 全链路步骤

| 步骤 | Phase 9 | 当前 | 任务 |
| --- | --- | --- | --- |
| ① HTTP decode 不 silent drop | Todo | **Fail** | 9.02 |
| ② routing 选 DeepSeek | Todo | Done | — |
| ③ adapter contract OpenAI 语义 | Todo | Partial | 9.04 |
| ③b gateway service DTO↔contract 映射 | Todo | **Partial（role+content）** | 9.15 |
| ③c 请求校验（Phase 4 text-only） | Todo | **Fail** | 9.16 |
| ③d authorization token 估算输入 | Todo | **Partial（role+content）** | 9.17 |
| ④ 请求翻译 → DeepSeek | Todo | Partial | 9.05, 9.08 |
| ⑤ 非流式响应翻译 | Todo | Partial | 9.06 |
| ⑤ 流式 stream translate | Todo | Partial | 9.07 |
| ⑥ settlement + optional usage 尾包 | Todo | Partial | 9.09 |
| ⑦ HTTP 写出 OpenAI 形状 | Todo | Partial | 9.03 |
| ⑧ DeepSeek 全链路验收 | Todo | Not started | 9.14 |

---

## E. Phase 9 done 判定（C1~C4 + DeepSeek）

以下条件全部满足，且矩阵中 C1~C4 项均为 Done 或 documented Passthrough/Reject：

1. OpenAI SDK 只改 URL/key 可跑通 chat/stream/include_usage。
2. DeepSeek DS-01~DS-07 验收用例通过（见 [DEEPSEEK_UPSTREAM.md](DEEPSEEK_UPSTREAM.md)）。
3. 无 silent drop 回归测试。
4. stream translate 已吸收 `normalizer/`，文档不再单独定义 Normalizer。

C5+（tools、response_format 等）可标记 Phase 9 partial done 后继续迭代。

**注意**：C1~C4 done 表示 text/reasoning/stream usage 等核心 drop-in 能力就绪，不等于 tools（C5）或 JSON mode（C6）已完整支持。
