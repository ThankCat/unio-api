package chatcompletions

import (
	"context"
	"errors"
	"fmt"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/logfields"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
)

// CreateChatCompletion 编排非流式 chat completion 请求，并返回 OpenAI-compatible HTTP DTO。
func (s *ChatCompletionService) CreateChatCompletion(ctx context.Context, req gatewayapi.ChatCompletionRequest) (*gatewayapi.ChatCompletionResponse, error) {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return nil, failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	requestRecord, err := s.createRequestRecord(ctx, principal, req, false)
	if err != nil {
		return nil, err
	}

	// outcome 默认 failed，仅在成功/取消路径覆盖；
	// defer 保证每个被编排的请求只计数一次，且不遗漏任何提前返回的失败分支。
	outcome := metrics.ChatOutcomeFailed
	defer func() {
		s.recordChatRequest(false, outcome)
	}()

	ctx, span := startGatewaySpan(ctx, "gateway.chat_completion")
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
		return nil, err
	}

	candidatePlan, err := s.prepareChatCandidates(ctx, req, plan.Candidates, false)
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, routingFailureCode(err), err)
		return nil, err
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
		return nil, err
	}

	var lastErr error

	for _, prepared := range candidatePlan.Candidates {
		index := prepared.RouteIndex
		candidate := prepared.Route

		// channel 处于熔断 open 状态时直接跳过，尝试下一个同模型 channel；
		// 跳过不产生上游调用，也不写 attempt（attempt_index 允许出现空洞）。
		channelKey := metricsID(candidate.Channel.ID)
		if !s.breakerAllow(channelKey) {
			continue
		}

		// 每个 candidate 都先创建 attempt，再调用 adapter。
		// 这样即使后续 fallback，也能在 request_attempts 里还原完整尝试链路。
		attemptRecord, err := s.createAttemptRecord(ctx, requestRecord, index, candidate)
		if err != nil {
			if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
				s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
				return nil, releaseErr
			}

			s.markRequestRecordFailed(ctx, requestRecord, "request_attempt_create_failed", err)
			return nil, err
		}

		chatAdapter, ok := s.registry.Chat(candidate.AdapterKey)
		if !ok {
			err := failure.New(
				failure.CodeGatewayAdapterNotRegistered,
				failure.WithMessage(fmt.Sprintf("gateway chat adapter %q not registered", candidate.AdapterKey)),
			)

			if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
				s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
				return nil, releaseErr
			}

			s.markAttemptRecordFailed(ctx, attemptRecord, "adapter_not_registered", err)
			s.markRequestRecordFailed(ctx, requestRecord, "adapter_not_registered", err)

			return nil, err
		}

		adapterCtx, adapterSpan := startGatewaySpan(ctx, "adapter.chat_completions", upstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
		upstreamStart := time.Now()
		adapterResp, err := chatAdapter.ChatCompletions(adapterCtx, candidate.Channel,
			mapGatewayRequestToAdapter(req, candidate.UpstreamModel))
		s.recordUpstream(candidate.ProviderID, candidate.Channel.ID, time.Since(upstreamStart), err)
		endGatewaySpan(adapterSpan, err)
		s.recordChannelHealth(channelKey, err)
		if err != nil {
			// 客户端取消不是上游失败，也不应该触发 fallback。
			// 此时还没有进入 settlement，不会写 usage、price snapshot 或 ledger。
			if errors.Is(err, context.Canceled) || errors.Is(ctx.Err(), context.Canceled) {
				if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return nil, releaseErr
				}

				outcome = metrics.ChatOutcomeCanceled
				s.markRequestCanceled(ctx, requestRecord, attemptRecord, err)
				return nil, err
			}

			s.markAttemptRecordFailed(ctx, attemptRecord, "adapter_error", err)

			if !s.retryClassifier.IsRetryable(err) {
				if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
					s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
					return nil, releaseErr
				}

				s.markRequestRecordFailed(ctx, requestRecord, "adapter_error", err)
				return nil, err
			}
			lastErr = err
			continue
		}

		s.recordRoutingSelected(candidate.ProviderID, candidate.Channel.ID, req.Model)
		logfields.SetRoute(ctx, req.Model, metricsID(candidate.ProviderID), metricsID(candidate.Channel.ID))

		// 非流式成功请求的账务事实必须在 settlement 事务内一起提交。
		// 这里不能先返回 HTTP response 再异步扣费，否则 usage、price snapshot、ledger 和 request status 会出现不一致窗口。
		settleCtx, settleSpan := startGatewaySpan(ctx, "gateway.settlement")
		settleErr := s.chatSettlement.SettleSuccessfulChat(settleCtx, ChatSettlementParams{
			RequestRecord:    requestRecord,
			AttemptRecord:    attemptRecord,
			Principal:        principal,
			Authorization:    authorization,
			ResponseProtocol: requestlog.ProtocolOpenAI,
			ResponseID:       adapterResp.ID,
			ResponseModelID:  req.Model,
			ModelDBID:        candidate.ModelDBID,
			FinalProviderID:  candidate.ProviderID,
			FinalChannelID:   candidate.Channel.ID,
			Facts:            adapterResp.Facts,
		})
		endSettlementSpan(settleSpan, settleErr)
		s.recordSettlement(settlementOutcomeFromErr(settleErr))
		if settleErr != nil && !IsChatSettlementRecoveryScheduled(settleErr) {
			s.markRequestRecordFailed(ctx, requestRecord, "chat_settlement_failed", settleErr)
			return nil, settleErr
		}

		outcome = metrics.ChatOutcomeSuccess

		resp := mapAdapterResponseToGateway(req.Model, *adapterResp)
		resp.Created = time.Now().Unix()
		return &resp, nil
	}

	if lastErr != nil {
		if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
			s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
			return nil, releaseErr
		}

		s.markRequestRecordFailed(ctx, requestRecord, "adapter_error", lastErr)
		return nil, lastErr
	}

	if releaseErr := s.releaseChatAuthorization(ctx, authorization); releaseErr != nil {
		s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_release_failed", releaseErr)
		return nil, releaseErr
	}

	err = failure.Wrap(
		failure.CodeRoutingNoAvailableChannel,
		routing.ErrNoAvailableChannel,
		failure.WithMessage(routing.ErrNoAvailableChannel.Error()),
	)
	s.markRequestRecordFailed(ctx, requestRecord, routingFailureCode(err), err)

	return nil, err
}
