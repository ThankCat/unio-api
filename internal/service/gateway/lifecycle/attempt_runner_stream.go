package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
)

// StreamUpstream 执行一次 timed 上游流式调用。
//
// 协议 service 在闭包内用 typed stream adapter 发起 StreamChatCompletions，并把 runner 提供的
// onChunk 原样透传给 adapter；onChunk 负责协议无关的 id/usage/emitted 维护，再分发给协议 EmitChunk。
// 返回 adapter 同次解析的 streamFacts（可能为 nil）与稳定错误，由 runner 分类。adapter span 由协议
// 闭包自行开启/结束（与非流式 Invoke 一致）。
type StreamUpstream func(ctx context.Context, candidate routing.ChatRouteCandidate, onChunk func(chatcompletionsadapter.ChatStreamChunk) error) (*adapter.ResponseFacts, error)

// EmitStreamChunk 由协议把单个上游内容 chunk 翻译为协议 SSE 帧（chat chunk / responses 命名事件）。
//
// runner 只在「非 usage、非纯 id」的内容 chunk 上调用它，并已先行置 emitted、计数 stream started。
type EmitStreamChunk func(chunk chatcompletionsadapter.ChatStreamChunk) error

// FinishStream 在流式结算成功后，让协议写出收尾帧。
//
// chat 按 include_usage 决定是否写 usage chunk；responses 总是写 response.completed。是否真正写帧的
// 协议差异由闭包内部决定，runner 在成功路径上总会调用一次。
type FinishStream func(streamID string, finalUsage adapter.ChatUsage, finishReason string) error

// RunStreamParams 是驱动一次流式候选 fallback 循环所需的协议无关参数（chat chunk 载体）。
//
// 它是 RunStreamParamsGeneric[chatcompletionsadapter.ChatStreamChunk] 的具名别称：OpenAI chat completions 与
// 现有 responses→chat 桥接两个调用点继续用本类型，零改动。responses 直传等其它载体走
// RunStreamGeneric。
type RunStreamParams struct {
	RequestRecord    requestlog.RequestRecord
	Principal        *auth.APIKeyPrincipal
	Authorization    ChatAuthorization
	Candidates       []Candidate
	RequestedModelID string
	ResponseProtocol requestlog.Protocol
	ResolveAdapter   ResolveAdapter
	Stream           StreamUpstream
	EmitChunk        EmitStreamChunk
	Finish           FinishStream

	// ConservativeInputTokens 是预授权阶段的保守输入估算，供 partial settlement 复用为 input 事实。
	ConservativeInputTokens int64
	// CountOutputTokens 按 upstream model 估算一段可见输出文本的 token 数，供 partial settlement 计 output。
	// 为 nil 时 partial 的 output 记 0（偏保守）。
	CountOutputTokens func(model string, text string) int64
	Codes             RunStreamCodes
}

// StreamChunkMeta 是从一个上游流式 chunk 提取出的协议无关元信息。
//
// 共享循环据此维护客户可见 stream id、final usage 与终态 finish，并决定该 chunk 是否对客户可见：
//   - ID 非空时更新 stream response id；
//   - FinishReason 非空时更新终态 finish；
//   - Usage 非 nil 时记为 final usage（仅供协议写出收尾帧，账务只认 adapter facts）；
//   - SuppressEmit 为 true 时该 chunk 仅用于内部事实提取（如 chat 的 usage 控制 chunk），
//     不写客户 SSE、也不置 emitted（保持「首字节前可 fallback」语义）。
type StreamChunkMeta struct {
	ID           string
	FinishReason string
	Usage        *adapter.ChatUsage
	SuppressEmit bool

	// VisibleText 是该 chunk 对客户可见的输出文本增量，仅供流式 partial settlement 估算 output token。
	// 不参与 full bill（账务只认 adapter facts）；usage 控制 chunk / 非文本帧应为空。
	VisibleText string
}

