package chatcompletions

import (
	"context"
	"fmt"
	"time"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// StreamChatCompletion 编排流式 chat completion 请求，并通过 emit 写出 OpenAI-compatible SSE chunk。
func (s *ChatCompletionService) StreamChatCompletion(ctx context.Context, req gatewayapi.ChatCompletionRequest, emit func(gatewayapi.ChatCompletionStreamResponse) error) error {
	principal, ok := auth.APIKeyPrincipalFromContext(ctx)
	if !ok {
		return failure.Wrap(
			failure.CodeAuthMissingAPIKey,
			auth.ErrMissingAPIKey,
			failure.WithMessage(auth.ErrMissingAPIKey.Error()),
		)
	}

	// 先创建 request_records，并标记为 running。
	requestRecord, err := s.createRequestRecord(ctx, principal, req, true)
	if err != nil {
		return err
	}

	// outcome 默认 failed，仅在成功/取消路径覆盖；defer 保证每个流式请求只计数一次。
	outcome := metrics.ChatOutcomeFailed
	defer func() {
		s.recordChatRequest(true, outcome)
	}()

	ctx, span := lifecycle.StartGatewaySpan(ctx, "gateway.chat_stream")
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
		return err
	}

	candidatePlan, err := s.prepareChatCandidates(ctx, req, plan.Candidates, plan.RouteMode, true)
	if err != nil {
		s.markRequestRecordFailed(ctx, requestRecord, lifecycle.RoutingFailureCode(err), err)
		return err
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
		return err
	}

	// 流式候选 fallback 循环（attempt 审计 / 熔断 / emitted 后禁止 fallback / final usage 缺失 /
	// 客户端取消 / tail-error 仍尽力结算 / settlement / 终态写入）由共享 AttemptRunner.RunStream 驱动；
	// OpenAI chat 与 responses 复用同一资金关键流式链路。协议差异通过 typed 闭包注入：
	// ResolveAdapter 解析 stream adapter；Stream 执行一次上游流式调用；EmitChunk 翻译并写出 SSE 帧；
	// Finish 在结算成功后按 include_usage 写收尾 usage chunk。
	var streamAdapter chatcompletionsadapter.StreamChatAdapter
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
		Stream: func(ctx context.Context, candidate routing.ChatRouteCandidate, onChunk func(chatcompletionsadapter.ChatStreamChunk) error) (*adapter.ResponseFacts, error) {
			streamCtx, streamSpan := lifecycle.StartGatewaySpan(ctx, "adapter.stream_chat_completions", lifecycle.UpstreamSpanAttrs(candidate.ProviderID, candidate.Channel.ID, candidate.UpstreamModel)...)
			streamOutcome, streamErr := streamAdapter.StreamChatCompletions(streamCtx, candidate.Channel,
				mapGatewayRequestToAdapter(req, candidate.UpstreamModel), onChunk)
			lifecycle.EndGatewaySpan(streamSpan, streamErr)
			return streamOutcome.Facts, streamErr
		},
		EmitChunk: func(chunk chatcompletionsadapter.ChatStreamChunk) error {
			chunkResp := mapAdapterStreamChunkToGateway(req.Model, chunk, req.StreamIncludeUsage())
			// 优先透传上游 chunk created；仅当上游未给出（0）时回退本地时间。
			if chunkResp.Created == 0 {
				chunkResp.Created = time.Now().Unix()
			}
			return emit(chunkResp)
		},
		Finish: func(streamID string, finalUsage adapter.ChatUsage, _ string) error {
			if !req.StreamIncludeUsage() {
				return nil
			}
			return emitClientStreamUsage(emit, req, streamID, finalUsage)
		},
	})
	outcome = runResult.Outcome
	return err
}

// emitClientStreamUsage 在流式成功结算后，按 OpenAI 约定向客户端写出 usage chunk。
func emitClientStreamUsage(
	emit func(gatewayapi.ChatCompletionStreamResponse) error,
	req gatewayapi.ChatCompletionRequest,
	streamID string,
	usage adapter.ChatUsage,
) error {
	if streamID == "" {
		streamID = "chatcmpl_unio"
	}

	usageResp := mapAdapterUsageToGateway(usage)
	return emit(gatewayapi.ChatCompletionStreamResponse{
		ID:      streamID,
		Object:  "chat.completion.chunk",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []gatewayapi.ChatCompletionStreamChoice{},
		Usage:   &usageResp,
	})
}
