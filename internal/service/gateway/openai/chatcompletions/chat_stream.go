package chatcompletions

import (
	"context"
	"errors"
	"fmt"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
)

// StreamChatCompletion 编排流式 chat completion 请求，并通过 emit 写出 OpenAI-compatible SSE chunk。
func (s *ChatCompletionService) StreamChatCompletion(ctx context.Context, req gatewayapi.ChatCompletionRequest, emit func(gatewayapi.ChatCompletionStreamResponse) error) error {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	// 先创建 request_records，并标记为 running。
	requestRecord, err := s.createRequestRecord(ctx, principal, req, true)
	if err != nil {
		return err
	}

	// outcome 默认 failed，仅在成功/取消路径覆盖；defer 保证每个流式请求只计数一次。
	outcome := metrics.ChatOutcomeFailed
	defer func() {
		s.recordChatRequest(true, outcome)
	}()

	ctx, span := startGatewaySpan(ctx, "gateway.chat_stream")
	defer span.End()

	planCtx, planSpan := startGatewaySpan(ctx, "gateway.routing")
	plan, err := s.router.PlanChat(planCtx, routing.ChatRouteRequest{
		ProjectID:       principal.ProjectID,
		ModelID:         req.Model,
		IngressProtocol: routing.ProtocolOpenAI,
	})
	endGatewaySpan(planSpan, err)
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, routingFailureCode(err), err)
		return err
	}

	candidatePlan, err := s.prepareChatCandidates(ctx, req, plan.Candidates, true)
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, routingFailureCode(err), err)
		return err
	}

	firstCandidate := candidatePlan.Candidates[0].Route
	authorization, err := s.chatAuthorizer.AuthorizeChat(ctx, ChatAuthorizeParams{
		RequestRecord:       requestRecord,
		Principal:           principal,
		ModelDBID:           firstCandidate.ModelDBID,
		InputTokens:         candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens: estimateMaxCompletionTokens(req),
	})
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_failed", err)
		return err
	}

	var lastErr error

	for _, prepared := range candidatePlan.Candidates {
		index := prepared.RouteIndex
		candidate := prepared.Route

		// channel 熔断 open 时跳过该 channel，尝试下一个同模型 channel。
		channelKey := metricsID(candidate.Channel.ID)
		if !s.breakerAllow(channelKey) {
			continue
		}

		// 每个 stream candidate 也必须先创建 attempt。
		// stream 的失败可能发生在首 chunk 前、首 chunk 后或客户端取消时，提前记录 attempt 才能审计这些状态。
		attemptRecord, err := s.createAttemptRecord(ctx, requestRecord, index, candidate)
		if err != nil {
			if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
				s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
				return releaseErr
			}

			s.markRequestRecordFailed(ctx, requestRecord, "request_attempt_create_failed", err)
			return err
		}

		streamAdapter, ok := s.registry.StreamChat(candidate.AdapterKey)
		if !ok {
			err := failure.New(
				failure.CodeGatewayAdapterNotRegistered,
				failure.WithMessage(fmt.Sprintf("gateway stream chat adapter %q not registered", candidate.AdapterKey)),
			)

			if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
				s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
				return releaseErr
			}

			s.markAttemptRecordFailed(ctx, attemptRecord, "adapter_not_registered", err)
			s.markRequestRecordFailed(ctx, requestRecord, "adapter_not_registered", err)

			return err
		}

		// emitted 表示是否已经尝试向客户端写出过 SSE chunk。
		// 一旦写出开始，就不能再 fallback 到其他 channel，否则同一个 SSE 响应会混入不同上游的内容。
		emitted := false

		// finalUsage 只用于按 OpenAI 协议向客户输出 include_usage chunk。
		// 账务结算只消费 adapter 同次解析返回的 StreamOutcome.Facts。
		var finalUsage *adapter.ChatUsage

		// streamFacts 是 adapter 在流式解析结束时返回的不可变结算事实。
		var streamFacts *adapter.ResponseFacts

		// streamResponseID 用于客户端可见的 stream chunk id 和最终 usage chunk。
		streamResponseID := ""

		// settleStreamFacts 使用 adapter 最终 facts 结算流式请求。
		// stream 结算不能依赖原始请求 ctx，因为客户端可能已经断开；
		// 只要上游已经返回 final usage，平台就有准确账务事实，必须尽力完成结算。
		settleStreamFacts := func() error {
			if streamFacts == nil {
				return failure.New(
					failure.CodeGatewayStreamUsageMissing,
					failure.WithMessage("gateway stream response facts are missing"),
				)
			}

			s.recordRoutingSelected(candidate.ProviderID, candidate.Channel.ID, req.Model)
			logfields.SetRoute(ctx, req.Model, metricsID(candidate.ProviderID), metricsID(candidate.Channel.ID))

			// 客户端断开会取消原始请求 ctx；结算属于服务端账务收口，
			// 不能因为客户端不再读取响应就放弃扣费。
			settlementCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()

			settleCtx, settleSpan := startGatewaySpan(settlementCtx, "gateway.settlement")
			responseID := streamResponseID
			if responseID == "" {
				responseID = streamFacts.UpstreamResponseID
			}
			settleErr := s.chatSettlement.SettleSuccessfulChat(settleCtx, ChatSettlementParams{
				RequestRecord:    requestRecord,
				AttemptRecord:    attemptRecord,
				Principal:        principal,
				Authorization:    authorization,
				ResponseProtocol: requestlog.ProtocolOpenAI,
				ResponseID:       responseID,
				ResponseModelID:  req.Model,
				ModelDBID:        candidate.ModelDBID,
				FinalProviderID:  candidate.ProviderID,
				FinalChannelID:   candidate.Channel.ID,
				Facts:            *streamFacts,
			})
			endSettlementSpan(settleSpan, settleErr)
			s.recordSettlement(settlementOutcomeFromErr(settleErr))
			return settleErr
		}

		streamCtx, streamSpan := startGatewaySpan(ctx, "adapter.stream_chat_completions", upstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
		upstreamStart := time.Now()
		streamOutcome, streamErr := streamAdapter.StreamChatCompletions(streamCtx, candidate.Channel,
			mapGatewayRequestToAdapter(req, candidate.UpstreamModel),
			func(chunk openai.ChatStreamChunk) error {
				if chunk.ID != "" {
					streamResponseID = chunk.ID
				}

				if chunk.Usage != nil {
					// usage chunk 是 adapter 给 gateway 的内部控制事件，不是用户可见内容。
					// 这里不能设置 emitted，也不能写出 SSE，否则客户端会收到空 choices chunk。
					usage := *chunk.Usage
					finalUsage = &usage

					return nil
				}

				if !emitted {
					emitted = true
					s.recordStreamEvent(metrics.StreamEventStarted)
				}

				chunkResp := mapAdapterStreamChunkToGateway(req.Model, chunk, req.StreamIncludeUsage())
				chunkResp.Created = time.Now().Unix()
				return emit(chunkResp)
			})
		streamFacts = streamOutcome.Facts
		err = streamErr
		s.recordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
		endGatewaySpan(streamSpan, err)
		s.recordChannelHealth(channelKey, err)

		if err != nil {
			// 有 final usage 时优先结算：这说明上游已经给出准确 token 用量。
			// 即使后续发生客户端取消、连接尾部错误或 adapter 返回错误，也不能让已产生成本的请求免费。
			if streamFacts != nil {
				if settleErr := settleStreamFacts(); settleErr != nil {
					if !IsChatSettlementRecoveryScheduled(settleErr) {
						s.markRequestRecordFailed(ctx, requestRecord, "stream_chat_settlement_failed", settleErr)
						return settleErr
					}
				}

				// 账务已经成功收口，但调用方仍需知道 stream 末尾发生过错误；
				// HTTP 层如果已写出 SSE，只能中断连接，不能再改写成 JSON error。
				return err
			}

			// 客户端取消不是上游失败，也不应该触发 fallback。
			// 没有 final usage 时缺少可靠用量事实，当前阶段只记录 canceled，不扣费。
			// 后续通过冻结余额、release 或异常风控处理恶意取消风险。
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				if releaseErr := s.releaseChatAuthorizationForBillingException(
					ctx,
					authorization,
					"stream_client_canceled_without_final_usage",
					"stream client canceled before final usage",
				); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return releaseErr
				}

				outcome = metrics.ChatOutcomeCanceled
				s.recordStreamEvent(metrics.StreamEventCanceled)
				s.markRequestCanceled(ctx, requestRecord, attemptRecord, err)
				return err
			}

			s.markAttemptRecordFailed(ctx, attemptRecord, "stream_adapter_error", err)

			if emitted {
				// SSE 已经写出后只能把当前请求标记为失败并结束。
				// 此时 HTTP 层不能再改写普通 JSON error，也不能换 channel 重放已写出的内容。
				if releaseErr := s.releaseChatAuthorizationForBillingException(
					ctx,
					authorization,
					"stream_interrupted_without_final_usage",
					"stream interrupted after emit before final usage",
				); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return releaseErr
				}

				s.markRequestRecordFailed(ctx, requestRecord, "stream_adapter_error_after_emit", err)
				return err
			}

			// 首 chunk 前失败时，客户端还没有看到任何上游内容；只有这时才允许同模型 fallback。
			if !s.retryClassifier.IsRetryable(err) {
				if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return releaseErr
				}

				s.markRequestRecordFailed(ctx, requestRecord, "stream_adapter_error", err)
				return err
			}

			lastErr = err
			continue
		}

		if streamFacts == nil || finalUsage == nil {
			// adapter 正常结束但没有 final usage，不能把它当作可计费成功请求。
			// 这类请求可能是上游不支持 include_usage、代理吞掉尾包，或 parser 漏解析。
			err := failure.New(
				failure.CodeGatewayStreamUsageMissing,
				failure.WithMessage("gateway stream final usage is missing"),
			)

			if releaseErr := s.releaseChatAuthorizationForBillingException(
				ctx,
				authorization,
				"stream_final_usage_missing",
				"stream ended without final usage",
			); releaseErr != nil {
				s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
				return releaseErr
			}

			s.recordStreamEvent(metrics.StreamEventMissingUsage)
			s.markAttemptRecordFailed(ctx, attemptRecord, "stream_usage_missing", err)
			s.markRequestRecordFailed(ctx, requestRecord, "stream_usage_missing", err)
			return err
		}

		if settleErr := settleStreamFacts(); settleErr != nil {
			if !IsChatSettlementRecoveryScheduled(settleErr) {
				s.markRequestRecordFailed(ctx, requestRecord, "stream_chat_settlement_failed", settleErr)
				return settleErr
			}
		}

		if req.StreamIncludeUsage() {
			if err := emitClientStreamUsage(emit, req, streamResponseID, *finalUsage); err != nil {
				return err
			}
		}

		outcome = metrics.ChatOutcomeSuccess
		s.recordStreamEvent(metrics.StreamEventCompleted)
		return nil
	}

	if lastErr != nil {
		if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
			s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
			return releaseErr
		}

		s.markRequestRecordFailed(ctx, requestRecord, "stream_adapter_error", lastErr)
		return lastErr
	}

	if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
		s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
		return releaseErr
	}

	err = failure.Wrap(
		failure.CodeRoutingNoAvailableChannel,
		routing.ErrNoAvailableChannel,
		failure.WithMessage(routing.ErrNoAvailableChannel.Error()),
	)
	s.markRequestRecordFailed(ctx, requestRecord, "no_available_channel", err)
	return err
}

// emitClientStreamUsage 在流式成功结算后，按 OpenAI 约定向客户端写出 usage chunk。
func emitClientStreamUsage(
	emit func(gatewayapi.ChatCompletionStreamResponse) error,
	req gatewayapi.ChatCompletionRequest,
	streamID string,
	usage adapter.ChatUsage,
) error {
	if streamID == "" {
		streamID = "chatcmpl_unio"
	}

	usageResp := mapAdapterUsageToGateway(usage)
	return emit(gatewayapi.ChatCompletionStreamResponse{
		ID:      streamID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []gatewayapi.ChatCompletionStreamChoice{},
		Usage:   &usageResp,
	})
}