// StreamUpstreamGeneric 执行一次 timed 上游流式调用（泛型载体版）。
type StreamUpstreamGeneric[C any] func(ctx context.Context, candidate routing.ChatRouteCandidate, onChunk func(C) error) (*adapter.ResponseFacts, error)

// EmitStreamChunkGeneric 把单个上游内容 chunk 翻译/透传为协议 SSE 帧（泛型载体版）。
type EmitStreamChunkGeneric[C any] func(chunk C) error

// RunStreamParamsGeneric 是泛型流式候选 fallback 循环参数。
//
// 资金关键流程与 RunStreamParams 完全一致；唯一差异是 chunk 载体类型 C 由调用方决定，并要求提供
// ChunkMeta 提取器把 C 归一为 StreamChunkMeta。RunStream（chat 载体）是 C=chatcompletionsadapter.ChatStreamChunk 的
// 薄封装。
type RunStreamParamsGeneric[C any] struct {
	RequestRecord    requestlog.RequestRecord
	Principal        *auth.APIKeyPrincipal
	Authorization    ChatAuthorization
	Candidates       []Candidate
	RequestedModelID string
	ResponseProtocol requestlog.Protocol
	ResolveAdapter   ResolveAdapter
	Stream           StreamUpstreamGeneric[C]
	EmitChunk        EmitStreamChunkGeneric[C]
	Finish           FinishStream
	// ChunkMeta 从一个上游 chunk 提取协议无关元信息；不得为 nil。
	ChunkMeta func(C) StreamChunkMeta

	// ConservativeInputTokens 是预授权阶段的保守输入估算，供 partial settlement 复用为 input 事实。
	ConservativeInputTokens int64
	// CountOutputTokens 按 upstream model 估算一段可见输出文本的 token 数，供 partial settlement 计 output。
	// 为 nil 时 partial 的 output 记 0（偏保守）。
	CountOutputTokens func(model string, text string) int64
	Codes             RunStreamCodes
}

// RunStreamCodes 是共享流式候选循环里的审计 code/reason 覆盖项。
//
// 空值使用 OpenAI chat 既有默认值，保证现有调用点零改动、历史观测语义不漂移。
type RunStreamCodes struct {
	AuthorizationReleaseFailedCode string
	SettlementFailedCode           string

	PartialSettlementBillingExceptionReasonCode string
	PartialSettlementBillingExceptionReason     string
	SettlementBillingExceptionReasonCode        string
	SettlementBillingExceptionReason            string
}

func (c RunStreamCodes) withDefaults() RunStreamCodes {
	if c.AuthorizationReleaseFailedCode == "" {
		c.AuthorizationReleaseFailedCode = "chat_authorization_release_failed"
	}
	if c.SettlementFailedCode == "" {
		c.SettlementFailedCode = "stream_chat_settlement_failed"
	}
	if c.PartialSettlementBillingExceptionReasonCode == "" {
		c.PartialSettlementBillingExceptionReasonCode = "stream_settlement_failed_after_upstream_success"
	}
	if c.PartialSettlementBillingExceptionReason == "" {
		c.PartialSettlementBillingExceptionReason = "stream partial settlement permanently failed without recovery job"
	}
	if c.SettlementBillingExceptionReasonCode == "" {
		c.SettlementBillingExceptionReasonCode = "stream_settlement_failed_after_upstream_success"
	}
	if c.SettlementBillingExceptionReason == "" {
		c.SettlementBillingExceptionReason = "stream settlement permanently failed after upstream success without recovery job"
	}
	return c
}

