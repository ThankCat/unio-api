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
	"github.com/ThankCat/unio-gateway/internal/core/servicetier"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
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
//   - 直传候选：直连上游 /responses，上游 SSE 命名事件原文透传（仅改写 model 回显），成功终态暂存到
//     durable settlement 完成后再原样写出；
//   - 桥接候选（chat-only 第三方）：沿用 DEC-014，chat SSE delta 经 streamEncoder 翻译成 Responses 事件，
//     收尾 response.completed 由 streamEncoder 在结算后补发。
//
// 资金关键流式链路（emitted 后禁止 fallback、final usage 缺失处理、tail-error 仍尽力结算、settlement、
// 终态写入）全部由 RunStreamGeneric 承担，与 chatcompletions 共用同一份实现。
//
// 每个桥接 attempt 都构造独立 streamEncoder，确保首字前 fallback 不会继承上一候选的协议状态；
// 直传候选不触碰 encoder。
func (s *ResponsesService) StreamResponse(ctx context.Context, req gatewayapi.ResponsesRequest, emit func(gatewayapi.ResponsesStreamEvent) error) error {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}
	tierRequest, err := servicetier.NormalizeOpenAIRequest(req.ServiceTier)
	if err != nil {
		return err
	}
	var effort string
	if req.Reasoning != nil && req.Reasoning.Effort != nil {
		effort = *req.Reasoning.Effort
	}
	routeRequest := routing.ChatRouteRequest{
		UserID:          principal.UserID,
		ModelID:         req.Model,
		IngressProtocol: routing.ProtocolOpenAI,
		Endpoint:        routing.EndpointResponses,
		RouteID:         principal.RouteID,
	}
	if err := s.router.ValidateChat(ctx, routeRequest); err != nil {
		s.lifecycle.RecordRequestRejected(err)
		return err
	}

	requestRecord, err := s.lifecycle.CreateRequestWithServiceTier(ctx, principal, req.Model, true, lifecycle.NormalizeOpenAIEffort(effort, req.Model), tierRequest.Tier)
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
	plan, err := s.router.PlanChat(planCtx, routeRequest)
	lifecycle.EndGatewaySpan(planSpan, err)
	if err != nil {
		s.lifecycle.RecordRoutingFailure(ctx, requestRecord, principal.RouteID, err)
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
	}

	// 会话粘性（大 uncache 缺口 P0）：提取会话键并 lookup 既有绑定，置顶绑定渠道；
	// 粘住渠道已被硬摘除（不在池/熔断）时清绑定重选（R5）。
	stickyHint := sessionhint.OpenAISessionHint(ctx, req.PromptCacheKey)
	stickySession := s.sticky.Resolve(ctx, lifecycle.StickyResolveParams{
		Protocol:   routing.ProtocolOpenAI,
		RouteID:    principal.RouteID,
		APIKeyID:   principal.APIKeyID,
		ModelID:    plan.ModelDBID,
		SessionKey: stickyHint.Key,
		Source:     stickyHint.Source,
		Candidates: plan.Candidates,
		Mode:       plan.RouteMode,
	})

	candidatePlan, err := s.prepareResponsesCandidates(ctx, req, plan.Candidates, plan.RouteMode, true, true, stickySession.BoundChannelID())
	if err != nil {
		if principal.RouteID != nil {
			s.lifecycle.RecordRoutingDecisionFailure(ctx, lifecycle.RoutingDecisionTraceInput{
				Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
				PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(),
				Sticky: stickySession.Audit(),
			}, err)
		}
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
	}
	stickySession.ApplyPlanOutcome(ctx, candidatePlan)
	if principal.RouteID != nil {
		s.lifecycle.RecordRoutingDecision(ctx, lifecycle.RoutingDecisionTraceInput{
			Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
			PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(),
			Sticky: stickySession.Audit(), Status: lifecycle.TraceStatusPartial,
		})
	}

	authorization, err := s.lifecycle.AuthorizeChat(ctx, lifecycle.ChatAuthorizeParams{
		RequestRecord:            requestRecord,
		Principal:                principal,
		CandidatePrices:          candidatePlan.CandidateSalePricesForTier(requestRecord.RequestedServiceTier),
		LongContextPolicy:        candidatePlan.LongContextPolicy(),
		InputTokens:              candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens:      estimateMaxCompletionTokens(req),
		CandidateMaxOutputTokens: candidatePlan.CandidateMaxOutputTokens(),
	})
	if err != nil {
		if principal.RouteID != nil {
			s.lifecycle.CompleteRoutingTrace(ctx, lifecycle.RoutingDecisionTraceInput{
				Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
				PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(),
				Sticky: stickySession.Audit(),
			}, lifecycle.RunResult{}, err)
		}
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, "chat_authorization_failed", err)
		return err
	}

	var (
		streamAdapter       chatcompletionsadapter.StreamChatAdapter
		directStreamAdapter responsesadapter.StreamResponsesAdapter
		encoder             *streamEncoder
		directSelected      bool
		directTerminal      *responsesadapter.StreamChunk
	)
	var activeWriteAcks lifecycle.StreamWriteAcks
	acknowledgedBridgeEmit := func(event gatewayapi.ResponsesStreamEvent) error {
		if err := emit(event); err != nil {
			return err
		}
		if activeWriteAcks.Frame != nil {
			activeWriteAcks.Frame()
		}
		if activeWriteAcks.FirstToken != nil && bridgeResponsesEventHasFirstToken(event) {
			activeWriteAcks.FirstToken()
		}
		return nil
	}
	withWriteAcks := func(acks lifecycle.StreamWriteAcks, write func() error) error {
		activeWriteAcks = acks
		defer func() { activeWriteAcks = lifecycle.StreamWriteAcks{} }()
		return write()
	}

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
			streamAdapter = nil
			directStreamAdapter = nil
			encoder = nil
			directSelected = false
			directTerminal = nil
			if s.registry.HasStreamResponses(candidate.AdapterKey) {
				adapter, ok := s.registry.StreamResponses(candidate.AdapterKey)
				if !ok {
					return failure.New(
						failure.CodeGatewayAdapterNotRegistered,
						failure.WithMessage(fmt.Sprintf("gateway stream responses adapter %q not registered", candidate.AdapterKey)),
					)
				}
				directStreamAdapter = adapter
				directSelected = true
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
			encoder = newStreamEncoder(req, newResponsesID("resp"), time.Now().Unix(), acknowledgedBridgeEmit)
			return nil
		},
		Stream: func(ctx context.Context, candidate routing.ChatRouteCandidate, onChunk func(responsesStreamCarrier) error) (*adapter.ResponseFacts, error) {
			attemptReq := requestForOpenAIChannel(req, tierRequest.Tier, candidate)
			if s.registry.HasStreamResponses(candidate.AdapterKey) {
				body, bodyErr := encodeUpstreamResponsesBody(attemptReq, candidate.UpstreamModel, true)
				if bodyErr != nil {
					return nil, bodyErr
				}
				streamCtx, streamSpan := lifecycle.StartGatewaySpan(ctx, "adapter.stream_responses", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
				streamOutcome, streamErr := directStreamAdapter.StreamResponse(streamCtx, candidate.Channel, responsesadapter.Request{Body: body, BetaHeader: req.OpenAIBeta}, func(chunk responsesadapter.StreamChunk) error {
					if isDirectResponsesSuccessTerminal(chunk.EventType) {
						terminal := chunk
						terminal.Data = append([]byte(nil), chunk.Data...)
						directTerminal = &terminal
					}
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
			chatReq, _ := mapResponsesRequestToChat(attemptReq, candidate.UpstreamModel)
			streamCtx, streamSpan := lifecycle.StartGatewaySpan(ctx, "adapter.stream_chat_completions", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
			streamOutcome, streamErr := streamAdapter.StreamChatCompletions(streamCtx, candidate.Channel, chatReq, func(chunk chatcompletionsadapter.ChatStreamChunk) error {
				delta := chunk
				return onChunk(responsesStreamCarrier{chat: &delta})
			})
			lifecycle.EndGatewaySpan(streamSpan, streamErr)
			return streamOutcome.Facts, streamErr
		},
		EmitChunk: func(carrier responsesStreamCarrier, acks lifecycle.StreamWriteAcks) error {
			if carrier.direct != nil {
				if err := emitDirectStreamEvent(emit, req.Model, *carrier.direct); err != nil {
					return err
				}
				acks.Frame()
				if responsesadapter.FirstTokenPayload(*carrier.direct) != "" {
					acks.FirstToken()
				}
				return nil
			}
			return withWriteAcks(acks, func() error {
				return encoder.Handle(*carrier.chat)
			})
		},
		Finish: func(_ string, finalUsage *adapter.ChatUsage, finishReason string, acks lifecycle.StreamWriteAcks) error {
			return withWriteAcks(acks, func() error {
				if directSelected {
					if directTerminal == nil {
						return failure.New(
							failure.CodeAdapterInvalidResponse,
							failure.WithMessage("upstream responses stream terminal event is missing"),
						)
					}
					if err := emitDirectStreamEvent(emit, req.Model, *directTerminal); err != nil {
						return err
					}
					if acks.Frame != nil {
						acks.Frame()
					}
					return nil
				}
				return encoder.Complete(finishReason, finalUsage)
			})
		},
		ChunkMeta: responsesStreamCarrierMeta,
		ChunkSize: responsesStreamCarrierSize,
	})
	// 每个请求在生命周期结束时都要把 partial trace 收口为 complete（§13.1），
	// 不只在发生 fallback 时——普通成功请求同样需要能解释「为什么选了这条渠道」。
	if principal.RouteID != nil {
		s.lifecycle.CompleteRoutingTrace(ctx, lifecycle.RoutingDecisionTraceInput{
			Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
			PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(),
			Sticky: stickySession.Audit(),
		}, runResult, err)
	}
	outcome = runResult.Outcome
	return err
}

func bridgeResponsesEventHasFirstToken(event gatewayapi.ResponsesStreamEvent) bool {
	switch event.Type {
	case gatewayapi.EventOutputTextDelta,
		gatewayapi.EventReasoningTextDelta,
		gatewayapi.EventReasoningSummaryTextDelta,
		gatewayapi.EventRefusalDelta,
		gatewayapi.EventFunctionCallArgsDelta:
		return event.Delta != ""
	case gatewayapi.EventOutputItemAdded:
		return event.Item != nil && event.Item.Type == "function_call" &&
			(event.Item.Name != "" || event.Item.Arguments != "")
	default:
		return false
	}
}

func responsesStreamCarrierSize(carrier responsesStreamCarrier) int {
	if carrier.direct != nil {
		return 128 + len(carrier.direct.EventType) + len(carrier.direct.Data) +
			len(responsesadapter.FirstTokenPayload(*carrier.direct))
	}
	if carrier.chat != nil {
		return 128 + len(carrier.chat.ID) + len(carrier.chat.Model) + len(carrier.chat.Content) +
			len(carrier.chat.ToolCalls) + len(carrier.chat.FunctionCall) +
			len(chatcompletionsadapter.FirstTokenPayload(*carrier.chat))
	}
	return 1
}
