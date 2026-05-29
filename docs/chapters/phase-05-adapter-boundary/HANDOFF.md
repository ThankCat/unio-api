# Phase 5 Handoff - Adapter Boundary

更新时间：2026-05-20

## 当前状态

阶段 5 的 OpenAI stream parser 欠账已经收口。

已完成：

1. `adapter.ChatRequest` 已承载当前 HTTP DTO 接收的可透传参数。
2. OpenAI 非流式 adapter 已完成请求映射、响应解析和 usage 映射。
3. OpenAI 流式 adapter 已支持 `stream_options.include_usage=true`。
4. OpenAI stream final usage 已能进入 `adapter.ChatStreamChunk.Usage`。
5. `GAP-5-002` 已关闭：OpenAI stream parser 已从逐行 `bufio.Scanner` 替换为项目级 SSE event reader。

## 本次新增学习点

下一次学习时，优先阅读：

```text
internal/core/adapter/sse
```

建议阅读顺序：

1. [reader.go](../../../internal/core/adapter/sse/reader.go)
2. [reader_test.go](../../../internal/core/adapter/sse/reader_test.go)
3. [openai/chat.go](../../../internal/core/adapter/openai/chat.go)
4. [openai/chat_test.go](../../../internal/core/adapter/openai/chat_test.go)

## 学习目标

这节课的核心不是“怎么写一个能跑的 parser”，而是理解为什么商业 API 网关需要一个可控的项目级 SSE 基础模块。

重点掌握：

1. SSE event 的边界是空行，不是一行 `data:`。
2. 一个 event 可以有多行 `data:`，读取后要用 `\n` 合并。
3. `event`、`id`、`retry` 是协议字段，OpenAI adapter 当前主要消费 `data`。
4. `internal/core/adapter/sse` 返回稳定错误，避免第三方库错误类型污染 adapter/gateway 契约。
5. OpenAI adapter 只做 provider-specific JSON decode 和 DTO 映射，不负责通用 SSE 协议解析。
6. 客户可见错误仍由 HTTP 层统一映射，不能透传 parser 内部错误。

## 已覆盖测试

SSE reader 覆盖：

1. 多行 `data:` 聚合。
2. CRLF、LF、CR 行结束。
3. UTF-8 BOM。
4. comment 行。
5. `event`、`id`、`retry` 字段。
6. 无 data event 跳过。
7. 单行超限和 event data 超限。
8. event 未完整结束时的 malformed stream。

OpenAI stream adapter 覆盖：

1. 普通 delta chunk。
2. final usage chunk。
3. `[DONE]`。
4. 多行 SSE event。
5. 超过旧 Scanner 1MB 上限的大 event。
6. raw OpenAI 风格 SSE fixture。
7. bad JSON。
8. emit backpressure。

## 验证命令

```bash
env GOCACHE=/private/tmp/unio-api-go-build-cache go test ./...
```

最近一次验证：2026-05-20，通过。

## 下一步

如果继续逐章复盘，进入阶段 6：

```text
provider / channel / model / routing / credential / project visibility
```

如果回到实现主线，进入阶段 7：

```text
7.17 余额预检查与预授权最小闭环
```
