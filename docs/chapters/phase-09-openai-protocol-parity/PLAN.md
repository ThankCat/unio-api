# Phase 9 Plan - OpenAI Protocol Parity

## 目标

让 Unio Gateway API 成为 OpenAI Chat Completions 的可替换实现。

客户只修改 `base_url` 和 `api_key`，现有 OpenAI SDK、LangChain 或自研 OpenAI-compatible 客户端，在声明支持的 capability 范围内行为一致。

本阶段必须守住四条原则：

1. **对外契约以 OpenAI 为准**；上游厂商 JSON 只存在于 adapter wire 层。
2. **禁止静默丢弃请求字段**；不支持的能力必须明确 400 或写入 Compatibility Matrix。
3. **响应翻译统一收口到 adapter**；gateway 只做路由、计费、审计和 HTTP 写出。
4. **不再单独维护 Normalizer 架构定义**；现有 `internal/core/adapter/openai/normalizer/` 视为过渡实现，本阶段吸收进「OpenAI 响应翻译 / stream translate」并重构命名/边界。

## 章节文档

| 文档 | 作用 |
| --- | --- |
| [OPENAI_PROTOCOL.md](OPENAI_PROTOCOL.md) | OpenAI 最新 Chat Completions 请求/非流式/流式响应字段解释 |
| [END_TO_END_PIPELINE.md](END_TO_END_PIPELINE.md) | 用户请求 → 选模型 → 请求翻译 → 上游 → 响应翻译 → 返回用户 |
| [DEEPSEEK_UPSTREAM.md](DEEPSEEK_UPSTREAM.md) | DeepSeek 上游请求/响应映射与验收用例 |
| [COMPATIBILITY_MATRIX.md](COMPATIBILITY_MATRIX.md) | 逐字段 Supported / Partial / Todo / Reject 实现矩阵 |

## 与 Phase 4 / Phase 5 的关系

| 阶段 | 已完成 | Phase 9 收口 |
| --- | --- | --- |
| Phase 4 | text-only MVP、SSE、严格 JSON | 公开 DTO 扩展到 OpenAI parity |
| Phase 5 | adapter contract、OpenAI wire、stream parser | contract 与 wire 对齐 OpenAI 语义；响应翻译补齐 |
| 探索代码 | `normalizer/` 包（DeepSeek stream 差异） | 合并进 TASK-9.07，不再作为独立章节概念 |

Phase 4 的 text-only 边界在 Phase 9 完成后视为 **被 parity 层取代**，而不是长期并存两套语义。

## 架构边界

```text
客户端 OpenAI 协议
  ↓ gatewayapi DTO（OpenAI 公开契约）
  ↓ gateway service（编排 / 计费 / 审计）
  ↓ adapter contract（OpenAI 语义内部模型）
  ↓ adapter/openai
      ├─ request map：OpenAI request → upstream wire
      ├─ response map：upstream wire → OpenAI response（非流式）
      └─ stream translate：upstream SSE event → OpenAI stream chunk（流式）
  ↓ upstream HTTP
```

关键决策：

| 问题 | 决策 |
| --- | --- |
| 协议以谁为主 | **OpenAI** |
| adapter contract 是否改成厂商定义 | **否**，contract 保持 OpenAI 语义 |
| 厂商差异放哪 | adapter 内部：request map + response map + stream translate |
| `normalizer/` 包 | **过渡代码**；Phase 9 收口后并入 stream translate，删除独立概念文档 |

完整链路见 [END_TO_END_PIPELINE.md](END_TO_END_PIPELINE.md)。

## Capability Matrix（验收分组）

| 代号 | 能力 | 优先级 |
| --- | --- | --- |
| C1 | 基础 chat：messages、sampling、stop、user、stream | P0 |
| C2 | `stream_options.include_usage` 请求/响应完整语义 | P0 |
| C3 | 请求字段不静默丢失；vendor extension passthrough | P0 |
| C4 | `reasoning_content`、usage details、DeepSeek thinking 回传 | P0 |
| C5 | tools / tool_calls / tool role | P1 |
| C6 | `response_format` / JSON mode | P1 |
| C7 | multimodal content passthrough | P2 |
| C8 | logprobs、seed、n 等高级 OpenAI 字段 | P2 |

