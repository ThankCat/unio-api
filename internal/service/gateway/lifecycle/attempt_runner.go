package lifecycle

import (
	"context"
	"errors"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
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
// 之后的尝试链路。OpenAI（chat completions / responses）与 Anthropic Messages 均已接入
// （非流式走 RunNonStream，流式走 RunStreamGeneric）。
type AttemptRunner struct {
	lifecycle       *RequestLifecycle
	retryClassifier RetryClassifier
	settlement      ChatSettlementExecutor
	permitManager   *AttemptPermitManager

	// headWait 保留 sticky 队首短等配置源，供 permit 容量重试策略使用。
	// nil 或 SampleHeadWait=0 表示不等，命中满员立即 failover。
	headWait *StickyRouter

	// logger 可选：记录 skip / waited_ms / 候选切换（大 uncache 缺口可观测）。
	logger *zap.Logger
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

// SetAttemptPermitManager 注入候选级全局准入。生产 generation 路径必须注入共享 manager。
// nil 仅保留给不经过 HTTP request-admission wrapper 的直接 service 单元测试/维护调用。
func (r *AttemptRunner) SetAttemptPermitManager(manager *AttemptPermitManager) {
	if r == nil {
		return
	}
	r.permitManager = manager
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

// NonStreamEndpointResolver selects the concrete upstream transport for one candidate.
// A nil resolver preserves the request lifecycle's default endpoint.
type NonStreamEndpointResolver func(candidate routing.ChatRouteCandidate) requestlog.UpstreamEndpoint

// NonStreamTransparentFallback describes one optional second transport on the same routing
// candidate. It is intentionally narrow: the first attempt is already terminal before Match is
// evaluated, and the second transport must pass a fresh permit admission and create a new attempt.
type NonStreamTransparentFallback struct {
	Match             func(candidate routing.ChatRouteCandidate, err error) bool
	UpstreamEndpoint requestlog.UpstreamEndpoint
	ResolveAdapter    ResolveAdapter
	Invoke            NonStreamInvoke
}

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

	// EndpointForCandidate overrides the default upstream endpoint for the primary transport.
	// TransparentFallback, when matched, performs exactly one separately admitted transport on the
	// same candidate. Compact Native 404/405 -> Synthetic is the only current consumer.
	EndpointForCandidate NonStreamEndpointResolver
	TransparentFallback   *NonStreamTransparentFallback

	// EstimatedTokens 是本请求保守预估的输入 token 数，用于 TPM 限流的上游调用前预占（P2-8）。
	// 结算后由 runner 按真实 billable token 回填差额。0 表示不参与 TPM 预占（仍走 RPM/RPD）。
	EstimatedTokens int64

	// UpstreamCostWithoutUsage 在 Invoke 返回的错误代表「上游可能已产生成本但拿不到可靠 usage」时返回 true。
	// 命中时 runner 既不重试（避免再调上游叠加成本）也不普通释放，而是释放冻结并记 risk_exposure（账务异常），
	// 杜绝静默白嫖（典型：原生 responses compact 2xx 缺 usage，P0-3）。nil 表示不启用该分类。
	UpstreamCostWithoutUsage func(err error) bool

	// Sticky 是本请求的会话粘性上下文（大 uncache 缺口 P0）：attempt 成功后 bind/改绑，
	// 粘住渠道熔断跳过时清绑定。nil 表示本请求不粘（方法 nil-safe）。
	Sticky *StickySession
}

// RunResult 汇报候选循环最终的业务 outcome，供协议 service 的 metrics defer 读取。
type RunResult struct {
	Outcome         metrics.ChatOutcome
	Attempts        int
	RoutingFallback bool
	TransportChain  []TransportAttempt

	// Delivery is set only when a non-stream request has settled successfully.
	// The HTTP handler owns its single terminal transition after the JSON write.
	Delivery DeliveryFinalizer
}

// TransportAttempt is one persisted attempt that proceeds to a real upstream
// transport. Admission-denied candidates are intentionally absent from this chain.
type TransportAttempt struct {
	ChannelID         int64                        `json:"channel_id"`
	UpstreamEndpoint requestlog.UpstreamEndpoint `json:"upstream_endpoint"`
}

func (r *RunResult) recordTransportAttempt(candidate routing.ChatRouteCandidate, endpoint requestlog.UpstreamEndpoint) {
	r.Attempts++
	r.TransportChain = append(r.TransportChain, TransportAttempt{
		ChannelID:         candidate.Channel.ID,
		UpstreamEndpoint: endpoint,
	})
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

	var lastErr error
	denials := attemptDenialSummary{capacityOnly: true}
	headWaitUsed := false

	for candIdx, prepared := range params.Candidates {
		index := prepared.RouteIndex
		candidate := prepared.Route
		endpoint := l.upstreamEndpoint()
		if params.EndpointForCandidate != nil {
			endpoint = params.EndpointForCandidate(candidate)
		}

		// adapter lookup 是本地、无副作用步骤，必须先于 Redis 候选准入。
		if params.ResolveAdapter != nil {
			if err := params.ResolveAdapter(candidate); err != nil {
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				l.MarkRequestFailed(ctx, requestRecord, "adapter_not_registered", err)
				return result, err
			}
		}

		var permitOwner *AttemptPermitOwner
		if r.permitManager != nil {
			admission, owner, err := r.acquireAttemptWithHeadWait(ctx, AttemptPermitAcquireParams{
				Candidate:            candidate,
				UpstreamEndpoint:    endpoint,
				RequestMode:          breakerstore.ModeNonStream,
				EstimatedInputTokens: params.EstimatedTokens,
			}, candIdx == 0, &headWaitUsed)
			if err != nil {
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				l.MarkRequestFailed(ctx, requestRecord, RoutingFailureCode(err), err)
				return result, err
			}
			if admission.Mode == breakerstore.AdmissionDenied {
				if admission.Reason == breakerstore.ReasonBreakerStoreUnavailable {
					err := failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("attempt admission store unavailable"))
					if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}
					l.MarkRequestFailed(ctx, requestRecord, string(failure.CodeGatewayBreakerStoreUnavailable), err)
					return result, err
				}
				denials.Record(admission.Reason)
				skipReason := attemptDeniedSkipReason(admission.Reason)
				r.recordRoutingSkip(skipReason)
				r.logRouting(ctx, "routing candidate skipped",
					zap.Int64("channel_id", candidate.Channel.ID),
					zap.String("skip_reason", skipReason),
				)
				if admission.Reason == breakerstore.ReasonOpen || admission.Reason == breakerstore.ReasonHalfOpenBusy {
					params.Sticky.ClearIfBound(ctx, candidate.Channel.ID)
				}
				if candIdx+1 < len(params.Candidates) {
					result.RoutingFallback = true
					l.RecordBalanceFallback(routeIDOf(params.Principal), skipReason)
				}
				continue
			}
			permitOwner = owner
			// Arm the terminal fallback before attempt persistence or any other fallible work.
			// Abort is first-terminal-wins, so this is a no-op after the normal Finish/Abort path.
			defer abortAttemptPermitOnExit(ctx, permitOwner)
		}

		// permit 成功后才创建 attempt；创建失败必须 Abort 精确归还全部候选资源。
		attemptRecord, err := l.CreateAttemptForEndpoint(
			ctx,
			requestRecord,
			result.Attempts,
			index,
			candidate,
			endpoint,
		)
		if err != nil {
			if permitOwner != nil {
				_ = permitOwner.Abort(ctx)
			}
			if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
				l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
				return result, releaseErr
			}
			l.MarkRequestFailed(ctx, requestRecord, "request_attempt_create_failed", err)
			return result, err
		}
		result.recordTransportAttempt(candidate, endpoint)

		upstreamStart := time.Now()
		success, err := r.invokeNonStreamAttempt(ctx, candidate, attemptRecord, permitOwner, params.Invoke)
		l.RecordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
		l.RecordCredentialResult(candidate, err)
		if errors.Is(err, ErrAttemptRuntimeFeedback) || errors.Is(err, errAttemptPermitFinish) {
			l.MarkAttemptFailed(ctx, attemptRecord, FailureCodeOrFallback(err, string(failure.CodeGatewayBreakerStoreUnavailable)), err)
			if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
				l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
				return result, releaseErr
			}
			l.MarkRequestFailed(ctx, requestRecord, RoutingFailureCode(err), err)
			return result, err
		}

		// A transparent fallback is a second real transport, not an adapter-internal retry. The first
		// permit has already reached Finish/Abort inside invokeNonStreamAttempt. Only after recording
		// that attempt do we resolve and freshly admit the second endpoint.
		fallback := params.TransparentFallback
		if err != nil && fallback != nil && fallback.Match != nil && fallback.Match(candidate, err) {
			result.RoutingFallback = true
			l.MarkAttemptFailed(ctx, attemptRecord, "adapter_error", err)
			l.RecordCostExposure(ctx, requestRecord, attemptRecord, candidate, params.EstimatedTokens, err)

			if fallback.ResolveAdapter != nil {
				if resolveErr := fallback.ResolveAdapter(candidate); resolveErr != nil {
					if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}
					l.MarkRequestFailed(ctx, requestRecord, "adapter_not_registered", resolveErr)
					return result, resolveErr
				}
			}

			var fallbackOwner *AttemptPermitOwner
			if r.permitManager != nil {
				admission, owner, acquireErr := r.acquireAttemptWithHeadWait(ctx, AttemptPermitAcquireParams{
					Candidate:            candidate,
					UpstreamEndpoint:    fallback.UpstreamEndpoint,
					RequestMode:          breakerstore.ModeNonStream,
					EstimatedInputTokens: params.EstimatedTokens,
				}, candIdx == 0, &headWaitUsed)
				if acquireErr != nil {
					if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}
					l.MarkRequestFailed(ctx, requestRecord, RoutingFailureCode(acquireErr), acquireErr)
					return result, acquireErr
				}
				if admission.Mode == breakerstore.AdmissionDenied {
					if admission.Reason == breakerstore.ReasonBreakerStoreUnavailable {
						storeErr := failure.New(failure.CodeGatewayBreakerStoreUnavailable, failure.WithMessage("attempt admission store unavailable"))
						if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
							l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
							return result, releaseErr
						}
						l.MarkRequestFailed(ctx, requestRecord, string(failure.CodeGatewayBreakerStoreUnavailable), storeErr)
						return result, storeErr
					}
					denials.Record(admission.Reason)
					skipReason := attemptDeniedSkipReason(admission.Reason)
					r.recordRoutingSkip(skipReason)
					r.logRouting(ctx, "routing candidate transparent fallback skipped",
						zap.Int64("channel_id", candidate.Channel.ID),
						zap.String("skip_reason", skipReason),
					)
					if admission.Reason == breakerstore.ReasonOpen || admission.Reason == breakerstore.ReasonHalfOpenBusy {
						params.Sticky.ClearIfBound(ctx, candidate.Channel.ID)
					}
					if candIdx+1 < len(params.Candidates) {
						l.RecordBalanceFallback(routeIDOf(params.Principal), skipReason)
					}
					continue
				}
				fallbackOwner = owner
				// Compact's synthetic fallback owns a second permit and needs the same panic/early-return guard.
				defer abortAttemptPermitOnExit(ctx, fallbackOwner)
			}

			fallbackAttempt, createErr := l.CreateAttemptForEndpoint(
				ctx,
				requestRecord,
				result.Attempts,
				index,
				candidate,
				fallback.UpstreamEndpoint,
			)
			if createErr != nil {
				if fallbackOwner != nil {
					_ = fallbackOwner.Abort(ctx)
				}
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				l.MarkRequestFailed(ctx, requestRecord, "request_attempt_create_failed", createErr)
				return result, createErr
			}
			result.recordTransportAttempt(candidate, fallback.UpstreamEndpoint)
			attemptRecord = fallbackAttempt

			upstreamStart = time.Now()
			success, err = r.invokeNonStreamAttempt(ctx, candidate, attemptRecord, fallbackOwner, fallback.Invoke)
			l.RecordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
			l.RecordCredentialResult(candidate, err)
			if errors.Is(err, ErrAttemptRuntimeFeedback) || errors.Is(err, errAttemptPermitFinish) {
				l.MarkAttemptFailed(ctx, attemptRecord, FailureCodeOrFallback(err, string(failure.CodeGatewayBreakerStoreUnavailable)), err)
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				l.MarkRequestFailed(ctx, requestRecord, RoutingFailureCode(err), err)
				return result, err
			}
		}
		if err != nil {
			// 客户端取消不是上游失败，也不触发 fallback；此时还没进入 settlement，不写账务。
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				// bill-on-disconnect 渠道：请求已发出、上游照常生成并计费，记平台成本敞口（阶段一）。
				l.RecordCostExposure(ctx, requestRecord, attemptRecord, candidate, params.EstimatedTokens, err)
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				result.Outcome = metrics.ChatOutcomeCanceled
				l.MarkRequestCanceled(ctx, requestRecord, attemptRecord, err)
				return result, err
			}

			l.MarkAttemptFailed(ctx, attemptRecord, "adapter_error", err)

			// bill-on-disconnect 渠道的 timeout/5xx：上游可能已生成并计费，记平台成本敞口（阶段一）。
			l.RecordCostExposure(ctx, requestRecord, attemptRecord, candidate, params.EstimatedTokens, err)

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
			if candIdx+1 < len(params.Candidates) {
				result.RoutingFallback = true
				category, _ := adapter.UpstreamCategoryOf(err)
				l.RecordBalanceFallback(routeIDOf(params.Principal), "upstream_"+string(category))
			}
			lastErr = err
			continue
		}

		l.RecordRoutingSelected(candidate.ProviderID, candidate.Channel.ID, params.RequestedModelID)
		l.RecordBalanceSelected(routeIDOf(params.Principal), candidate.Channel.ID)

		// 非流式成功请求的账务事实必须在 settlement 事务内一起提交，不能先返回响应再异步扣费。
		settleCtx, settleSpan := StartGatewaySpan(ctx, "gateway.settlement")
		settleErr := r.settlement.SettleSuccessfulChat(settleCtx, ChatSettlementParams{
			RequestRecord:           requestRecord,
			AttemptRecord:           attemptRecord,
			Principal:               params.Principal,
			Authorization:           authorization,
			ResponseProtocol:        params.ResponseProtocol,
			ResponseID:              success.ResponseID,
			ResponseModelID:         params.RequestedModelID,
			ResponseStartedAt:       nil,
			ModelDBID:               candidate.ModelDBID,
			FinalProviderID:         candidate.ProviderID,
			FinalChannelID:          candidate.Channel.ID,
			ChannelPriceID:          candidate.ChannelPriceID,
			CostBaseModelPriceID:    candidate.CostBaseModelPriceID,
			ChannelCostMultiplierID: candidate.ChannelCostMultiplierID,
			ChannelRechargeFactorID: candidate.ChannelRechargeFactorID,
			SalePrice:               candidate.SalePrice,
			PriceRatio:              candidate.PriceRatio,
			LongContextPolicy:       candidate.LongContextPolicy,
			Facts:                   success.Facts,
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

		// 零价渠道误配监控（P2-4）：售价快照全部非正即客户侧 $0 收入，记指标供运维定位误配渠道。
		if candidate.SalePrice.IsEffectivelyFree() {
			l.RecordZeroPriceServed(candidate.ProviderID, candidate.Channel.ID, params.RequestedModelID)
		}

		// attempt 成功：sticky bind/改绑（决议 2）。跳过/失败候选不会走到这里，天然不覆盖绑定。
		params.Sticky.BindSuccess(ctx, candidate.Channel.ID)

		result.Outcome = metrics.ChatOutcomeSuccess
		result.Delivery = l.NewNonStreamDeliveryFinalizer(ctx, requestRecord)
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

	if denials.seen {
		if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
			l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
			return result, releaseErr
		}
		err := denials.FinalError()
		l.MarkRequestFailed(ctx, requestRecord, RoutingFailureCode(err), err)
		return result, err
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
