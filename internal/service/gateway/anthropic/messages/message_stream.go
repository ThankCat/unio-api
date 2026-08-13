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

	routeRequest := routing.ChatRouteRequest{
		UserID:          principal.UserID,
		ModelID:         req.Model,
		IngressProtocol: routing.ProtocolAnthropic,
		Endpoint:        routing.EndpointMessages,
		RouteID:         principal.RouteID,
	}
	if err := s.router.ValidateChat(ctx, routeRequest); err != nil {
		s.lifecycle.RecordRequestRejected(err)
		return err
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
	plan, err := s.router.PlanChat(planCtx, routeRequest)
	lifecycle.EndGatewaySpan(planSpan, err)
	if err != nil {
		s.lifecycle.RecordRoutingFailure(ctx, requestRecord, principal.RouteID, err)
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
	}

	// 会话粘性（大 uncache 缺口 P0）：x-claude-code-session-id 头优先、metadata.user_id 回退；
	// 粘住渠道已被硬摘除（不在池/熔断）时清绑定重选（R5）。
	stickyHint := sessionhint.AnthropicSessionHint(ctx, req.Metadata)
	stickySession := s.sticky.Resolve(ctx, lifecycle.StickyResolveParams{
		Protocol:   routing.ProtocolAnthropic,
		RouteID:    principal.RouteID,
		APIKeyID:   principal.APIKeyID,
		ModelID:    plan.ModelDBID,
		SessionKey: stickyHint.Key,
		Source:     stickyHint.Source,
		Candidates: plan.Candidates,
		Mode:       plan.RouteMode,
	})

	candidatePlan, err := s.prepareMessageCandidates(ctx, req, plan.Candidates, plan.RouteMode, true, stickySession.BoundChannelID())
	if err != nil {
		if principal.RouteID != nil {
			s.lifecycle.RecordRoutingDecisionFailure(ctx, lifecycle.RoutingDecisionTraceInput{
				Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
				PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(),
				Sticky: stickySession.Audit(),
			}, err)
		}
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
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
		CandidatePrices:          candidatePlan.CandidateSalePrices(),
		LongContextPolicy:        candidatePlan.LongContextPolicy(),
		InputTokens:              candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens:      estimateMaxOutputTokens(req),
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
			streamOutcome, streamErr := streamAdapter.StreamMessages(streamCtx, candidate.Channel,
				mapGatewayRequestToAdapter(req, candidate.UpstreamModel), onChunk)
			lifecycle.EndGatewaySpan(streamSpan, streamErr)
			return streamOutcome.Facts, streamErr
		},
		EmitChunk: func(ev messagesadapter.MessageStreamEvent, acks lifecycle.StreamWriteAcks) error {
			if err := emit(gatewayapi.StreamFrame{
				EventType: ev.Type,
				Data:      patchStreamEventCatalogModel(req.Model, ev),
			}); err != nil {
				return err
			}
			acks.Frame()
			if messagesadapter.FirstTokenPayload(ev) != "" {
				acks.FirstToken()
			}
			return nil
		},
		Finish: func(_ string, _ *adapter.ChatUsage, _ string, acks lifecycle.StreamWriteAcks) error {
			stopPayload, marshalErr := json.Marshal(gatewayapi.StreamMessageStop{Type: "message_stop"})
			if marshalErr != nil {
				return marshalErr
			}
			if err := emit(gatewayapi.StreamFrame{EventType: "message_stop", Data: stopPayload}); err != nil {
				return err
			}
			acks.Frame()
			return nil
		},
		ChunkMeta: messagesStreamChunkMeta,
		ChunkSize: messagesStreamChunkSize,
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

func messagesStreamChunkSize(ev messagesadapter.MessageStreamEvent) int {
	return 128 + len(ev.Type) + len(ev.Data) + len(messagesadapter.FirstTokenPayload(ev))
}

func anthropicPartialOutputTokenCounter(_ string, text string) int64 {
	return messagesadapter.CountOutputTokens(text)
}

func messagesStreamChunkMeta(ev messagesadapter.MessageStreamEvent) lifecycle.StreamChunkMeta {
	// 首字判定与可见文本同源（见 adapter 侧 FirstTokenPayload 的说明）。
	firstTokenPayload := messagesadapter.FirstTokenPayload(ev)
	meta := lifecycle.StreamChunkMeta{
		FirstTokenEligible: firstTokenPayload != "",
		VisibleText:        firstTokenPayload,
		ProtocolEventType:  ev.Type,
		TokenKind:          messagesTokenKind(ev),
		Classification:     messagesEventClassification(ev, firstTokenPayload),
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

func messagesTokenKind(ev messagesadapter.MessageStreamEvent) string {
	if messagesadapter.FirstTokenPayload(ev) == "" {
		return ""
	}
	if ev.Type == "content_block_start" {
		var payload struct {
			ContentBlock struct {
				Type string `json:"type"`
			} `json:"content_block"`
		}
		if json.Unmarshal(ev.Data, &payload) == nil {
			switch payload.ContentBlock.Type {
			case "thinking":
				return "reasoning"
			case "tool_use", "server_tool_use":
				return "tool_call"
			default:
				return "text"
			}
		}
	}
	if ev.Type == "content_block_delta" {
		var payload struct {
			Delta struct {
				Type string `json:"type"`
			} `json:"delta"`
		}
		if json.Unmarshal(ev.Data, &payload) == nil {
			switch payload.Delta.Type {
			case "thinking_delta":
				return "reasoning"
			case "input_json_delta":
				return "tool_call"
			default:
				return "text"
			}
		}
	}
	return "generated_output"
}

func messagesEventClassification(ev messagesadapter.MessageStreamEvent, firstTokenPayload string) string {
	if firstTokenPayload != "" {
		return "effective_token"
	}
	switch ev.Type {
	case "message_start":
		return "lifecycle"
	case "ping":
		return "heartbeat"
	case "message_delta":
		if ev.Usage != nil {
			return "usage"
		}
		return "terminal"
	case "content_block_stop", "message_stop", "error":
		return "terminal"
	default:
		return "empty_generation"
	}
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
