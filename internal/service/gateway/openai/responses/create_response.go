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

// nonStreamStrategy 注入一次非流式 Responses 族请求的协议差异：候选能力过滤口径（allowDirect）
// 与 per-candidate 的 adapter 解析 / 上游调用闭包。资金关键 scaffold（routing/authorization/
// settlement/终态）由 runNonStream 统一承担，CreateResponse 与 CompactHistory 共用同一份。
type nonStreamStrategy struct {
	allowDirect bool
	resolve     lifecycle.ResolveAdapter
	invoke      lifecycle.NonStreamInvoke

	// upstreamCostWithoutUsage 可选：命中时 runner 释放冻结并记 risk_exposure（不重试/不普通释放），
	// 用于「上游可能已计费但无可靠 usage」（compact 2xx 缺 usage，P0-3）。nil 表示沿用普通失败语义。
	upstreamCostWithoutUsage func(err error) bool

	// codes 可选：覆盖 runner 审计 code/reason（如 compact 专用 risk_exposure 文案）。零值用通用默认。
	codes lifecycle.RunNonStreamCodes
}

// executeResponse 执行非流式 Responses 候选 fallback 计费循环，按候选能力分流直传/桥接。
//
// allowDirect=false 时强制全部走桥接（与 CompactHistory 的 synthetic 估算口径一致）。协议差异（直传/
// 桥接调用与响应捕获）由 resolve/invoke 闭包按候选注入，scaffold 复用 runNonStream。
func (s *ResponsesService) executeResponse(ctx context.Context, req gatewayapi.ResponsesRequest, allowDirect bool) (responseResult, error) {
	var (
		chatAdapter   chatcompletionsadapter.ChatAdapter
		directAdapter responsesadapter.ResponsesAdapter
		result        responseResult
	)
	err := s.runNonStream(ctx, req, nonStreamStrategy{
		allowDirect: allowDirect,
		resolve: func(candidate routing.ChatRouteCandidate) error {
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
		invoke: func(ctx context.Context, candidate routing.ChatRouteCandidate) (lifecycle.AttemptSuccess, error) {
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
	return result, err
}

// runNonStream 执行 authorization 之后由共享 AttemptRunner 驱动的非流式 Responses 候选 fallback 计费循环。
//
// 本方法承担 routing、authorization、共享候选循环、metrics outcome 与终态写入；协议/路径差异（候选能力
// 过滤口径、per-candidate 上游调用与响应捕获）由 strat 注入。CreateResponse（直传/桥接）与 CompactHistory
// （native/synthetic）共用本 scaffold，资金关键链路只此一份。
func (s *ResponsesService) runNonStream(ctx context.Context, req gatewayapi.ResponsesRequest, strat nonStreamStrategy) error {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	requestRecord, err := s.lifecycle.CreateRequest(ctx, principal, req.Model, false)
	if err != nil {
		return err
	}

	// outcome 默认 failed，仅成功/取消路径覆盖；defer 保证每个请求只计一次，不遗漏提前返回的失败分支。
	outcome := metrics.ChatOutcomeFailed
	defer func() {
		s.lifecycle.RecordRequest(false, outcome)
	}()

	ctx, span := lifecycle.StartGatewaySpan(ctx, "gateway.responses")
	defer span.End()

	planCtx, planSpan := lifecycle.StartGatewaySpan(ctx, "gateway.routing")
	plan, err := s.router.PlanChat(planCtx, routing.ChatRouteRequest{
		UserID:          principal.UserID,
		ModelID:         req.Model,
		IngressProtocol: routing.ProtocolOpenAI,
		Operation:       routing.OperationResponses,
		RouteID:         principal.RouteID,
	})
	lifecycle.EndGatewaySpan(planSpan, err)
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
	}

	candidatePlan, err := s.prepareResponsesCandidates(ctx, req, plan.Candidates, plan.RouteMode, false, strat.allowDirect)
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
	}

	authorization, err := s.chatAuthorizer.AuthorizeChat(ctx, lifecycle.ChatAuthorizeParams{
		RequestRecord:            requestRecord,
		Principal:                principal,
		CandidatePrices:          candidatePlan.CandidateSalePrices(),
		InputTokens:              candidatePlan.ConservativeInputTokens,
		MaxCompletionTokens:      estimateMaxCompletionTokens(req),
		CandidateMaxOutputTokens: candidatePlan.CandidateMaxOutputTokens(),
	})
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, "chat_authorization_failed", err)
		return err
	}

	runResult, err := s.attemptRunner.RunNonStream(ctx, lifecycle.RunNonStreamParams{
		RequestRecord:            requestRecord,
		Principal:                principal,
		Authorization:            authorization,
		Candidates:               candidatePlan.Candidates,
		RequestedModelID:         req.Model,
		ResponseProtocol:         requestlog.ProtocolOpenAI,
		EstimatedTokens:          candidatePlan.ConservativeInputTokens,
		ResolveAdapter:           strat.resolve,
		Invoke:                   strat.invoke,
		Codes:                    strat.codes,
		UpstreamCostWithoutUsage: strat.upstreamCostWithoutUsage,
	})
	outcome = runResult.Outcome
	return err
}
