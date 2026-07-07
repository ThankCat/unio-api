package messages

import (
	"context"
	"sync/atomic"
)

// beta.go 实现官方 1P adapter 的 anthropic-beta 头转发策略。
//
// 策略(P0-B / B,2026-07)是**运行时可配的**「透传 + 小黑名单」:默认放行,仅拦截有计费/解析缺口的 beta。
// 与 OpenAI 侧「未知字段全透传」对齐,符合「官方 1P = 忠实透传」立场,避免白名单滞后于官方
// 导致已建模能力静默失效(历史上 extended-cache-ttl 被白名单漏掉、1h 缓存降级为 5m)。
// 策略由管理端配置(见 app_settings key=anthropic.beta_policy),通过 BetaPolicyProvider 注入;
// 未注入时回退到 DefaultBetaPolicy。详见 providers/anthropic/passthrough-audit.md。
//
// 代价:客户传入的无效/上游不认的 beta 会被原样转发、由上游返回 400(显式失败,可接受),
// 而非在网关静默吞掉。

// BetaMode 是 anthropic-beta 头的转发模式。
type BetaMode string

const (
	// BetaModePassthrough 透传所有 beta(最宽松)。
	BetaModePassthrough BetaMode = "passthrough"
	// BetaModeFilter 透传,但 List(黑名单)内的不转发(默认)。
	BetaModeFilter BetaMode = "filter"
	// BetaModeWhitelist 只转发 List(白名单)内的(最严)。
	BetaModeWhitelist BetaMode = "whitelist"
)

// BetaPolicy 是一份 anthropic-beta 转发策略。
type BetaPolicy struct {
	Mode BetaMode
	// List 语义随 Mode 变化:filter=黑名单,whitelist=白名单,passthrough=忽略。
	List []string
}

// BetaPolicyProvider 提供运行时最新的 beta 策略(实现方负责 TTL 缓存等)。
type BetaPolicyProvider interface {
	BetaPolicy(ctx context.Context) BetaPolicy
}

// DefaultBetaPolicy 是未注入 provider 时的兜底策略:
// filter 模式 + 拦截 context-1m(遗留模型 >200K 分层价,channel_prices 无分层列;
// 当前模型已 GA 平价、上游忽略该头,故拦截无副作用)。
// 注:code-execution 已按「走 1(透传吸收 container 成本)」不在默认黑名单内。
func DefaultBetaPolicy() BetaPolicy {
	return BetaPolicy{
		Mode: BetaModeFilter,
		List: []string{"context-1m-2025-08-07"},
	}
}

// betaPolicyProvider 是进程级注入点(对齐 tokenest.Configure 先例);nil 时用 DefaultBetaPolicy。
var betaPolicyProvider atomic.Pointer[BetaPolicyProvider]

// SetBetaPolicyProvider 在进程启动时注入 beta 策略 provider(bootstrap 调用)。
func SetBetaPolicyProvider(p BetaPolicyProvider) {
	if p == nil {
		betaPolicyProvider.Store(nil)
		return
	}
	betaPolicyProvider.Store(&p)
}

// activeBetaPolicy 返回当前生效策略:provider 已注入取其最新值,否则用默认。
func activeBetaPolicy(ctx context.Context) BetaPolicy {
	if p := betaPolicyProvider.Load(); p != nil {
		return (*p).BetaPolicy(ctx)
	}
	return DefaultBetaPolicy()
}

// forwardableBetas 按策略返回可转发到 upstream 的 beta token,保持相对顺序、去重。
func forwardableBetas(betas []string, policy BetaPolicy) []string {
	if len(betas) == 0 {
		return nil
	}

	set := sliceToSet(policy.List)
	seen := make(map[string]struct{}, len(betas))
	out := make([]string, 0, len(betas))
	for _, beta := range betas {
		if _, dup := seen[beta]; dup {
			continue
		}
		if !betaAllowed(beta, policy.Mode, set) {
			continue
		}
		seen[beta] = struct{}{}
		out = append(out, beta)
	}

	if len(out) == 0 {
		return nil
	}

	return out
}

// blockedBetas 按策略返回 ingress beta 中不会转发 upstream 的 token(供脱敏审计)。
func blockedBetas(betas []string, policy BetaPolicy) []string {
	if len(betas) == 0 {
		return nil
	}

	set := sliceToSet(policy.List)
	var out []string
	for _, beta := range betas {
		if !betaAllowed(beta, policy.Mode, set) {
			out = append(out, beta)
		}
	}

	return out
}

// betaAllowed 判断单个 beta 在给定模式下是否转发。
func betaAllowed(beta string, mode BetaMode, list map[string]struct{}) bool {
	switch mode {
	case BetaModePassthrough:
		return true
	case BetaModeWhitelist:
		_, ok := list[beta]
		return ok
	case BetaModeFilter:
		_, blocked := list[beta]
		return !blocked
	default:
		// 未知模式按 filter 语义兜底(不因配置异常放开一切)。
		_, blocked := list[beta]
		return !blocked
	}
}

func sliceToSet(items []string) map[string]struct{} {
	set := make(map[string]struct{}, len(items))
	for _, it := range items {
		set[it] = struct{}{}
	}
	return set
}
