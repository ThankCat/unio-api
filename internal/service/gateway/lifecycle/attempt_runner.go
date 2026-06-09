package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
)

// AttemptRunner 驱动协议无关的候选 fallback 计费循环。
//
// 它把 OpenAI 协议族（chat completions / responses）原本逐字复制的「routing 后候选循环」
// （breaker 跳过 → 建 attempt → 解析 adapter → 上游调用 → 错误分类/release/fallback →
// settlement → request 状态推进 → metrics outcome）收口到一处。协议差异通过 typed 闭包注入
// （ResolveAdapter / Invoke），lifecycle 只消费 AttemptSuccess 事实与稳定错误，不接触协议 DTO。
//
// 不持有 router / registry / candidates / authorizer：候选准备与 authorization 仍由协议 service
// 在进入循环前完成（它们依赖 typed 请求做 tokenizer 估算），AttemptRunner 只接管 authorization
// 之后的尝试链路。Anthropic Messages 暂不接入（保留自身循环）。
type AttemptRunner struct {
	lifecycle       *RequestLifecycle
	retryClassifier RetryClassifier
	settlement      ChatSettlementExecutor
}

// NewAttemptRunner 构造候选循环驱动。retryClassifier 为 nil 时保守地不重试。
func NewAttemptRunner(lc *RequestLifecycle, retryClassifier RetryClassifier, settlement ChatSettlementExecutor) *AttemptRunner {
	if lc == nil {
		panic("lifecycle: attempt runner requires request lifecycle")
	}
	if settlement == nil {
		panic("lifecycle: attempt runner requires chat settlement executor")
	}
	if retryClassifier == nil {
		retryClassifier = NeverRetryClassifier{}
	}
	return &AttemptRunner{lifecycle: lc, retryClassifier: retryClassifier, settlement: settlement}
}

// AttemptSuccess 是一次成功上游调用交给 settlement 的协议无关事实。
//
// ResponseID 是写入 request_records 的响应 ID（chat 用 adapter response id）；
// Facts 是 adapter 同次解析产生的不可变结算事实。
type AttemptSuccess struct {
	ResponseID string
	Facts      adapter.ResponseFacts
}

// ResolveAdapter 在 timed 上游调用前解析候选对应的 typed adapter。
//
// 返回非 nil 错误表示该候选 adapter 不可用（如未注册）：这是 fatal 配置错误，不计入上游指标、
// 不触发 fallback。协议 service 在闭包内把解析到的 typed adapter 捕获到调用方作用域，供 Invoke 使用。
type ResolveAdapter func(candidate routing.ChatRouteCandidate) error

// NonStreamInvoke 执行一次 timed 非流式上游调用。
//
// 协议 service 在闭包内持 typed request/response：成功时把 typed response 捕获到调用方作用域
// 供后续映射，并返回结算所需 AttemptSuccess；失败时返回稳定错误交由 runner 分类。
type NonStreamInvoke func(ctx context.Context, candidate routing.ChatRouteCandidate) (AttemptSuccess, error)

// RunNonStreamParams 是驱动一次非流式候选 fallback 循环所需的协议无关参数。
type RunNonStreamParams struct {
	RequestRecord    requestlog.RequestRecord
	Principal        *auth.APIKeyPrincipal
	Authorization    ChatAuthorization
	Candidates       []Candidate
	RequestedModelID string
	ResponseProtocol requestlog.Protocol
	// RequiredCapabilities 是 ingress 推断的所需能力 key，写入每个 attempt 的 capability 审计快照（可空）。
	RequiredCapabilities []string
	ResolveAdapter       ResolveAdapter
	Invoke               NonStreamInvoke
}

// RunResult 汇报候选循环最终的业务 outcome，供协议 service 的 metrics defer 读取。
type RunResult struct {
	Outcome metrics.ChatOutcome
}

