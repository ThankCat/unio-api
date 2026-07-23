package messages

import (
	"context"
	"fmt"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic/messages"
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

// CreateMessage 编排非流式 Anthropic Messages 请求，并返回公开 DTO 与内部交付 finalizer。
func (s *MessagesService) CreateMessage(ctx context.Context, req gatewayapi.MessageRequest) (*lifecycle.NonStreamResult[*gatewayapi.MessageResponse], error) {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return nil, failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	requestRecord, err := s.createMessageRequestRecord(ctx, principal, req, false)
	if err != nil {
		return nil, err
	}

	outcome := metrics.ChatOutcomeFailed
	defer func() {
		s.recordMessageRequest(false, outcome)
	}()

	ctx, span := lifecycle.StartGatewaySpan(ctx, "gateway.messages")
	defer span.End()

	planCtx, planSpan := lifecycle.StartGatewaySpan(ctx, "gateway.routing")
	plan, err := s.router.PlanChat(planCtx, routing.ChatRouteRequest{
		UserID:          principal.UserID,
		ModelID:         req.Model,
		IngressProtocol: routing.ProtocolAnthropic,
		Operation:       routing.OperationMessages,
		RouteID:         principal.RouteID,
	})
	lifecycle.EndGatewaySpan(planSpan, err)
	if err != nil {
		s.lifecycle.RecordRoutingFailure(ctx, requestRecord, principal.RouteID, err)
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
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

	candidatePlan, err := s.prepareMessageCandidates(ctx, req, plan.Candidates, plan.RouteMode, false, stickySession.BoundChannelID())
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
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
		return nil, err
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
		return nil, err
	}

	var (
		messagesAdapter messagesadapter.MessagesAdapter
		adapterResp     *messagesadapter.MessageResponse
	)
	runResult, err := s.attemptRunner.RunNonStream(ctx, lifecycle.RunNonStreamParams{
		RequestRecord:    requestRecord,
		Principal:        principal,
		Authorization:    authorization,
		Candidates:       candidatePlan.Candidates,
		RequestedModelID: req.Model,
		ResponseProtocol: requestlog.ProtocolAnthropic,
		EstimatedTokens:  candidatePlan.ConservativeInputTokens,
		Sticky:           stickySession,
		Codes: lifecycle.RunNonStreamCodes{
			AuthorizationReleaseFailedCode:       "messages_authorization_release_failed",
			SettlementFailedCode:                 "messages_settlement_failed",
			SettlementBillingExceptionReasonCode: "messages_settlement_failed_after_upstream_success",
			SettlementBillingExceptionReason:     "messages settlement permanently failed after upstream success without recovery job",
		},
		ResolveAdapter: func(candidate routing.ChatRouteCandidate) error {
			adapter, ok := s.registry.Messages(candidate.AdapterKey)
			if !ok {
				return failure.New(
					failure.CodeGatewayAdapterNotRegistered,
					failure.WithMessage(fmt.Sprintf("gateway messages adapter %q not registered", candidate.AdapterKey)),
				)
			}
			messagesAdapter = adapter
			return nil
		},
		Invoke: func(ctx context.Context, candidate routing.ChatRouteCandidate) (lifecycle.AttemptSuccess, error) {
			adapterCtx, adapterSpan := lifecycle.StartGatewaySpan(ctx, "adapter.messages", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
			resp, err := messagesAdapter.Messages(adapterCtx, candidate.Channel, mapGatewayRequestToAdapter(req, candidate.UpstreamModel))
			lifecycle.EndGatewaySpan(adapterSpan, err)
			if err != nil {
				return lifecycle.AttemptSuccess{}, err
			}
			adapterResp = resp
			return lifecycle.AttemptSuccess{ResponseID: resp.ID, Facts: resp.Facts}, nil
		},
	})
	if runResult.RoutingFallback && principal.RouteID != nil {
		s.lifecycle.RecordRoutingDecision(ctx, lifecycle.RoutingDecisionTraceInput{
			Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
			PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(),
			FallbackOccurred: true, FallbackChain: runResult.TransportChain,
		})
	}
	outcome = runResult.Outcome
	if err != nil {
		return nil, err
	}
	resp := mapAdapterResponseToGateway(req.Model, *adapterResp)
	return lifecycle.NewNonStreamResult(&resp, runResult.Delivery), nil
}
