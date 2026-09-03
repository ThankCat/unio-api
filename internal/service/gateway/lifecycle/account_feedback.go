package lifecycle

import (
	"context"
	"errors"
	"net"
	"strings"
	"syscall"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
)

// recordAccountRuntimeFeedback 把真实上游结果按账号归因（归因分层，修订 ADR-0014）：
//
//   - 429 → 账号冷却，时长优先取上游给的重置时刻（adapter 已把 x-codex-* 重置头折算进
//     metadata.RetryAfter），解析不到用渠道 429 策略的秒级兜底；
//   - 401 / 403 → 账号临时不可调度（token_refresh），给刷新服务留窗口；确认吊销才禁用（第六节）；
//   - 持久传输错误（代理认证失败、连接拒绝、DNS 失败、无路由）→ 疑似代理故障隔离约 10 分钟；
//   - 瞬态传输错误（超时、重置、EOF）→ 只换号不处置账号；
//   - 5xx → 不在这里处理：它归 Provider/Channel breaker，由 Finish 的 outcome 通道照旧记账。
//
// 反馈失败与渠道级同语义：返回 ErrAttemptRuntimeFeedback 让 runner 终止 fallback——
// Redis 状态不确定时继续发上游请求会放大封禁风险。
func (o *AttemptPermitOwner) recordAccountRuntimeFeedback(ctx context.Context, upstreamErr error) error {
	if o.accountFeedbackStore == nil {
		return nil
	}
	category, categoryOK := adapter.UpstreamCategoryOf(upstreamErr)
	if !categoryOK {
		return nil
	}
	metadata, _ := adapter.UpstreamMetadataOf(upstreamErr)

	var feedbackErr error
	action := ""
	switch {
	case category == adapter.UpstreamErrorRateLimit && (metadata.StatusCode == 429 || metadata.StatusCode == 200):
		cooldown := metadata.RetryAfter
		if o.runtimeFeedbackPolicy != nil {
			cooldown = o.runtimeFeedbackPolicy.Resolve(metadata.RetryAfter)
		}
		durationMs := cooldown.Milliseconds()
		if durationMs <= 0 {
			return nil
		}
		action = "account_cooldown"
		opCtx, cancel := o.operationContext(ctx)
		_, feedbackErr = o.accountFeedbackStore.SetAccountCooldown(opCtx, o.permit.AccountID, durationMs, "")
		cancel()
		o.logAccountFeedback(ctx, action, durationMs, feedbackErr)
		// 429 响应头携带的最新水位（通常 100%）回写快照：管理页在冷却期内也能看到真实用量与重置时刻。
		if o.accountUsageObserver != nil && metadata.AccountUsage != nil {
			obsCtx, obsCancel := o.operationContext(ctx)
			o.accountUsageObserver.RecordAccountUsageObservation(obsCtx, o.permit.AccountID, metadata.AccountUsage)
			obsCancel()
		}

	case category == adapter.UpstreamErrorAuth || category == adapter.UpstreamErrorPermission:
		// 上游错误体带明确吊销码（token_revoked/token_invalidated，实测样本）时直接确认禁用：
		// 隔离等刷新确认是为「可能只是过期」的 401 准备的迂回，明确吊销没有恢复可能，
		// 立即禁用可少一次注定失败的刷新调用，且管理页立刻显示「需重新授权」（与 sub2api 对齐）。
		// 禁用失败（DB 抖动等）退回隔离路径，不放大故障。
		if o.accountRevocation != nil && upstreamBodyConfirmsRevocation(metadata.ResponseSnippet) {
			opCtx, cancel := o.operationContext(ctx)
			revokeErr := o.accountRevocation.MarkAccountRevoked(opCtx, o.permit.AccountID)
			cancel()
			if revokeErr == nil {
				o.logAccountFeedback(ctx, "account_confirmed_revoked", 0, nil)
				return nil
			}
			o.logAccountFeedback(ctx, "account_confirmed_revoked", 0, revokeErr)
		}
		action = "account_token_refresh_window"
		opCtx, cancel := o.operationContext(ctx)
		_, feedbackErr = o.accountFeedbackStore.MarkAccountUnschedulable(
			opCtx, o.permit.AccountID, accountAuthRefreshWindowMs, breakerstore.AccountUnschedulableTokenRefresh,
		)
		cancel()
		o.logAccountFeedback(ctx, action, accountAuthRefreshWindowMs, feedbackErr)

	case isPersistentTransportError(category, metadata.StatusCode, upstreamErr):
		action = "account_proxy_suspect"
		opCtx, cancel := o.operationContext(ctx)
		_, feedbackErr = o.accountFeedbackStore.MarkAccountUnschedulable(
			opCtx, o.permit.AccountID, accountProxySuspectWindowMs, breakerstore.AccountUnschedulableProxySuspect,
		)
		cancel()
		o.logAccountFeedback(ctx, action, accountProxySuspectWindowMs, feedbackErr)

	default:
		return nil
	}
	if feedbackErr == nil {
		return nil
	}
	return failure.Wrap(
		failure.CodeGatewayBreakerStoreUnavailable,
		errors.Join(ErrAttemptRuntimeFeedback, normalizeAttemptStoreError(feedbackErr), upstreamErr),
		failure.WithMessage("account runtime feedback store unavailable"),
	)
}

