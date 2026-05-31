# Phase 4 Plan - OpenAI-compatible API

## 目标

对外提供 OpenAI-compatible HTTP API 入口，让用户可以用接近 OpenAI SDK 的方式调用 Unio API。

本阶段必须守住三条边界：

1. HTTP 层只负责协议入口、DTO decode/validation、错误格式和 SSE 写出。
2. gateway 负责业务编排，HTTP handler 不选择 provider/channel，不计费。
3. 对外 DTO、内部 adapter DTO、OpenAI 上游 wire DTO 必须分开。

## 涉及文件

| 文件 | 作用 |
| --- | --- |
| [internal/app/gatewayapi/openai_dto.go](../../../internal/app/gatewayapi/openai_dto.go) | 对外 OpenAI-compatible DTO。 |
| [internal/app/gatewayapi/chat_completions_handler.go](../../../internal/app/gatewayapi/chat_completions_handler.go) | `/v1/chat/completions` HTTP handler。 |
| [internal/app/gatewayapi/models_handler.go](../../../internal/app/gatewayapi/models_handler.go) | `/v1/models` HTTP handler。 |
| [internal/app/gatewayapi/router.go](../../../internal/app/gatewayapi/router.go) | HTTP route 装配。 |
| [internal/platform/httpx/json.go](../../../internal/platform/httpx/json.go) | JSON decode。 |
| [internal/platform/httpx/response.go](../../../internal/platform/httpx/response.go) | JSON/error response。 |
| [internal/service/gateway](../../../internal/service/gateway) | chat completions 的业务编排。 |

## 任务

<a id="task-4-01-models-endpoint"></a>
### TASK-4.01 `/v1/models`

状态：partial

目标：

```text
让客户端能获取当前项目可见且可路由的模型列表。
```

已完成：

1. `GET /v1/models` endpoint 已存在。
2. response 使用 OpenAI-compatible list structure。
3. 数据来源已从空列表推进到 model catalog。

当前欠账：

```text
当前模型列表还是全局可用视角，未体现 project 级模型可见性、预算、禁用或专属 channel 策略。
```

计划实现：

1. handler 从 auth context 获取 project_id。
2. model catalog 查询接收 project_id。
3. catalog 与 routing 共用 project policy。
4. 保证“可见模型”和“可路由模型”一致。

关联 GAP：

- [GAP-6-006](../../production/TODO_REGISTER.md#gap-6-006)


<a id="task-4-02-chat-endpoint"></a>
### TASK-4.02 `/v1/chat/completions`

状态：done

目标：

```text
提供非流式和流式 chat completions HTTP 入口。
```

已完成：

1. decode OpenAI-compatible request。
2. 根据 `stream` 分支调用 gateway 非流式或流式方法。
3. 非流式返回 JSON。
4. 流式返回 SSE。
5. handler 不直接调用 adapter。

关键约束：

1. handler 创建的是 `httpapi.ChatCompletionRequest`，不是 `adapter.ChatRequest`。
2. handler 不知道 provider/channel。
3. handler 不做 billing settlement。

<a id="task-4-03-chat-dto-validation"></a>
### TASK-4.03 Chat DTO 深度校验

状态：done

目标：

```text
公开 API 前，明确哪些 OpenAI-compatible 参数被支持、哪些被拒绝、哪些会透传。
```

已完成：

1. `model` 必须非空且不能只有空白字符。
2. `messages` 必须非空。
3. message role 只支持当前 text-only MVP 的 `system`、`user`、`assistant`。
4. message content 必须非空且不能只有空白字符。
5. `temperature`、`top_p`、`max_tokens`、`presence_penalty`、`frequency_penalty` 已校验取值边界。
6. `stop` 最多 4 个，且每个 stop sequence 不能为空。
7. `user` 如果传入，不能为空且长度最多 512。

当前边界：

1. 暂不支持 tool/function/developer role。
2. 暂不支持 multimodal content。
3. 已透传字段继续保持与 [TASK-5.01](../phase-05-adapter-boundary/PLAN.md#task-5-01-chat-parameter-contract) 的 adapter contract 一致。

完整 OpenAI parity（请求不 silent drop、响应 reasoning/tools/usage details、stream translate 收口）由 [Phase 9](../phase-09-openai-protocol-parity/PLAN.md) 负责；Phase 4 text-only MVP 在 Phase 9 done 后视为被 parity 层取代。

验证方式：

```bash
go test ./internal/app/gatewayapi
```

关联 GAP：

- [GAP-4-001](../../production/TODO_REGISTER.md#gap-4-001)


<a id="task-4-04-strict-json-error"></a>
### TASK-4.04 严格 JSON decode 与错误格式

状态：done

目标：

```text
让公网 API 对 malformed request body 的响应稳定、可预期、OpenAI-compatible。
```

已完成：

1. 校验 `Content-Type`。
2. 拒绝尾随 JSON token。
3. 区分 empty body、malformed JSON、body too large。
4. 将 decode 错误映射成安全的 OpenAI-compatible error。
5. 不把 Go 原始 decode 错误直接暴露给用户。

验证方式：

```bash
go test ./internal/platform/httpx ./internal/app/gatewayapi
```

涉及文件：

1. [internal/platform/httpx/json.go](../../../internal/platform/httpx/json.go)
2. [internal/platform/httpx/response.go](../../../internal/platform/httpx/response.go)
3. [internal/app/gatewayapi/chat_completions_handler.go](../../../internal/app/gatewayapi/chat_completions_handler.go)

关联 GAP：

- [GAP-4-002](../../production/TODO_REGISTER.md#gap-4-002)


<a id="task-4-05-sse-write-error"></a>
### TASK-4.05 SSE 写出后的错误语义

状态：done

目标：

```text
明确 stream response 一旦写出后，错误不能再用普通 JSON error 表达。
```

已完成：

1. stream handler 已使用 SSE。
2. gateway 已记录 request/attempt 状态。
3. 有 final usage 时能作为账务事实 settlement。
4. 写出后错误会尽力返回 OpenAI-compatible data-only SSE error chunk。
5. 写出后错误不会返回普通 JSON error，也不会写出 `[DONE]`。

计划实现：

1. 写出前错误继续返回 OpenAI-compatible JSON error。
2. 写出后错误返回 data-only SSE error chunk，并保留 request/attempt 状态事实。
3. 不做跨 channel fallback，因为已有 bytes 写给客户端。

关联 GAP：

- [GAP-7-006](../../production/TODO_REGISTER.md#gap-7-006) 已关闭
