package messages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
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
		return err
	}

	candidatePlan, err := s.prepareMessageCandidates(ctx, req, plan.Candidates, plan.RouteMode, true)
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
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
		return err
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
				return releaseErr
			}

			s.markRequestRecordFailed(ctx, requestRecord, "request_attempt_create_failed", err)
			return err
		}

		streamAdapter, ok := s.registry.StreamMessages(candidate.AdapterKey)
		if !ok {
			err := failure.New(
				failure.CodeGatewayAdapterNotRegistered,
				failure.WithMessage(fmt.Sprintf("gateway stream messages adapter %q not registered", candidate.AdapterKey)),
			)

			if releaseErr := s.releaseMessageAuthorization(ctx, authorization); releaseErr != nil {
				s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
				return releaseErr
			}

			s.markAttemptRecordFailed(ctx, attemptRecord, "adapter_not_registered", err)
			s.markRequestRecordFailed(ctx, requestRecord, "adapter_not_registered", err)

			return err
		}

		emitted := false
		var streamFacts *adapter.ResponseFacts
		messageID := ""
		var responseStartedAt *time.Time

		settleStreamFacts := func() error {
			if streamFacts == nil {
				return failure.New(
					failure.CodeGatewayStreamUsageMissing,
					failure.WithMessage("gateway stream response facts are missing"),
				)
			}

			s.recordRoutingSelected(candidate.ProviderID, candidate.Channel.ID, req.Model)
			logfields.SetRoute(ctx, req.Model, lifecycle.MetricsID(candidate.ProviderID), lifecycle.MetricsID(candidate.Channel.ID))

			settlementCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
			defer cancel()

			settleCtx, settleSpan := lifecycle.StartGatewaySpan(settlementCtx, "gateway.settlement")
			responseID := messageID
			if responseID == "" {
				responseID = streamFacts.UpstreamResponseID
			}
			settleErr := s.chatSettlement.SettleSuccessfulChat(settleCtx, lifecycle.ChatSettlementParams{
				RequestRecord:     requestRecord,
				AttemptRecord:     attemptRecord,
				Principal:         principal,
				Authorization:     authorization,
				ResponseProtocol:  requestlog.ProtocolAnthropic,
				ResponseID:        responseID,
				ResponseModelID:   req.Model,
				ResponseStartedAt: responseStartedAt,
				ModelDBID:         candidate.ModelDBID,
				FinalProviderID:   candidate.ProviderID,
				FinalChannelID:    candidate.Channel.ID,
				Facts:             *streamFacts,
			})
			lifecycle.EndSettlementSpan(settleSpan, settleErr)
			s.recordSettlement(lifecycle.SettlementOutcomeFromErr(settleErr))
			return settleErr
		}

		streamCtx, streamSpan := lifecycle.StartGatewaySpan(ctx, "adapter.stream_messages", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
		upstreamStart := time.Now()
		streamOutcome, streamErr := streamAdapter.StreamMessages(streamCtx, candidate.Channel,
			mapGatewayRequestToAdapter(req, candidate.UpstreamModel),
			func(ev messagesadapter.MessageStreamEvent) error {
				if messageID == "" && ev.Type == "message_start" {
					messageID = parseStreamMessageID(ev.Data)
				}

				if !emitted {
					now := time.Now()
					responseStartedAt = &now
					emitted = true
					s.recordStreamEvent(metrics.StreamEventStarted)
				}

				return emit(gatewayapi.StreamFrame{
					EventType: ev.Type,
					Data:      patchStreamEventCatalogModel(req.Model, ev),
				})
			})
		streamFacts = streamOutcome.Facts
		err = streamErr
		s.recordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
		lifecycle.EndGatewaySpan(streamSpan, err)
		s.recordChannelHealth(channelKey, err)

		if err != nil {
			if streamFacts != nil {
				if settleErr := settleStreamFacts(); settleErr != nil {
					if !lifecycle.IsChatSettlementRecoveryScheduled(settleErr) {
						// settlement 永久失败且无 recovery job 接管：释放冻结余额并记账务异常风险，
						// 否则用户余额被永久冻结（同非流式处理；release 自身幂等）。
						if releaseErr := s.releaseMessageAuthorizationForBillingException(
							ctx,
							authorization,
							"stream_messages_settlement_failed_after_upstream_success",
							"stream messages settlement permanently failed after upstream success without recovery job",
						); releaseErr != nil {
							s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
							return releaseErr
						}
						s.markRequestRecordFailed(ctx, requestRecord, "stream_messages_settlement_failed", settleErr)
						return settleErr
					}
				}
				return err
			}

			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				if releaseErr := s.releaseMessageAuthorizationForBillingException(
					ctx,
					authorization,
					"stream_client_canceled_without_final_usage",
					"stream client canceled before final usage",
				); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
					return releaseErr
				}

				outcome = metrics.ChatOutcomeCanceled
				s.recordStreamEvent(metrics.StreamEventCanceled)
				s.markRequestCanceled(ctx, requestRecord, attemptRecord, err)
				return err
			}

			s.markAttemptRecordFailed(ctx, attemptRecord, "stream_adapter_error", err)

			if emitted {
				if releaseErr := s.releaseMessageAuthorizationForBillingException(
					ctx,
					authorization,
					"stream_interrupted_without_final_usage",
					"stream interrupted after emit before final usage",
				); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
					return releaseErr
				}

				s.markRequestRecordFailed(ctx, requestRecord, "stream_adapter_error_after_emit", err)
				return err
			}

			if !s.retryClassifier.IsRetryable(err) {
				if releaseErr := s.releaseMessageAuthorization(ctx, authorization); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
					return releaseErr
				}

				s.markRequestRecordFailed(ctx, requestRecord, "stream_adapter_error", err)
				return err
			}

			lastErr = err
			continue
		}

		if streamFacts == nil {
			err := failure.New(
				failure.CodeGatewayStreamUsageMissing,
				failure.WithMessage("gateway stream final usage is missing"),
			)

			if releaseErr := s.releaseMessageAuthorizationForBillingException(
				ctx,
				authorization,
				"stream_final_usage_missing",
				"stream ended without final usage",
			); releaseErr != nil {
				s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
				return releaseErr
			}

			s.recordStreamEvent(metrics.StreamEventMissingUsage)
			s.markAttemptRecordFailed(ctx, attemptRecord, "stream_usage_missing", err)
			s.markRequestRecordFailed(ctx, requestRecord, "stream_usage_missing", err)
			return err
		}

		if settleErr := settleStreamFacts(); settleErr != nil {
			if !lifecycle.IsChatSettlementRecoveryScheduled(settleErr) {
				// settlement 永久失败且无 recovery job 接管：释放冻结余额并记账务异常风险，
				// 否则用户余额被永久冻结（同非流式处理；release 自身幂等）。
				if releaseErr := s.releaseMessageAuthorizationForBillingException(
					ctx,
					authorization,
					"stream_messages_settlement_failed_after_upstream_success",
					"stream messages settlement permanently failed after upstream success without recovery job",
				); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
					return releaseErr
				}
				s.markRequestRecordFailed(ctx, requestRecord, "stream_messages_settlement_failed", settleErr)
				return settleErr
			}
		}

		stopPayload, marshalErr := json.Marshal(gatewayapi.StreamMessageStop{Type: "message_stop"})
		if marshalErr != nil {
			return marshalErr
		}
		if err := emit(gatewayapi.StreamFrame{EventType: "message_stop", Data: stopPayload}); err != nil {
			return err
		}

		outcome = metrics.ChatOutcomeSuccess
		s.recordStreamEvent(metrics.StreamEventCompleted)
		return nil
	}

	if lastErr != nil {
		if releaseErr := s.releaseMessageAuthorization(ctx, authorization); releaseErr != nil {
			s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
			return releaseErr
		}

		s.markRequestRecordFailed(ctx, requestRecord, "stream_adapter_error", lastErr)
		return lastErr
	}

	if releaseErr := s.releaseMessageAuthorization(ctx, authorization); releaseErr != nil {
		s.markRequestRecordFailed(ctx, requestRecord, "messages_authorization_release_failed", releaseErr)
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