func (o *AttemptPermitOwner) logAccountFeedback(ctx context.Context, action string, durationMs int64, err error) {
	fields := o.logData(
		zap.Int64("channel_id", o.permit.ChannelID),
		zap.Int64("account_id", o.permit.AccountID),
		zap.String("action", action),
		zap.Int64("duration_ms", durationMs),
	)
	if err != nil {
		fields = append(fields, zap.String("error_message", err.Error()))
		logging.Error(o.logger, "runtime", "account", "account runtime feedback failed", fields...)
		return
	}
	logging.Warn(o.logger, "runtime", "account", "account runtime feedback applied", fields...)
}

// accountRevocationCodes 是上游 401/403 错误体里的「明确吊销」错误码（实测样本 token_revoked；
// token_invalidated 与 sub2api 生产清单对齐）。命中即确认吊销，无恢复可能。
var accountRevocationCodes = []string{"token_revoked", "token_invalidated"}

// upstreamBodyConfirmsRevocation 判断上游错误体截断快照是否携带明确吊销码。
func upstreamBodyConfirmsRevocation(snippet string) bool {
	if snippet == "" {
		return false
	}
	lowered := strings.ToLower(snippet)
	for _, code := range accountRevocationCodes {
		if strings.Contains(lowered, code) {
			return true
		}
	}
	return false
}

// isPersistentTransportError 区分持久与瞬态传输错误（边界 16，【Sub2API】判据 + Go net 语义复核）：
//
//   - 持久：还没把请求送进上游就失败，且重试大概率同样失败——连接拒绝、DNS 解析失败、无路由、
//     代理认证失败（407 或 proxyconnect 阶段错误）。这些指向账号出口（代理）配置或网络环境损坏；
//   - 瞬态：超时、连接重置、EOF——上游或链路抖动，换号继续即可，不处置账号。
//
// 判定只在 status==0（未收到上游响应头）时进行：拿到了响应头说明链路是通的。
func isPersistentTransportError(category adapter.UpstreamErrorCategory, statusCode int, err error) bool {
	if statusCode != 0 || category == adapter.UpstreamErrorTimeout || category == adapter.UpstreamErrorCanceled {
		return false
	}

	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) {
		return true
	}
	if errors.Is(err, syscall.ECONNREFUSED) || errors.Is(err, syscall.EHOSTUNREACH) || errors.Is(err, syscall.ENETUNREACH) {
		return true
	}
	// 代理建连失败：net/http 把代理 CONNECT 阶段的错误包成 "proxyconnect" 前缀；
	// 代理要求认证时表现为 CONNECT 阶段的 407。两者都没有公开类型，只能按稳定文案匹配。
	message := err.Error()
	if strings.Contains(message, "proxyconnect") ||
		strings.Contains(message, "Proxy Authentication Required") {
		return true
	}
	return false
}
