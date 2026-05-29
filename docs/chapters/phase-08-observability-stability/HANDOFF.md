# Phase 8 Handoff

更新时间：2026-05-29

## 当前进度

阶段 8 五节中已完成四节，只剩 TASK-8.05。

| 任务 | 状态 | 摘要 |
| --- | --- | --- |
| TASK-8.01 | done | adapter metadata + provider error 结构化分类 |
| TASK-8.02 | done | Prometheus metrics |
| TASK-8.03 | done | structured logs + OpenTelemetry |
| TASK-8.04 | done | retry/fallback + channel 熔断 |
| TASK-8.05 | planned | HTTP SSE Writer（下一节，关联 GAP-8-002） |

## 已完成内容（今天）

### 入场前先修了阶段 7 遗留的红

这些是集成测试（无 `DATABASE_URL` 时 skip，平时没暴露）与阶段 7 已落地 schema 的脱节，全部是测试侧修复：

```text
requestlog 状态机：succeeded 后不能转 failed（GAP-7-003），失败映射改到独立 running attempt。
ledger reservation：补 estimated_amount（GAP-7-014 拆分 estimated/authorized）。
prices 排他约束：测试创建了重叠 enabled 生效窗口（GAP-7-010），改为相邻/收口窗口。
cost price：+1ns 偏移低于 PostgreSQL timestamptz 微秒精度，改为 +time.Minute。
```

### TASK-8.01 adapter metadata 与 provider error classification

```text
internal/core/adapter/upstream_error.go
= UpstreamErrorCategory（auth/permission/rate_limit/bad_request/timeout/canceled/server_error/unknown）
= UpstreamMetadata{StatusCode, RequestID} + UpstreamError（cause 仍是 *failure.Failure，CodeOf/errors.Is 不变）
= UpstreamCategoryOf / UpstreamMetadataOf
internal/core/adapter/openai/errors.go = status/网络错误 → category，读 X-Request-Id
gateway ProviderErrorClassifier = 按 category 判定 retryable（rate_limit/timeout/server_error 可 fallback）
GAP-8-001 已关闭：ChatResponse/ChatStreamChunk 增加 Upstream，settlement 写真实 upstream status/request id，
  settlement_recovery_jobs 新增 upstream_status_code/upstream_request_id 列，重放写回一致。
```

### TASK-8.02 Prometheus metrics

```text
internal/platform/observability/metrics
= HTTP（计数/耗时/状态）、gateway 请求结果、routing 选中、上游调用+错误分类+耗时、
  结算结果、流式事件、限流判定 + Go runtime/process 采集器。
/metrics 挂 gateway 根路由（不经 /v1 鉴权）。
business 指标在 gateway service 层 + 中间件层采集，核心 adapter/routing/billing 包不感知 metrics。
project_id 等高基数维度不作为 label（按 request_records/usage_records 聚合）。
```

### TASK-8.03 structured logs + OpenTelemetry

```text
internal/platform/observability/logfields
= 按请求传播的可变 *Fields（context 内指针）；request_id 中间件安装（correlation_id=HTTP X-Request-ID），
  认证中间件填 user/project/api_key，gateway 填业务 request_id 和 model/provider/channel，HTTP Logger 输出统一字段。
= 脱敏：访问日志不记录请求体/prompt/API key/Authorization/credential（有测试覆盖）。
internal/platform/observability/tracing
= Setup 默认关闭（opt-in OTLP HTTP）；httpmw.Tracing 建 server span（ServeHTTP 后 SetName 补路由模板）；
  gateway 拆分 gateway.chat_completion(.stream) / gateway.routing / adapter.* / gateway.settlement span，同属一条 trace。
= provider 在 NewGatewayServerApp 安装，main 优雅关闭时 flush。
```

### TASK-8.04 retry + circuit breaker

```text
retry/fallback 在 8.01 已落地（ProviderErrorClassifier）。
internal/service/gateway/channel_breaker.go = 进程内 channel 熔断器（固定窗口错误率 + closed/open/half-open 状态机）。
gateway 拿到 route plan 后跳过 open channel（fallback 下一个同模型 channel），每次尝试后按 category 记录健康：
  timeout/server_error/rate_limit/auth/permission 计 channel 故障，bad_request/canceled 不计。
核心 routing 保持纯 DB 查询，熔断过滤在 service 层。默认启用，阈值可配置。
跨实例共享健康 + 后台手动恢复 → 阶段 9 admin。
```

## 重要边界与约定（继续开发请遵守）

```text
1. 可观测性依赖只停在 app/service/中间件边界；核心 adapter/routing/billing/ledger 包不感知 metrics/tracing。
2. metrics label 只用 admin 可控、取值有界的维度；不放 project_id/api key/prompt/raw URL/request_id。
3. 日志/trace 不记录 prompt、API key、credential、上游 Authorization。
4. metrics recorder、tracing、channel breaker 在 gateway 内均为可选（nil 安全）。
5. tracing 默认关闭；真实导出需 OTLP collector，单测用内存 span recorder（tracetest）验证。
```

## 下一步：TASK-8.05 HTTP SSE Writer

当前 stream 写出只靠 [httpx.WriteSSE](../../../internal/platform/httpx/response.go) 的 data-only helper（关联 [GAP-8-002](../../production/TODO_REGISTER.md#gap-8-002)）。

目标（详见 [PLAN.md](PLAN.md#task-8-05-http-sse-writer)）：

```text
抽出项目级 SSE Writer：支持 data / event / id / retry / comment heartbeat、多行 data 拆分、
写出前检查 request context、flush 失败返回稳定错误、写出后错误事件，覆盖客户端取消。
注意：项目级 SSE Reader 已在 internal/core/adapter/sse，Writer 优先只在 HTTP 层使用，不污染 gateway/adapter contract。
```

建议阅读顺序：

1. [internal/platform/httpx/response.go](../../../internal/platform/httpx/response.go)（当前 WriteSSE）
2. [internal/core/adapter/sse/reader.go](../../../internal/core/adapter/sse/reader.go)（已有 Reader 风格参考）
3. [internal/app/gatewayapi/chat_completions_handler.go](../../../internal/app/gatewayapi/chat_completions_handler.go)（SSE 写出调用方）

## 验证命令

```bash
DATABASE_URL=postgres://unio:***@localhost:5432/unio?sslmode=disable go test ./...
go vet ./...
git diff --check
```

最近一次验证：2026-05-29，24 包全绿。
