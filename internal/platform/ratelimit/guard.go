package ratelimit

import (
	"context"
	"log/slog"
	"strconv"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// 限流窗口/桶粒度（P2-8）：
//   - RPM/TPM 用 1 分钟窗口、1 秒桶（60 桶）；
//   - RPD 用 1 天窗口、1 小时桶（24 桶）。
const (
	rpmWindow = time.Minute
	rpmBucket = time.Second
	tpmWindow = time.Minute
	tpmBucket = time.Second
	rpdWindow = 24 * time.Hour
	rpdBucket = time.Hour
)

// Scope 表示限流主体维度（API Key 或 channel）。
type Scope string

const (
	// ScopeKey 表示 API Key 级限流（已废弃，改为 ScopeRouteUser；保留常量以兼容历史计数键）。
	ScopeKey Scope = "key"
	// ScopeChannel 表示渠道级限流。
	ScopeChannel Scope = "chan"
	// ScopeRouteUser 表示「线路 + 用户」级限流（DEC-027）：同一用户在某线路下所有 Key 共享一个桶。
	ScopeRouteUser Scope = "ru"
)

// 限流维度标识，用于 subject 拼接、响应头与日志。
const (
	DimensionRPM = "rpm"
	DimensionTPM = "tpm"
	DimensionRPD = "rpd"
)

// Limits 表示某主体的 RPM/TPM/RPD 限流上限覆盖值：
// nil 表示「继承全局默认」，0 表示「显式不限」，>0 表示具体上限。
type Limits struct {
	RPM *int64
	TPM *int64
	RPD *int64
}

// DefaultLimits 是全局默认限流上限（0 表示不限）。
type DefaultLimits struct {
	RPM int64
	TPM int64
	RPD int64
}

// Decision 表示一次限流判定结果。
type Decision struct {
	Allowed   bool
	Dimension string
	Limit     int64
	Remaining int64
	ResetAt   time.Time
}

// slidingStore 抽象 Guard 依赖的滑动窗口计数能力，便于单测注入。
type slidingStore interface {
	// CheckAndAdd 严格门槛：sum+amount>limit 即拒（用于 RPM/RPD 请求计数维度）。
	CheckAndAdd(ctx context.Context, subject string, limit int64, window, bucket time.Duration, amount int64) (CountResult, error)
	// CheckThenAdd 准入门槛：仅当进入前 sum>=limit 才拒（用于 TPM token 维度，单条大请求不因自身预估超限被拒）。
	CheckThenAdd(ctx context.Context, subject string, limit int64, window, bucket time.Duration, amount int64) (CountResult, error)
	Add(ctx context.Context, subject string, window, bucket time.Duration, delta int64) error
}

// Guard 在 API Key 与 channel 两层执行 RPM/TPM/RPD 限流，并支持 TPM 预估占用后的实际用量回填。
type Guard struct {
	store    slidingStore
	defaults DefaultLimits
	failOpen bool
	logger   *slog.Logger
}

// NewGuard 创建限流 Guard。failOpen 为 true 时计数后端故障放行（仅记录告警），否则故障即拒绝。
func NewGuard(store slidingStore, defaults DefaultLimits, failOpen bool, logger *slog.Logger) *Guard {
	return &Guard{
		store:    store,
		defaults: defaults,
		failOpen: failOpen,
		logger:   logger,
	}
}

// AllowRouteUserRequest 检查「线路+用户」的 RPM 与 RPD（请求计数维度），任一超限即拒绝；
// 返回 RPM 维度判定供响应头使用。同一用户在该线路下的所有 Key 共享同一计数桶（DEC-027）。
func (g *Guard) AllowRouteUserRequest(ctx context.Context, routeID, userID int64, limits Limits) (Decision, error) {
	subjectRPM := routeUserSubject(routeID, userID, DimensionRPM)
	rpm := effectiveLimit(limits.RPM, g.defaults.RPM)
	rpmDecision, err := g.checkSubject(ctx, subjectRPM, DimensionRPM, rpm, rpmWindow, rpmBucket, 1, gateHard)
	if err != nil || !rpmDecision.Allowed {
		return rpmDecision, err
	}

	subjectRPD := routeUserSubject(routeID, userID, DimensionRPD)
	rpd := effectiveLimit(limits.RPD, g.defaults.RPD)
	rpdDecision, err := g.checkSubject(ctx, subjectRPD, DimensionRPD, rpd, rpdWindow, rpdBucket, 1, gateHard)
	if err != nil {
		return rpdDecision, err
	}
	if !rpdDecision.Allowed {
		return rpdDecision, nil
	}

	return rpmDecision, nil
}

// AllowRouteUserTokens 按预估 token 数对「线路+用户」的 TPM 做准入检查并占用，用于上游调用前。
//
// 采用「准入门槛」（DEC-028）：只要窗口已用量未达上限即放行，本次预估无论多大都不挡——避免 Codex 每轮
// 重发大缓存上下文时，单条保守预占自身 ≥ 上限而「一说话就 429」。占用仍按预估计入，结算回填退回缓存部分。
func (g *Guard) AllowRouteUserTokens(ctx context.Context, routeID, userID int64, limits Limits, estTokens int64) (Decision, error) {
	tpm := effectiveLimit(limits.TPM, g.defaults.TPM)
	return g.checkSubject(ctx, routeUserSubject(routeID, userID, DimensionTPM), DimensionTPM, tpm, tpmWindow, tpmBucket, nonNegative(estTokens), gateAdmit)
}

// AllowChannel 检查 channel 的 RPM/RPD/TPM，用于上游调用前；命中任一维度即拒绝（上层据此跳过该候选 fallback）。
// RPM/RPD 用严格门槛；TPM 用准入门槛（同 AllowRouteUserTokens，单条大请求不因自身预估超限被跳过）。
func (g *Guard) AllowChannel(ctx context.Context, channelID int64, limits Limits, estTokens int64) (Decision, error) {
	rpm := effectiveLimit(limits.RPM, g.defaults.RPM)
	if decision, err := g.check(ctx, ScopeChannel, channelID, DimensionRPM, rpm, rpmWindow, rpmBucket, 1, gateHard); err != nil || !decision.Allowed {
		return decision, err
	}

	rpd := effectiveLimit(limits.RPD, g.defaults.RPD)
	if decision, err := g.check(ctx, ScopeChannel, channelID, DimensionRPD, rpd, rpdWindow, rpdBucket, 1, gateHard); err != nil || !decision.Allowed {
		return decision, err
	}

	tpm := effectiveLimit(limits.TPM, g.defaults.TPM)
	return g.check(ctx, ScopeChannel, channelID, DimensionTPM, tpm, tpmWindow, tpmBucket, nonNegative(estTokens), gateAdmit)
}

// TokensEnforced 报告某主体的 TPM 是否实际生效（>0），供调用方决定是否需要结算回填。
func (g *Guard) TokensEnforced(limits Limits) bool {
	return effectiveLimit(limits.TPM, g.defaults.TPM) > 0
}

// BackfillRouteUserTokens 在结算拿到真实 token 用量后，按 (actual-est) 修正「线路+用户」的 TPM 计数（delta 可为负）。
func (g *Guard) BackfillRouteUserTokens(ctx context.Context, routeID, userID int64, delta int64) {
	g.backfillSubject(ctx, routeUserSubject(routeID, userID, DimensionTPM), delta)
}

// BackfillChannelTokens 在结算拿到真实 token 用量后，按 (actual-est) 修正 channel 的 TPM 计数（delta 可为负）。
func (g *Guard) BackfillChannelTokens(ctx context.Context, channelID int64, delta int64) {
	g.backfillSubject(ctx, subjectFor(ScopeChannel, channelID, DimensionTPM), delta)
}

func (g *Guard) backfillSubject(ctx context.Context, subject string, delta int64) {
	if delta == 0 {
		return
	}
	if err := g.store.Add(ctx, subject, tpmWindow, tpmBucket, delta); err != nil && g.logger != nil {
		args := []any{"subject", subject, "delta", delta}
		args = append(args, failure.LogArgs(err)...)
		g.logger.Warn("rate limit tpm backfill failed", args...)
	}
}

// gateMode 选择限流门槛语义：hard=严格（sum+amount>limit 拒，用于 RPM/RPD 请求计数）；
// admit=准入（进入前 sum>=limit 才拒，用于 TPM token 维度，单条大请求不因自身预估超限被拒，DEC-028）。
type gateMode int

const (
	gateHard gateMode = iota
	gateAdmit
)

// check 对单一维度执行检查并占用 amount（按 scope+id 构造 subject）。limit<=0 视为不限：直接放行且不计数。
func (g *Guard) check(ctx context.Context, scope Scope, id int64, dim string, limit int64, window, bucket time.Duration, amount int64, gate gateMode) (Decision, error) {
	return g.checkSubject(ctx, subjectFor(scope, id, dim), dim, limit, window, bucket, amount, gate)
}

// checkSubject 对给定 subject 的单一维度执行检查并占用 amount。limit<=0 视为不限：直接放行且不计数。
func (g *Guard) checkSubject(ctx context.Context, subject, dim string, limit int64, window, bucket time.Duration, amount int64, gate gateMode) (Decision, error) {
	if limit <= 0 {
		return Decision{Allowed: true, Dimension: dim, Limit: 0}, nil
	}

	var (
		result CountResult
		err    error
	)
	switch gate {
	case gateAdmit:
		result, err = g.store.CheckThenAdd(ctx, subject, limit, window, bucket, amount)
	default:
		result, err = g.store.CheckAndAdd(ctx, subject, limit, window, bucket, amount)
	}
	if err != nil {
		return g.onStoreError(subject, dim, err)
	}

	remaining := limit - result.Count
	if remaining < 0 {
		remaining = 0
	}

	return Decision{
		Allowed:   result.Allowed,
		Dimension: dim,
		Limit:     limit,
		Remaining: remaining,
		ResetAt:   result.ResetAt,
	}, nil
}

// onStoreError 按 failOpen 策略处理计数后端故障。
func (g *Guard) onStoreError(subject, dim string, err error) (Decision, error) {
	if g.failOpen {
		if g.logger != nil {
			args := []any{"subject", subject, "dimension", dim}
			args = append(args, failure.LogArgs(err)...)
			g.logger.Warn("rate limit store failed; failing open", args...)
		}
		return Decision{Allowed: true, Dimension: dim}, nil
	}
	return Decision{}, failure.Wrap(
		failure.CodeRateLimitStoreFailed,
		err,
		failure.WithMessage("rate limit counter"),
	)
}

func effectiveLimit(override *int64, def int64) int64 {
	if override != nil {
		return *override
	}
	return def
}

func nonNegative(v int64) int64 {
	if v < 0 {
		return 0
	}
	return v
}

func subjectFor(scope Scope, id int64, dim string) string {
	return string(scope) + ":" + strconv.FormatInt(id, 10) + ":" + dim
}

// routeUserSubject 构造「线路+用户」复合计数主体：ru:<routeID>:<userID>:<dim>。
// 同一用户在该线路下的所有 Key 共享此桶（多建 Key 不放大配额）；不同用户各自独立（DEC-027）。
func routeUserSubject(routeID, userID int64, dim string) string {
	return string(ScopeRouteUser) + ":" + strconv.FormatInt(routeID, 10) + ":" + strconv.FormatInt(userID, 10) + ":" + dim
}
