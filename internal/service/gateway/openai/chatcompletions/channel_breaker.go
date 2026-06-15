package chatcompletions

import (
	"github.com/ThankCat/unio-api/internal/core/routing"
)

// channel_breaker.go 内的方法是 lifecycle.RequestLifecycle 的 receiver-bound forward。
// 真正的熔断只读判定与状态记录在 lifecycle 包（breaker.go + request_lifecycle.go）共享，
// OpenAI 与 Anthropic 两侧 service 调用同一份实现，避免逐字复制。

func (s *ChatCompletionService) candidateAvailable(candidate routing.ChatRouteCandidate) bool {
	return s.lifecycle.CandidateAvailable(candidate)
}

func (s *ChatCompletionService) breakerAllow(channelKey string) bool {
	return s.lifecycle.BreakerAllow(channelKey)
}

func (s *ChatCompletionService) recordChannelHealth(channelKey string, err error) {
	s.lifecycle.RecordChannelHealth(channelKey, err)
}

func (s *ChatCompletionService) channelHealthScore(channelKey string) float64 {
	return s.lifecycle.ChannelHealthScore(channelKey)
}
