package lifecycle

import (
	"context"

	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/ratelimit"
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

// ChannelConcurrencyLimiter 定义候选循环在调用上游前占用渠道在途名额的能力（DEC-029）。
//
// override 是渠道行 concurrency_limit：nil=继承全局默认，0=不限，>0=具体上限。
// ok=false 表示该渠道在途已满，调用方跳过该候选 fallback 到下一渠道；ok=true 时返回的
// release 必须在上游调用完全结束后调用（release 必须幂等，重复调用只释放一次）。
type ChannelConcurrencyLimiter interface {
	AcquireChannel(channelID int64, override *int64) (release func(), ok bool)
}

// SetConcurrencyLimiter 注入渠道在途并发限制器（bootstrap 连线用）。nil 表示不启用并发限制。
func (r *AttemptRunner) SetConcurrencyLimiter(limiter ChannelConcurrencyLimiter) {
	r.concurrency = limiter
}

// acquireChannelSlot 尝试占用候选渠道的在途名额；未注入限制器时恒放行（no-op release）。
func (r *AttemptRunner) acquireChannelSlot(candidate routing.ChatRouteCandidate) (func(), bool) {
	if r.concurrency == nil {
		return func() {}, true
	}
	return r.concurrency.AcquireChannel(candidate.Channel.ID, candidate.ConcurrencyLimit)
}

// channelConcurrencyLimitedError 构造「全部候选渠道在途并发均满」错误（映射 HTTP 429）。
func channelConcurrencyLimitedError() error {
	return failure.New(
		failure.CodeGatewayChannelConcurrencyLimited,
		failure.WithMessage("all candidate channels are at concurrency limit"),
	)
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

// tpmReservations 跟踪一次请求内「实际生效且已预占」的 TPM 计数（route+user 级 + 各候选 channel 级），
// 用于请求收尾时释放那些「没有被成功结算回填对账」的预占（DEC-028）。
//
// 背景：进入上游前按 ConservativeInputTokens 预占 TPM 窗口；只有胜出候选走 backfillRateTokens 用真实
// billable token 对账。历史实现里失败/取消/无结算的请求、以及 fallback 中落选/失败的候选渠道，其预占
// 从不显式回退，只能干等 60s 滑动窗口自然过期——这会把额度「泄漏」在窗口里，造成 bursty/易错负载过早 429，
// 也让渠道级 TPM 负载虚高（fallback 时每个尝试过的渠道都被计入，却只有胜者被对账）。
//
// 释放只针对 TPM（token 维度）。channel/ingress 的 RPM/RPD（请求计数）代表「确实发起过一次尝试」，
// 按上游保护/防滥用的行业惯例不回退。
type tpmReservations struct {
	routeID       int64
	userID        int64
	keyEst        int64
	hasKey        bool
	keyReconciled bool
	channels      []channelTPMReservation
}

// channelTPMReservation 记录某候选 channel 的 TPM 预占量与是否已被结算回填对账。
type channelTPMReservation struct {
	channelID  int64
	est        int64
	reconciled bool
}

// recordKeyTPMReservation 在 route+user TPM 预占成功且该主体 TPM 实际生效时登记预占量。
//
// 只在真正会写入 TPM 窗口时登记（guard 非空、有 route+user 主体、TPM 生效），保证释放量与预占量一致。
func (r *AttemptRunner) recordKeyTPMReservation(res *tpmReservations, principal *auth.APIKeyPrincipal, estTokens int64) {
	if r.guard == nil || res == nil {
		return
	}
	routeID, userID, ok := routeUserOf(principal)
	if !ok || !r.guard.TokensEnforced(keyRateLimits(principal)) {
		return
	}
	res.routeID, res.userID, res.keyEst, res.hasKey = routeID, userID, nonNegativeTokens(estTokens), true
}

// recordChannelTPMReservation 在某候选 channel TPM 预占成功且该 channel TPM 实际生效时登记预占量。
func (r *AttemptRunner) recordChannelTPMReservation(res *tpmReservations, candidate routing.ChatRouteCandidate, estTokens int64) {
	if r.guard == nil || res == nil {
		return
	}
	if !r.guard.TokensEnforced(channelRateLimits(candidate)) {
		return
	}
	res.channels = append(res.channels, channelTPMReservation{channelID: candidate.Channel.ID, est: nonNegativeTokens(estTokens)})
}

// markReconciled 在结算按真实用量回填后，标记 route+user 与胜出 channel 的预占已对账，收尾时不再释放。
//
// 即使 backfill 的 delta 为 0（预占恰等于真实用量）也应调用：此时预占本身就是正确值，不能被当作泄漏释放掉。
func (res *tpmReservations) markReconciled(channelID int64) {
	if res == nil {
		return
	}
	res.keyReconciled = true
	for i := range res.channels {
		if res.channels[i].channelID == channelID {
			res.channels[i].reconciled = true
		}
	}
}

// releaseUnreconciledTPM 释放请求收尾时仍未被结算回填的 TPM 预占：route+user 未对账（失败/取消/无结算），
// 以及 fallback 中落选/失败的候选渠道。只回退 TPM（token 维度），不回退 RPM/RPD（请求计数）。
//
// 用脱离 cancel 的上下文，保证客户端断开也能释放。释放采用现有 Backfill*（负增量）：与结算回填同一入口，
// 允许桶短暂为负（下一分钟自然收敛），与既有 backfill 语义一致。
func (r *AttemptRunner) releaseUnreconciledTPM(ctx context.Context, res *tpmReservations) {
	if r.guard == nil || res == nil {
		return
	}
	bgCtx := context.WithoutCancel(ctx)
	if res.hasKey && res.keyEst > 0 && !res.keyReconciled {
		r.guard.BackfillRouteUserTokens(bgCtx, res.routeID, res.userID, -res.keyEst)
	}
	for _, c := range res.channels {
		if c.reconciled || c.est <= 0 {
			continue
		}
		r.guard.BackfillChannelTokens(bgCtx, c.channelID, -c.est)
	}
}

func nonNegativeTokens(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

// billableTPMTokens 把统一 usage facts 折算成用于 TPM 计数的 token 总量（缓存感知，DEC-028）。
//
// = uncached 输入 + cache_write（首次写缓存 5m/1h）+ 权威输出总量（含 reasoning）。
// 明确「排除」cache_read（缓存命中读取）：缓存命中的 token 上游不重新计算、近乎零吞吐负载，
// 若计入每分钟 TPM，会把重发缓存上下文的 agent（如 Codex，每轮重发 ~8-9 万缓存 token）迅速挤爆
// 窗口——对齐 Anthropic「cache-aware ITPM」（cache_read 不计、cache_creation 计）。cache_write 仍全额
// 计入（首次处理有真实上游负载）。行业口径上限流不做「打折」：要么全额、要么不计，0.1~0.25 是计费折扣。
// 不另加 ReasoningOutputTokens（已含于 OutputTokensTotal）。unknown 维度按 0 处理（TPM 是限流非计费，
// 偏少不会误扣费，且结算侧另有 risk_exposure 兜底）。
func billableTPMTokens(f usage.Facts) int64 {
	total := int64(0)
	for _, c := range []usage.TokenCount{
		f.UncachedInputTokens,
		f.CacheWrite5mInputTokens,
		f.CacheWrite1hInputTokens,
		f.CacheWrite30mInputTokens,
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
