package lifecycle

import "github.com/ThankCat/unio-api/internal/core/adapter"

// RetryClassifier 定义 gateway 判断一次上游错误是否允许尝试下一个同模型 channel 的能力。
//
// 它是协议无关的共享 lifecycle 能力：只消费 adapter 给出的稳定上游错误分类，OpenAI 与
// Anthropic 两个协议族的 service 编排都复用同一套 fallback 判定，不各自实现一份。
type RetryClassifier interface {
	IsRetryable(err error) bool
}

// NeverRetryClassifier 是保守的错误分类器，默认不重试任何错误。
type NeverRetryClassifier struct{}

// IsRetryable 始终返回 false，避免没有明确错误分类时误触发 fallback。
func (NeverRetryClassifier) IsRetryable(err error) bool {
	return false
}

// ProviderErrorClassifier 依据 adapter 返回的上游错误分类决定是否允许同模型 channel fallback。
//
// 它只消费 adapter.UpstreamCategoryOf 给出的稳定分类，不解析 provider 原始错误 body，
// 因此 gateway 不会因为不同 provider 的错误文案差异而做出不同决策。
type ProviderErrorClassifier struct{}

// IsRetryable 仅对与请求内容无关的瞬时上游故障返回 true。
//
// 判定规则：
//   - rate_limit / timeout / server_error：上游瞬时故障，换同模型 channel 可能成功，允许 fallback。
//   - auth / permission：多为平台 channel 凭据或授权配置问题，盲目重试会放大错误并掩盖配置故障，不重试。
//   - bad_request：请求本身非法，换 channel 内容不变也不会成功，不重试。
//   - canceled：客户端主动取消，不是上游故障，不重试。
//   - unknown 或链上没有 *adapter.UpstreamError：缺乏可靠依据，保守地不重试。
func (ProviderErrorClassifier) IsRetryable(err error) bool {
	category, ok := adapter.UpstreamCategoryOf(err)
	if !ok {
		return false
	}

	switch category {
	case adapter.UpstreamErrorRateLimit,
		adapter.UpstreamErrorTimeout,
		adapter.UpstreamErrorServer:
		return true
	default:
		return false
	}
}
