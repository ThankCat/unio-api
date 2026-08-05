package lifecycle

import (
	"strconv"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/tpmobserver"
)

// TPMAttemptScope 是一次 attempt 的 TPM 观测归属（§8.1）。
//
// Route 维度按客户请求聚合，Channel 维度按单个 attempt 聚合：同一请求 fallback 到第二条渠道时，
// Route 输入仍然只记一次，Channel 输入按真实 attempt 各记一次。
// RouteID 来自 permit（由 request admission token 注入），为 0 时只观测 Channel 维度。
type TPMAttemptScope struct {
	RouteID          int64
	RequestID        int64
	RequestStartedAt time.Time
	ChannelID        int64
	AttemptID        int64
	InputEstimate    int64
}

func (s TPMAttemptScope) route() (tpmobserver.Scope, bool) {
	if s.RouteID <= 0 || s.RequestID <= 0 {
		return tpmobserver.Scope{}, false
	}
	return tpmobserver.Scope{
		Kind: breakerstore.TPMScopeRoute,
		ID:   s.RouteID,
		Key:  strconv.FormatInt(s.RequestID, 10),
	}, true
}

func (s TPMAttemptScope) channel() (tpmobserver.Scope, bool) {
	if s.ChannelID <= 0 || s.AttemptID <= 0 {
		return tpmobserver.Scope{}, false
	}
	return tpmobserver.Scope{
		Kind: breakerstore.TPMScopeChannel,
		ID:   s.ChannelID,
		Key:  strconv.FormatInt(s.AttemptID, 10),
	}, true
}

// SetTPMObserver 注入分钟级 TPM 观测器（§8）；nil 表示不观测。
func (l *RequestLifecycle) SetTPMObserver(observer *tpmobserver.Observer) {
	if l != nil {
		l.tpmObserver = observer
	}
}

// newTPMAttemptScope 组装一次 attempt 的观测归属。owner 为 nil（未启用 permit 的装配）时
// 拿不到 RouteID，此时只观测 Channel 维度。
func (l *RequestLifecycle) newTPMAttemptScope(
	request requestlog.RequestRecord,
	attempt requestlog.AttemptRecord,
	candidate routing.ChatRouteCandidate,
	owner *AttemptPermitOwner,
	inputEstimate int64,
) TPMAttemptScope {
	return TPMAttemptScope{
		RouteID:          owner.RouteID(),
		RequestID:        request.ID,
		RequestStartedAt: request.StartedAt,
		ChannelID:        candidate.Channel.ID,
		AttemptID:        attempt.ID,
		InputEstimate:    inputEstimate,
	}
}

// ObserveAttemptInput 记录一次真实上游调用的输入观测。
//
// Channel 输入归入 transport 开始分钟，Route 输入归入客户请求开始分钟，两者取值相同——
// 都是这个 attempt 真正发出去的输入估算，而不是全候选里最大的保守估算。
// 调用是幂等的：响应头钩子与 attempt 终态补记都会调它，同一 scope 只有第一次生效。
func (l *RequestLifecycle) ObserveAttemptInput(scope TPMAttemptScope, upstreamStartedAt time.Time) {
	if l == nil || l.tpmObserver == nil || scope.InputEstimate <= 0 || upstreamStartedAt.IsZero() {
		return
	}
	if channelScope, ok := scope.channel(); ok {
		l.tpmObserver.Input(channelScope, upstreamStartedAt, scope.InputEstimate)
	}
	if routeScope, ok := scope.route(); ok {
		at := scope.RequestStartedAt
		if at.IsZero() {
			at = upstreamStartedAt
		}
		l.tpmObserver.Input(routeScope, at, scope.InputEstimate)
	}
}

// ObserveChannelOutput 记录一个已经从上游解析出来的输出增量。
// 它先于客户交付发生：客户端提前断开时 Channel 会比 Route 多记一点已产生但未交付的输出。
func (l *RequestLifecycle) ObserveChannelOutput(scope TPMAttemptScope, at time.Time, tokens int64) {
	if l == nil || l.tpmObserver == nil || tokens <= 0 {
		return
	}
	if channelScope, ok := scope.channel(); ok {
		l.tpmObserver.Output(channelScope, at, tokens)
	}
}

// ObserveRouteOutput 记录一个已经确认写给客户的输出增量。
func (l *RequestLifecycle) ObserveRouteOutput(scope TPMAttemptScope, at time.Time, tokens int64) {
	if l == nil || l.tpmObserver == nil || tokens <= 0 {
		return
	}
	if routeScope, ok := scope.route(); ok {
		l.tpmObserver.Output(routeScope, at, tokens)
	}
}

// FinalizeTPMObservation 用本次结算认可的 usage 收口两个维度的观测。
//
// partial estimate 不是上游真实 usage，不能用来修正桶：那种情况只增加 missing_usage_count，
// 已经观察到的估算与对应 provisional 原样保留。
func (l *RequestLifecycle) FinalizeTPMObservation(
	scope TPMAttemptScope,
	at time.Time,
	facts *adapter.ResponseFacts,
) {
	if l == nil || l.tpmObserver == nil {
		return
	}
	reliable := facts != nil && !facts.UsageSource.IsPartialEstimate()
	var usageFacts = usageFactsOf(facts)
	if channelScope, ok := scope.channel(); ok {
		l.tpmObserver.Finalize(channelScope, at, usageFacts, reliable)
	}
	if routeScope, ok := scope.route(); ok {
		l.tpmObserver.Finalize(routeScope, at, usageFacts, reliable)
	}
}
