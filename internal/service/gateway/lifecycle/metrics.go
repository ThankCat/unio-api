package lifecycle

import (
	"strconv"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
)

// MetricsRecorder 定义 gateway 记录业务指标所需的能力。
//
// 它是协议无关的共享 lifecycle 契约：OpenAI 与 Anthropic 两个协议族的 service 编排上报
// 同一组业务指标，由 internal/platform/observability/metrics 提供实现，核心 adapter/routing/billing
// 包不需要感知 metrics。
type MetricsRecorder interface {
	IncChatRequest(stream bool, outcome metrics.ChatOutcome)
	IncRoutingSelected(provider string, channel string, model string)
	ObserveUpstream(provider string, channel string, success bool, errorCategory string, duration time.Duration)
	IncSettlement(outcome metrics.SettlementOutcome)
	IncStreamEvent(event metrics.StreamEvent)
	IncPartialSettlement(reason string)
	IncRetryableFallback(errorCategory string)
	IncZeroPriceServed(provider string, channel string, model string)
	// IncRoutingSkip 记录候选在写 attempt 前被跳过（breaker/concurrency/ratelimit；大 uncache 缺口可观测）。
	IncRoutingSkip(reason string)
	// ObserveRoutingHeadWait 记录队首 TPM/并发短等实际等待时长（P1）。
	ObserveRoutingHeadWait(duration time.Duration)
}

type routingBalanceMetricsRecorder interface {
	ObserveRoutingBalance(mode, result string, poolSize, candidateCount int, loadSkew float64)
	IncRoutingBalanceSelected(route, channel string)
	IncRoutingBalanceFallback(route, reason string)
	IncRoutingCapacityRead(result string)
	IncRoutingMarginGuard(result string)
	SetBalancedFinalWeight(route, channel string, weight float64)
}

type routingTraceMetricsRecorder interface {
	IncRoutingTraceWrite(result string)
}

type breakerRoutingMetricsRecorder interface {
	SetBreakerState(scope, id, state string)
	IncBreakerSkip(scope, reason string)
	IncChannelConfigRevisionMismatch(operation string)
	IncOriginStatusRevisionMismatch(operation string)
}

type attemptRuntimeMetricsRecorder interface {
	ObserveUpstreamTiming(providerID, originID, channelID, protocol, operation, mode string, total time.Duration, ttft *time.Duration)
	IncOriginFailure(originID, category string)
	IncChannelFailure(channelID, category string)
}

// RecordAttemptRuntimeMetrics records transport timing and the same bounded failure attribution
// submitted to BreakerStore. A missing transport boundary produces no upstream observation.
func (l *RequestLifecycle) RecordAttemptRuntimeMetrics(
	candidate routing.ChatRouteCandidate,
	operation requestlog.UpstreamEndpoint,
	stream bool,
	facts AttemptTimingFacts,
	outcome breakerstore.FinishOutcome,
	err error,
) {
	if l == nil {
		return
	}
	m, ok := l.metrics.(attemptRuntimeMetricsRecorder)
	if !ok || facts.UpstreamStartedAt == nil || facts.UpstreamCompletedAt == nil {
		return
	}

	total := facts.UpstreamCompletedAt.Sub(*facts.UpstreamStartedAt)
	if total < 0 {
		total = 0
	}
	var ttft *time.Duration
	if stream && facts.UpstreamFirstTokenAt != nil {
		duration := facts.UpstreamFirstTokenAt.Sub(*facts.UpstreamStartedAt)
		if duration < 0 {
			duration = 0
		}
		ttft = &duration
	}
	mode := "non_stream"
	if stream {
		mode = "stream"
	}
	m.ObserveUpstreamTiming(
		MetricsID(candidate.ProviderID),
		MetricsID(candidate.ProviderOriginID),
		MetricsID(candidate.Channel.ID),
		candidate.Protocol,
		string(operation),
		mode,
		total,
		ttft,
	)

	category := attemptFailureMetricCategory(err)
	if outcome.OriginEvidence != breakerstore.OriginEvidenceNone {
		m.IncOriginFailure(MetricsID(candidate.ProviderOriginID), string(outcome.OriginEvidence))
	} else if outcome.OriginOutcome == breakerstore.OutcomeEligibleFailure {
		m.IncOriginFailure(MetricsID(candidate.ProviderOriginID), category)
	}
	if outcome.ChannelOutcome == breakerstore.OutcomeEligibleFailure {
		m.IncChannelFailure(MetricsID(candidate.Channel.ID), category)
	}
}

func attemptFailureMetricCategory(err error) string {
	if category, ok := adapter.UpstreamCategoryOf(err); ok && category != "" {
		return string(category)
	}
	return "unknown"
}

