# Phase 8 Status

状态：planned

## 尚未开始

| 任务 | 状态 | 说明 |
| --- | --- | --- |
| TASK-8.01 | done | 错误分类半：adapter 返回结构化 `UpstreamError`（category + metadata），OpenAI adapter 按 status/网络错误分类，gateway `ProviderErrorClassifier` 已按 category 判定 retryable 并接入 fallback 主链路。metadata 持久化半（[GAP-8-001](../../production/TODO_REGISTER.md#gap-8-001)）：`ChatResponse`/`ChatStreamChunk` 增加 `Upstream`，settlement 写入真实 upstream status/request id，幂等断言与 `settlement_recovery_jobs`（新增 `upstream_status_code`/`upstream_request_id` 列）同步。2026-05-29 已在干净 `migrate up` 本地库通过集成测试。retry/fallback 的 channel health 与熔断仍在 TASK-8.04。 |
| TASK-8.02 | done | Prometheus 指标已落地于 `internal/platform/observability/metrics`：HTTP（计数/耗时/状态）、gateway 请求结果、routing 选中、上游调用结果+错误分类+耗时、结算结果、流式生命周期事件、限流判定。`/metrics` 在 gateway 根路由暴露并含 Go runtime/process 采集器。business 指标在 gateway service 层与中间件层采集，核心包不感知 metrics；project_id 等高基数维度不作为 label（按 request_records/usage_records 聚合）。已通过 metrics 包、httpmw 中间件和 gateway 传播单测。 |
| TASK-8.03 | done | structured logs：`logfields` 按请求传播 `*Fields`（correlation_id=HTTP X-Request-ID + 业务 request_id + user/project/api_key + model/provider/channel），HTTP Logger 输出统一字段，脱敏规则（不记录 prompt/API key/Authorization/credential）有测试覆盖。OpenTelemetry：`tracing.Setup` 默认关闭（opt-in OTLP HTTP），`httpmw.Tracing` 建 server span，gateway 拆分 gateway/routing/adapter/settlement span 并同属一条 trace；provider 在 app 装配并在 main 优雅关闭时 flush。用内存 span recorder 验证；真实导出需 OTLP collector。 |
| TASK-8.04 | done | retry/fallback（8.01）：`ProviderErrorClassifier` 按上游 category 判定 retryable，非流式未写出前可同模型 fallback，stream 写出后不 fallback。channel 熔断（本任务）：`channel_breaker.go` 进程内熔断器（固定窗口错误率 + open/half-open/closed 状态机），gateway 拿到 plan 后跳过 open channel 并按 category 记录健康（timeout/server_error/rate_limit/auth/permission 计故障，bad_request/canceled 不计）。默认启用，阈值可配置。核心 routing 保持纯查询，熔断过滤在 service 层。跨实例共享健康与后台手动恢复属阶段 9 admin。 |
| TASK-8.05 | planned | HTTP SSE Writer 尚未实现；当前 [httpx.WriteSSE](../../../internal/platform/httpx/response.go) 只覆盖 OpenAI-compatible data-only 写出，关联 [GAP-8-002](../../production/TODO_REGISTER.md#gap-8-002)。 |

## 进入阶段 8 前置条件

1. 阶段 7 P0 blocker 关闭。
2. request/attempt 状态和 settlement 幂等稳定。
3. stream 计费策略完成。
