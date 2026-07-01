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

	// guard 是可选的两层限流 Guard（P2-8）：Key 级 TPM 预占、channel 级 RPM/RPD/TPM 预占与结算回填。
	// nil 表示未启用限流，所有 guard 调用放行，保证未注入限流的调用点零行为变化。
	guard RateLimitGuard
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
	ResolveAdapter   ResolveAdapter
	Invoke           NonStreamInvoke
	Codes            RunNonStreamCodes

	// EstimatedTokens 是本请求保守预估的输入 token 数，用于 TPM 限流的上游调用前预占（P2-8）。
	// 结算后由 runner 按真实 billable token 回填差额。0 表示不参与 TPM 预占（仍走 RPM/RPD）。
	EstimatedTokens int64

	// UpstreamCostWithoutUsage 在 Invoke 返回的错误代表「上游可能已产生成本但拿不到可靠 usage」时返回 true。
	// 命中时 runner 既不重试（避免再调上游叠加成本）也不普通释放，而是释放冻结并记 risk_exposure（账务异常），
	// 杜绝静默白嫖（典型：原生 responses compact 2xx 缺 usage，P0-3）。nil 表示不启用该分类。
	UpstreamCostWithoutUsage func(err error) bool
}

// RunResult 汇报候选循环最终的业务 outcome，供协议 service 的 metrics defer 读取。
type RunResult struct {
	Outcome metrics.ChatOutcome
}

// RunNonStreamCodes 是共享非流式候选循环里的审计 code/reason 覆盖项。
//
// 空值使用 OpenAI chat 既有默认值，保证现有调用点零改动、历史观测语义不漂移。
type RunNonStreamCodes struct {
	AuthorizationReleaseFailedCode       string
	SettlementFailedCode                 string
	SettlementBillingExceptionReasonCode string
	SettlementBillingExceptionReason     string

	// UpstreamCostWithoutUsage* 用于 UpstreamCostWithoutUsage 命中分支：request_records 终态 error_code
	// 与 risk_exposure 账务异常的 reason_code/reason。空值使用通用默认。
	UpstreamCostWithoutUsageCode       string
	UpstreamCostWithoutUsageReasonCode string
	UpstreamCostWithoutUsageReason     string
}

