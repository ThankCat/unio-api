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

	// StreamEventInterrupted 表示流式请求已输出后被上游/链路错误中断。
	StreamEventInterrupted StreamEvent = "interrupted"

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

	partialSettlementTotal *prometheus.CounterVec
	retryableFallbackTotal *prometheus.CounterVec
	zeroPriceServedTotal   *prometheus.CounterVec

	rateLimitDecisions *prometheus.CounterVec

	capabilityCheckTotal    *prometheus.CounterVec
	capabilityRequiredTotal *prometheus.CounterVec
	capabilityMissingTotal  *prometheus.CounterVec

	stickyEventsTotal *prometheus.CounterVec

	routingSkipTotal       *prometheus.CounterVec
	routingHeadWaitSeconds prometheus.Histogram

	routingBalanceTotal          *prometheus.CounterVec
	routingBalanceCandidateCount *prometheus.HistogramVec
	routingBalancePoolSize       *prometheus.HistogramVec
	routingBalanceSelected       *prometheus.CounterVec
	routingBalanceFallback       *prometheus.CounterVec
	routingBalanceLoadSkew       prometheus.Histogram
	routingCapacityRead          *prometheus.CounterVec
	routingMarginGuard           *prometheus.CounterVec
	routingTraceWrite            *prometheus.CounterVec

	breakerState                          *prometheus.GaugeVec
	breakerTransitionTotal                *prometheus.CounterVec
	breakerSkipTotal                      *prometheus.CounterVec
	breakerStoreOperationTotal            *prometheus.CounterVec
	breakerStoreLatencySeconds            *prometheus.HistogramVec
	breakerStoreUnavailable               prometheus.Gauge
	breakerStoreReady                     prometheus.Gauge
	runtimeStateIntegrity                 *prometheus.GaugeVec
	runtimeStateLossRecoveryTotal         *prometheus.CounterVec
	requestAdmissionOperationTotal        *prometheus.CounterVec
	requestAdmissionActive                prometheus.Gauge
	breakerPermitOperationTotal           *prometheus.CounterVec
	breakerPermitActive                   prometheus.Gauge
	breakerIgnoredResultTotal             *prometheus.CounterVec
	channelConfigRevisionMismatchTotal    *prometheus.CounterVec
	channelCredentialVerificationTotal    *prometheus.CounterVec
	endpointBaseURLRevisionFence          *prometheus.GaugeVec
	endpointBaseURLRevisionPendingSeconds *prometheus.GaugeVec
	endpointStatusRevisionFence           *prometheus.GaugeVec
	endpointStatusRevisionPendingSeconds  *prometheus.GaugeVec
	endpointStatusRevisionMismatchTotal   *prometheus.CounterVec
	runtimeControlOperationTotal          *prometheus.CounterVec
	runtimeControlPending                 *prometheus.GaugeVec
	runtimeControlPendingSeconds          *prometheus.GaugeVec
	runtimeControlRevisionMismatchTotal   *prometheus.CounterVec
	runtimeControlRecoveryTotal           *prometheus.CounterVec
	endpointFailureTotal                  *prometheus.CounterVec
	channelFailureTotal                   *prometheus.CounterVec
	upstreamTTFTSeconds                   *prometheus.HistogramVec
	upstreamTotalDurationSeconds          *prometheus.HistogramVec
	balancedFinalWeight                   *prometheus.GaugeVec
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

		partialSettlementTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_partial_settlement_total",
			Help: "流式 partial settlement（按已吐内容保守估算收费）发生次数，按原因聚合（P2-2 监控偏少收/滥用）。",
		}, []string{"reason"}),

		retryableFallbackTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_retryable_fallback_total",
			Help: "因可重试上游错误切换到下一候选的次数，按上游错误分类聚合（P2-3 监控前序候选可能已产生但未计费的成本）。",
		}, []string{"error_category"}),

		zeroPriceServedTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_zero_price_served_total",
			Help: "以零售价（客户侧 $0）成功结算的请求次数，按 provider/channel/model 聚合（P2-4 零价渠道误配告警）。",
		}, []string{"provider", "channel", "model"}),

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

		stickyEventsTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_sticky_events_total",
			Help: "会话粘性路由事件计数（大 uncache 缺口 P0）：hit/miss（绑定查询）、bind/rebind/clear（绑定写入）、" +
				"pinned_preferred/pinned_non_preferred（置顶渠道是否恰为策略首选——non_preferred 占比即 sticky 成本漂移，R2）、" +
				"pin_lost（绑定渠道被硬摘除，清绑定重选）。",
		}, []string{"event"}),

		routingSkipTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_routing_skip_total",
			Help: "候选在写 attempt 前被跳过的次数（大 uncache 缺口可观测）：reason=breaker/concurrency/ratelimit/ratelimit_store。",
		}, []string{"reason"}),

		routingHeadWaitSeconds: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "unio_gateway_routing_head_wait_seconds",
			Help:    "队首候选 TPM/并发短等实际等待时长（秒，P1）。仅在 waited_ms>0 时观测。",
			Buckets: []float64{0.05, 0.1, 0.25, 0.5, 0.75, 1, 2},
		}),

		routingBalanceTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_routing_balance_total",
			Help: "P3 route scheduling decisions by mode and bounded result.",
		}, []string{"mode", "result"}),
		routingBalanceCandidateCount: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "unio_gateway_routing_balance_candidate_count",
			Help:    "Eligible candidate count after hard filters.",
			Buckets: []float64{0, 1, 2, 3, 5, 8, 13, 21},
		}, []string{"mode"}),
		routingBalancePoolSize: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "unio_gateway_routing_balance_pool_size",
			Help:    "Explicit route channel pool size.",
			Buckets: []float64{0, 1, 2, 3, 5, 8, 13, 21},
		}, []string{"mode"}),
		routingBalanceSelected: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_routing_balance_selected_total",
			Help: "Actual successful route/channel selections.",
		}, []string{"route", "channel"}),
		routingBalanceFallback: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_routing_balance_fallback_total",
			Help: "In-route fallback transitions by bounded reason.",
		}, []string{"route", "reason"}),
		routingBalanceLoadSkew: prometheus.NewHistogram(prometheus.HistogramOpts{
			Name:    "unio_gateway_routing_balance_load_skew",
			Help:    "Difference between maximum and minimum normalized candidate weights.",
			Buckets: []float64{0, 0.05, 0.1, 0.2, 0.4, 0.6, 0.8, 1},
		}),
		routingCapacityRead: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_routing_balance_capacity_read_total",
			Help: "Channel capacity snapshot reads by result.",
		}, []string{"result"}),
		routingMarginGuard: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_routing_margin_guard_total",
			Help: "Negative-margin guard outcomes.",
		}, []string{"result"}),
		routingTraceWrite: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_routing_trace_write_total",
			Help: "Routing decision trace persistence outcomes.",
		}, []string{"result"}),

		breakerState: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "unio_gateway_breaker_state",
			Help: "Current breaker state; exactly one state is 1 for each scope and business ID.",
		}, []string{"scope", "id", "state"}),
		breakerTransitionTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_breaker_transition_total",
			Help: "Breaker state transitions by bounded scope, state and reason.",
		}, []string{"scope", "from", "to", "reason"}),
		breakerSkipTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_breaker_skip_total",
			Help: "Candidate skips caused by breaker or another authoritative runtime gate.",
		}, []string{"scope", "reason"}),
		breakerStoreOperationTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_breaker_store_operation_total",
			Help: "BreakerStore operations by bounded operation and result.",
		}, []string{"operation", "result"}),
		breakerStoreLatencySeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "unio_gateway_breaker_store_latency_seconds",
			Help:    "BreakerStore operation latency in seconds.",
			Buckets: apiLatencyBuckets,
		}, []string{"operation"}),
		breakerStoreUnavailable: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "unio_gateway_breaker_store_unavailable",
			Help: "Whether the latest BreakerStore health observation was unavailable.",
		}),
		breakerStoreReady: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "unio_gateway_breaker_store_ready",
			Help: "Whether BreakerStore and its required runtime controls are ready.",
		}),
		runtimeStateIntegrity: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "unio_gateway_runtime_state_integrity",
			Help: "Runtime integrity state; exactly one bounded state is 1.",
		}, []string{"state"}),
		runtimeStateLossRecoveryTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_runtime_state_loss_recovery_total",
			Help: "Durable runtime-state loss recovery outcomes.",
		}, []string{"result"}),
		requestAdmissionOperationTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_request_admission_operation_total",
			Help: "Request-admission token operations by bounded operation and result.",
		}, []string{"operation", "result"}),
		requestAdmissionActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "unio_gateway_request_admission_active",
			Help: "Active request-admission sessions owned by this process.",
		}),
		breakerPermitOperationTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_breaker_permit_operation_total",
			Help: "AttemptPermit operations by bounded operation and result.",
		}, []string{"operation", "result"}),
		breakerPermitActive: prometheus.NewGauge(prometheus.GaugeOpts{
			Name: "unio_gateway_breaker_permit_active",
			Help: "Active AttemptPermits owned by this process.",
		}),
		breakerIgnoredResultTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_breaker_ignored_result_total",
			Help: "Upstream results ignored by breaker attribution, by bounded scope and reason.",
		}, []string{"scope", "reason"}),
		channelConfigRevisionMismatchTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_channel_config_revision_mismatch_total",
			Help: "Channel configuration revision mismatches by bounded operation.",
		}, []string{"operation"}),
		channelCredentialVerificationTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_channel_credential_rotation_verification_total",
			Help: "Credential rotation verification outcomes.",
		}, []string{"state"}),
		endpointBaseURLRevisionFence: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "unio_gateway_endpoint_base_url_revision_fence",
			Help: "Endpoint BaseURL revision fence state.",
		}, []string{"endpoint_id", "state"}),
		endpointBaseURLRevisionPendingSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "unio_gateway_endpoint_base_url_revision_pending_seconds",
			Help: "Seconds the Endpoint BaseURL revision fence has remained pending.",
		}, []string{"endpoint_id"}),
		endpointStatusRevisionFence: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "unio_gateway_endpoint_status_revision_fence",
			Help: "Endpoint status revision fence state.",
		}, []string{"endpoint_id", "state"}),
		endpointStatusRevisionPendingSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "unio_gateway_endpoint_status_revision_pending_seconds",
			Help: "Seconds the Endpoint status revision fence has remained pending.",
		}, []string{"endpoint_id"}),
		endpointStatusRevisionMismatchTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_endpoint_status_revision_mismatch_total",
			Help: "Endpoint status revision mismatches by bounded operation.",
		}, []string{"operation"}),
		runtimeControlOperationTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_runtime_control_operation_total",
			Help: "Durable runtime-control operations by fixed target, operation and result.",
		}, []string{"target", "operation", "result"}),
		runtimeControlPending: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "unio_gateway_runtime_control_pending",
			Help: "Whether a fixed runtime-control target has pending durable work.",
		}, []string{"target"}),
		runtimeControlPendingSeconds: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "unio_gateway_runtime_control_pending_seconds",
			Help: "Age in seconds of pending work for a fixed runtime-control target.",
		}, []string{"target"}),
		runtimeControlRevisionMismatchTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_runtime_control_revision_mismatch_total",
			Help: "Runtime-control revision mismatches by fixed target and bounded operation.",
		}, []string{"target", "operation"}),
		runtimeControlRecoveryTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_runtime_control_recovery_total",
			Help: "Runtime-control reconciliation outcomes by fixed target.",
		}, []string{"target", "result"}),
		endpointFailureTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_endpoint_failure_total",
			Help: "Endpoint-attributed failures by business ID and bounded category.",
		}, []string{"endpoint_id", "category"}),
		channelFailureTotal: prometheus.NewCounterVec(prometheus.CounterOpts{
			Name: "unio_gateway_channel_failure_total",
			Help: "Channel-attributed failures by business ID and bounded category.",
		}, []string{"channel_id", "category"}),
		upstreamTTFTSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "unio_gateway_upstream_ttft_seconds",
			Help:    "Upstream first-token latency. Only valid streaming samples are observed.",
			Buckets: upstreamLatencyBuckets,
		}, []string{"provider_id", "endpoint_id", "channel_id", "protocol", "operation", "sample_source"}),
		upstreamTotalDurationSeconds: prometheus.NewHistogramVec(prometheus.HistogramOpts{
			Name:    "unio_gateway_upstream_total_duration_seconds",
			Help:    "Full upstream transport duration for streaming and non-streaming attempts.",
			Buckets: upstreamLatencyBuckets,
		}, []string{"provider_id", "endpoint_id", "channel_id", "protocol", "operation", "mode"}),
		balancedFinalWeight: prometheus.NewGaugeVec(prometheus.GaugeOpts{
			Name: "unio_gateway_balanced_final_weight",
			Help: "Latest balanced-routing final weight by route and channel business ID.",
		}, []string{"route_id", "channel_id"}),
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
		m.partialSettlementTotal,
		m.retryableFallbackTotal,
		m.zeroPriceServedTotal,
		m.rateLimitDecisions,
		m.capabilityCheckTotal,
		m.capabilityRequiredTotal,
		m.capabilityMissingTotal,
		m.stickyEventsTotal,
		m.routingSkipTotal,
		m.routingHeadWaitSeconds,
		m.routingBalanceTotal,
		m.routingBalanceCandidateCount,
		m.routingBalancePoolSize,
		m.routingBalanceSelected,
		m.routingBalanceFallback,
		m.routingBalanceLoadSkew,
		m.routingCapacityRead,
		m.routingMarginGuard,
		m.routingTraceWrite,
		m.breakerState,
		m.breakerTransitionTotal,
		m.breakerSkipTotal,
		m.breakerStoreOperationTotal,
		m.breakerStoreLatencySeconds,
		m.breakerStoreUnavailable,
		m.breakerStoreReady,
		m.runtimeStateIntegrity,
		m.runtimeStateLossRecoveryTotal,
		m.requestAdmissionOperationTotal,
		m.requestAdmissionActive,
		m.breakerPermitOperationTotal,
		m.breakerPermitActive,
		m.breakerIgnoredResultTotal,
		m.channelConfigRevisionMismatchTotal,
		m.channelCredentialVerificationTotal,
		m.endpointBaseURLRevisionFence,
		m.endpointBaseURLRevisionPendingSeconds,
		m.endpointStatusRevisionFence,
		m.endpointStatusRevisionPendingSeconds,
		m.endpointStatusRevisionMismatchTotal,
		m.runtimeControlOperationTotal,
		m.runtimeControlPending,
		m.runtimeControlPendingSeconds,
		m.runtimeControlRevisionMismatchTotal,
		m.runtimeControlRecoveryTotal,
		m.endpointFailureTotal,
		m.channelFailureTotal,
		m.upstreamTTFTSeconds,
		m.upstreamTotalDurationSeconds,
		m.balancedFinalWeight,
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

