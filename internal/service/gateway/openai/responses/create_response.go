package responses

import (
	"context"
	"fmt"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	responsesadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// CreateResponse 编排非流式 Responses 请求，并返回 OpenAI Responses 协议响应对象。
//
// 按候选 adapter 能力分流（DEC：上游 responses 直传 + 第三方桥接）：
//   - 直传候选（adapter 原生支持上游 /responses）：直连上游 /responses，响应原文透传（仅改写 model 回显）；
//   - 桥接候选（chat-only 第三方，如 deepseek）：沿用 DEC-014 responses→chat 桥接，再翻译回 Responses 响应。
//
// 两条路径产出统一 adapter.ResponseFacts，资金关键循环、attempt 审计与终态写入由共享 AttemptRunner 承担。
func (s *ResponsesService) CreateResponse(ctx context.Context, req gatewayapi.ResponsesRequest) (*gatewayapi.ResponsesResponse, error) {
	result, err := s.executeResponse(ctx, req, true)
	if err != nil {
		return nil, err
	}
	if result.direct != nil {
		// 直传：原文透传上游响应体，仅改写顶层 model 回显为客户请求名（零转换）。
		data := rewriteResponsesModel(result.direct.Raw, req.Model)
		return gatewayapi.RawResponsesResponse(data), nil
	}
	resp := mapChatResponseToResponses(req, *result.chat)
	return &resp, nil
}

// responseResult 是一次非流式 Responses 成功调用的判别式结果：恰好其一非空。
//
// direct 来自上游 responses 直传（Raw 原文 + facts）；chat 来自 responses→chat 桥接（内部 ChatResponse）。
type responseResult struct {
	chat   *chatcompletionsadapter.ChatResponse
	direct *responsesadapter.Response
}

// executeNonStreamChat 执行非流式桥接编排，返回内部 ChatResponse（强制桥接，不走直传）。
//
// CompactHistory（无状态会话压缩）共用本入口：它本质是一次 chat 摘要调用，不适用 responses 直传。
func (s *ResponsesService) executeNonStreamChat(ctx context.Context, req gatewayapi.ResponsesRequest) (*chatcompletionsadapter.ChatResponse, error) {
	result, err := s.executeResponse(ctx, req, false)
	if err != nil {
		return nil, err
	}
	return result.chat, nil
}

// executeResponse 执行非流式 Responses 候选 fallback 计费循环，按候选能力分流直传/桥接。
//
// allowDirect=false 时强制全部走桥接（CompactHistory）。本方法承担 routing、authorization、共享
// AttemptRunner 候选 fallback 计费循环、metrics outcome 与终态写入；协议差异（直传/桥接调用与响应捕获）
// 由 ResolveAdapter / Invoke 闭包按候选注入。
func (s *ResponsesService) executeResponse(ctx context.Context, req gatewayapi.ResponsesRequest, allowDirect bool) (responseResult, error) {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return responseResult{}, failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	requestRecord, err := s.lifecycle.CreateRequest(ctx, principal, req.Model, false)
	if err != nil {
		return responseResult{}, err
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
		ProjectID:             principal.ProjectID,
		ModelID:               req.Model,
		IngressProtocol:       routing.ProtocolOpenAI,
		Operation:             routing.OperationResponses,
		RequiredCapabilities:  requiredCapabilities,
		RequestLimits:         gatewayapi.RequestLimits(req),
		RouteID:               principal.RouteID,
		ProjectDefaultRouteID: principal.ProjectDefaultRouteID,
	})
	lifecycle.EndGatewaySpan(planSpan, err)
	// 闸门判定（含 enforce 拒绝）先落审计列，再处理路由错误：observation 在 enforce 拒绝时仍随 plan 返回。
	s.lifecycle.RecordCapabilityResult(ctx, requestRecord, plan.Capability)
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return responseResult{}, err
	}

	candidatePlan, err := s.prepareResponsesCandidates(ctx, req, plan.Candidates, plan.RouteMode, false, allowDirect)
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return responseResult{}, err
	}

	authorization, err := s.chatAuthorizer.AuthorizeChat(ctx, lifecycle.ChatAuthorizeParams{
		RequestRecord:       requestRecord,
		Principal:           principal,
		CandidatePrices:     candidatePlan.CandidateSalePrices(),
		InputTokens:         candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens: estimateMaxCompletionTokens(req),
	})
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, "chat_authorization_failed", err)
		return responseResult{}, err
	}

	// ResolveAdapter / Invoke 按候选 adapter 是否支持 responses 直传分流，并把对应 typed 响应捕获到本作用域。
	var (
		chatAdapter   chatcompletionsadapter.ChatAdapter
		directAdapter responsesadapter.ResponsesAdapter
		result        responseResult
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
			if allowDirect && s.registry.HasResponses(candidate.AdapterKey) {
				adapter, ok := s.registry.Responses(candidate.AdapterKey)
				if !ok {
					return failure.New(
						failure.CodeGatewayAdapterNotRegistered,
						failure.WithMessage(fmt.Sprintf("gateway responses adapter %q not registered", candidate.AdapterKey)),
					)
				}
				directAdapter = adapter
				return nil
			}
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
			if allowDirect && s.registry.HasResponses(candidate.AdapterKey) {
				body, err := encodeUpstreamResponsesBody(req, candidate.UpstreamModel, false)
				if err != nil {
					return lifecycle.AttemptSuccess{}, err
				}
				adapterCtx, adapterSpan := lifecycle.StartGatewaySpan(ctx, "adapter.responses", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
				resp, err := directAdapter.CreateResponse(adapterCtx, candidate.Channel, responsesadapter.Request{Body: body})
				lifecycle.EndGatewaySpan(adapterSpan, err)
				if err != nil {
					return lifecycle.AttemptSuccess{}, err
				}
				result = responseResult{direct: resp}
				return lifecycle.AttemptSuccess{ResponseID: resp.ResponseID, Facts: resp.Facts}, nil
			}

			chatReq, _ := mapResponsesRequestToChat(req, candidate.UpstreamModel)
			adapterCtx, adapterSpan := lifecycle.StartGatewaySpan(ctx, "adapter.chat_completions", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
			resp, err := chatAdapter.ChatCompletions(adapterCtx, candidate.Channel, chatReq)
			lifecycle.EndGatewaySpan(adapterSpan, err)
			if err != nil {
				return lifecycle.AttemptSuccess{}, err
			}

			result = responseResult{chat: resp}
			return lifecycle.AttemptSuccess{ResponseID: resp.ID, Facts: resp.Facts}, nil
		},
	})
	outcome = runResult.Outcome
	if err != nil {
		return responseResult{}, err
	}

	return result, nil
}
