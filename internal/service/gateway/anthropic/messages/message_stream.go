package messages

import (
	"context"
	"encoding/json"
	"fmt"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
)

// StreamMessage 编排流式 Anthropic Messages 请求，并通过 emit 写出原生 SSE 事件。
func (s *MessagesService) StreamMessage(ctx context.Context, req gatewayapi.MessageRequest, emit func(gatewayapi.StreamFrame) error) error {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	requestRecord, err := s.createMessageRequestRecord(ctx, principal, req, true)
	if err != nil {
		return err
	}

	outcome := metrics.ChatOutcomeFailed
	defer func() {
		s.recordMessageRequest(true, outcome)
	}()

	ctx, span := lifecycle.StartGatewaySpan(ctx, "gateway.messages_stream")
	defer span.End()

	planCtx, planSpan := lifecycle.StartGatewaySpan(ctx, "gateway.routing")
	plan, err := s.router.PlanChat(planCtx, routing.ChatRouteRequest{
		UserID:          principal.UserID,
		ModelID:         req.Model,
		IngressProtocol: routing.ProtocolAnthropic,
		Endpoint:       routing.EndpointMessages,
		RouteID:         principal.RouteID,
	})
	lifecycle.EndGatewaySpan(planSpan, err)
	if err != nil {
		s.lifecycle.RecordRoutingFailure(ctx, requestRecord, principal.RouteID, err)
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
	}

	// 会话粘性（大 uncache 缺口 P0）：x-claude-code-session-id 头优先、metadata.user_id 回退；
	// 粘住渠道已被硬摘除（不在池/熔断）时清绑定重选（R5）。
	stickySession := s.sticky.Resolve(ctx, lifecycle.StickyResolveParams{
		Protocol:           routing.ProtocolAnthropic,
		RouteID:            principal.RouteID,
		APIKeyID:           principal.APIKeyID,
		SessionKey:         sessionhint.AnthropicSessionKey(ctx, req.Metadata),
		RouteStickyEnabled: plan.RouteStickyEnabled,
	})

	candidatePlan, err := s.prepareMessageCandidates(ctx, req, plan.Candidates, plan.RouteMode, true, stickySession.BoundChannelID())
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
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
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
	}

	authorization, err := s.chatAuthorizer.AuthorizeChat(ctx, lifecycle.ChatAuthorizeParams{
		RequestRecord:            requestRecord,
		Principal:                principal,
		CandidatePrices:          candidatePlan.CandidateSalePrices(),
		LongContextPolicy:        candidatePlan.LongContextPolicy(),
		InputTokens:              candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens:      estimateMaxOutputTokens(req),
		CandidateMaxOutputTokens: candidatePlan.CandidateMaxOutputTokens(),
	})
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_failed", err)
		return err
	}

	var streamAdapter messagesadapter.StreamMessagesAdapter
	runResult, err := lifecycle.RunStreamGeneric(ctx, s.attemptRunner, lifecycle.RunStreamParamsGeneric[messagesadapter.MessageStreamEvent]{
		RequestRecord:           requestRecord,
		Principal:               principal,
		Authorization:           authorization,
		Candidates:              candidatePlan.Candidates,
		RequestedModelID:        req.Model,
		ResponseProtocol:        requestlog.ProtocolAnthropic,
		ConservativeInputTokens: candidatePlan.ConservativeInputTokens,
		CountOutputTokens:       anthropicPartialOutputTokenCounter,
		Sticky:                  stickySession,
		Codes: lifecycle.RunStreamCodes{
			AuthorizationReleaseFailedCode:              "messages_authorization_release_failed",
			SettlementFailedCode:                        "stream_messages_settlement_failed",
			PartialSettlementBillingExceptionReasonCode: "stream_messages_settlement_failed_after_upstream_success",
			PartialSettlementBillingExceptionReason:     "stream messages partial settlement permanently failed without recovery job",
			SettlementBillingExceptionReasonCode:        "stream_messages_settlement_failed_after_upstream_success",
			SettlementBillingExceptionReason:            "stream messages settlement permanently failed after upstream success without recovery job",
		},
		ResolveAdapter: func(candidate routing.ChatRouteCandidate) error {
			adapter, ok := s.registry.StreamMessages(candidate.AdapterKey)
			if !ok {
				return failure.New(
					failure.CodeGatewayAdapterNotRegistered,
					failure.WithMessage(fmt.Sprintf("gateway stream messages adapter %q not registered", candidate.AdapterKey)),
				)
			}
			streamAdapter = adapter
			return nil
		},
		Stream: func(ctx context.Context, candidate routing.ChatRouteCandidate, onChunk func(messagesadapter.MessageStreamEvent) error) (*adapter.ResponseFacts, error) {
			streamCtx, streamSpan := lifecycle.StartGatewaySpan(ctx, "adapter.stream_messages", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
			streamOutcome, streamErr := streamAdapter.StreamMessages(streamCtx, candidate.Channel, mapGatewayRequestToAdapter(req, candidate.UpstreamModel), onChunk)
			lifecycle.EndGatewaySpan(streamSpan, streamErr)
			return streamOutcome.Facts, streamErr
		},
		EmitChunk: func(ev messagesadapter.MessageStreamEvent, ack lifecycle.StreamWriteAck) error {
			if err := emit(gatewayapi.StreamFrame{
				EventType: ev.Type,
				Data:      patchStreamEventCatalogModel(req.Model, ev),
			}); err != nil {
				return err
			}
			ack()
			return nil
		},
		Finish: func(_ string, _ adapter.ChatUsage, _ string, ack lifecycle.StreamWriteAck) error {
			stopPayload, marshalErr := json.Marshal(gatewayapi.StreamMessageStop{Type: "message_stop"})
			if marshalErr != nil {
				return marshalErr
			}
			if err := emit(gatewayapi.StreamFrame{EventType: "message_stop", Data: stopPayload}); err != nil {
				return err
			}
			ack()
			return nil
		},
		ChunkMeta: messagesStreamChunkMeta,
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

func anthropicPartialOutputTokenCounter(_ string, text string) int64 {
	return messagesadapter.CountOutputTokens(text)
}

func messagesStreamChunkMeta(ev messagesadapter.MessageStreamEvent) lifecycle.StreamChunkMeta {
	meta := lifecycle.StreamChunkMeta{
		FirstTokenEligible: ev.Type == "message_start" || ev.Type == "content_block_delta",
		VisibleText:        parseStreamTextDelta(ev),
	}
	if ev.Type == "message_start" {
		meta.ID = parseStreamMessageID(ev.Data)
	}
	if ev.Usage != nil {
		usage := *ev.Usage
		meta.Usage = &adapter.ChatUsage{
			PromptTokens:     int(usage.InputTokens),
			CompletionTokens: int(usage.OutputTokens),
			TotalTokens:      int(usage.InputTokens + usage.OutputTokens),
		}
	}
	return meta
}

func parseStreamMessageID(data json.RawMessage) string {
	var payload struct {
		Message struct {
			ID string `json:"id"`
		} `json:"message"`
	}
	if err := json.Unmarshal(data, &payload); err != nil {
		return ""
	}
	return payload.Message.ID
}

// parseStreamTextDelta 从 Anthropic content_block_delta 事件提取可见文本增量（text_delta），
// 供 partial settlement 估算 output token；非文本增量返回空。
func parseStreamTextDelta(ev messagesadapter.MessageStreamEvent) string {
	if ev.Type != "content_block_delta" {
		return ""
	}
	var payload struct {
		Delta struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"delta"`
	}
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		return ""
	}
	if payload.Delta.Type != "text_delta" {
		return ""
	}
	return payload.Delta.Text
}
