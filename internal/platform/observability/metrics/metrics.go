// Package metrics 提供 Unio 的 Prometheus 指标注册、记录和 /metrics 暴露能力。
//
// 标签基数原则（见 AGENTS.md「Observability」与阶段 8 计划）：
//   - 只使用 admin 可控、取值有界的业务维度作为 label：
//     method、route（chi 路由模板）、status、outcome、model、provider、channel、error_category、event、decision。
//   - 绝不把 project_id、API key、用户 prompt、完整 URL、request_id 这类高基数或敏感值放进 label。
//   - 按 project 聚合属于账务/审计维度，由 request_records / usage_records 等业务表回答，不进 Prometheus。
package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// ChatOutcome 表示一次 gateway chat 请求的最终结果分类。
type ChatOutcome string

const (
	// ChatOutcomeSuccess 表示请求成功并完成结算。
	ChatOutcomeSuccess ChatOutcome = "success"

	// ChatOutcomeFailed 表示请求因路由、上游或结算失败而终止。
	ChatOutcomeFailed ChatOutcome = "failed"

	// ChatOutcomeCanceled 表示请求被客户端取消。
	ChatOutcomeCanceled ChatOutcome = "canceled"
)

// SettlementOutcome 表示一次结算调用的结果分类。
type SettlementOutcome string

const (
	// SettlementOutcomeSuccess 表示结算成功收口。
	SettlementOutcomeSuccess SettlementOutcome = "success"

	// SettlementOutcomeFailed 表示结算失败且未进入 recovery。
	SettlementOutcomeFailed SettlementOutcome = "failed"

	// SettlementOutcomeRecoveryScheduled 表示结算失败但已由 recovery job 接管。
	SettlementOutcomeRecoveryScheduled SettlementOutcome = "recovery_scheduled"
)

// StreamEvent 表示流式请求生命周期中的可观测事件。
type StreamEvent string

const (
	// StreamEventStarted 表示已向客户端写出至少一个 SSE chunk。
	StreamEventStarted StreamEvent = "started"

	// StreamEventCompleted 表示流式请求正常结束并拿到 final usage。
	StreamEventCompleted StreamEvent = "completed"

	// StreamEventCanceled 表示流式请求被客户端取消。
	StreamEventCanceled StreamEvent = "canceled"

	// StreamEventMissingUsage 表示流正常结束但缺少 final usage。
	StreamEventMissingUsage StreamEvent = "missing_usage"
)

// RateLimitDecision 表示一次限流判定结果分类。
type RateLimitDecision string

const (
	// RateLimitDecisionAllowed 表示请求被放行。
	RateLimitDecisionAllowed RateLimitDecision = "allowed"

	// RateLimitDecisionLimited 表示请求被限流拒绝。
	RateLimitDecisionLimited RateLimitDecision = "limited"

	// RateLimitDecisionFailOpen 表示 Redis 故障且按 fail-open 放行。
	RateLimitDecisionFailOpen RateLimitDecision = "redis_failure_fail_open"

	// RateLimitDecisionFailClosed 表示 Redis 故障且按 fail-closed 拒绝。
	RateLimitDecisionFailClosed RateLimitDecision = "redis_failure_fail_closed"
)

const (
	// upstreamErrorCategoryNone 是上游调用成功时 error_category label 的占位值，避免 label 缺失。
	upstreamErrorCategoryNone = "none"
)

// apiLatencyBuckets 覆盖普通 HTTP API 的延迟分布。
var apiLatencyBuckets = []float64{0.005, 0.01, 0.025, 0.05, 0.1, 0.25, 0.5, 1, 2.5, 5, 10}

// upstreamLatencyBuckets 覆盖上游模型调用的延迟分布，模型补全可能持续较久。
var upstreamLatencyBuckets = []float64{0.1, 0.25, 0.5, 1, 2, 5, 10, 20, 30, 60, 120, 300}

