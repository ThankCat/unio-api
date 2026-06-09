package responses

import (
	"context"
	"fmt"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// StreamResponse 编排流式 Responses 请求，并通过 emit 写出 Responses 命名事件（Codex 主路径）。
//
// 桥接路径（DEC-014）与非流式一致：routing → authorization → 共享 AttemptRunner.RunStream 调用 OpenAI
// stream adapter，上游 Chat SSE delta 经 streamEncoder 翻译成 Responses 命名事件序列。资金关键流式链路
// （emitted 后禁止 fallback、final usage 缺失处理、tail-error 仍尽力结算、settlement、终态写入）全部由
// RunStream 承担，与 chatcompletions 共用同一份实现。
//
// streamEncoder 在整个请求生命周期只构造一次：RunStream 仅在「首帧前」允许同模型 fallback，而 encoder
// 只在 EmitChunk（首个内容 chunk）后才推进状态，因此 fallback 时 encoder 仍是初始态，可安全复用。
func (s *ResponsesService) StreamResponse(ctx context.Context, req gatewayapi.ResponsesRequest, emit func(gatewayapi.ResponsesStreamEvent) error) error {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	requestRecord, err := s.lifecycle.CreateRequest(ctx, principal, req.Model, true)
	if err != nil {
		return err
	}

	// outcome 默认 failed，仅成功/取消路径覆盖；defer 保证每个流式请求只计一次。
	outcome := metrics.ChatOutcomeFailed
	defer func() {
		s.lifecycle.RecordRequest(true, outcome)
	}()

	ctx, span := lifecycle.StartGatewaySpan(ctx, "gateway.responses_stream")
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
		return err
	}

	candidatePlan, err := s.prepareResponsesCandidates(ctx, req, plan.Candidates, true)
	if err != nil {
		s.lifecycle.MarkRequestFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
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
		return err
	}

	var streamAdapter openai.StreamChatAdapter
	encoder := newStreamEncoder(req, newResponsesID("resp"), time.Now().Unix(), emit)

	runResult, err := s.attemptRunner.RunStream(ctx, lifecycle.RunStreamParams{
		RequestRecord:        requestRecord,
		Principal:            principal,
		Authorization:        authorization,
		Candidates:           candidatePlan.Candidates,
		RequestedModelID:     req.Model,
		ResponseProtocol:     requestlog.ProtocolOpenAI,
		RequiredCapabilities: requiredCapabilities.StringKeys(),
		ResolveAdapter: func(candidate routing.ChatRouteCandidate) error {
			adapter, ok := s.registry.StreamChat(candidate.AdapterKey)
			if !ok {
				return failure.New(
					failure.CodeGatewayAdapterNotRegistered,
					failure.WithMessage(fmt.Sprintf("gateway stream chat adapter %q not registered", candidate.AdapterKey)),
				)
			}
			streamAdapter = adapter
			return nil
		},
		Stream: func(ctx context.Context, candidate routing.ChatRouteCandidate, onChunk func(openai.ChatStreamChunk) error) (*adapter.ResponseFacts, error) {
			chatReq, _ := mapResponsesRequestToChat(req, candidate.UpstreamModel)
			streamCtx, streamSpan := lifecycle.StartGatewaySpan(ctx, "adapter.stream_chat_completions", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
			streamOutcome, streamErr := streamAdapter.StreamChatCompletions(streamCtx, candidate.Channel, chatReq, onChunk)
			lifecycle.EndGatewaySpan(streamSpan, streamErr)
			return streamOutcome.Facts, streamErr
		},
		EmitChunk: func(chunk openai.ChatStreamChunk) error {
			return encoder.Handle(chunk)
		},
		Finish: func(_ string, finalUsage adapter.ChatUsage, finishReason string) error {
			usage := finalUsage
			return encoder.Complete(finishReason, &usage)
		},
	})
	outcome = runResult.Outcome
	return err
}
