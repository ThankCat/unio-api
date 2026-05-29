package gateway

import (
	"strconv"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
)

// MetricsRecorder 定义 gateway 记录业务指标所需的能力。
// 由 internal/platform/observability/metrics 提供实现，这里只声明消费契约，
// 因此核心 adapter/routing/billing 包不需要感知 metrics。
type MetricsRecorder interface {
	IncChatRequest(stream bool, outcome metrics.ChatOutcome)
	IncRoutingSelected(provider string, channel string, model string)
	ObserveUpstream(provider string, channel string, success bool, errorCategory string, duration time.Duration)
	IncSettlement(outcome metrics.SettlementOutcome)
	IncStreamEvent(event metrics.StreamEvent)
}

// recordChatRequest 在配置了 recorder 时记录一次请求最终结果。
func (s *ChatCompletionService) recordChatRequest(stream bool, outcome metrics.ChatOutcome) {
	if s.metrics == nil {
		return
	}

	s.metrics.IncChatRequest(stream, outcome)
}

// recordRoutingSelected 记录一次实际选中的 provider/channel/model。
func (s *ChatCompletionService) recordRoutingSelected(providerID int64, channelID int64, model string) {
	if s.metrics == nil {
		return
	}

	s.metrics.IncRoutingSelected(metricsID(providerID), metricsID(channelID), model)
}

// recordUpstream 记录一次上游 adapter 调用的结果、错误分类和耗时。
// err 为 nil 表示调用成功；否则用 adapter.UpstreamCategoryOf 提取稳定上游错误分类。
func (s *ChatCompletionService) recordUpstream(providerID int64, channelID int64, duration time.Duration, err error) {
	if s.metrics == nil {
		return
	}

	if err == nil {
		s.metrics.ObserveUpstream(metricsID(providerID), metricsID(channelID), true, "", duration)
		return
	}

	category, _ := adapter.UpstreamCategoryOf(err)
	s.metrics.ObserveUpstream(metricsID(providerID), metricsID(channelID), false, string(category), duration)
}

// recordSettlement 记录一次结算调用的结果。
func (s *ChatCompletionService) recordSettlement(outcome metrics.SettlementOutcome) {
	if s.metrics == nil {
		return
	}

	s.metrics.IncSettlement(outcome)
}

// recordStreamEvent 记录一次流式请求生命周期事件。
func (s *ChatCompletionService) recordStreamEvent(event metrics.StreamEvent) {
	if s.metrics == nil {
		return
	}

	s.metrics.IncStreamEvent(event)
}

// settlementOutcomeFromErr 把 settlement 返回的错误映射成结算指标结果分类。
func settlementOutcomeFromErr(err error) metrics.SettlementOutcome {
	switch {
	case err == nil:
		return metrics.SettlementOutcomeSuccess
	case IsChatSettlementRecoveryScheduled(err):
		return metrics.SettlementOutcomeRecoveryScheduled
	default:
		return metrics.SettlementOutcomeFailed
	}
}

// metricsID 把 provider/channel 数据库 ID 转成稳定 label 值。
// provider/channel 由后台管理、取值有界，使用 ID 作为 label 不会引入高基数风险。
func metricsID(id int64) string {
	return strconv.FormatInt(id, 10)
}