func (c RunNonStreamCodes) withDefaults() RunNonStreamCodes {
	if c.AuthorizationReleaseFailedCode == "" {
		c.AuthorizationReleaseFailedCode = "chat_authorization_release_failed"
	}
	if c.SettlementFailedCode == "" {
		c.SettlementFailedCode = "chat_settlement_failed"
	}
	if c.SettlementBillingExceptionReasonCode == "" {
		c.SettlementBillingExceptionReasonCode = "settlement_failed_after_upstream_success"
	}
	if c.SettlementBillingExceptionReason == "" {
		c.SettlementBillingExceptionReason = "settlement permanently failed after upstream success without recovery job"
	}
	if c.UpstreamCostWithoutUsageCode == "" {
		c.UpstreamCostWithoutUsageCode = "upstream_cost_without_usage"
	}
	if c.UpstreamCostWithoutUsageReasonCode == "" {
		c.UpstreamCostWithoutUsageReasonCode = "upstream_cost_without_usage"
	}
	if c.UpstreamCostWithoutUsageReason == "" {
		c.UpstreamCostWithoutUsageReason = "upstream call may have incurred cost but returned no reliable usage"
	}
	return c
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
	codes := params.Codes.withDefaults()

	// Key 级 TPM 预占（P2-8）：RPM/RPD 已在 ingress 中间件处理，这里只做 token 维度。
	// 命中即释放冻结并以 429 上抛；计数后端 fail_closed 故障同样释放冻结后上抛。
	if dec, allowed, err := r.guardKeyTokens(ctx, params.Principal, params.EstimatedTokens); err != nil {
		if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
			l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
			return result, releaseErr
		}
		l.MarkRequestFailed(ctx, requestRecord, string(failure.CodeRateLimitStoreFailed), err)
		return result, err
	} else if !allowed {
		if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
			l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
			return result, releaseErr
		}
		rlErr := keyTokenRateLimitError(dec)
		l.MarkRequestFailed(ctx, requestRecord, string(failure.CodeRateLimitExceeded), rlErr)
		return result, rlErr
	}

	// TPM 预占跟踪（DEC-028）：登记已实际生效的 route+user 预占，收尾时释放所有未被结算回填对账的预占
	// （失败/取消/无结算的 route+user，以及 fallback 落选/失败的候选渠道），避免额度泄漏在 TPM 窗口。
	res := &tpmReservations{}
	r.recordKeyTPMReservation(res, params.Principal, params.EstimatedTokens)
	defer r.releaseUnreconciledTPM(ctx, res)

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

		// 渠道级限流预占（P2-8）：命中任一维度即跳过该候选 fallback 到下一渠道（与熔断 open 同语义，不写 attempt）。
		// 计数后端 fail_closed 故障同样保守跳过该候选；fail_open 时 Guard 内部已放行。
		if dec, allowed, err := r.guardChannel(ctx, candidate, params.EstimatedTokens); err != nil {
			lastErr = err
			continue
		} else if !allowed {
			lastErr = channelRateLimitedError(dec)
			continue
		}

		// 该候选已通过渠道级 TPM 预占（额度已写入窗口）：登记预占，收尾时若非胜出（fallback 落选/失败）则释放。
		r.recordChannelTPMReservation(res, candidate, params.EstimatedTokens)

		// 每个 candidate 都先创建 attempt，再调用 adapter，保证 fallback 链路可在 request_attempts 还原。
		attemptRecord, err := l.CreateAttempt(ctx, requestRecord, index, candidate)
		if err != nil {
			if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
				l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
				return result, releaseErr
			}
			l.MarkRequestFailed(ctx, requestRecord, "request_attempt_create_failed", err)
			return result, err
		}

		if params.ResolveAdapter != nil {
			if err := params.ResolveAdapter(candidate); err != nil {
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				l.MarkAttemptFailed(ctx, attemptRecord, "adapter_not_registered", err)
				l.MarkRequestFailed(ctx, requestRecord, "adapter_not_registered", err)
				return result, err
			}
		}

		upstreamStart := time.Now()
		success, err := params.Invoke(ctx, candidate)
		responseStartedAt := time.Now()
		l.RecordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
		l.RecordChannelHealth(channelKey, err)
		if err != nil {
			// 客户端取消不是上游失败，也不触发 fallback；此时还没进入 settlement，不写账务。
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				result.Outcome = metrics.ChatOutcomeCanceled
				l.MarkRequestCanceled(ctx, requestRecord, attemptRecord, err)
				return result, err
			}

			l.MarkAttemptFailed(ctx, attemptRecord, "adapter_error", err)

			// 上游 429：按 Retry-After 登记渠道冷却，后续 fallback 在冷却窗口内直接跳过该渠道（P2-7）。
			l.RecordChannelRateLimit(channelKey, err)

			// 上游可能已产生成本但无可靠 usage（如原生 compact 2xx 缺 usage，P0-3）：不重试、不普通释放，
			// 而是释放冻结并记 risk_exposure，保留「平台可能承担成本」的审计事实，杜绝静默白嫖。
			// 该分支优先于 retry 分类——再尝试只会在另一渠道叠加成本。
			if params.UpstreamCostWithoutUsage != nil && params.UpstreamCostWithoutUsage(err) {
				if releaseErr := l.ReleaseAuthorizationForBillingException(
					ctx,
					authorization,
					codes.UpstreamCostWithoutUsageReasonCode,
					codes.UpstreamCostWithoutUsageReason,
				); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				l.MarkRequestFailed(ctx, requestRecord, codes.UpstreamCostWithoutUsageCode, err)
				return result, err
			}

			if !r.retryClassifier.IsRetryable(err) {
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				l.MarkRequestFailed(ctx, requestRecord, "adapter_error", err)
				return result, err
			}
			// 可重试错误切换候选：前一候选可能已在上游产生成本却不会被结算（P2-3），记指标供监控。
			l.RecordRetryableFallback(err)
			lastErr = err
			continue
		}

		l.RecordRoutingSelected(candidate.ProviderID, candidate.Channel.ID, params.RequestedModelID)
		logfields.SetRoute(ctx, params.RequestedModelID, MetricsID(candidate.ProviderID), MetricsID(candidate.Channel.ID))

		// 非流式成功请求的账务事实必须在 settlement 事务内一起提交，不能先返回响应再异步扣费。
		settleCtx, settleSpan := StartGatewaySpan(ctx, "gateway.settlement")
		settleErr := r.settlement.SettleSuccessfulChat(settleCtx, ChatSettlementParams{
			RequestRecord:     requestRecord,
			AttemptRecord:     attemptRecord,
			Principal:         params.Principal,
			Authorization:     authorization,
			ResponseProtocol:  params.ResponseProtocol,
			ResponseID:        success.ResponseID,
			ResponseModelID:   params.RequestedModelID,
			ResponseStartedAt: &responseStartedAt,
			ModelDBID:         candidate.ModelDBID,
			FinalProviderID:   candidate.ProviderID,
			FinalChannelID:    candidate.Channel.ID,
			ChannelPriceID:    candidate.ChannelPriceID,
			SalePrice:         candidate.SalePrice,
			Facts:             success.Facts,
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
				codes.SettlementBillingExceptionReasonCode,
				codes.SettlementBillingExceptionReason,
			); releaseErr != nil {
				l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
				return result, releaseErr
			}
			l.MarkRequestFailed(ctx, requestRecord, codes.SettlementFailedCode, settleErr)
			return result, settleErr
		}

		// 结算成功后按真实 billable token 回填 Key/channel 的 TPM 计数差额（P2-8），并标记该 route+user 与
		// 胜出 channel 的预占已对账——收尾释放不再回退它们（其余落选候选仍会被释放，DEC-028）。
		r.backfillRateTokens(ctx, params.Principal, candidate, params.EstimatedTokens, success.Facts.Usage)
		res.markReconciled(candidate.Channel.ID)

		// 零价渠道误配监控（P2-4）：售价快照全部非正即客户侧 $0 收入，记指标供运维定位误配渠道。
		if candidate.SalePrice.IsEffectivelyFree() {
			l.RecordZeroPriceServed(candidate.ProviderID, candidate.Channel.ID, params.RequestedModelID)
		}

		// 非流式成功：响应将由协议 service 在本调用返回后写出，交付视为完成（completed）。
		l.MarkDeliveryCompleted(ctx, requestRecord)

		result.Outcome = metrics.ChatOutcomeSuccess
		return result, nil
	}

	if lastErr != nil {
		if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
			l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
			return result, releaseErr
		}
		l.MarkRequestFailed(ctx, requestRecord, "adapter_error", lastErr)
		return result, lastErr
	}

	if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
		l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
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
