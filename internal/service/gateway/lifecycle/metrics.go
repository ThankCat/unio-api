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
