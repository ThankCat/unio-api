package messages

import (
	"context"
	"fmt"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// CreateMessage 编排非流式 Anthropic Messages 请求，并返回原生 Message 响应。
func (s *MessagesService) CreateMessage(ctx context.Context, req gatewayapi.MessageRequest) (*gatewayapi.MessageResponse, error) {
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
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
	}

	candidatePlan, err := s.prepareMessageCandidates(ctx, req, plan.Candidates, plan.RouteMode, false)
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
	}

	authorization, err := s.chatAuthorizer.AuthorizeChat(ctx, lifecycle.ChatAuthorizeParams{
		RequestRecord:            requestRecord,
		Principal:                principal,
		CandidatePrices:          candidatePlan.CandidateSalePrices(),
		LongContextPolicy:       candidatePlan.LongContextPolicy(),
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
	outcome = runResult.Outcome
	if err != nil {
		return nil, err
	}
	resp := mapAdapterResponseToGateway(req.Model, *adapterResp)
	return &resp, nil
}
