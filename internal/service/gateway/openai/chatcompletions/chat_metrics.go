package chatcompletions

import (
	"time"

	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
)

// chat_metrics.go 内的方法是 lifecycle.RequestLifecycle 的 receiver-bound forward。
// 真正的指标上报（含 metrics 为 nil 时的 no-op 守护与 provider/channel 维度 label 转换）
// 在 lifecycle 包共享，OpenAI 与 Anthropic 两侧 service 调用同一份实现，避免逐字复制。

func (s *ChatCompletionService) recordChatRequest(stream bool, outcome metrics.ChatOutcome) {
	s.lifecycle.RecordRequest(stream, outcome)
}

func (s *ChatCompletionService) recordRoutingSelected(providerID int64, channelID int64, model string) {
	s.lifecycle.RecordRoutingSelected(providerID, channelID, model)
}

func (s *ChatCompletionService) recordUpstream(providerID int64, channelID int64, duration time.Duration, err error) {
	s.lifecycle.RecordUpstream(providerID, channelID, duration, err)
}

func (s *ChatCompletionService) recordSettlement(outcome metrics.SettlementOutcome) {
	s.lifecycle.RecordSettlement(outcome)
}

func (s *ChatCompletionService) recordStreamEvent(event metrics.StreamEvent) {
	s.lifecycle.RecordStreamEvent(event)
}