// Metrics 持有 Unio 所有 Prometheus 指标和专用 registry。
//
// 使用专用 registry 而非全局 default，保证测试隔离，并精确控制 /metrics 暴露内容。
type Metrics struct {
	registry *prometheus.Registry

	httpRequestsTotal   *prometheus.CounterVec
	httpRequestDuration *prometheus.HistogramVec

	chatRequestsTotal *prometheus.CounterVec
	routingSelected   *prometheus.CounterVec

	upstreamRequestsTotal *prometheus.CounterVec
	upstreamDuration      *prometheus.HistogramVec

	settlementTotal  *prometheus.CounterVec
	streamEventTotal *prometheus.CounterVec

	rateLimitDecisions *prometheus.CounterVec

	capabilityCheckTotal    *prometheus.CounterVec
	capabilityRequiredTotal *prometheus.CounterVec
	capabilityMissingTotal  *prometheus.CounterVec
}

// New 创建并注册 Unio 全部指标。
//
// 它在专用 registry 上同时注册 Go runtime 和 process 采集器，
// 因此 /metrics 既包含业务指标，也包含进程基础指标。
func New() *Metrics {
	registry := prometheus.NewRegistry()

	m := &Metrics{
		registry: registry,

		httpRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_http_requests_total",
			Help: "HTTP 请求总数，按方法、路由模板和状态码聚合。",
		}, []string{"method", "route", "status"}),

		httpRequestDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "unio_http_request_duration_seconds",
			Help:    "HTTP 请求处理耗时（秒），按方法和路由模板聚合。",
			Buckets: apiLatencyBuckets,
		}, []string{"method", "route"}),

		chatRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_chat_requests_total",
			Help: "Gateway chat 请求总数，按是否流式和最终结果聚合。",
		}, []string{"stream", "outcome"}),

		routingSelected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_routing_selected_total",
			Help: "Gateway 实际选中的 provider/channel/model 调用次数。",
		}, []string{"provider", "channel", "model"}),

		upstreamRequestsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_upstream_requests_total",
			Help: "上游 adapter 调用总数，按 provider/channel、结果和错误分类聚合。",
		}, []string{"provider", "channel", "outcome", "error_category"}),

		upstreamDuration: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "unio_gateway_upstream_duration_seconds",
			Help:    "上游 adapter 调用耗时（秒），按 provider/channel 聚合。",
			Buckets: upstreamLatencyBuckets,
		}, []string{"provider", "channel"}),

		settlementTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_settlement_total",
			Help: "Gateway 结算调用总数，按结果聚合。",
		}, []string{"outcome"}),

		streamEventTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_stream_events_total",
			Help: "Gateway 流式请求生命周期事件计数。",
		}, []string{"event"}),

		rateLimitDecisions: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_ratelimit_decisions_total",
			Help: "限流判定结果计数，包含放行、限流和 Redis 故障降级。",
		}, []string{"decision"}),

		capabilityCheckTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_capability_check_total",
			Help: "Gateway capability 闸门判定计数，按 ingress 协议与结果聚合（ok/model_unavailable/channel_unavailable/unprovisioned/no_required/error）。observe 期据此复核误拒风险。",
		}, []string{"protocol", "result"}),

		capabilityRequiredTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_capability_required_total",
			Help: "Gateway 请求推断出的所需能力计数，按 ingress 协议与 capability key 聚合，反映客户实际用到的能力分布。",
		}, []string{"protocol", "capability"}),

		capabilityMissingTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_capability_missing_total",
			Help: "Gateway capability 闸门判定为缺失的能力计数，按 ingress 协议、capability key 与缺失层级（model/channel）聚合，用于 enforce 前定位需补声明的能力。",
		}, []string{"protocol", "capability", "scope"}),
	}

	registry.MustRegister(
		collectors.NewGoCollector(),
		collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}),
		m.httpRequestsTotal,
		m.httpRequestDuration,
		m.chatRequestsTotal,
		m.routingSelected,
		m.upstreamRequestsTotal,
		m.upstreamDuration,
		m.settlementTotal,
		m.streamEventTotal,
		m.rateLimitDecisions,
		m.capabilityCheckTotal,
		m.capabilityRequiredTotal,
		m.capabilityMissingTotal,
	)

	return m
}

// Handler 返回暴露当前 registry 指标的 HTTP handler。
func (m *Metrics) Handler() http.Handler {
	return promhttp.HandlerFor(m.registry, promhttp.HandlerOpts{})
}