// IncPartialSettlement 记录一次流式 partial settlement（P2-2）。reason 为有界原因（interrupted/canceled/missing_usage 等）。
func (m *Metrics) IncPartialSettlement(reason string) {
	if reason == "" {
		reason = "unknown"
	}
	m.partialSettlementTotal.WithLabelValues(reason).Inc()
}

// IncRetryableFallback 记录一次因可重试上游错误而切换候选（P2-3）。errorCategory 为有界上游错误分类。
func (m *Metrics) IncRetryableFallback(errorCategory string) {
	if errorCategory == "" {
		errorCategory = upstreamErrorCategoryNone
	}
	m.retryableFallbackTotal.WithLabelValues(errorCategory).Inc()
}

// IncZeroPriceServed 记录一次以零售价成功结算的请求（P2-4 零价渠道误配）。
func (m *Metrics) IncZeroPriceServed(provider string, channel string, model string) {
	m.zeroPriceServedTotal.WithLabelValues(provider, channel, model).Inc()
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

// IncStickyEvent 记录一次会话粘性路由事件（大 uncache 缺口 P0）。
// event 为有界稳定取值：hit/miss/bind/rebind/clear/pinned_preferred/pinned_non_preferred/pin_lost。
func (m *Metrics) IncStickyEvent(event string) {
	m.stickyEventsTotal.WithLabelValues(event).Inc()
}

// IncRoutingSkip 记录一次候选在写 attempt 前被跳过。
// reason 为有界稳定取值：breaker/concurrency/ratelimit/ratelimit_store。
func (m *Metrics) IncRoutingSkip(reason string) {
	if reason == "" {
		reason = "unknown"
	}
	m.routingSkipTotal.WithLabelValues(reason).Inc()
}

// ObserveRoutingHeadWait 记录一次队首短等的实际等待时长。
func (m *Metrics) ObserveRoutingHeadWait(duration time.Duration) {
	if duration <= 0 {
		return
	}
	m.routingHeadWaitSeconds.Observe(duration.Seconds())
}

func (m *Metrics) ObserveRoutingBalance(mode, result string, poolSize, candidateCount int, loadSkew float64) {
	if mode == "" {
		mode = "unknown"
	}
	if result == "" {
		result = "unknown"
	}
	m.routingBalanceTotal.WithLabelValues(mode, result).Inc()
	m.routingBalancePoolSize.WithLabelValues(mode).Observe(float64(poolSize))
	m.routingBalanceCandidateCount.WithLabelValues(mode).Observe(float64(candidateCount))
	if loadSkew >= 0 {
		m.routingBalanceLoadSkew.Observe(loadSkew)
	}
}

func (m *Metrics) IncRoutingBalanceSelected(route, channel string) {
	m.routingBalanceSelected.WithLabelValues(route, channel).Inc()
}

func (m *Metrics) IncRoutingBalanceFallback(route, reason string) {
	if reason == "" {
		reason = "unknown"
	}
	m.routingBalanceFallback.WithLabelValues(route, reason).Inc()
}

func (m *Metrics) IncRoutingCapacityRead(result string) {
	m.routingCapacityRead.WithLabelValues(result).Inc()
}

func (m *Metrics) IncRoutingMarginGuard(result string) {
	m.routingMarginGuard.WithLabelValues(result).Inc()
}

func (m *Metrics) IncRoutingTraceWrite(result string) {
	m.routingTraceWrite.WithLabelValues(result).Inc()
}

// SetBreakerState exposes one-hot state for a channel or Endpoint breaker.
func (m *Metrics) SetBreakerState(scope, id, state string) {
	for _, candidate := range []string{"closed", "open", "half_open"} {
		value := 0.0
		if candidate == state {
			value = 1
		}
		m.breakerState.WithLabelValues(scope, id, candidate).Set(value)
	}
}

func (m *Metrics) IncBreakerTransition(scope, from, to, reason string) {
	m.breakerTransitionTotal.WithLabelValues(scope, from, to, reason).Inc()
}

func (m *Metrics) IncBreakerSkip(scope, reason string) {
	m.breakerSkipTotal.WithLabelValues(scope, reason).Inc()
}

func (m *Metrics) ObserveBreakerStoreOperation(operation, result string, duration time.Duration) {
	m.breakerStoreOperationTotal.WithLabelValues(operation, result).Inc()
	m.breakerStoreLatencySeconds.WithLabelValues(operation).Observe(duration.Seconds())
}

func (m *Metrics) SetBreakerStoreHealth(ready, unavailable bool) {
	m.breakerStoreReady.Set(boolFloat(ready))
	m.breakerStoreUnavailable.Set(boolFloat(unavailable))
}

func (m *Metrics) SetRuntimeStateIntegrity(state string) {
	for _, candidate := range []string{"ready", "lost"} {
		value := 0.0
		if candidate == state {
			value = 1
		}
		m.runtimeStateIntegrity.WithLabelValues(candidate).Set(value)
	}
}

func (m *Metrics) IncRuntimeStateLossRecovery(result string) {
	m.runtimeStateLossRecoveryTotal.WithLabelValues(result).Inc()
}

func (m *Metrics) IncRequestAdmissionOperation(operation, result string) {
	m.requestAdmissionOperationTotal.WithLabelValues(operation, result).Inc()
}

func (m *Metrics) AddRequestAdmissionActive(delta float64) {
	m.requestAdmissionActive.Add(delta)
}

func (m *Metrics) IncBreakerPermitOperation(operation, result string) {
	m.breakerPermitOperationTotal.WithLabelValues(operation, result).Inc()
}

func (m *Metrics) AddBreakerPermitActive(delta float64) {
	m.breakerPermitActive.Add(delta)
}

func (m *Metrics) IncBreakerIgnoredResult(scope, reason string) {
	m.breakerIgnoredResultTotal.WithLabelValues(scope, reason).Inc()
}

func (m *Metrics) IncChannelConfigRevisionMismatch(operation string) {
	m.channelConfigRevisionMismatchTotal.WithLabelValues(operation).Inc()
}

func (m *Metrics) IncChannelCredentialRotationVerification(state string) {
	m.channelCredentialVerificationTotal.WithLabelValues(state).Inc()
}

func (m *Metrics) SetEndpointBaseURLRevisionFence(endpointID, state string, pending time.Duration) {
	setFenceState(m.endpointBaseURLRevisionFence, endpointID, state)
	m.endpointBaseURLRevisionPendingSeconds.WithLabelValues(endpointID).Set(nonNegativeSeconds(pending))
}

func (m *Metrics) SetEndpointStatusRevisionFence(endpointID, state string, pending time.Duration) {
	setFenceState(m.endpointStatusRevisionFence, endpointID, state)
	m.endpointStatusRevisionPendingSeconds.WithLabelValues(endpointID).Set(nonNegativeSeconds(pending))
}

func (m *Metrics) IncEndpointStatusRevisionMismatch(operation string) {
	m.endpointStatusRevisionMismatchTotal.WithLabelValues(operation).Inc()
}

func (m *Metrics) IncRuntimeControlOperation(target, operation, result string) {
	m.runtimeControlOperationTotal.WithLabelValues(target, operation, result).Inc()
}

func (m *Metrics) SetRuntimeControlPending(target string, pending bool, age time.Duration) {
	m.runtimeControlPending.WithLabelValues(target).Set(boolFloat(pending))
	if !pending {
		age = 0
	}
	m.runtimeControlPendingSeconds.WithLabelValues(target).Set(nonNegativeSeconds(age))
}

func (m *Metrics) IncRuntimeControlRevisionMismatch(target, operation string) {
	m.runtimeControlRevisionMismatchTotal.WithLabelValues(target, operation).Inc()
}

func (m *Metrics) IncRuntimeControlRecovery(target, result string) {
	m.runtimeControlRecoveryTotal.WithLabelValues(target, result).Inc()
}

func (m *Metrics) IncEndpointFailure(endpointID, category string) {
	m.endpointFailureTotal.WithLabelValues(endpointID, category).Inc()
}

func (m *Metrics) IncChannelFailure(channelID, category string) {
	m.channelFailureTotal.WithLabelValues(channelID, category).Inc()
}

// ObserveUpstreamTiming records total duration for every real transport and TTFT only for a
// valid stream-only FirstToken sample. A nil TTFT therefore emits no TTFT observation.
func (m *Metrics) ObserveUpstreamTiming(
	providerID, endpointID, channelID, protocol, operation, mode string,
	total time.Duration,
	ttft *time.Duration,
) {
	m.upstreamTotalDurationSeconds.WithLabelValues(
		providerID, endpointID, channelID, protocol, operation, mode,
	).Observe(nonNegativeSeconds(total))
	if ttft != nil && *ttft >= 0 {
		m.upstreamTTFTSeconds.WithLabelValues(
			providerID, endpointID, channelID, protocol, operation, "stream_only",
		).Observe(ttft.Seconds())
	}
}

func (m *Metrics) SetBalancedFinalWeight(routeID, channelID string, weight float64) {
	if weight < 0 {
		weight = 0
	}
	m.balancedFinalWeight.WithLabelValues(routeID, channelID).Set(weight)
}

func setFenceState(gauge *prometheus.GaugeVec, endpointID, state string) {
	for _, candidate := range []string{"active", "pending"} {
		value := 0.0
		if candidate == state {
			value = 1
		}
		gauge.WithLabelValues(endpointID, candidate).Set(value)
	}
}

func nonNegativeSeconds(duration time.Duration) float64 {
	if duration < 0 {
		return 0
	}
	return duration.Seconds()
}

func boolFloat(value bool) float64 {
	if value {
		return 1
	}
	return 0
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
