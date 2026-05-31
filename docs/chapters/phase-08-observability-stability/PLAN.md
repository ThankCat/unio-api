# Phase 8 Plan - 可观测性与稳定性

## 目标

让平台具备生产排障、稳定性治理和 provider/channel 健康管理能力。

阶段 8 不是简单加日志，而是要让系统回答这些生产问题：

1. 哪个 project、model、provider、channel 的错误率最高？
2. 是用户请求错误、平台配置错误、上游认证错误、上游限流，还是上游 5xx？
3. 哪些 channel 应该被降权、熔断或临时禁用？
4. stream 中断发生在写出前还是写出后？
5. request、attempt、usage、ledger 能否通过 correlation id 串起来？

进入阶段 8 前，阶段 7 的 P0 计费阻断必须先关闭。

## 涉及文件

| 文件 | 作用 |
| --- | --- |
| [internal/service/gateway](../../../internal/service/gateway) | request 编排、attempt 状态、fallback 决策。 |
| [internal/core/adapter](../../../internal/core/adapter) | provider error 和 upstream metadata 的来源。 |
| [internal/platform/httpmw/logger.go](../../../internal/platform/httpmw/logger.go) | HTTP structured log。 |
| [internal/platform/httpmw/request_id.go](../../../internal/platform/httpmw/request_id.go) | correlation id。 |
| [internal/core/routing/router.go](../../../internal/core/routing/router.go) | channel 选择和后续 health 策略入口。 |
| [docs/production/THIRD_PARTY_POLICY.md](../../production/THIRD_PARTY_POLICY.md) | Prometheus/OpenTelemetry 等依赖选择规则。 |

## 任务

<a id="task-8-01-adapter-metadata-provider-errors"></a>
### TASK-8.01 Adapter metadata 与 provider error classification

状态：done

目标：

```text
让 adapter 不只返回 error string，而是返回 gateway 可以理解和决策的结构化错误。
```

计划实现：

1. adapter response metadata 增加 upstream status code。
2. adapter response metadata 增加 upstream request id。
3. adapter error 增加 category。
4. category 至少区分 auth、permission、rate_limit、timeout、canceled、bad_request、server_error、unknown。
5. gateway 根据 category 决定是否 retry/fallback。
6. HTTP 层继续返回安全错误，不直接透传上游 body。

关键约束：

1. 上游 `401/403` 多数是平台 channel credential 问题，不应该让用户误以为自己的 API key 错了。
2. stream 已经写出后不能跨 channel fallback。
3. provider 原始错误只进入内部日志和 request record 内部字段。

关联 GAP：