// Registry 暴露底层 registry，主要供测试断言指标输出。
func (m *Metrics) Registry() *prometheus.Registry {
	return m.registry
}

// ObserveHTTPRequest 记录一次 HTTP 请求的计数和耗时。
// route 必须是有界的路由模板（chi RoutePattern），不能是原始 URL。
func (m *Metrics) ObserveHTTPRequest(method string, route string, status int, duration time.Duration) {
	statusText := httpStatusLabel(status)
	m.httpRequestsTotal.WithLabelValues(method, route, statusText).Inc()
	m.httpRequestDuration.WithLabelValues(method, route).Observe(duration.Seconds())
}

// IncChatRequest 记录一次 gateway chat 请求的最终结果。
func (m *Metrics) IncChatRequest(stream bool, outcome ChatOutcome) {
	m.chatRequestsTotal.WithLabelValues(streamLabel(stream), string(outcome)).Inc()
}

// IncRoutingSelected 记录 gateway 实际选中的 provider/channel/model。
func (m *Metrics) IncRoutingSelected(provider string, channel string, model string) {
	m.routingSelected.WithLabelValues(provider, channel, model).Inc()
}

// ObserveUpstream 记录一次上游 adapter 调用的结果、错误分类和耗时。
// errorCategory 为空表示调用成功，会记录为 "none"。
func (m *Metrics) ObserveUpstream(provider string, channel string, success bool, errorCategory string, duration time.Duration) {
	outcome := "error"
	category := errorCategory
	if success {
		outcome = "success"
		category = upstreamErrorCategoryNone
	}
	if category == "" {
		category = upstreamErrorCategoryNone
	}

	m.upstreamRequestsTotal.WithLabelValues(provider, channel, outcome, category).Inc()
	m.upstreamDuration.WithLabelValues(provider, channel).Observe(duration.Seconds())
}

// IncSettlement 记录一次结算调用的结果。
func (m *Metrics) IncSettlement(outcome SettlementOutcome) {
	m.settlementTotal.WithLabelValues(string(outcome)).Inc()
}

// IncStreamEvent 记录一次流式请求生命周期事件。
func (m *Metrics) IncStreamEvent(event StreamEvent) {
	m.streamEventTotal.WithLabelValues(string(event)).Inc()
}

// IncRateLimitDecision 记录一次限流判定结果。
func (m *Metrics) IncRateLimitDecision(decision RateLimitDecision) {
	m.rateLimitDecisions.WithLabelValues(string(decision)).Inc()
}

// IncCapabilityCheck 记录一次 capability 闸门判定结果。
// protocol 为 ingress 协议族（openai/anthropic）；result 为有界稳定取值
// （ok/model_unavailable/channel_unavailable/unprovisioned/no_required/error 等闸门结论）。
func (m *Metrics) IncCapabilityCheck(protocol string, result string) {
	m.capabilityCheckTotal.WithLabelValues(protocol, result).Inc()
}

// IncCapabilityRequired 记录一次请求推断出的所需能力（每个 capability key 一次）。
// capability 为有界的注册能力 key；protocol 为 ingress 协议族。
func (m *Metrics) IncCapabilityRequired(protocol string, capability string) {
	m.capabilityRequiredTotal.WithLabelValues(protocol, capability).Inc()
}

// IncCapabilityMissing 记录一次闸门判定为缺失的能力。
// scope 为缺失层级（model/channel）；capability 为有界的注册能力 key；protocol 为 ingress 协议族。
func (m *Metrics) IncCapabilityMissing(protocol string, capability string, scope string) {
	m.capabilityMissingTotal.WithLabelValues(protocol, capability, scope).Inc()
}

// streamLabel 把是否流式转换成稳定 label 值。
func streamLabel(stream bool) string {
	if stream {
		return "true"
	}

	return "false"
}

// httpStatusLabel 把 HTTP 状态码转成 label，未写出状态码时回退为 "0"。
func httpStatusLabel(status int) string {
	if status <= 0 {
		return "0"
	}

	return strconv.Itoa(status)
}
