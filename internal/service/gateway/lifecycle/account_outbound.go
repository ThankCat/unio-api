package lifecycle

import (
	"context"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// AccountOutbound 是一次出站所需的账号凭据与身份（permit 固化账号后按 ID 解析）。
type AccountOutbound struct {
	// AccessToken 是出站 Bearer；解析器负责「不新鲜则带锁同步刷一次再出站」（第六节）。
	AccessToken string
	// UpstreamAccountID 是上游账号标识（Codex 的 chatgpt_account_id）。
	UpstreamAccountID string
	// ProxyURL 是账号绑定出口；空串直连。
	ProxyURL string
	// FingerprintMode / FingerprintSeed 是账号指纹收敛档位与系统种子（WP4），交给 wire 派生出站设备身份。
	FingerprintMode channel.FingerprintMode
	FingerprintSeed string
	// ResponseTimeout / FirstTokenTimeout 是账号级超时覆写：nil 继承渠道（渠道再继承全局默认），
	// 0 表示不限制，正数覆写——与渠道行 response_timeout_ms / first_token_timeout_ms 同语义。
	ResponseTimeout   *time.Duration
	FirstTokenTimeout *time.Duration
}

// AccountOutboundResolver 按 permit 固化的账号 ID 解析出站凭据。
//
// 长流请求凭据只在 transport 开始时取一次，流建立后令牌过期不影响已建立的流（边界 13）。
type AccountOutboundResolver interface {
	ResolveAccountOutbound(ctx context.Context, accountID int64) (AccountOutbound, error)
}

// AccountHealthSink 消费成功传输后的账号观测（用量快照、LRU 时间、阈值暂停）。
// 观测写入 best-effort：失败不影响客户交付。
type AccountHealthSink interface {
	RecordAccountSuccess(ctx context.Context, accountID int64, usage *adapter.AccountUsageFacts)
}

// SetAccountOutboundResolver 注入账号出站凭据解析器；nil 表示进程不服务池型渠道。
func (l *RequestLifecycle) SetAccountOutboundResolver(resolver AccountOutboundResolver) {
	if l != nil {
		l.accountOutbound = resolver
	}
}

// SetAccountHealthSink 注入账号健康观测消费者；nil 表示不观测。
func (l *RequestLifecycle) SetAccountHealthSink(sink AccountHealthSink) {
	if l != nil {
		l.accountHealth = sink
	}
}

// applyAccountOutbound 把 permit 固化的账号身份注入候选的渠道运行时（adapter 对号池无感知）。
// accountID 为 0（credential 型渠道）时原样返回。
func (l *RequestLifecycle) applyAccountOutbound(
	ctx context.Context,
	candidate routing.ChatRouteCandidate,
	accountID int64,
) (routing.ChatRouteCandidate, error) {
	if accountID <= 0 {
		return candidate, nil
	}
	if l == nil || l.accountOutbound == nil {
		return candidate, accountOutboundMissingError()
	}
	outbound, err := l.accountOutbound.ResolveAccountOutbound(ctx, accountID)
	if err != nil {
		return candidate, err
	}
	candidate.Channel.APIKey = outbound.AccessToken
	candidate.Channel.Account = channel.AccountIdentity{
		ID:                accountID,
		UpstreamAccountID: outbound.UpstreamAccountID,
		ProxyURL:          outbound.ProxyURL,
		FingerprintMode:   outbound.FingerprintMode,
		FingerprintSeed:   outbound.FingerprintSeed,
	}
	// 账号级超时是三层继承的最后一层：账号显式配置（含 0=不限制）覆盖渠道/全局解析出的值。
	if outbound.ResponseTimeout != nil {
		candidate.Channel.ResponseTimeout = *outbound.ResponseTimeout
	}
	if outbound.FirstTokenTimeout != nil {
		candidate.Channel.FirstTokenTimeout = *outbound.FirstTokenTimeout
	}
	return candidate, nil
}

// accountOutboundMissingError 表示进程未注入账号凭据解析器却路由到了池型候选——装配错误。
func accountOutboundMissingError() error {
	return failure.New(
		failure.CodeRoutingCredentialResolveFailed,
		failure.WithMessage("account outbound resolver is not configured for pool channels"),
	)
}

// RecordAccountSuccess 在池型渠道的传输完整成功后上报账号观测。
func (l *RequestLifecycle) RecordAccountSuccess(ctx context.Context, accountID int64, facts *adapter.ResponseFacts) {
	if l == nil || l.accountHealth == nil || accountID <= 0 {
		return
	}
	var usage *adapter.AccountUsageFacts
	if facts != nil {
		usage = facts.AccountUsage
	}
	l.accountHealth.RecordAccountSuccess(context.WithoutCancel(ctx), accountID, usage)
}