// RunStream 执行 authorization 之后的流式候选 fallback 循环（chat chunk 载体）。
//
// 它把 chat_stream.go 原有的资金关键流式链路收口到一处，供 OpenAI chat 与 responses 桥接复用；现已
// 委托给泛型 RunStreamGeneric，逻辑逐字不变（chunk 元信息提取由 chatStreamChunkMeta 等价复刻原
// inline onChunk）。
func (r *AttemptRunner) RunStream(ctx context.Context, params RunStreamParams) (RunResult, error) {
	return RunStreamGeneric(ctx, r, RunStreamParamsGeneric[chatcompletionsadapter.ChatStreamChunk]{
		RequestRecord:           params.RequestRecord,
		Principal:               params.Principal,
		Authorization:           params.Authorization,
		Candidates:              params.Candidates,
		RequestedModelID:        params.RequestedModelID,
		ResponseProtocol:        params.ResponseProtocol,
		ResolveAdapter:          params.ResolveAdapter,
		Stream:                  StreamUpstreamGeneric[chatcompletionsadapter.ChatStreamChunk](params.Stream),
		EmitChunk:               EmitStreamChunkGeneric[chatcompletionsadapter.ChatStreamChunk](params.EmitChunk),
		Finish:                  params.Finish,
		ChunkMeta:               chatStreamChunkMeta,
		ConservativeInputTokens: params.ConservativeInputTokens,
		CountOutputTokens:       params.CountOutputTokens,
		Codes:                   params.Codes,
	})
}

// chatStreamChunkMeta 等价复刻原 chat inline onChunk 的元信息提取：usage 控制 chunk 抑制 emit，
// 普通内容 chunk 透传。
func chatStreamChunkMeta(chunk chatcompletionsadapter.ChatStreamChunk) StreamChunkMeta {
	meta := StreamChunkMeta{
		ID:           chunk.ID,
		Usage:        chunk.Usage,
		SuppressEmit: chunk.Usage != nil,
		VisibleText:  chunk.Content,
	}
	if chunk.FinishReason != nil {
		meta.FinishReason = *chunk.FinishReason
	}
	return meta
}

