package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
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
type StreamUpstream func(ctx context.Context, candidate routing.ChatRouteCandidate, onChunk func(openai.ChatStreamChunk) error) (*adapter.ResponseFacts, error)

// EmitStreamChunk 由协议把单个上游内容 chunk 翻译为协议 SSE 帧（chat chunk / responses 命名事件）。
//
// runner 只在「非 usage、非纯 id」的内容 chunk 上调用它，并已先行置 emitted、计数 stream started。
type EmitStreamChunk func(chunk openai.ChatStreamChunk) error

// FinishStream 在流式结算成功后，让协议写出收尾帧。
//
// chat 按 include_usage 决定是否写 usage chunk；responses 总是写 response.completed。是否真正写帧的
// 协议差异由闭包内部决定，runner 在成功路径上总会调用一次。
type FinishStream func(streamID string, finalUsage adapter.ChatUsage, finishReason string) error

// RunStreamParams 是驱动一次流式候选 fallback 循环所需的协议无关参数。
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
}

// RunStream 执行 authorization 之后的流式候选 fallback 循环。
//
// 它把 chat_stream.go 原有的资金关键流式链路收口到一处，供 OpenAI chat 与 responses 复用：attempt
// 审计、熔断跳过、adapter 解析、上游流式调用、emitted 后禁止 fallback、final usage 缺失处理、客户端
// 取消处理、tail-error 仍尽力结算、settlement 与 request/attempt 终态写入。所有审计 error_code、
// release 原因码与 stream metrics 事件均与抽取前的 chat_stream.go 逐字一致，避免改变可观测/账务事实。
//
// 账务只消费 adapter 同次解析返回的 streamFacts；finalUsage 仅供协议向客户写出 usage/completed 帧。
func (r *AttemptRunner) RunStream(ctx context.Context, params RunStreamParams) (RunResult, error) {
	result := RunResult{Outcome: metrics.ChatOutcomeFailed}
	l := r.lifecycle
	requestRecord := params.RequestRecord
	authorization := params.Authorization

	var lastErr error

	for _, prepared := range params.Candidates {
		index := prepared.RouteIndex
		candidate := prepared.Route

		// channel 熔断 open 时跳过该 channel，尝试下一个同模型 channel。
		channelKey := MetricsID(candidate.Channel.ID)
		if !l.BreakerAllow(channelKey) {
			continue
		}

		// 每个 stream candidate 也必须先创建 attempt：流式失败可能发生在首 chunk 前、首 chunk 后或
		// 客户端取消时，提前记录 attempt 才能审计这些状态。
		attemptRecord, err := l.CreateAttempt(ctx, requestRecord, index, candidate)
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

		// emitted 表示是否已向客户端写出过 SSE 帧。一旦写出开始就不能再 fallback，否则同一个 SSE
		// 响应会混入不同上游内容。
		emitted := false

		// finalUsage 仅用于协议向客户写出 usage/completed 帧；结算只消费 streamFacts。
		var finalUsage *adapter.ChatUsage

		// streamFacts 是 adapter 流式解析结束时返回的不可变结算事实。
		var streamFacts *adapter.ResponseFacts

		// streamResponseID 用于客户端可见的 stream id 与收尾帧。
		streamResponseID := ""

		// finishReason 取上游最后一个非空 finish_reason，供协议收尾帧映射终态。
		finishReason := ""

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
				RequestRecord:    requestRecord,
				AttemptRecord:    attemptRecord,
				Principal:        params.Principal,
				Authorization:    authorization,
				ResponseProtocol: params.ResponseProtocol,
				ResponseID:       responseID,
				ResponseModelID:  params.RequestedModelID,
				ModelDBID:        candidate.ModelDBID,
				FinalProviderID:  candidate.ProviderID,
				FinalChannelID:   candidate.Channel.ID,
				Facts:            *streamFacts,
			})
			EndSettlementSpan(settleSpan, settleErr)
			l.RecordSettlement(SettlementOutcomeFromErr(settleErr))
			return settleErr
		}

		onChunk := func(chunk openai.ChatStreamChunk) error {
			if chunk.ID != "" {
				streamResponseID = chunk.ID
			}
			if chunk.FinishReason != nil && *chunk.FinishReason != "" {
				finishReason = *chunk.FinishReason
			}

			if chunk.Usage != nil {
				// usage chunk 是 adapter 给 gateway 的内部控制事件，不是用户可见内容：
				// 不置 emitted、不写 SSE，否则客户端会收到空 choices 帧。
				usage := *chunk.Usage
				finalUsage = &usage
				return nil
			}

			if !emitted {
				emitted = true
				l.RecordStreamEvent(metrics.StreamEventStarted)
			}
			return params.EmitChunk(chunk)
		}

		upstreamStart := time.Now()
		streamFacts, err = params.Stream(ctx, candidate, onChunk)
		l.RecordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
		l.RecordChannelHealth(channelKey, err)

		if err != nil {
			// 有 final usage 时优先结算：上游已给出准确 token 用量，即使尾部出错也不能让已产生成本的请求免费。
			if streamFacts != nil {
				if settleErr := settleStreamFacts(); settleErr != nil {
					if !IsChatSettlementRecoveryScheduled(settleErr) {
						l.MarkRequestFailed(ctx, requestRecord, "stream_chat_settlement_failed", settleErr)
						return result, settleErr
					}
				}
				// 账务已收口，但调用方仍需知道流尾发生过错误；HTTP 层若已写出 SSE 只能中断连接。
				return result, err
			}

			// 客户端取消不是上游失败，也不触发 fallback；没有 final usage 时缺少可靠用量事实，
			// 当前阶段只记录 canceled、不扣费。
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				if releaseErr := l.ReleaseAuthorizationForBillingException(
					ctx,
					authorization,
					"stream_client_canceled_without_final_usage",
					"stream client canceled before final usage",
				); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return result, releaseErr
				}

				result.Outcome = metrics.ChatOutcomeCanceled
				l.RecordStreamEvent(metrics.StreamEventCanceled)
				l.MarkRequestCanceled(ctx, requestRecord, attemptRecord, err)
				return result, err
			}

			l.MarkAttemptFailed(ctx, attemptRecord, "stream_adapter_error", err)

			if emitted {
				// SSE 已写出后只能把当前请求标记为失败并结束：HTTP 层不能再改写 JSON error，
				// 也不能换 channel 重放已写出的内容。
				if releaseErr := l.ReleaseAuthorizationForBillingException(
					ctx,
					authorization,
					"stream_interrupted_without_final_usage",
					"stream interrupted after emit before final usage",
				); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return result, releaseErr
				}

				l.MarkRequestFailed(ctx, requestRecord, "stream_adapter_error_after_emit", err)
				return result, err
			}

			// 首 chunk 前失败时客户端还没看到上游内容，只有这时允许同模型 fallback。
			if !r.retryClassifier.IsRetryable(err) {
				if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return result, releaseErr
				}

				l.MarkRequestFailed(ctx, requestRecord, "stream_adapter_error", err)
				return result, err
			}

			lastErr = err
			continue
		}

		if streamFacts == nil || finalUsage == nil {
			// adapter 正常结束但缺 final usage，不能当作可计费成功请求（上游不支持 include_usage、
			// 代理吞尾包或 parser 漏解析）。
			err := failure.New(
				failure.CodeGatewayStreamUsageMissing,
				failure.WithMessage("gateway stream final usage is missing"),
			)

			if releaseErr := l.ReleaseAuthorizationForBillingException(
				ctx,
				authorization,
				"stream_final_usage_missing",
				"stream ended without final usage",
			); releaseErr != nil {
				l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
				return result, releaseErr
			}

			l.RecordStreamEvent(metrics.StreamEventMissingUsage)
			l.MarkAttemptFailed(ctx, attemptRecord, "stream_usage_missing", err)
			l.MarkRequestFailed(ctx, requestRecord, "stream_usage_missing", err)
			return result, err
		}

		if settleErr := settleStreamFacts(); settleErr != nil {
			if !IsChatSettlementRecoveryScheduled(settleErr) {
				l.MarkRequestFailed(ctx, requestRecord, "stream_chat_settlement_failed", settleErr)
				return result, settleErr
			}
		}

		if err := params.Finish(streamResponseID, *finalUsage, finishReason); err != nil {
			return result, err
		}

		result.Outcome = metrics.ChatOutcomeSuccess
		l.RecordStreamEvent(metrics.StreamEventCompleted)
		return result, nil
	}

	if lastErr != nil {
		if releaseErr := l.ReleaseAuthorization(ctx, authorization); releaseErr != nil {
			l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
			return result, releaseErr
		}
		l.MarkRequestFailed(ctx, requestRecord, "stream_adapter_error", lastErr)
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
	l.MarkRequestFailed(ctx, requestRecord, "no_available_channel", err)
	return result, err
}