## 涉及文件

| 文件 | 作用 |
| --- | --- |
| [internal/app/gatewayapi/openai_dto.go](../../../internal/app/gatewayapi/openai_dto.go) | 对外 OpenAI 公开契约。 |
| [internal/app/gatewayapi/chat_completions_handler.go](../../../internal/app/gatewayapi/chat_completions_handler.go) | 请求 decode、校验、SSE/JSON 写出。 |
| [internal/core/adapter/chat.go](../../../internal/core/adapter/chat.go) | adapter 内部 OpenAI 语义 contract。 |
| [internal/core/adapter/openai/chat.go](../../../internal/core/adapter/openai/chat.go) | 上游 HTTP、request/response/stream 主流程。 |
| [internal/core/adapter/openai/dto.go](../../../internal/core/adapter/openai/dto.go) | upstream wire DTO。 |
| [internal/core/adapter/openai/normalizer/](../../../internal/core/adapter/openai/normalizer/) | **过渡实现**；TASK-9.07 吸收/refactor 为 stream translate。 |
| [internal/service/gateway/chat_completion.go](../../../internal/service/gateway/chat_completion.go) | 非流式 gateway 编排。 |
| [internal/service/gateway/chat_stream.go](../../../internal/service/gateway/chat_stream.go) | 流式 gateway 编排。 |
| [docs/production/DECISIONS.md](../../production/DECISIONS.md) | OpenAI-first 兼容策略 ADR。 |

## 任务

<a id="task-9-01-openai-first-adr"></a>
### TASK-9.01 OpenAI-first 兼容策略 ADR

状态：partial

目标：

```text
把「对外 OpenAI、对内 wire、禁止 silent drop、stream translate 收口」写成稳定决策，
避免后续再次按厂商字段 patch gateway。
```

计划实现：

