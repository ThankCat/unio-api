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
	// RequiredCapabilities 是 ingress 推断的所需能力 key，写入每个 attempt 的 capability 审计快照（可空）。
	RequiredCapabilities []string
	ResolveAdapter       ResolveAdapter
	Stream               StreamUpstream
	EmitChunk            EmitStreamChunk
	Finish               FinishStream
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
	RequestRecord        requestlog.RequestRecord
	Principal            *auth.APIKeyPrincipal
	Authorization        ChatAuthorization
	Candidates           []Candidate
	RequestedModelID     string
	ResponseProtocol     requestlog.Protocol
	RequiredCapabilities []string
	ResolveAdapter       ResolveAdapter
	Stream               StreamUpstreamGeneric[C]
	EmitChunk            EmitStreamChunkGeneric[C]
	Finish               FinishStream
	// ChunkMeta 从一个上游 chunk 提取协议无关元信息；不得为 nil。
	ChunkMeta func(C) StreamChunkMeta
}

// RunStream 执行 authorization 之后的流式候选 fallback 循环（chat chunk 载体）。
//
// 它把 chat_stream.go 原有的资金关键流式链路收口到一处，供 OpenAI chat 与 responses 桥接复用；现已
// 委托给泛型 RunStreamGeneric，逻辑逐字不变（chunk 元信息提取由 chatStreamChunkMeta 等价复刻原
// inline onChunk）。
func (r *AttemptRunner) RunStream(ctx context.Context, params RunStreamParams) (RunResult, error) {
	return RunStreamGeneric(ctx, r, RunStreamParamsGeneric[chatcompletionsadapter.ChatStreamChunk]{
		RequestRecord:        params.RequestRecord,
		Principal:            params.Principal,
		Authorization:        params.Authorization,
		Candidates:           params.Candidates,
		RequestedModelID:     params.RequestedModelID,
		ResponseProtocol:     params.ResponseProtocol,
		RequiredCapabilities: params.RequiredCapabilities,
		ResolveAdapter:       params.ResolveAdapter,
		Stream:               StreamUpstreamGeneric[chatcompletionsadapter.ChatStreamChunk](params.Stream),
		EmitChunk:            EmitStreamChunkGeneric[chatcompletionsadapter.ChatStreamChunk](params.EmitChunk),
		Finish:               params.Finish,
		ChunkMeta:            chatStreamChunkMeta,
	})
}

// chatStreamChunkMeta 等价复刻原 chat inline onChunk 的元信息提取：usage 控制 chunk 抑制 emit，
// 普通内容 chunk 透传。
func chatStreamChunkMeta(chunk chatcompletionsadapter.ChatStreamChunk) StreamChunkMeta {
	meta := StreamChunkMeta{
		ID:           chunk.ID,
		Usage:        chunk.Usage,
		SuppressEmit: chunk.Usage != nil,
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
						// settlement 永久失败且无 recovery job 接管：释放冻结余额并记账务异常风险，
						// 否则用户余额永久冻结（同非流式 settlement_failed_after_upstream_success 处理）。
						if releaseErr := l.ReleaseAuthorizationForBillingException(
							ctx,
							authorization,
							"stream_settlement_failed_after_upstream_success",
							"stream settlement permanently failed after upstream success without recovery job",
						); releaseErr != nil {
							l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
							return result, releaseErr
						}
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
				// settlement 永久失败且无 recovery job 接管：释放冻结余额并记账务异常风险，
				// 否则用户余额永久冻结（同非流式 settlement_failed_after_upstream_success 处理）。
				if releaseErr := l.ReleaseAuthorizationForBillingException(
					ctx,
					authorization,
					"stream_settlement_failed_after_upstream_success",
					"stream settlement permanently failed after upstream success without recovery job",
				); releaseErr != nil {
					l.MarkRequestFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return result, releaseErr
				}
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