- [GAP-8-001](../../production/TODO_REGISTER.md#gap-8-001)


<a id="task-8-02-metrics"></a>
### TASK-8.02 Prometheus metrics

状态：done

目标：

```text
提供可聚合、低基数、可告警的业务和基础设施 metrics。
```

计划实现：

1. 引入 Prometheus 官方 client。
2. HTTP request count、latency、status。
3. Gateway request count、success/failure/canceled。
4. Adapter upstream latency、error category。
5. Routing selected provider/channel/model count。
6. Billing settlement success/failure count。
7. Stream started/completed/canceled/missing_usage count。
8. Rate limit decision、limited、Redis failure 和 fail-open count。

label 约束：

1. 可以使用 model、provider、channel、status、error_category 等 admin 可控、取值有界的维度。
2. 不要使用 raw request_id、api key、user input、完整 URL 作为 label。
3. API key 只能用 prefix 或 internal id，且默认不放高基数 label。

已实现（`internal/platform/observability/metrics`）：

```text
unio_http_requests_total{method,route,status}            HTTP 请求计数（route=chi 路由模板）
unio_http_request_duration_seconds{method,route}         HTTP 处理耗时直方图
unio_gateway_chat_requests_total{stream,outcome}         请求结果：success/failed/canceled
unio_gateway_routing_selected_total{provider,channel,model}  实际选中渠道
unio_gateway_upstream_requests_total{provider,channel,outcome,error_category}  上游调用结果与错误分类
unio_gateway_upstream_duration_seconds{provider,channel} 上游调用耗时直方图
unio_gateway_settlement_total{outcome}                   结算：success/failed/recovery_scheduled
unio_gateway_stream_events_total{event}                  流式：started/completed/canceled/missing_usage
unio_ratelimit_decisions_total{decision}                 限流：allowed/limited/redis_failure_fail_open|fail_closed
```

基数决策：

```text
project_id 是用户驱动的高基数维度，按 AGENTS「不污染 metrics label」规则不作为 Prometheus label。
按 project / api key 的聚合属于账务与审计维度，由 request_records / usage_records 等业务表回答。
provider/channel 使用数据库 ID（admin 可控、取值有界）作为 label 值。
business 指标在 gateway service 层与中间件层采集，核心 adapter/routing/billing 包不感知 metrics，保持核心边界干净。
```

`/metrics` 在 gateway 根路由挂载（不经过 `/v1` 的 API key 鉴权），并注册 Go runtime / process 基础采集器。

验证方式：

```bash
go test ./internal/platform/observability/... ./internal/platform/httpmw/... ./internal/service/gateway/...
curl /metrics
```

<a id="task-8-03-logs-traces"></a>
### TASK-8.03 Structured logs 与 OpenTelemetry

状态：done

目标：

```text
让一次请求能从 HTTP、gateway、routing、adapter、billing 串起来。
```

计划实现：

1. 统一 log fields：correlation_id、request_id、user_id、project_id、api_key_id、model、provider、channel。
2. 敏感字段脱敏：API key、credential、上游 Authorization header、用户 prompt 默认不进日志。
3. 阶段 8 后期接入 OpenTelemetry trace。
4. trace span 按 HTTP、gateway、routing、adapter、settlement 拆分。
5. request record 的业务 request_id 与 HTTP correlation id 都要可查。

常见坑：

1. 不要把用户 prompt 全量打进日志。
2. 不要把 credential_ref 解析后的明文 credential 打进日志。
3. 不要让高基数字段污染 metrics label。

已实现（structured logs）：

```text
internal/platform/observability/logfields
= 按请求传播的可变 *Fields（context 内指针）。
= request_id 中间件在最外层安装，correlation_id = HTTP X-Request-ID。
= 认证中间件填充 user_id/project_id/api_key_id。
= gateway 填充业务 request_id 和命中的 model/provider/channel。
= HTTP Logger 在请求结束时输出全量字段，下游写入对外层可见（共享指针）。
```

统一日志字段：

```text
correlation_id  HTTP 请求 ID（X-Request-ID，可被客户端透传，受字符/长度校验）
request_id      业务 request_records.request_id（服务端生成）
user_id / project_id / api_key_id   认证身份
model / provider / channel          实际命中的路由（provider/channel 为数据库 ID）
method / path / status / duration_ms 基础访问字段
```

脱敏规则（已通过测试覆盖）：

```text
访问日志只记录方法、路径、状态码、耗时和上述稳定标识字段。
绝不记录：请求体 / 用户 prompt、API key 明文、Authorization header、credential。
错误日志统一用 failure.LogArgs，只输出 error/code/category 和少量安全 field。
request_records.error_message 只存安全展示文案；内部细节进 internal_error_detail。
```

已实现（OpenTelemetry trace）：

```text
internal/platform/observability/tracing
= Setup(opts) 默认关闭（opt-in）；未启用或缺 endpoint 时不安装全局 provider，
  otel 返回 no-op tracer，全链路 Start/End 为零成本空操作。
= 启用时用 OTLP HTTP exporter + W3C TraceContext/Baggage 传播器 + ParentBased(TraceIDRatio) 采样。
httpmw.Tracing       入站 server span（提取 traceparent，按路由模板命名，记录状态码）。
gateway span 拆分    gateway.chat_completion / gateway.chat_stream（父）
                     → gateway.routing、adapter.chat_completions(.stream)、gateway.settlement（子）。
provider 生命周期    NewGatewayServerApp 安装，main 优雅关闭时 flush exporter。
```

tracing 配置（默认关闭）：

```text
OTEL_TRACING_ENABLED            默认 false
OTEL_EXPORTER_OTLP_ENDPOINT     OTLP HTTP collector 地址，空则视为关闭
OTEL_EXPORTER_OTLP_INSECURE     默认 true（本地/内网明文）
OTEL_SERVICE_NAME               默认 unio-gateway
OTEL_TRACES_SAMPLER_RATIO       默认 1.0
```

脱敏：span 只记录 method/route/status 和 provider/channel/model 等非敏感属性，不记录 prompt/凭据。
验证：用内存 span recorder（tracetest）断言 server span 命名与属性、gateway span 层级同属一条 trace；真实导出需 OTLP collector。

<a id="task-8-04-retry-circuit-breaker"></a>
### TASK-8.04 Retry 与 circuit breaker

状态：done（后台查看/手动恢复 channel health 属阶段 10 admin）

目标：

```text
让 fallback 和 channel health 基于明确错误分类，而不是靠字符串猜测。
```

计划实现：

1. 先完成 provider error classification。
2. 定义 retryable error。
3. 非流式请求在未写出响应前可以尝试同模型 channel fallback。
4. stream 在写出后不能 fallback。
5. channel health 根据错误率和时间窗口降权或熔断。
6. routing 查询排除熔断 channel。
7. 后台可以查看和手动恢复 channel health。

完成标准：

1. 401/403 不盲目重试。
2. timeout/5xx 可以按策略 retry/fallback。
3. 429 可以按策略降权。
4. 所有 retry/fallback 都写 attempt record。

已实现：

```text
retry/fallback（8.01 落地）：ProviderErrorClassifier 按上游 category 判定 retryable，
  rate_limit/timeout/server_error 允许同模型 channel fallback；auth/permission/bad_request/canceled 不重试；
  非流式未写出前可 fallback，stream 写出后不 fallback；每次尝试写 attempt record。
channel 熔断（本任务）：internal/service/gateway/channel_breaker.go 进程内熔断器，
  按固定时间窗统计错误率，达到 MinRequests 且 failures/total >= FailureRatio 时熔断（open），
  冷却 OpenDuration 后放行一次 half-open 探测，成功恢复闭合、失败重新熔断。
gateway 集成：拿到 route plan 后跳过 open channel（fallback 到下一个同模型 channel），
  每次尝试后按 category 记录健康；只把 timeout/server_error/rate_limit/auth/permission 计为 channel 故障，
  bad_request/canceled 不惩罚渠道。
```

设计与边界：

```text
熔断状态进程内维护（每实例自我保护），不依赖共享存储；核心 routing 仍是纯 DB 查询，
  熔断过滤放在 gateway service 层（"routing 查询排除熔断 channel" 在编排层落地）。
配置（默认启用，保守阈值）：
  CIRCUIT_BREAKER_ENABLED         默认 true
  CIRCUIT_BREAKER_WINDOW          默认 30s
  CIRCUIT_BREAKER_MIN_REQUESTS    默认 20
  CIRCUIT_BREAKER_FAILURE_RATIO   默认 0.5
  CIRCUIT_BREAKER_OPEN_DURATION   默认 30s
跨实例共享 channel health、后台查看与手动恢复属于阶段 10 admin，不在本任务范围。
```

<a id="task-8-05-http-sse-writer"></a>
### TASK-8.05 HTTP SSE Writer

状态：done

目标：

```text
让 Unio 同时具备项目级 SSE Reader 和 HTTP SSE Writer，而不是只靠 data-only helper 写出 OpenAI-compatible stream。
```

已实现：

```text
internal/platform/httpx/sse_writer.go
= SSEWriter：per-request 有状态写出器，封装支持 flush 的 ResponseWriter。
= SSEEvent{Type,Data,ID,RetryMilliseconds}：形状与 sse.Event 对称但独立，避免 platform 反向依赖 core。
= NewSSEWriter：构造时一次性做 flusher 检查（不支持返回 CodeHTTPStreamingUnsupported），此时不写 header。
= WriteEvent / WriteData / WriteComment：写出前 guard（sticky error + ctx 检查）→ 首个 event 才 ensureStarted 安装
  SSE header → 按字段行写出 → 空行结束 → flush。
= 多行 data 按 SSE 规则拆成多行 data:（与 Reader 的 join 对称）；客户端断开返回稳定 CodeHTTPClientDisconnected；
  写出失败记 sticky error，后续写出短路；Started()/Err() 暴露状态。
internal/app/gatewayapi/chat_completions_handler.go
= stream 分支改用 SSEWriter：Started() 取代手工 streamStarted，flusher 检查收敛进构造函数，
  错误 chunk 经同一个 writer 写出（复用 context 检查与 sticky 短路）。
旧的 data-only httpx.WriteSSE 及其测试已删除；ErrStreamingUnsupported/ContentTypeSSE 保留供 Writer 使用。
```

边界与约束：

```text
1. Writer 仅在 HTTP 层使用，不进入 gateway/adapter contract（adapter 侧仍是 sse.Reader）。
2. stream 生命周期 metrics（started/completed/canceled/missing_usage）仍由 gateway service 层记录，Writer 不感知 metrics，避免重复与分层污染。
3. header 延迟到首个 event 写出，保留首 chunk 前可退回普通 JSON error 的能力。
4. 自动 heartbeat ticker 暂不实现（并发写同一连接需加锁）；WriteComment 提供手动保活能力。
```

测试覆盖（`internal/platform/httpx/sse_writer_test.go`）：

```text
data-only、event+data（含 id/retry）、多行 data 拆分、comment heartbeat、[DONE] 哨兵、
非 flusher 构造失败、ctx 已取消短路、写出失败 sticky 短路。
```

关联 GAP：

- [GAP-8-002](../../production/TODO_REGISTER.md#gap-8-002)（已关闭）
