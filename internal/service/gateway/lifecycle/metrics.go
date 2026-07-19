package lifecycle

import (
	"strconv"
	"time"

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
}

type routingTraceMetricsRecorder interface {
	IncRoutingTraceWrite(result string)
}

func (l *RequestLifecycle) recordRoutingPlan(in RoutingDecisionTraceInput) {
	m, ok := l.metrics.(routingBalanceMetricsRecorder)
	if !ok || in.Attempts > 0 {
		return
	}
	result := "planned"
	if in.Plan.CapacityDegraded {
		result = "capacity_degraded"
	} else if in.Plan.AllCapacityZero {
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
