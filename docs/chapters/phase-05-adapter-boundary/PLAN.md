# Phase 5 Plan - Adapter 边界

历史说明：本文件保留 Phase 5 当时的阶段规划。Phase 10 后 adapter contract 已按协议族拆分，本文只维护必要的有效链接；当前架构说明以后续阶段状态和 [../../architecture/PROJECT_STRUCTURE.md](../../architecture/PROJECT_STRUCTURE.md) 为准。

## 目标

建立 Unio 内部 adapter contract，让 gateway 不依赖 provider-specific HTTP 细节。

adapter 是代码能力，不是 provider/channel 管理系统。它只负责：

1. 请求转换。
2. 上游 HTTP 调用。
3. 响应解析。
4. stream parser。
5. usage 映射。
6. provider-specific error/metadata 映射。

adapter 不负责：

1. 选择 provider/channel。
2. 读取正式 provider env。
3. 查询数据库。
4. 管理 credential。
5. 计费和 ledger。

## 涉及文件

| 文件 | 作用 |
| --- | --- |
| [internal/core/adapter/openai/contract.go](../../../internal/core/adapter/openai/contract.go) | OpenAI chat adapter contract。 |
| [internal/core/adapter/openai/registry.go](../../../internal/core/adapter/openai/registry.go) | OpenAI adapter registry。 |
| [internal/core/adapter/sse/reader.go](../../../internal/core/adapter/sse/reader.go) | 项目级 SSE event reader。 |
| [internal/core/adapter/openai/dto.go](../../../internal/core/adapter/openai/dto.go) | OpenAI 上游 wire DTO。 |
| [internal/core/adapter/openai/chat.go](../../../internal/core/adapter/openai/chat.go) | OpenAI adapter 实现。 |
| [internal/core/channel/runtime.go](../../../internal/core/channel/runtime.go) | gateway/routing 传给 adapter 的运行时 channel 参数。 |

## 任务

<a id="task-5-01-chat-parameter-contract"></a>
### TASK-5.01 Chat 参数 contract

状态：done

目标：

```text
避免用户传入的 OpenAI-compatible 参数在 HTTP 层被接收，却在 adapter 层被静默丢弃。
```

已完成：

1. 盘点 [internal/app/gatewayapi/openai/chatcompletions/dto.go](../../../internal/app/gatewayapi/openai/chatcompletions/dto.go) 中 chat request 当前已接收字段。
2. 将 `temperature`、`top_p`、`max_tokens`、`presence_penalty`、`frequency_penalty`、`stop`、`user` 加入 [internal/core/adapter/openai/contract.go](../../../internal/core/adapter/openai/contract.go)。
3. 将这些可透传字段映射到 [internal/core/adapter/openai/dto.go](../../../internal/core/adapter/openai/dto.go)。
4. 非流式和流式 OpenAI adapter 均会把可透传字段写入上游请求 body。
5. optional scalar 使用指针保留显式零值。

验证方式：

```bash
go test ./internal/core/adapter ./internal/core/adapter/openai ./internal/service/gateway ./internal/app/gatewayapi
```

完成标准：

1. 已支持参数不会丢。
2. optional scalar 使用指针保留显式零值。
3. Chat DTO 的 role/content/参数值深度校验继续由阶段 4 [TASK-4.03](../phase-04-openai-compatible-api/PLAN.md#task-4-03-chat-dto-validation) 收口。

关联 GAP：

- [GAP-5-001](../../production/TODO_REGISTER.md#gap-5-001)


<a id="task-5-02-openai-non-stream-adapter"></a>
### TASK-5.02 OpenAI 非流式 adapter

状态：done

目标：

```text
把内部 chat request 转换为 OpenAI-compatible 上游请求，并解析非流式响应。
```

已完成：

1. 构造 OpenAI request。
2. 使用 `channel.Runtime` 中的 base URL、credential、model mapping。
3. 发起 HTTP 请求。
4. 解析 OpenAI response。
5. 映射 `prompt_tokens`、`completion_tokens`、`total_tokens`。
6. 映射 `cached_tokens`。
7. 映射 `reasoning_tokens`。

关键约束：

1. adapter 不知道用户余额。
2. adapter 不写 request record。
3. adapter 不读取 database。

<a id="task-5-03-openai-stream-adapter"></a>
### TASK-5.03 OpenAI stream adapter

状态：done

目标：

```text
支持 OpenAI-compatible SSE stream，并能解析 final usage chunk。
```

已完成：

1. stream request 设置 `stream_options.include_usage=true`。
2. 解析普通 delta chunk。
3. 解析 usage-only final chunk。
4. 将 final usage 放入 `adapter.ChatStreamChunk.Usage`。
5. 评估成熟 SSE parser 后，选择自研项目级 `internal/core/adapter/sse` event reader，避免第三方错误类型污染 adapter/gateway 契约。
6. OpenAI stream adapter 已按 SSE event 边界解析上游响应，而不是逐行解析 `data:`。
7. SSE reader 支持多行 `data:` 聚合、comment 忽略、`event`/`id`/`retry` 字段、CRLF/LF/CR 行结束、line/event size 上限和稳定错误。
8. 流式测试已覆盖 final usage、多行 data、大 event、bad JSON、emit backpressure 和 `[DONE]`。

收口结果：

```text
GAP-5-002 已关闭；后续 tool_calls / multimodal 属于新的 HTTP DTO、adapter contract、billing 和 fallback 语义设计，不再由 Scanner parser 欠账阻塞。
```

关联 GAP：

- [GAP-5-002](../../production/TODO_REGISTER.md#gap-5-002)


<a id="task-5-04-adapter-error-metadata"></a>
### TASK-5.04 Adapter 错误与 metadata contract

状态：planned

目标：

```text
让 adapter 返回足够的 provider metadata，支撑阶段 8 的 error classification、fallback 和观测。
```

计划实现：

1. adapter error 结构化，区分 auth、rate limit、timeout、server error、bad request。
2. adapter response 暴露 upstream status code。
3. adapter response 暴露 upstream request id。
4. gateway 根据错误分类决定是否 retry/fallback。
5. 用户错误仍由 HTTP 层做安全映射，不透传上游原始 body。

关联 GAP：

- [GAP-8-001](../../production/TODO_REGISTER.md#gap-8-001)
