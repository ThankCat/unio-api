package chatcompletions

import (
	"context"
	"fmt"
	"time"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
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

	ctx, span := lifecycle.StartGatewaySpan(ctx, "gateway.chat_completion")
	defer span.End()

	planCtx, planSpan := lifecycle.StartGatewaySpan(ctx, "gateway.routing")
	plan, err := s.router.PlanChat(planCtx, routing.ChatRouteRequest{
		UserID:          principal.UserID,
		ModelID:         req.Model,
		IngressProtocol: routing.ProtocolOpenAI,
		Operation:       routing.OperationChatCompletions,
		RouteID:         principal.RouteID,
	})
	lifecycle.EndGatewaySpan(planSpan, err)
	if err != nil {
		s.lifecycle.RecordRoutingFailure(ctx, requestRecord, principal.RouteID, err)
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
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

	candidatePlan, err := s.prepareChatCandidates(ctx, req, plan.Candidates, plan.RouteMode, false, stickySession.BoundChannelID())
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
		s.markRequestRecordFailed(ctx, requestRecord, "chat_authorization_failed", err)
		return nil, err
	}

	// 候选 fallback 循环（attempt 审计 / 上游指标 / 错误分类 / settlement / 终态写入）由共享
	// AttemptRunner 驱动；OpenAI chat 与 responses 复用同一份资金关键链路，避免逐字复制。
	// 协议差异通过两个 typed 闭包注入：ResolveAdapter 解析 typed adapter，Invoke 执行一次上游调用
	// 并把 typed response 捕获到本作用域，供成功后映射 HTTP DTO。
	var (
		chatAdapter chatcompletionsadapter.ChatAdapter
		adapterResp *chatcompletionsadapter.ChatResponse
	)
	runResult, err := s.attemptRunner.RunNonStream(ctx, lifecycle.RunNonStreamParams{
		RequestRecord:    requestRecord,
		Principal:        principal,
		Authorization:    authorization,
		Candidates:       candidatePlan.Candidates,
		RequestedModelID: req.Model,
		ResponseProtocol: requestlog.ProtocolOpenAI,
		EstimatedTokens:  candidatePlan.ConservativeInputTokens,
		Sticky:           stickySession,
		ResolveAdapter: func(candidate routing.ChatRouteCandidate) error {
			adapter, ok := s.registry.Chat(candidate.AdapterKey)
			if !ok {
				return failure.New(
					failure.CodeGatewayAdapterNotRegistered,
					failure.WithMessage(fmt.Sprintf("gateway chat adapter %q not registered", candidate.AdapterKey)),
				)
			}
			chatAdapter = adapter
			return nil
		},
		Invoke: func(ctx context.Context, candidate routing.ChatRouteCandidate) (lifecycle.AttemptSuccess, error) {
			adapterCtx, adapterSpan := lifecycle.StartGatewaySpan(ctx, "adapter.chat_completions", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
			resp, err := chatAdapter.ChatCompletions(adapterCtx, candidate.Channel,
				mapGatewayRequestToAdapter(req, candidate.UpstreamModel))
			lifecycle.EndGatewaySpan(adapterSpan, err)
			if err != nil {
				return lifecycle.AttemptSuccess{}, err
			}

			adapterResp = resp
			return lifecycle.AttemptSuccess{ResponseID: resp.ID, Facts: resp.Facts}, nil
		},
	})
	if runResult.Attempts > 1 && principal.RouteID != nil {
		s.lifecycle.RecordRoutingDecision(ctx, lifecycle.RoutingDecisionTraceInput{
			Request: requestRecord, RouteID: *principal.RouteID, Mode: plan.RouteMode,
			PoolSize: plan.PoolSize, Plan: candidatePlan, StickyChannelID: stickySession.ResolvedChannelID(), Attempts: runResult.Attempts,
		})
	}
	outcome = runResult.Outcome
	if err != nil {
		return nil, err
	}

	resp := mapAdapterResponseToGateway(req.Model, *adapterResp)
	// 优先透传上游 created；仅当上游未返回（0）时回退本地时间，保持 OpenAI 形状有值。
	if resp.Created == 0 {
		resp.Created = time.Now().Unix()
	}
	return &resp, nil
}
