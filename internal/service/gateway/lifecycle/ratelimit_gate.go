package lifecycle

import (
	"context"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/core/usage"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
)

// RateLimitGuard 抽象 AttemptRunner 在候选循环内依赖的两层限流能力（DEC-027）。
//
// 仅覆盖「上游调用前」生效的维度：线路+用户级 TPM 预占、channel 级 RPM/RPD/TPM 预占，以及结算后
// 按真实用量回填 TPM。线路+用户级 RPM/RPD（请求计数维度）在 ingress 中间件已处理，不在此重复。
// AttemptRunner.guard 为 nil 时全部放行，保证未注入限流的调用点零行为变化。
type RateLimitGuard interface {
	AllowRouteUserTokens(ctx context.Context, routeID, userID int64, limits ratelimit.Limits, estTokens int64) (ratelimit.Decision, error)
	AllowChannel(ctx context.Context, channelID int64, limits ratelimit.Limits, estTokens int64) (ratelimit.Decision, error)
	TokensEnforced(limits ratelimit.Limits) bool
	BackfillRouteUserTokens(ctx context.Context, routeID, userID int64, delta int64)
	BackfillChannelTokens(ctx context.Context, channelID int64, delta int64)
}

// SetRateLimitGuard 注入限流 Guard（bootstrap 连线用）。nil 表示不启用限流。
func (r *AttemptRunner) SetRateLimitGuard(guard RateLimitGuard) {
	r.guard = guard
}

func keyRateLimits(p *auth.APIKeyPrincipal) ratelimit.Limits {
	return ratelimit.Limits{RPM: p.RPMLimit, TPM: p.TPMLimit, RPD: p.RPDLimit}
}

// routeUserOf 从 principal 取 (线路ID, 用户ID) 复合计数主体；线路必填、恒有值。
func routeUserOf(p *auth.APIKeyPrincipal) (routeID, userID int64, ok bool) {
	if p == nil || p.RouteID == nil {
		return 0, 0, false
	}
	return *p.RouteID, p.UserID, true
}

func channelRateLimits(c routing.ChatRouteCandidate) ratelimit.Limits {
	return ratelimit.Limits{RPM: c.RPMLimit, TPM: c.TPMLimit, RPD: c.RPDLimit}
}

// guardKeyTokens 在进入候选循环前对 Key 做 TPM 预占。
//
// 返回 (decision, allowed, err)：err!=nil 为计数后端 fail_closed 故障（调用方释放冻结后按 500/限流上抛）；
// allowed=false 且 err=nil 为命中 Key 级 TPM 限流（映射 429）。guard 为 nil 或无 principal 时放行。
func (r *AttemptRunner) guardKeyTokens(ctx context.Context, principal *auth.APIKeyPrincipal, estTokens int64) (ratelimit.Decision, bool, error) {
	routeID, userID, ok := routeUserOf(principal)
	if r.guard == nil || !ok {
		return ratelimit.Decision{Allowed: true}, true, nil
	}
	dec, err := r.guard.AllowRouteUserTokens(ctx, routeID, userID, keyRateLimits(principal), estTokens)
	if err != nil {
		return dec, false, err
	}
	return dec, dec.Allowed, nil
}

// guardChannel 在调用某候选上游前对其命中 channel 做 RPM/RPD/TPM 预占。
//
// 返回 (decision, allowed, err)：allowed=false 表示该候选被渠道级限流命中，调用方应跳过它 fallback 到下一个；
// err!=nil 为计数后端 fail_closed 故障，调用方同样按「跳过该候选」处理（fail_open 时 Guard 内部已放行）。
func (r *AttemptRunner) guardChannel(ctx context.Context, candidate routing.ChatRouteCandidate, estTokens int64) (ratelimit.Decision, bool, error) {
	if r.guard == nil {
		return ratelimit.Decision{Allowed: true}, true, nil
	}
	dec, err := r.guard.AllowChannel(ctx, candidate.Channel.ID, channelRateLimits(candidate), estTokens)
	if err != nil {
		return dec, false, err
	}
	return dec, dec.Allowed, nil
}

// backfillRateTokens 在结算拿到真实 token 用量后，按 (actual-est) 修正 Key 与 channel 的 TPM 计数。
//
// 预检阶段按预估 token 占用了 TPM 窗口；结算后用真实 billable token 回填差额（可正可负），
// 使下一分钟的 TPM 计数收敛到真实用量。仅当对应主体 TPM 实际生效（>0）时才回填，避免无谓写。
// 计数写入用脱离请求 ctx 的上下文，确保客户端断开也能完成回填。
func (r *AttemptRunner) backfillRateTokens(ctx context.Context, principal *auth.APIKeyPrincipal, candidate routing.ChatRouteCandidate, estTokens int64, facts usage.Facts) {
	if r.guard == nil {
		return
	}
	actual := billableTPMTokens(facts)
	delta := actual - estTokens
	if delta == 0 {
		return
	}
	bgCtx := context.WithoutCancel(ctx)
	if routeID, userID, ok := routeUserOf(principal); ok {
		if keyLimits := keyRateLimits(principal); r.guard.TokensEnforced(keyLimits) {
			r.guard.BackfillRouteUserTokens(bgCtx, routeID, userID, delta)
		}
	}
	if chanLimits := channelRateLimits(candidate); r.guard.TokensEnforced(chanLimits) {
		r.guard.BackfillChannelTokens(bgCtx, candidate.Channel.ID, delta)
	}
}

// billableTPMTokens 把统一 usage facts 折算成用于 TPM 计数的 token 总量。
//
// = 全部输入维度（uncached/cache_read/cache_write_5m/cache_write_1h）+ 权威输出总量（含 reasoning）。
// 不另加 ReasoningOutputTokens（已含于 OutputTokensTotal）。unknown 维度按 0 处理（TPM 是限流非计费，
// 偏少不会误扣费，且结算侧另有 risk_exposure 兜底）。
func billableTPMTokens(f usage.Facts) int64 {
	total := int64(0)
	for _, c := range []usage.TokenCount{
		f.UncachedInputTokens,
		f.CacheReadInputTokens,
		f.CacheWrite5mInputTokens,
		f.CacheWrite1hInputTokens,
		f.OutputTokensTotal,
	} {
		if v, ok := c.BillableValue(); ok && v > 0 {
			total += v
		}
	}
	return total
}

// keyTokenRateLimitError 构造 Key 级 TPM 限流命中错误（映射 HTTP 429）。
func keyTokenRateLimitError(dec ratelimit.Decision) error {
	return failure.New(
		failure.CodeRateLimitExceeded,
		failure.WithMessage("api key token rate limit exceeded"),
		failure.WithField("dimension", dec.Dimension),
		failure.WithField("limit", dec.Limit),
	)
}

// channelRateLimitedError 构造「全部候选渠道均被渠道级限流命中」错误（映射 HTTP 429）。
func channelRateLimitedError(dec ratelimit.Decision) error {
	return failure.New(
		failure.CodeGatewayChannelRateLimited,
		failure.WithMessage("all candidate channels are rate limited"),
		failure.WithField("dimension", dec.Dimension),
	)
}
