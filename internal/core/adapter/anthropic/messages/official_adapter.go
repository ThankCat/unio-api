package messages

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/channel"
)

// OfficialAdapter 是 Anthropic 官方一方(1P) Messages adapter。
//
// 复用协议族 base（零 Drop 忠实 wire），叠加官方专属能力：
//   - anthropic-beta 头透传到 upstream，仅拦截有计费/解析缺口的 beta（黑名单,见 beta.go）；
//   - MessagesInputTokenizer 复用 base 对完整 wire 的保守估算（upgrade-plan N2）。
type OfficialAdapter struct {
	base   *Adapter
	logger *slog.Logger
}

// NewOfficialAdapter 创建 Anthropic 官方 1P adapter。
func NewOfficialAdapter(client *http.Client, logger *slog.Logger) *OfficialAdapter {
	if logger == nil {
		logger = slog.Default()
	}

	return &OfficialAdapter{
		base:   NewAdapter(client),
		logger: logger,
	}
}

// Messages 应用 beta 转发策略（透传 + 小黑名单）后调用 base。
func (a *OfficialAdapter) Messages(ctx context.Context, ch channel.Runtime, req MessageRequest) (*MessageResponse, error) {
	req = a.applyBetaPolicy(ctx, req)
	return a.base.Messages(ctx, ch, req)
}

// StreamMessages 应用 beta 转发策略（透传 + 小黑名单）后调用 base。
func (a *OfficialAdapter) StreamMessages(ctx context.Context, ch channel.Runtime, req MessageRequest, emit func(MessageStreamEvent) error) (adapter.StreamOutcome, error) {
	req = a.applyBetaPolicy(ctx, req)
	return a.base.StreamMessages(ctx, ch, req, emit)
}

// CountMessagesInputTokens 复用 base 对完整官方 wire 的保守估算（官方无 Drop）。
func (a *OfficialAdapter) CountMessagesInputTokens(req MessagesInputTokenizeRequest) (int64, error) {
	return a.base.CountMessagesInputTokens(req)
}

func (a *OfficialAdapter) applyBetaPolicy(ctx context.Context, req MessageRequest) MessageRequest {
	if len(req.AnthropicBeta) == 0 {
		return req
	}

	policy := activeBetaPolicy(ctx)

	if blocked := blockedBetas(req.AnthropicBeta, policy); len(blocked) > 0 {
		a.logger.DebugContext(ctx, "anthropic official adapter blocked beta headers per policy",
			slog.String("protocol", "anthropic"),
			slog.String("adapter_key", "anthropic"),
			slog.String("beta_mode", string(policy.Mode)),
			slog.Any("blocked_beta_headers", blocked),
		)
	}

	req.AnthropicBeta = forwardableBetas(req.AnthropicBeta, policy)
	return req
}

var (
	_ MessagesAdapter        = (*OfficialAdapter)(nil)
	_ StreamMessagesAdapter  = (*OfficialAdapter)(nil)
	_ MessagesInputTokenizer = (*OfficialAdapter)(nil)
)
