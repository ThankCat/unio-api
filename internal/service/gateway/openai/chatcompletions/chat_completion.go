package chatcompletions

import (
	"context"
	"fmt"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
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

	requiredCapabilities := gatewayapi.RequiredCapabilities(req)

	planCtx, planSpan := lifecycle.StartGatewaySpan(ctx, "gateway.routing")
	plan, err := s.router.PlanChat(planCtx, routing.ChatRouteRequest{
		ProjectID:             principal.ProjectID,
		ModelID:               req.Model,
		IngressProtocol:       routing.ProtocolOpenAI,
		Operation:             routing.OperationChatCompletions,
		RequiredCapabilities:  requiredCapabilities,
		RequestLimits:         gatewayapi.RequestLimits(req),
		RouteID:               principal.RouteID,
		ProjectDefaultRouteID: principal.ProjectDefaultRouteID,
	})
	lifecycle.EndGatewaySpan(planSpan, err)
	// 闸门判定（含 enforce 拒绝）先落审计列，再处理路由错误：observation 在 enforce 拒绝时仍随 plan 返回。
	s.lifecycle.RecordCapabilityResult(ctx, requestRecord, plan.Capability)
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
	}

	candidatePlan, err := s.prepareChatCandidates(ctx, req, plan.Candidates, plan.RouteMode, false)
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
	}

	authorization, err := s.chatAuthorizer.AuthorizeChat(ctx, lifecycle.ChatAuthorizeParams{
		RequestRecord:       requestRecord,
		Principal:           principal,
		CandidatePrices:     candidatePlan.CandidateSalePrices(),
		InputTokens:         candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens: estimateMaxCompletionTokens(req),
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
		RequestRecord:        requestRecord,
		Principal:            principal,
		Authorization:        authorization,
		Candidates:           candidatePlan.Candidates,
		RequestedModelID:     req.Model,
		ResponseProtocol:     requestlog.ProtocolOpenAI,
		RequiredCapabilities: requiredCapabilities.StringKeys(),
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
