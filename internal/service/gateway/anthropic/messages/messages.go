package messages

import (
	"context"
	"errors"
	"fmt"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/logfields"
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
		ProjectID:             principal.ProjectID,
		ModelID:               req.Model,
		IngressProtocol:       routing.ProtocolAnthropic,
		Operation:             routing.OperationMessages,
		RouteID:               principal.RouteID,
		ProjectDefaultRouteID: principal.ProjectDefaultRouteID,
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
		RequestRecord:       requestRecord,
		Principal:           principal,
		CandidatePrices:     candidatePlan.CandidateSalePrices(),
		InputTokens:         candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens: estimateMaxOutputTokens(req),
	})
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_failed", err)
		return nil, err
	}

	var lastErr error

	for _, prepared := range candidatePlan.Candidates {
		index := prepared.RouteIndex
		candidate := prepared.Route

		channelKey := lifecycle.MetricsID(candidate.Channel.ID)
		if !s.breakerAllow(channelKey) {
			continue
		}

		attemptRecord, err := s.createAttemptRecord(ctx, requestRecord, index, candidate)
		if err != nil {
			if releaseErr := s.releaseMessageAuthorization(ctx, authorization); releaseErr != nil {
				s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
				return nil, releaseErr
			}

			s.markRequestRecordFailed(ctx, requestRecord, "request_attempt_create_failed", err)
			return nil, err
		}

		messagesAdapter, ok := s.registry.Messages(candidate.AdapterKey)
		if !ok {
			err := failure.New(
				failure.CodeGatewayAdapterNotRegistered,
				failure.WithMessage(fmt.Sprintf("gateway messages adapter %q not registered", candidate.AdapterKey)),
			)

			if releaseErr := s.releaseMessageAuthorization(ctx, authorization); releaseErr != nil {
				s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
				return nil, releaseErr
			}

			s.markAttemptRecordFailed(ctx, attemptRecord, "adapter_not_registered", err)
			s.markRequestRecordFailed(ctx, requestRecord, "adapter_not_registered", err)

			return nil, err
		}

		adapterCtx, adapterSpan := lifecycle.StartGatewaySpan(ctx, "adapter.messages", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
		upstreamStart := time.Now()
		adapterResp, err := messagesAdapter.Messages(adapterCtx, candidate.Channel,
			mapGatewayRequestToAdapter(req, candidate.UpstreamModel))
		responseStartedAt := time.Now()
		s.recordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
		lifecycle.EndGatewaySpan(adapterSpan, err)
		s.recordChannelHealth(channelKey, err)
		if err != nil {
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				if releaseErr := s.releaseMessageAuthorization(ctx, authorization); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
					return nil, releaseErr
				}

				outcome = metrics.ChatOutcomeCanceled
				s.markRequestCanceled(ctx, requestRecord, attemptRecord, err)
				return nil, err
			}

			s.markAttemptRecordFailed(ctx, attemptRecord, "adapter_error", err)

			if !s.retryClassifier.IsRetryable(err) {
				if releaseErr := s.releaseMessageAuthorization(ctx, authorization); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
					return nil, releaseErr
				}

				s.markRequestRecordFailed(ctx, requestRecord, "adapter_error", err)
				return nil, err
			}
			lastErr = err
			continue
		}

		s.recordRoutingSelected(candidate.ProviderID, candidate.Channel.ID, req.Model)
		logfields.SetRoute(ctx, req.Model, lifecycle.MetricsID(candidate.ProviderID), lifecycle.MetricsID(candidate.Channel.ID))

		settleCtx, settleSpan := lifecycle.StartGatewaySpan(ctx, "gateway.settlement")
		settleErr := s.chatSettlement.SettleSuccessfulChat(settleCtx, lifecycle.ChatSettlementParams{
			RequestRecord:     requestRecord,
			AttemptRecord:     attemptRecord,
			Principal:         principal,
			Authorization:     authorization,
			ResponseProtocol:  requestlog.ProtocolAnthropic,
			ResponseID:        adapterResp.ID,
			ResponseModelID:   req.Model,
			ResponseStartedAt: &responseStartedAt,
			ModelDBID:         candidate.ModelDBID,
			FinalProviderID:   candidate.ProviderID,
			FinalChannelID:    candidate.Channel.ID,
			Facts:             adapterResp.Facts,
		})
		lifecycle.EndSettlementSpan(settleSpan, settleErr)
		s.recordSettlement(lifecycle.SettlementOutcomeFromErr(settleErr))
		if settleErr != nil && !lifecycle.IsChatSettlementRecoveryScheduled(settleErr) {
			// 上游已成功、settlement 永久失败且无 recovery job 接管：释放冻结余额并记账务异常风险，
			// 否则用户余额被永久冻结（GAP-7-007 只覆盖 job 已创建后的重试/dead；release 自身幂等）。
			if releaseErr := s.releaseMessageAuthorizationForBillingException(
				ctx,
				authorization,
				"messages_settlement_failed_after_upstream_success",
				"messages settlement permanently failed after upstream success without recovery job",
			); releaseErr != nil {
				s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
				return nil, releaseErr
			}
			s.markRequestRecordFailed(ctx, requestRecord, "messages_settlement_failed", settleErr)
			return nil, settleErr
		}

		// 非流式成功：响应将由 handler 在本调用返回后写出，交付视为完成。
		s.lifecycle.MarkDeliveryCompleted(ctx, requestRecord)

		outcome = metrics.ChatOutcomeSuccess

		resp := mapAdapterResponseToGateway(req.Model, *adapterResp)
		return &resp, nil
	}

	if lastErr != nil {
		if releaseErr := s.releaseMessageAuthorization(ctx, authorization); releaseErr != nil {
			s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
			return nil, releaseErr
		}

		s.markRequestRecordFailed(ctx, requestRecord, "adapter_error", lastErr)
		return nil, lastErr
	}

	if releaseErr := s.releaseMessageAuthorization(ctx, authorization); releaseErr != nil {
		s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
		return nil, releaseErr
	}

	err = failure.Wrap(
		failure.CodeRoutingNoAvailableChannel,
		routing.ErrNoAvailableChannel,
		failure.WithMessage(routing.ErrNoAvailableChannel.Error()),
	)
	s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)

	return nil, err
}
