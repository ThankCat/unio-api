package responses

import (
	"context"
	"fmt"
	"time"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
)

// partialOutputTokenCounter 按 upstream model 估算可见输出文本的 token 数，供 partial settlement 使用。
// 直传候选的可见文本暂不计入（VisibleText 为空，P0 偏保守）；tokenizer 失败返回 0。
func partialOutputTokenCounter(model string, text string) int64 {
	n, err := chatcompletionsadapter.CountOutputTokens(model, text)
	if err != nil {
		return 0
	}
	return n
}

// StreamResponse 编排流式 Responses 请求，并通过 emit 写出 Responses 命名事件（Codex 主路径）。
//
// 按候选 adapter 能力分流（统一 chunk 载体 responsesStreamCarrier，混合候选池共享一条 AttemptRunner
// 流式 fallback 循环）：
//   - 直传候选：直连上游 /responses，上游 SSE 命名事件原文透传（仅改写 model 回显），response.completed
//     由上游下发，不再补发；
//   - 桥接候选（chat-only 第三方）：沿用 DEC-014，chat SSE delta 经 streamEncoder 翻译成 Responses 事件，
//     收尾 response.completed 由 streamEncoder 在结算后补发。
//
// 资金关键流式链路（emitted 后禁止 fallback、final usage 缺失处理、tail-error 仍尽力结算、settlement、
// 终态写入）全部由 RunStreamGeneric 承担，与 chatcompletions 共用同一份实现。
//
// streamEncoder 在整个请求生命周期只构造一次：RunStream 仅在「首帧前」允许同模型 fallback，而 encoder
// 只在首个桥接内容 chunk 后才推进状态，fallback 时仍是初始态，可安全复用；直传候选不触碰 encoder。
func (s *ResponsesService) StreamResponse(ctx context.Context, req gatewayapi.ResponsesRequest, emit func(gatewayapi.ResponsesStreamEvent) error) error {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	var effort string
	if req.Reasoning != nil && req.Reasoning.Effort != nil {
		effort = *req.Reasoning.Effort
	}
	requestRecord, err := s.lifecycle.CreateRequest(ctx, principal, req.Model, true, lifecycle.NormalizeOpenAIEffort(effort, req.Model))
	if err != nil {
		return err
	}

	// outcome 默认 failed，仅成功/取消路径覆盖；defer 保证每个流式请求只计一次。
	outcome := metrics.ChatOutcomeFailed
	defer func() {
		s.lifecycle.RecordRequest(true, outcome)
	}()

	ctx, span := lifecycle.StartGatewaySpan(ctx, "gateway.responses_stream")
	defer span.End()

	planCtx, planSpan := lifecycle.StartGatewaySpan(ctx, "gateway.routing")
	plan, err := s.router.PlanChat(planCtx, routing.ChatRouteRequest{
		UserID:          principal.UserID,
		ModelID:         req.Model,
		IngressProtocol: routing.ProtocolOpenAI,
		Operation:       routing.OperationResponses,
		RouteID:         principal.RouteID,
	})
	lifecycle.EndGatewaySpan(planSpan, err)
	if err != nil {
		s.lifecycle.RecordRoutingFailure(ctx, requestRecord, principal.RouteID, err)
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
	}

	// 会话粘性（大 uncache 缺口 P0）：提取会话键并 lookup 既有绑定，置顶绑定渠道；
	// 粘住渠道已被硬摘除（不在池/熔断）时清绑定重选（R5）。
	stickySession := s.sticky.Resolve(ctx, lifecycle.StickyResolveParams{
		Protocol:           routing.ProtocolOpenAI,
		RouteID:            principal.RouteID,
		APIKeyID:           principal.APIKeyID,
		SessionKey:         sessionhint.OpenAISessionKey(ctx, req.PromptCacheKey),
		RouteStickyEnabled: plan.RouteStickyEnabled,
	})

	candidatePlan, err := s.prepareResponsesCandidates(ctx, req, plan.Candidates, plan.RouteMode, true, true, stickySession.BoundChannelID())
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
	}
	stickySession.ApplyPlanOutcome(ctx, candidatePlan)
	if principal.RouteID != nil {
		s.lifecycle.RecordRoutingDecision(ctx, lifecycle.RoutingDecisionTraceInput{
			Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
			PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(),
		})
	}
	if err := requestadmission.ReserveIfPresent(ctx, candidatePlan.ConservativeInputTokens); err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
	}

	authorization, err := s.chatAuthorizer.AuthorizeChat(ctx, lifecycle.ChatAuthorizeParams{
		RequestRecord:            requestRecord,
		Principal:                principal,
		CandidatePrices:          candidatePlan.CandidateSalePrices(),
		LongContextPolicy:        candidatePlan.LongContextPolicy(),
		InputTokens:              candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens:      estimateMaxCompletionTokens(req),
		CandidateMaxOutputTokens: candidatePlan.CandidateMaxOutputTokens(),
	})
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, "chat_authorization_failed", err)
		return err
	}

	var (
		streamAdapter       chatcompletionsadapter.StreamChatAdapter
		directStreamAdapter responsesadapter.StreamResponsesAdapter
		// usedDirect 记录成功路径是否走了直传：直传的 response.completed 已原文透传，收尾不再补发。
		usedDirect bool
	)
	var activeWriteAck lifecycle.StreamWriteAck
	acknowledgedEmit := func(event gatewayapi.ResponsesStreamEvent) error {
		if err := emit(event); err != nil {
			return err
		}
		if activeWriteAck != nil {
			activeWriteAck()
		}
		return nil
	}
	withWriteAck := func(ack lifecycle.StreamWriteAck, write func() error) error {
		activeWriteAck = ack
		defer func() { activeWriteAck = nil }()
		return write()
	}
	encoder := newStreamEncoder(req, newResponsesID("resp"), time.Now().Unix(), acknowledgedEmit)

	runResult, err := lifecycle.RunStreamGeneric(ctx, s.attemptRunner, lifecycle.RunStreamParamsGeneric[responsesStreamCarrier]{
		RequestRecord:           requestRecord,
		Principal:               principal,
		Authorization:           authorization,
		Candidates:              candidatePlan.Candidates,
		RequestedModelID:        req.Model,
		ResponseProtocol:        requestlog.ProtocolOpenAI,
		ConservativeInputTokens: candidatePlan.ConservativeInputTokens,
		CountOutputTokens:       partialOutputTokenCounter,
		Sticky:                  stickySession,
		ResolveAdapter: func(candidate routing.ChatRouteCandidate) error {
			if s.registry.HasStreamResponses(candidate.AdapterKey) {
				adapter, ok := s.registry.StreamResponses(candidate.AdapterKey)
				if !ok {
					return failure.New(
						failure.CodeGatewayAdapterNotRegistered,
						failure.WithMessage(fmt.Sprintf("gateway stream responses adapter %q not registered", candidate.AdapterKey)),
					)
				}
				directStreamAdapter = adapter
				return nil
			}
			adapter, ok := s.registry.StreamChat(candidate.AdapterKey)
			if !ok {
				return failure.New(
					failure.CodeGatewayAdapterNotRegistered,
					failure.WithMessage(fmt.Sprintf("gateway stream chat adapter %q not registered", candidate.AdapterKey)),
				)
			}
			streamAdapter = adapter
			return nil
		},
		Stream: func(ctx context.Context, candidate routing.ChatRouteCandidate, onChunk func(responsesStreamCarrier) error) (*adapter.ResponseFacts, error) {
			if s.registry.HasStreamResponses(candidate.AdapterKey) {
				body, bodyErr := encodeUpstreamResponsesBody(req, candidate.UpstreamModel, true)
				if bodyErr != nil {
					return nil, bodyErr
				}
				streamCtx, streamSpan := lifecycle.StartGatewaySpan(ctx, "adapter.stream_responses", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
				streamOutcome, streamErr := directStreamAdapter.StreamResponse(streamCtx, candidate.Channel, responsesadapter.Request{Body: body, BetaHeader: req.OpenAIBeta}, func(chunk responsesadapter.StreamChunk) error {
					event := chunk
					return onChunk(responsesStreamCarrier{direct: &event})
				})
				lifecycle.EndGatewaySpan(streamSpan, streamErr)
				return streamOutcome.Facts, streamErr
			}

			// multi-agent 无法降级为单请求 Chat Completions：桥接候选显式拒绝，避免静默退化为单 agent 却照常计费。
			if req.MultiAgentEnabled() {
				return nil, multiAgentBridgeUnsupported()
			}
			chatReq, _ := mapResponsesRequestToChat(req, candidate.UpstreamModel)
			streamCtx, streamSpan := lifecycle.StartGatewaySpan(ctx, "adapter.stream_chat_completions", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
			streamOutcome, streamErr := streamAdapter.StreamChatCompletions(streamCtx, candidate.Channel, chatReq, func(chunk chatcompletionsadapter.ChatStreamChunk) error {
				delta := chunk
				return onChunk(responsesStreamCarrier{chat: &delta})
			})
			lifecycle.EndGatewaySpan(streamSpan, streamErr)
			return streamOutcome.Facts, streamErr
		},
		EmitChunk: func(carrier responsesStreamCarrier, ack lifecycle.StreamWriteAck) error {
			return withWriteAck(ack, func() error {
				if carrier.direct != nil {
					usedDirect = true
					return emitDirectStreamEvent(acknowledgedEmit, req.Model, *carrier.direct)
				}
				return encoder.Handle(*carrier.chat)
			})
		},
		Finish: func(_ string, finalUsage adapter.ChatUsage, finishReason string, ack lifecycle.StreamWriteAck) error {
			return withWriteAck(ack, func() error {
				if usedDirect {
					// 直传：response.completed 已在流中由上游原文透传，无需补发收尾帧。
					return nil
				}
				usage := finalUsage
				return encoder.Complete(finishReason, &usage)
			})
		},
		ChunkMeta: responsesStreamCarrierMeta,
	})
	if runResult.RoutingFallback && principal.RouteID != nil {
		s.lifecycle.RecordRoutingDecision(ctx, lifecycle.RoutingDecisionTraceInput{
			Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
			PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(),
			FallbackOccurred: true, FallbackChain: runResult.TransportChain,
		})
	}
	outcome = runResult.Outcome
	return err
}