func (l *RequestLifecycle) recordRoutingPlan(in RoutingDecisionTraceInput) {
	m, ok := l.metrics.(routingBalanceMetricsRecorder)
	if !ok || in.FallbackOccurred {
		return
	}
	result := "planned"
	if in.Plan.AllCapacityZero {
		result = "all_capacity_zero"
	} else if len(in.Plan.Candidates) == 0 {
		result = "no_candidate"
	}
	m.ObserveRoutingBalance(in.Mode, result, in.PoolSize, len(in.Plan.Candidates), routingLoadSkew(in.Plan.Candidates))
	for _, candidate := range in.Plan.Candidates {
		readResult := "success"
		if candidate.Balance.CapacityReadFailed {
			readResult = "failed"
		} else if candidate.Balance.CapacityUnknown {
			readResult = "unknown"
		}
		m.IncRoutingCapacityRead(readResult)
		m.SetBalancedFinalWeight(MetricsID(in.RouteID), MetricsID(candidate.Route.Channel.ID), candidate.Balance.Weight)
	}
	for _, excluded := range in.Plan.Excluded {
		m.SetBalancedFinalWeight(MetricsID(in.RouteID), MetricsID(excluded.ChannelID), 0)
	}
	l.recordBreakerRoutingFacts(in.Plan)
}

func (l *RequestLifecycle) recordBreakerRoutingFacts(plan CandidatePlan) {
	m, ok := l.metrics.(breakerRoutingMetricsRecorder)
	if !ok {
		return
	}
	for _, candidate := range plan.Candidates {
		recordBreakerStates(m, candidate.Route, candidate.Balance)
	}
	for _, excluded := range plan.Excluded {
		recordBreakerStates(m, excluded.Route, excluded.Balance)
		switch excluded.Reason {
		case "stale_config_revision":
			m.IncChannelConfigRevisionMismatch("snapshot")
		case "stale_status_revision":
			m.IncOriginStatusRevisionMismatch("snapshot")
		}
		scope := "channel"
		if excluded.Balance.OriginBreakerState == "open" || excluded.Balance.OriginBreakerState == "half_open" {
			scope = "origin"
		}
		m.IncBreakerSkip(scope, excluded.Reason)
	}
}

func recordBreakerStates(m breakerRoutingMetricsRecorder, candidate routing.ChatRouteCandidate, score BalanceScore) {
	if score.OriginBreakerState != "" && candidate.ProviderOriginID > 0 {
		m.SetBreakerState("origin", MetricsID(candidate.ProviderOriginID), score.OriginBreakerState)
	}
	if score.ChannelBreakerState != "" && candidate.Channel.ID > 0 {
		m.SetBreakerState("channel", MetricsID(candidate.Channel.ID), score.ChannelBreakerState)
	}
}

func routingLoadSkew(candidates []Candidate) float64 {
	if len(candidates) < 2 {
		return 0
	}
	total := 0.0
	for _, candidate := range candidates {
		total += candidate.Balance.Weight
	}
	if total <= 0 {
		return 0
	}
	minShare, maxShare := 1.0, 0.0
	for _, candidate := range candidates {
		share := candidate.Balance.Weight / total
		if share < minShare {
			minShare = share
		}
		if share > maxShare {
			maxShare = share
		}
	}
	return maxShare - minShare
}

func (l *RequestLifecycle) RecordBalanceSelected(routeID *int64, channelID int64) {
	m, ok := l.metrics.(routingBalanceMetricsRecorder)
	if !ok || routeID == nil {
		return
	}
	m.IncRoutingBalanceSelected(MetricsID(*routeID), MetricsID(channelID))
}

func (l *RequestLifecycle) RecordBalanceFallback(routeID *int64, reason string) {
	m, ok := l.metrics.(routingBalanceMetricsRecorder)
	if !ok || routeID == nil {
		return
	}
	m.IncRoutingBalanceFallback(MetricsID(*routeID), reason)
}

func (l *RequestLifecycle) recordMarginGuard(result string) {
	if m, ok := l.metrics.(routingBalanceMetricsRecorder); ok {
		m.IncRoutingMarginGuard(result)
	}
}

// MetricsID 把 provider/channel 数据库 ID 转成稳定 label 值。
//
// provider/channel 由后台管理、取值有界，使用 ID 作为 label 不会引入高基数风险。
func MetricsID(id int64) string {
	return strconv.FormatInt(id, 10)
}

// SettlementOutcomeFromErr 把 settlement 返回的错误映射成结算指标结果分类。
//
// 协议无关：双协议 service 编排骨架在 settle 阶段共用一套结算指标语义。
// recovery 已接管的 settlement 失败记为 recovery_scheduled；判定见 IsChatSettlementRecoveryScheduled。
func SettlementOutcomeFromErr(err error) metrics.SettlementOutcome {
	switch {
	case err == nil:
		return metrics.SettlementOutcomeSuccess
	case IsChatSettlementRecoveryScheduled(err):
		return metrics.SettlementOutcomeRecoveryScheduled
	default:
		return metrics.SettlementOutcomeFailed
	}
}