1. ~~在 [docs/production/DECISIONS.md](../../production/DECISIONS.md) 记录 OpenAI-first 原则。~~ → [DEC-005](../../production/DECISIONS.md#dec-005-openai-first-公开契约与-adapter-响应翻译)
2. 明确三层 DTO 职责：gatewayapi / adapter contract / upstream wire。
3. 明确 Normalizer 不再作为独立架构层维护；统一称为 adapter 响应翻译。
4. 定义 Capability Matrix 与 Supported / Passthrough / Rejected 三态。

关联 GAP：

- [GAP-9-001](../../production/TODO_REGISTER.md#gap-9-001)


<a id="task-9-02-request-no-silent-drop"></a>
### TASK-9.02 请求 decode：禁止静默丢弃

状态：planned

目标：

```text
typed OpenAI 字段 + 保留未知字段/扩展 body，
确保客户端传入的参数不会在 JSON decode 阶段被 Go struct 静默吃掉。
```

计划实现：

1. 设计 request decode 双轨策略（typed fields + raw extensions merge）。
2. 对已识别但不支持的字段返回明确 OpenAI-compatible 400。
3. 对 vendor extension（如 DeepSeek `thinking`）支持 passthrough 到 upstream wire。
4. 补 handler 测试：传入 tools/thinking 等字段时不能 silent drop。

关联 GAP：

- [GAP-9-001](../../production/TODO_REGISTER.md#gap-9-001)


<a id="task-9-03-public-openai-dto"></a>
### TASK-9.03 公开 OpenAI DTO 扩展

状态：partial

目标：

```text
gatewayapi 对外 request/response/stream chunk 对齐 OpenAI Chat Completions 形状。
```

已完成（探索/MVP）：

1. `stream_options.include_usage` 客户端响应尾包（课 4）。

计划实现：

1. messages 支持 OpenAI 消息结构（content string/array、tool_calls、tool_call_id 等）。
2. 响应 message/delta 增加 `reasoning_content`。
3. usage 增加 `prompt_tokens_details` / `completion_tokens_details`。
4. 流式中间 chunk 支持 OpenAI `usage: null` 语义（C2）。
5. 扩展 `tools`、`tool_choice`、`response_format`、`max_completion_tokens`、`reasoning_effort` 等 typed 字段（按 Capability Matrix 分批）。

关联 GAP：

- [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002)


<a id="task-9-04-adapter-contract-openai-semantics"></a>
### TASK-9.04 Adapter contract 对齐 OpenAI 语义

状态：planned

目标：

```text
adapter.ChatRequest / ChatResponse / ChatStreamChunk 承载 OpenAI 语义，
而不是窄 MVP 字段或 vendor 字段。
```

计划实现：

1. 扩展 [internal/core/adapter/chat.go](../../../internal/core/adapter/chat.go) message/chunk/usage 结构。
2. gateway service 只做 OpenAI DTO ↔ adapter contract 映射，不做 vendor 分支。
3. settlement 继续消费 `adapter.ChatUsage`（含 cached/reasoning），不受对外 DTO 扩展影响。

关联 GAP：

- [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002)


<a id="task-9-05-request-upstream-map"></a>
### TASK-9.05 请求翻译：OpenAI → upstream wire

状态：planned

目标：

```text
adapter 将 OpenAI 语义 request 翻译成 upstream wire JSON，
并 merge vendor extensions。
```

计划实现：

1. 扩展 [internal/core/adapter/openai/dto.go](../../../internal/core/adapter/openai/dto.go) wire 字段。
2. 非流式/流式 upstream 请求均写入完整 wire body。
3. DeepSeek：`thinking`、`reasoning_effort`、assistant 历史 `reasoning_content` 回传。
4. 内部 settlement 所需 `stream_options.include_usage=true` 与客户端 `include_usage` 策略分离。

关联 GAP：

- [GAP-9-001](../../production/TODO_REGISTER.md#gap-9-001)


<a id="task-9-06-non-stream-response-map"></a>
### TASK-9.06 非流式响应翻译：upstream wire → OpenAI

状态：planned

目标：

```text
非流式响应完整映射 message.content、message.reasoning_content、
tool_calls、usage details，而不是只取 content。
```

计划实现：

1. 扩展 upstream response wire DTO。
2. adapter 输出 OpenAI 语义 `ChatResponse`。
3. gateway 写回 OpenAI JSON response。
4. 补 DeepSeek reasoning 非流式回归测试。

关联 GAP：

- [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002)


<a id="task-9-07-stream-response-translate"></a>
### TASK-9.07 流式响应翻译：upstream SSE → OpenAI chunk

状态：partial

目标：

```text
统一收口 OpenAI-compatible 上游的流式响应翻译；
吸收现有 normalizer/ 过渡代码，不再单独维护 Normalizer 架构定义。
```

已完成（探索/MVP，待 Phase 9 收口）：

1. usage 尾包在非空 choices 时也可读取（课 2）。
2. DeepSeek `reasoning_content` 临时合并进 content（课 3，**本任务需改回 OpenAI 双字段**）。

计划实现：

1. 将 `normalizer/` 重构为 adapter 内明确的 stream translate 模块（可改名，如 `stream_translate/`）。
2. 按 `providers.slug` 注册 vendor stream 规则；default 为 OpenAI 基线。
3. 输出 OpenAI 语义 chunk：`delta.content` 与 `delta.reasoning_content` 分离。
4. 正确处理 usage 尾包、空 heartbeat、finish_reason 位置差异。
5. 删除独立 Normalizer 概念文档；代码注释改为「stream response translation」。
6. gateway 不再对 vendor 做 stream 特殊分支。

关联 GAP：

- [GAP-9-003](../../production/TODO_REGISTER.md#gap-9-003)


<a id="task-9-08-deepseek-reasoning-roundtrip"></a>
### TASK-9.08 DeepSeek reasoning 多轮回传

状态：planned

目标：

```text
thinking 模式多轮对话（尤其 tool calls）时，
assistant 历史 reasoning_content 必须原样回传 upstream，避免 400。
```

计划实现：

1. messages 历史保留 assistant `reasoning_content`。
2. adapter request map 写回 upstream。
3. 补多轮 + tool call 回归测试。

关联 GAP：

- [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002)


<a id="task-9-09-stream-usage-parity"></a>
### TASK-9.09 Stream usage 完整语义

状态：partial

目标：

```text
对齐 OpenAI stream_options.include_usage 的请求/响应行为。
```

已完成：

1. 客户端 `include_usage=true` 时在 `[DONE]` 前写出 usage chunk（课 4）。

计划实现：

1. 明确客户端 `include_usage` 与内部 settlement 所需 upstream include_usage 的策略。
2. 中间 content chunk 按 OpenAI 约定输出 `usage: null`（若启用 include_usage）。
3. usage chunk 数字来自 settlement 使用的 final usage。

关联 GAP：

- [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002)


<a id="task-9-10-tools"></a>
### TASK-9.10 Tools / tool_calls

状态：planned

目标：

```text
支持 OpenAI tools 请求与 tool_calls 响应（C5）。
```

计划实现：

1. 请求侧 tools、tool_choice、tool role。
2. 响应侧 assistant tool_calls 与非流式/流式 delta tool_calls。
3. 与 TASK-9.08 联动，保证 DeepSeek thinking + tools 多轮可用。

关联 GAP：

- [GAP-9-004](../../production/TODO_REGISTER.md#gap-9-004)


<a id="task-9-11-structured-output"></a>
### TASK-9.11 Structured output / response_format

状态：planned

目标：

```text
支持 response_format（至少 json_object；后续 json_schema 按上游能力扩展）。
```

关联 GAP：

- [GAP-9-004](../../production/TODO_REGISTER.md#gap-9-004)


<a id="task-9-12-sdk-blackbox-tests"></a>
### TASK-9.12 OpenAI SDK 黑盒验收

状态：planned

目标：

```text
用未修改的 OpenAI Python/JS SDK 指向 Unio，作为阶段主验收标准。
```

计划实现：

1. 非流式/流式 chat。
2. include_usage stream。
3. DeepSeek reasoning 分离字段。
4. tools 多轮（C5 完成后）。

关联 GAP：

- [GAP-9-001](../../production/TODO_REGISTER.md#gap-9-001)
- [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002)
- [GAP-9-003](../../production/TODO_REGISTER.md#gap-9-003)


<a id="task-9-14-deepseek-upstream-e2e"></a>
### TASK-9.14 DeepSeek 上游全链路验收

状态：planned

目标：

```text
在 OpenAI 形状的全链路实现完成后，用 DeepSeek 作为第一个 upstream 做 end-to-end 验收。
原则：能适配就完整适配；语义等价则映射；确实不能适配则 Reject 并写入 Compatibility Matrix。
```

计划实现：

1. 按 [DEEPSEEK_UPSTREAM.md](DEEPSEEK_UPSTREAM.md) 完成请求/响应/stream translate 映射。
2. 跑通 DS-01~DS-07 验收用例。
3. 用未修改 OpenAI SDK + Unio URL 做 DeepSeek model 黑盒测试。
4. 更新 [COMPATIBILITY_MATRIX.md](COMPATIBILITY_MATRIX.md) 当前列。

依赖：TASK-9.05 ~ TASK-9.09 完成后执行。

关联 GAP：

- [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002)
- [GAP-9-003](../../production/TODO_REGISTER.md#gap-9-003)


<a id="task-9-13-compatibility-matrix-doc"></a>
### TASK-9.13 Compatibility Matrix 文档

状态：partial

目标：

```text
对外文档明确 Supported / Passthrough / Rejected 字段与能力包（C1~C8）。
```

已完成：

1. 初版矩阵见 [COMPATIBILITY_MATRIX.md](COMPATIBILITY_MATRIX.md)。

计划实现：

1. 每个 TASK 完成后同步矩阵状态。
2. 与 DECISIONS ADR 保持一致。
3. Phase 9 done 前不允许再新增 silent drop 行为。

关联 GAP：

- [GAP-9-001](../../production/TODO_REGISTER.md#gap-9-001)


<a id="task-9-15-gateway-service-mapping"></a>
### TASK-9.15 Gateway service OpenAI 字段映射

状态：planned

目标：

```text
gateway service 在 gatewayapi DTO ↔ adapter contract 之间完整传递 OpenAI 语义，
不得只映射 role+content 或硬编码 finish_reason。
```

计划实现：

1. [internal/service/gateway/chat_completion.go](../../../internal/service/gateway/chat_completion.go) 非流式映射补齐 message、finish_reason、usage details。
2. [internal/service/gateway/chat_stream.go](../../../internal/service/gateway/chat_stream.go) 流式映射补齐 delta 全字段与 finish_reason。
3. 删除非流式 `finish_reason: "stop"` 硬编码，改由 adapter 输出驱动。

关联 GAP：

- [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002)


<a id="task-9-16-request-validation"></a>
### TASK-9.16 请求校验升级（Phase 4 → OpenAI parity）

状态：planned

目标：

```text
升级 chat_completions_handler 校验逻辑，与 OpenAI parity DTO 一致；
不再拒绝 tool/developer 等 OpenAI 合法 role，也不再强制 content 非空字符串。
```

计划实现：

1. 扩展 [internal/app/gatewayapi/chat_completions_handler.go](../../../internal/app/gatewayapi/chat_completions_handler.go) 校验规则。
2. multimodal content array、tool_calls、空 content assistant 等按 OpenAI 规则处理。
3. 不支持字段走 Reject 路径，而非 silent drop 或 Phase 4 text-only 拒绝。

关联 GAP：

- [GAP-9-001](../../production/TODO_REGISTER.md#gap-9-001)


<a id="task-9-17-authorization-token-estimate"></a>
### TASK-9.17 Authorization / token 估算输入

状态：planned

目标：

```text
chat authorization 与 token 估算不得把 messages 剥成 role+content 窄结构，
导致 upstream 所需字段在计费前丢失。
```

计划实现：

1. [internal/service/gateway/chat_authorization.go](../../../internal/service/gateway/chat_authorization.go) 使用 parity message 结构或等价 canonical 表示。
2. 估算逻辑对 tool_calls、reasoning_content 等字段有明确策略（计入或 documented 近似）。
3. 与 TASK-9.04 contract 扩展联动，保证 freeze/settlement 不受 DTO 扩展破坏。

关联 GAP：

- [GAP-9-002](../../production/TODO_REGISTER.md#gap-9-002)

## 推荐实施顺序

```text
1. TASK-9.01 ADR
2. TASK-9.02 禁止 silent drop
3. TASK-9.03 + 9.04 contract/DTO 扩展（C1~C4）
4. TASK-9.16 校验升级 + TASK-9.17 authorization 输入
5. TASK-9.05 请求翻译
6. TASK-9.06 非流式响应翻译 + TASK-9.15 gateway service 映射
7. TASK-9.07 流式响应翻译（吸收 normalizer/）
8. TASK-9.08 ~ 9.09 DeepSeek reasoning 回传 + stream usage 收口
9. TASK-9.10 ~ 9.11 tools / response_format
10. TASK-9.14 DeepSeek 上游全链路验收
11. TASK-9.12 SDK 黑盒 + 9.13 矩阵维护
```

## 进入本阶段前必须检查

```bash
rg -n "TODO|GAP-" AGENTS.md docs cmd internal migrations sql
rg -n "normalizer|Normalizer" docs internal
```

必须阅读：

```text
docs/chapters/phase-09-openai-protocol-parity/OPENAI_PROTOCOL.md
docs/chapters/phase-09-openai-protocol-parity/END_TO_END_PIPELINE.md
docs/chapters/phase-09-openai-protocol-parity/DEEPSEEK_UPSTREAM.md
docs/chapters/phase-09-openai-protocol-parity/COMPATIBILITY_MATRIX.md
docs/PROJECT_STATUS.md
docs/chapters/phase-04-openai-compatible-api/PLAN.md
docs/chapters/phase-05-adapter-boundary/PLAN.md
docs/production/TODO_REGISTER.md
docs/production/DECISIONS.md
```
