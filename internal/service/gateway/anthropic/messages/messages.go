package messages

import (
	"context"
	"errors"
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

	routeRequest := routing.ChatRouteRequest{
		UserID:          principal.UserID,
		ModelID:         req.Model,
		IngressProtocol: routing.ProtocolAnthropic,
		Endpoint:        routing.EndpointMessages,
		RouteID:         principal.RouteID,
	}
	if err := s.router.ValidateChat(ctx, routeRequest); err != nil {
		s.lifecycle.RecordRequestRejected(err)
		return nil, err
	}

	requestParams, err := s.prepareMessageRequestRecord(ctx, principal, req, false)
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
	plan, err := s.router.PlanChat(planCtx, routeRequest)
	lifecycle.EndGatewaySpan(planSpan, err)
	if err != nil {
		requestRecord, createErr := s.lifecycle.CreatePreparedRequest(ctx, requestParams)
		if createErr != nil {
			return nil, createErr
		}
		s.lifecycle.RecordRoutingFailure(ctx, requestRecord, principal.RouteID, err)
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
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

	candidatePlan, err := s.prepareMessageCandidates(ctx, req, plan.Candidates, plan.RouteMode, false, stickySession.BoundChannelID())
	if err != nil {
		requestRecord, createErr := s.lifecycle.CreatePreparedRequest(ctx, requestParams)
		if createErr != nil {
			return nil, createErr
		}
		if principal.RouteID != nil {
			s.lifecycle.RecordRoutingDecisionFailure(ctx, lifecycle.RoutingDecisionTraceInput{
				Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
				PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(),
				Sticky: stickySession.Audit(),
			}, err)
		}
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
	}

	authorized, err := s.lifecycle.AuthorizeNewChat(ctx, lifecycle.ChatAuthorizeNewRequestParams{
		Request:                  requestParams,
		CandidatePrices:          candidatePlan.CandidateSalePrices(),
		LongContextPolicy:        candidatePlan.LongContextPolicy(),
		InputTokens:              candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens:      estimateMaxOutputTokens(req),
		CandidateMaxOutputTokens: candidatePlan.CandidateMaxOutputTokens(),
	})
	if err != nil {
		return nil, err
	}
	requestRecord := authorized.RequestRecord
	authorization := authorized.Authorization
	stickySession.ApplyPlanOutcome(ctx, candidatePlan)
	if principal.RouteID != nil {
		s.lifecycle.RecordRoutingDecision(ctx, lifecycle.RoutingDecisionTraceInput{
			Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
			PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(),
			Sticky: stickySession.Audit(), Status: lifecycle.TraceStatusPartial,
		})
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
		UpstreamCostWithoutUsage: func(err error) bool {
			return errors.Is(err, messagesadapter.ErrMessagesMissingUsage)
		},
		Sticky: stickySession,
		Codes: lifecycle.RunNonStreamCodes{
			AuthorizationReleaseFailedCode:       "messages_authorization_release_failed",
			SettlementFailedCode:                 "messages_settlement_failed",
			SettlementBillingExceptionReasonCode: "messages_settlement_failed_after_upstream_success",
			SettlementBillingExceptionReason:     "messages settlement permanently failed after upstream success without recovery job",
			UpstreamCostWithoutUsageCode:         "messages_cost_without_usage",
			UpstreamCostWithoutUsageReasonCode:   "messages_missing_usage",
			UpstreamCostWithoutUsageReason:       "anthropic messages returned 2xx without required usage; upstream cost may have been incurred",
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
			resp, err := messagesAdapter.Messages(adapterCtx, candidate.Channel,
				mapGatewayRequestToAdapter(req, candidate.UpstreamModel))
			lifecycle.EndGatewaySpan(adapterSpan, err)
			if err != nil {
				return lifecycle.AttemptSuccess{}, err
			}
			adapterResp = resp
			return lifecycle.AttemptSuccess{ResponseID: resp.ID, Facts: resp.Facts}, nil
		},
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
	if err != nil {
		return nil, err
	}
	resp := mapAdapterResponseToGateway(req.Model, *adapterResp)
	return lifecycle.NewNonStreamResult(&resp, runResult.Delivery), nil
}