// RunStreamGeneric 执行 authorization 之后的流式候选 fallback 循环（泛型载体）。
//
// 资金关键链路（attempt 审计、熔断跳过、adapter 解析、上游流式调用、emitted 后禁止 fallback、
// final usage 缺失处理、客户端取消处理、tail-error 仍尽力结算、settlement 与 request/attempt 终态写入）
// 与原 chat 实现逐字一致；唯一抽象点是 chunk 载体类型 C 与 ChunkMeta 提取器。所有审计 error_code、
// release 原因码与 stream metrics 事件均不变。
//
// 账务只消费 adapter 同次解析返回的 streamFacts；finalUsage 仅供协议向客户写出 usage/completed 帧。
func RunStreamGeneric[C any](ctx context.Context, r *AttemptRunner, params RunStreamParamsGeneric[C]) (RunResult, error) {
	result := RunResult{Outcome: metrics.ChatOutcomeFailed}
	l := r.lifecycle
	requestRecord := params.RequestRecord
	authorization := params.Authorization
	codes := params.Codes.withDefaults()

	// Key 级 TPM 预占（P2-8）：RPM/RPD 已在 ingress 中间件处理，这里只做 token 维度。
	// 命中即释放冻结并以 429 上抛；计数后端 fail_closed 故障同样释放冻结后上抛。
	if dec, allowed, err := r.guardKeyTokens(ctx, params.Principal, params.ConservativeInputTokens); err != nil {
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
	r.recordKeyTPMReservation(res, params.Principal, params.ConservativeInputTokens)
	defer r.releaseUnreconciledTPM(ctx, res)

	var lastErr error

	for _, prepared := range params.Candidates {
		index := prepared.RouteIndex
		candidate := prepared.Route

		// channel 熔断 open 时跳过该 channel，尝试下一个同模型 channel。
		channelKey := MetricsID(candidate.Channel.ID)
		if !l.BreakerAllow(channelKey) {
			continue
		}

		// 渠道级限流预占（P2-8）：命中任一维度即跳过该候选 fallback 到下一渠道（与熔断 open 同语义，不写 attempt）。
		// 计数后端 fail_closed 故障同样保守跳过该候选；fail_open 时 Guard 内部已放行。
		if dec, allowed, err := r.guardChannel(ctx, candidate, params.ConservativeInputTokens); err != nil {
			lastErr = err
			continue
		} else if !allowed {
			lastErr = channelRateLimitedError(dec)
			continue
		}

		// 该候选已通过渠道级 TPM 预占（额度已写入窗口）：登记预占，收尾时若非胜出（fallback 落选/失败）则释放。
		r.recordChannelTPMReservation(res, candidate, params.ConservativeInputTokens)

		// 每个 stream candidate 也必须先创建 attempt：流式失败可能发生在首 chunk 前、首 chunk 后或
		// 客户端取消时，提前记录 attempt 才能审计这些状态。
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

		// emitted 表示是否已向客户端写出过 SSE 帧。一旦写出开始就不能再 fallback，否则同一个 SSE
		// 响应会混入不同上游内容。
		emitted := false

		// partialOutputTokens 累计「已 emit 可见文本」的估算 output token，仅用于 partial settlement。
		var partialOutputTokens int64

		// finalUsage 仅用于协议向客户写出 usage/completed 帧；结算只消费 streamFacts。
		var finalUsage *adapter.ChatUsage

		// streamFacts 是 adapter 流式解析结束时返回的不可变结算事实。
		var streamFacts *adapter.ResponseFacts

		// streamResponseID 用于客户端可见的 stream id 与收尾帧。
		streamResponseID := ""

		// finishReason 取上游最后一个非空 finish_reason，供协议收尾帧映射终态。
		finishReason := ""

		settledRequestStatus := requestlog.RequestStatusSucceeded
		settledAttemptStatus := requestlog.AttemptStatusSucceeded
		settledErrorCode, settledErrorMessage, settledInternalErrorDetail := "", "", ""

		// responseStartedAt 记录第一个真正对客户可见的上游 chunk 到达时间，用于 TTFT。
		var responseStartedAt *time.Time

		// settleStreamFacts 使用 adapter 最终 facts 结算流式请求。结算不能依赖原始请求 ctx：客户端
		// 可能已断开，但只要上游已返回 final usage，平台就有准确账务事实，必须尽力完成结算。
		settleStreamFacts := func() error {
			if streamFacts == nil {
				return failure.New(
					failure.CodeGatewayStreamUsageMissing,
					failure.WithMessage("gateway stream response facts are missing"),
				)
			}

			l.RecordRoutingSelected(candidate.ProviderID, candidate.Channel.ID, params.RequestedModelID)
			logfields.SetRoute(ctx, params.RequestedModelID, MetricsID(candidate.ProviderID), MetricsID(candidate.Channel.ID))

			settlementCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()

			settleCtx, settleSpan := StartGatewaySpan(settlementCtx, "gateway.settlement")
			responseID := streamResponseID
			if responseID == "" {
				responseID = streamFacts.UpstreamResponseID
			}
			settleErr := r.settlement.SettleSuccessfulChat(settleCtx, ChatSettlementParams{
				RequestRecord:       requestRecord,
				AttemptRecord:       attemptRecord,
				Principal:           params.Principal,
				Authorization:       authorization,
				ResponseProtocol:    params.ResponseProtocol,
				ResponseID:          responseID,
				ResponseModelID:     params.RequestedModelID,
				ResponseStartedAt:   responseStartedAt,
				RequestFinalStatus:  settledRequestStatus,
				AttemptFinalStatus:  settledAttemptStatus,
				ErrorCode:           settledErrorCode,
				ErrorMessage:        settledErrorMessage,
				InternalErrorDetail: settledInternalErrorDetail,
				ModelDBID:           candidate.ModelDBID,
				FinalProviderID:     candidate.ProviderID,
				FinalChannelID:      candidate.Channel.ID,
				ChannelPriceID:      candidate.ChannelPriceID,
				SalePrice:           candidate.SalePrice,
				Facts:               *streamFacts,
			})
			EndSettlementSpan(settleSpan, settleErr)
			l.RecordSettlement(SettlementOutcomeFromErr(settleErr))
			// 结算成功（或已交 recovery 接管）后按真实 billable token 回填 Key/channel 的 TPM 计数差额（P2-8），
			// 并标记该 route+user 与胜出 channel 的预占已对账——收尾释放不再回退它们（DEC-028）。
			//
			// 仅当拿到「真实」usage 时才回填并对账。partial 估算（流中断/客户端取消/缺 final usage）不含真实
			// cache 拆分、把全部输入按「未缓存」计，若据此回填 billableTPMTokens≈预占额→退款为 0，预占无法退回，
			// 会把用户 TPM 窗口顶到上限、连累后续请求（上游流中断不应惩罚用户限流）。此时保持未对账，
			// 由收尾 releaseUnreconciledTPM 退还整笔 route+user 与 channel 预占，使窗口回落。
			if settleErr == nil || IsChatSettlementRecoveryScheduled(settleErr) {
				if !streamFacts.UsageSource.IsPartialEstimate() {
					r.backfillRateTokens(ctx, params.Principal, candidate, params.ConservativeInputTokens, streamFacts.Usage)
					res.markReconciled(candidate.Channel.ID)
				}
			}
			return settleErr
		}

		// finishPartial 处理「已 emit 但无 adapter final usage」的 partial settlement（路线 B/D）：
		// 合成 partial_stream_estimate 事实走与 full bill 相同的结算管道（attempt/request 标 succeeded、
		// final_usage_received=false）；settlement 永久失败且无 recovery 接管时，退回释放冻结并记风险敞口
		// （与上游成功后 settlement 失败同语义）。reason 落到 upstream_finish_reason 区分 B/D。
		finishPartial := func(reason string, outcome metrics.ChatOutcome, streamEvent metrics.StreamEvent, deliveryCompleted bool, returnErr error) (RunResult, error) {
			settledRequestStatus = requestlog.RequestStatusSucceeded
			settledAttemptStatus = requestlog.AttemptStatusSucceeded
			settledErrorCode, settledErrorMessage, settledInternalErrorDetail = "", "", ""
			switch reason {
			case PartialReasonClientCanceled:
				settledRequestStatus = requestlog.RequestStatusCanceled
				settledAttemptStatus = requestlog.AttemptStatusCanceled
				settledErrorCode, settledErrorMessage, settledInternalErrorDetail = l.requestLogCancelFacts(returnErr)
			case PartialReasonInterrupted:
				settledRequestStatus = requestlog.RequestStatusFailed
				settledAttemptStatus = requestlog.AttemptStatusFailed
				settledErrorCode, settledErrorMessage, settledInternalErrorDetail = l.requestLogErrorFacts("stream_adapter_error", returnErr)
			}
			facts := BuildPartialStreamFacts(PartialStreamFactsParams{
				Candidate:        candidate,
				StreamResponseID: streamResponseID,
				RequestRecordID:  requestRecord.ID,
				InputTokens:      params.ConservativeInputTokens,
				OutputTokens:     partialOutputTokens,
				Reason:           reason,
			})
			streamFacts = &facts

			if settleErr := settleStreamFacts(); settleErr != nil {
				if !IsChatSettlementRecoveryScheduled(settleErr) {
					if releaseErr := l.ReleaseAuthorizationForBillingException(
						ctx,
						authorization,
						codes.PartialSettlementBillingExceptionReasonCode,
						codes.PartialSettlementBillingExceptionReason,
					); releaseErr != nil {
						l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
						return result, releaseErr
					}
					l.MarkRequestFailed(ctx, requestRecord, codes.SettlementFailedCode, settleErr)
					return result, settleErr
				}
			}

			// 交付状态：路线 D（上游正常结束、仅缺 final usage）客户已拿到全部内容 → completed；
			// 路线 B（客户端取消 / 上游中断）客户未拿到完整响应 → interrupted。
			if deliveryCompleted {
				l.MarkDeliveryCompleted(ctx, requestRecord)
			} else {
				l.MarkDeliveryInterrupted(ctx, requestRecord)
			}

			result.Outcome = outcome
			l.RecordStreamEvent(streamEvent)
			// partial settlement 按已吐内容保守估算收费，系统性偏少收（P2-2）：记指标供监控占比/滥用。
			l.RecordPartialSettlement(reason)
			return result, returnErr
		}
		_ = finishPartial

		onChunk := func(chunk C) error {
			meta := params.ChunkMeta(chunk)
			if meta.ID != "" {
				streamResponseID = meta.ID
			}
			if meta.FinishReason != "" {
				finishReason = meta.FinishReason
			}

			if meta.Usage != nil {
				usage := *meta.Usage
				finalUsage = &usage
			}

			if meta.SuppressEmit {
				// 仅内部事实提取的控制 chunk（如 chat 的 usage 控制 chunk）：不置 emitted、不写 SSE，
				// 否则客户端会收到空 choices 帧，也会误锁「首字节后禁止 fallback」。
				return nil
			}

			if !emitted {
				now := time.Now()
				responseStartedAt = &now
				emitted = true
				l.MarkResponseStarted(ctx, requestRecord, attemptRecord, now)
				l.RecordStreamEvent(metrics.StreamEventStarted)
			}

			// 累计已 emit 可见文本的估算 output token，供 partial settlement（无 final usage 时）使用。
			if params.CountOutputTokens != nil && meta.VisibleText != "" {
				partialOutputTokens += params.CountOutputTokens(candidate.UpstreamModel, meta.VisibleText)
			}

			return params.EmitChunk(chunk)
		}

		upstreamStart := time.Now()
		streamFacts, err = params.Stream(ctx, candidate, onChunk)
		l.RecordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
		l.RecordChannelHealth(channelKey, err)

		if err != nil {
			// 上游 429：按 Retry-After 登记渠道冷却，后续 fallback 在冷却窗口内直接跳过该渠道（P2-7）。
			l.RecordChannelRateLimit(channelKey, err)

			// 有 final usage 时优先结算：上游已给出准确 token 用量，即使尾部出错也不能让已产生成本的请求免费。
			if streamFacts != nil {
				if settleErr := settleStreamFacts(); settleErr != nil {
					if !IsChatSettlementRecoveryScheduled(settleErr) {
						// settlement 永久失败且无 recovery job 接管：释放冻结余额并记账务异常风险，
						// 否则用户余额永久冻结（同非流式 settlement_failed_after_upstream_success 处理）。
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
				}
				// 账务已收口，但调用方仍需知道流尾发生过错误；HTTP 层若已写出 SSE 只能中断连接。
				// 已 emit 后尾部出错：客户未拿到完整响应，交付标 interrupted。
				if emitted {
					l.MarkDeliveryInterrupted(ctx, requestRecord)
				}
				return result, err
			}

			// 客户端取消不是上游失败，也不触发 fallback。已 emit 时按 partial settlement 计费（路线 B）；
			// 首 token 前取消则普通释放冻结、不扣费（路线 C）。
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				if emitted {
					return finishPartial(PartialReasonClientCanceled, metrics.ChatOutcomeCanceled, metrics.StreamEventCanceled, false, err)
				}

				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}

				result.Outcome = metrics.ChatOutcomeCanceled
				l.RecordStreamEvent(metrics.StreamEventCanceled)
				l.MarkRequestCanceled(ctx, requestRecord, attemptRecord, err)
				return result, err
			}

			if emitted {
				// SSE 已写出后无法再 fallback 或改写 JSON error。
				if partialOutputTokens > 0 {
					// 已 emit 可用输出内容：按 partial settlement 计费（路线 B）；不在此处 MarkAttemptFailed——
					// partial 走 settlement 会先结算 usage/ledger 再把 attempt 标 failed。
					return finishPartial(PartialReasonInterrupted, metrics.ChatOutcomeFailed, metrics.StreamEventInterrupted, false, err)
				}
				// 已 emit 帧但无可用输出内容（仅控制帧/空内容后上游中断）：视同「上游流中断、无可用输出」——
				// 一分钱不扣、全额释放预扣（对齐 new-api PR #4199）；TPM 预占由收尾 releaseUnreconciledTPM 退还。
				l.MarkAttemptFailed(ctx, attemptRecord, "stream_adapter_error", err)
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}
				l.MarkRequestFailed(ctx, requestRecord, "stream_adapter_error", err)
				l.MarkDeliveryInterrupted(ctx, requestRecord)
				result.Outcome = metrics.ChatOutcomeFailed
				l.RecordStreamEvent(metrics.StreamEventInterrupted)
				return result, err
			}

			// 首 token 前失败：attempt 记失败；客户端还没看到上游内容，只有这时允许同模型 fallback。
			l.MarkAttemptFailed(ctx, attemptRecord, "stream_adapter_error", err)

			if !r.retryClassifier.IsRetryable(err) {
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
					return result, releaseErr
				}

				l.MarkRequestFailed(ctx, requestRecord, "stream_adapter_error", err)
				return result, err
			}

			// 首字节前可重试错误切换候选：前一候选可能已在上游产生成本却不会被结算（P2-3），记指标供监控。
			l.RecordRetryableFallback(err)
			lastErr = err
			continue
		}

		// 账务唯一真源是 adapter facts（B4）：只看 streamFacts 是否缺失，不依赖客户帧用的 finalUsage。
		if streamFacts == nil {
			// adapter 正常结束但缺 final usage（上游不支持 include_usage、代理吞尾包或 parser 漏解析）。
			// 已 emit 时按 partial settlement 计费并标渠道异常（路线 D）；未 emit 则普通释放、不扣费（路线 C）。
			if emitted {
				return finishPartial(PartialReasonFinalUsageMissing, metrics.ChatOutcomeSuccess, metrics.StreamEventMissingUsage, true, nil)
			}

			err := failure.New(
				failure.CodeGatewayStreamUsageMissing,
				failure.WithMessage("gateway stream final usage is missing"),
			)

			if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
				l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
				return result, releaseErr
			}

			l.RecordStreamEvent(metrics.StreamEventMissingUsage)
			l.MarkAttemptFailed(ctx, attemptRecord, "stream_usage_missing", err)
			l.MarkRequestFailed(ctx, requestRecord, "stream_usage_missing", err)
			return result, err
		}

		if settleErr := settleStreamFacts(); settleErr != nil {
			if !IsChatSettlementRecoveryScheduled(settleErr) {
				// settlement 永久失败且无 recovery job 接管：释放冻结余额并记账务异常风险，
				// 否则用户余额永久冻结（同非流式 settlement_failed_after_upstream_success 处理）。
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
		}

		// B4：streamFacts 非空即 full bill；finalUsage 仅用于客户收尾帧，缺失时跳过（不影响计费）。
		if finalUsage != nil {
			if err := params.Finish(streamResponseID, *finalUsage, finishReason); err != nil {
				return result, err
			}
		}

		// 流式正常结束（路线 A）：所有 chunk 与收尾帧已写出，交付完成。
		l.MarkDeliveryCompleted(ctx, requestRecord)

		// 零价渠道误配监控（P2-4）：售价快照全部非正即客户侧 $0 收入，记指标供运维定位误配渠道。
		if candidate.SalePrice.IsEffectivelyFree() {
			l.RecordZeroPriceServed(candidate.ProviderID, candidate.Channel.ID, params.RequestedModelID)
		}

		result.Outcome = metrics.ChatOutcomeSuccess
		l.RecordStreamEvent(metrics.StreamEventCompleted)
		return result, nil
	}

	if lastErr != nil {
		if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
			l.MarkRequestFailed(ctx, requestRecord, codes.AuthorizationReleaseFailedCode, releaseErr)
			return result, releaseErr
		}
		l.MarkRequestFailed(ctx, requestRecord, "stream_adapter_error", lastErr)
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
	l.MarkRequestFailed(ctx, requestRecord, "no_available_channel", err)
	return result, err
}