// RunNonStream 执行 authorization 之后的非流式候选 fallback 循环。
//
// 成功时由协议 service 通过 Invoke 闭包捕获的 typed response 完成映射；本方法只负责 attempt
// 审计、上游/熔断指标、错误分类与 fallback、settlement 以及 request/attempt 终态写入。所有审计
// error_code 与抽取前的 chat_completion.go 保持一致，避免改变可观测事实。
func (r *AttemptRunner) RunNonStream(ctx context.Context, params RunNonStreamParams) (RunResult, error) {
	result := RunResult{Outcome: metrics.ChatOutcomeFailed}
	l := r.lifecycle
	requestRecord := params.RequestRecord
	authorization := params.Authorization

	var lastErr error

	for _, prepared := range params.Candidates {
		index := prepared.RouteIndex
		candidate := prepared.Route

		// channel 处于熔断 open 状态时直接跳过，尝试下一个同模型 channel；
		// 跳过不产生上游调用，也不写 attempt（attempt_index 允许出现空洞）。
		channelKey := MetricsID(candidate.Channel.ID)
		if !l.BreakerAllow(channelKey) {
			continue
		}

		// 每个 candidate 都先创建 attempt，再调用 adapter，保证 fallback 链路可在 request_attempts 还原。
		attemptRecord, err := l.CreateAttempt(ctx, requestRecord, index, candidate, params.RequiredCapabilities)
		if err != nil {
			if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
				l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
				return result, releaseErr
			}
			l.MarkRequestFailed(ctx, requestRecord, "request_attempt_create_failed", err)
			return result, err
		}

		if params.ResolveAdapter != nil {
			if err := params.ResolveAdapter(candidate); err != nil {
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return result, releaseErr
				}
				l.MarkAttemptFailed(ctx, attemptRecord, "adapter_not_registered", err)
				l.MarkRequestFailed(ctx, requestRecord, "adapter_not_registered", err)
				return result, err
			}
		}

		upstreamStart := time.Now()
		success, err := params.Invoke(ctx, candidate)
		l.RecordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
		l.RecordChannelHealth(channelKey, err)
		if err != nil {
			// 客户端取消不是上游失败，也不触发 fallback；此时还没进入 settlement，不写账务。
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return result, releaseErr
				}
				result.Outcome = metrics.ChatOutcomeCanceled
				l.MarkRequestCanceled(ctx, requestRecord, attemptRecord, err)
				return result, err
			}

			l.MarkAttemptFailed(ctx, attemptRecord, "adapter_error", err)

			if !r.retryClassifier.IsRetryable(err) {
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return result, releaseErr
				}
				l.MarkRequestFailed(ctx, requestRecord, "adapter_error", err)
				return result, err
			}
			lastErr = err
			continue
		}

		l.RecordRoutingSelected(candidate.ProviderID, candidate.Channel.ID, params.RequestedModelID)
		logfields.SetRoute(ctx, params.RequestedModelID, MetricsID(candidate.ProviderID), MetricsID(candidate.Channel.ID))

		// 非流式成功请求的账务事实必须在 settlement 事务内一起提交，不能先返回响应再异步扣费。
		settleCtx, settleSpan := StartGatewaySpan(ctx, "gateway.settlement")
		settleErr := r.settlement.SettleSuccessfulChat(settleCtx, ChatSettlementParams{
			RequestRecord:    requestRecord,
			AttemptRecord:    attemptRecord,
			Principal:        params.Principal,
			Authorization:    authorization,
			ResponseProtocol: params.ResponseProtocol,
			ResponseID:       success.ResponseID,
			ResponseModelID:  params.RequestedModelID,
			ModelDBID:        candidate.ModelDBID,
			FinalProviderID:  candidate.ProviderID,
			FinalChannelID:   candidate.Channel.ID,
			Facts:            success.Facts,
		})
		EndSettlementSpan(settleSpan, settleErr)
		l.RecordSettlement(SettlementOutcomeFromErr(settleErr))
		if settleErr != nil && !IsChatSettlementRecoveryScheduled(settleErr) {
			// 上游已成功（成本已产生），但 settlement 永久失败且没有 recovery job 接管（典型为 recovery
			// job 创建失败，此时内层 settlement 尚未 capture，reservation 仍停留在 authorized）。必须释放
			// 冻结余额并记账务异常风险，否则用户余额被永久冻结——GAP-7-007 只覆盖「job 已创建后重试/dead」，
			// 不覆盖「job 创建失败」窗口。release 自身幂等（captured 拒绝、released no-op），不会破坏已结算事实。
			if releaseErr := l.ReleaseAuthorizationForBillingException(
				ctx,
				authorization,
				"settlement_failed_after_upstream_success",
				"settlement permanently failed after upstream success without recovery job",
			); releaseErr != nil {
				l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
				return result, releaseErr
			}
			l.MarkRequestFailed(ctx, requestRecord, "chat_settlement_failed", settleErr)
			return result, settleErr
		}

		result.Outcome = metrics.ChatOutcomeSuccess
		return result, nil
	}

	if lastErr != nil {
		if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
			l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
			return result, releaseErr
		}
		l.MarkRequestFailed(ctx, requestRecord, "adapter_error", lastErr)
		return result, lastErr
	}

	if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
		l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
		return result, releaseErr
	}

	err := failure.Wrap(
		failure.CodeRoutingNoAvailableChannel,
		routing.ErrNoAvailableChannel,
		failure.WithMessage(routing.ErrNoAvailableChannel.Error()),
	)
	l.MarkRequestFailed(ctx, requestRecord, RoutingFailureCode(err), err)
	return result, err
}
