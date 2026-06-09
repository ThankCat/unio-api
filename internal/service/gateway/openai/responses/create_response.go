package responses

import (
	"context"
	"fmt"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// CreateResponse 编排非流式 Responses 请求，并返回 OpenAI Responses 协议响应对象。
//
// 桥接路径（DEC-014）：Responses 请求经 routing → authorization → 共享 AttemptRunner 调用 OpenAI
// adapter（请求方向 responses→chat 翻译在 Invoke 内按候选 upstream model 进行），成功后把内部
// ChatResponse 翻译回 Responses 响应。资金关键循环、attempt 审计与终态写入由 AttemptRunner 统一承担。
func (s *ResponsesService) CreateResponse(ctx context.Context, req gatewayapi.ResponsesRequest) (*gatewayapi.ResponsesResponse, error) {
	chatResp, err := s.executeNonStreamChat(ctx, req)
	if err != nil {
		return nil, err
	}
	resp := mapChatResponseToResponses(req, *chatResp)
	return &resp, nil
}

// executeNonStreamChat 执行非流式桥接编排，返回内部 ChatResponse。
//
// CreateResponse 与 CompactHistory（无状态压缩）共用同一条资金关键链路：两者都是一次可计费的
// 非流式上游调用，差异只在成功后的响应翻译方向。本方法承担 routing、authorization、共享
// AttemptRunner 候选 fallback 计费循环、metrics outcome 与终态写入。
func (s *ResponsesService) executeNonStreamChat(ctx context.Context, req gatewayapi.ResponsesRequest) (*openai.ChatResponse, error) {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return nil, failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	requestRecord, err := s.lifecycle.CreateRequest(ctx, principal, req.Model, false)
	if err != nil {
		return nil, err
	}

	// outcome 默认 failed，仅成功/取消路径覆盖；defer 保证每个请求只计一次，不遗漏提前返回的失败分支。
	outcome := metrics.ChatOutcomeFailed
	defer func() {
		s.lifecycle.RecordRequest(false, outcome)
	}()

	ctx, span := lifecycle.StartGatewaySpan(ctx, "gateway.responses")
	defer span.End()

	requiredCapabilities := gatewayapi.RequiredCapabilities(req)

	planCtx, planSpan := lifecycle.StartGatewaySpan(ctx, "gateway.routing")
	plan, err := s.router.PlanChat(planCtx, routing.ChatRouteRequest{
		ProjectID:            principal.ProjectID,
		ModelID:              req.Model,
		IngressProtocol:      routing.ProtocolOpenAI,
		Operation:            routing.OperationResponses,
		RequiredCapabilities: requiredCapabilities,
		RequestLimits:        gatewayapi.RequestLimits(req),
	})
	lifecycle.EndGatewaySpan(planSpan, err)
	// 闸门判定（含 enforce 拒绝）先落审计列，再处理路由错误：observation 在 enforce 拒绝时仍随 plan 返回。
	s.lifecycle.RecordCapabilityResult(ctx, requestRecord, plan.Capability)
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
	}

	candidatePlan, err := s.prepareResponsesCandidates(ctx, req, plan.Candidates, false)
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return nil, err
	}

	firstCandidate := candidatePlan.Candidates[0].Route
	authorization, err := s.chatAuthorizer.AuthorizeChat(ctx, lifecycle.ChatAuthorizeParams{
		RequestRecord:       requestRecord,
		Principal:           principal,
		ModelDBID:           firstCandidate.ModelDBID,
		InputTokens:         candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens: estimateMaxCompletionTokens(req),
	})
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, "chat_authorization_failed", err)
		return nil, err
	}

	// 候选 fallback 计费循环复用共享 AttemptRunner（与 chatcompletions 同一份资金关键链路）。
	// ResolveAdapter 解析复用的 OpenAI adapter；Invoke 在候选维度做 responses→chat 请求翻译并发起一次上游调用，
	// 把内部 ChatResponse 捕获到本作用域，供成功后翻译回 Responses 响应。
	var (
		chatAdapter openai.ChatAdapter
		adapterResp *openai.ChatResponse
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
			chatReq, _ := mapResponsesRequestToChat(req, candidate.UpstreamModel)
			adapterCtx, adapterSpan := lifecycle.StartGatewaySpan(ctx, "adapter.chat_completions", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
			resp, err := chatAdapter.ChatCompletions(adapterCtx, candidate.Channel, chatReq)
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

	return adapterResp, nil
}
