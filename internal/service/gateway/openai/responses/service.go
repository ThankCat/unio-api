package responses

import (
	"context"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// ChatRouter 定义 gateway 为 Responses 请求生成有序 route plan 所需的 routing 能力。
//
// Responses 复用 OpenAI Chat routing：客户模型名（方案 A，DEC-014）按 ProtocolOpenAI 解析候选。
type ChatRouter interface {
	PlanChat(ctx context.Context, req routing.ChatRouteRequest) (routing.ChatRoutePlan, error)
}

// AdapterRegistry 定义 Responses 桥接复用的 OpenAI adapter 查找能力。
//
// DEC-014：不新增 Responses adapter，直接复用既有 openai.ChatAdapter 与 tokenizer。
type AdapterRegistry interface {
	Chat(adapterKey string) (openai.ChatAdapter, bool)
	StreamChat(adapterKey string) (openai.StreamChatAdapter, bool)
	ChatInputTokenizer(adapterKey string) (openai.ChatInputTokenizer, bool)
}

// ResponsesService 编排 OpenAI Responses API（POST /v1/responses）请求的 routing、桥接翻译、
// adapter 调用、request log 与结算（DEC-014 responses-to-chat 桥接）。
//
// 协议无关的候选 fallback 计费循环（attempt/上游指标/错误分类/settlement/终态）委托给共享
// lifecycle.AttemptRunner，与 OpenAI chatcompletions 复用同一份资金关键链路；Responses 只注入
// 协议差异（请求翻译 responses→chat、响应翻译 chat→responses）。本阶段不牵扯 Anthropic Messages。
type ResponsesService struct {
	router         ChatRouter
	registry       AdapterRegistry
	candidates     lifecycle.CandidatePreparer
	chatAuthorizer lifecycle.ChatAuthorizer
	lifecycle      *lifecycle.RequestLifecycle
	attemptRunner  *lifecycle.AttemptRunner
}

// NewResponsesService 创建 Responses gateway service。
// metricsRecorder 与 breaker 均可为 nil，分别表示不采集业务指标、不启用 channel 熔断。
func NewResponsesService(
	router ChatRouter,
	registry AdapterRegistry,
	candidates lifecycle.CandidatePreparer,
	retryClassifier lifecycle.RetryClassifier,
	requestLog requestlog.Service,
	chatSettlement lifecycle.ChatSettlementExecutor,
	chatAuthorizer lifecycle.ChatAuthorizer,
	metricsRecorder lifecycle.MetricsRecorder,
	breaker lifecycle.ChannelBreaker,
) *ResponsesService {
	if retryClassifier == nil {
		retryClassifier = lifecycle.NeverRetryClassifier{}
	}
	if candidates == nil {
		panic("gateway: lifecycle candidate preparer is required")
	}
	if requestLog == nil {
		panic("gateway: request log service is required")
	}
	if chatSettlement == nil {
		panic("gateway: chat settlement service is required")
	}
	if chatAuthorizer == nil {
		panic("gateway: chat authorizer service is required")
	}

	requestLifecycle := lifecycle.NewRequestLifecycle(lifecycle.RequestLifecycleParams{
		RequestLog:      requestLog,
		Authorizer:      chatAuthorizer,
		Metrics:         metricsRecorder,
		Breaker:         breaker,
		IngressProtocol: requestlog.ProtocolOpenAI,
		Operation:       requestlog.OperationResponses,
		SafeMessage:     responsesSafeMessage,
	})

	return &ResponsesService{
		router:         router,
		registry:       registry,
		candidates:     candidates,
		chatAuthorizer: chatAuthorizer,
		lifecycle:      requestLifecycle,
		attemptRunner:  lifecycle.NewAttemptRunner(requestLifecycle, retryClassifier, chatSettlement),
	}
}

// responsesSafeMessage 把 Responses 编排专用 ad-hoc string code 映射成可展示文案；
// 返回空串表示由 lifecycle 兜底。资金关键 code 与 chatcompletions 复用同一组（AttemptRunner 共享）。
func responsesSafeMessage(code string) string {
	switch code {
	case "chat_authorization_failed":
		return "Request authorization failed."
	case "chat_authorization_release_failed":
		return "Request billing cleanup failed."
	case "chat_settlement_failed":
		return "Request settlement failed."
	}
	return ""
}

// prepareResponsesCandidates 复用共享 lifecycle executor 生成 Responses 候选 fallback plan。
//
// 与 chatcompletions 一致按 stream 选择 Stream/NonStream 能力过滤（外加 InputTokenizer）：流式请求只保留
// 支持流式的候选，非流式请求只保留支持非流式的候选；否则会把仅支持一种模式的候选误选/误排，导致
// authorization 之后在 adapter 调用阶段失败。输入 token 估算先把 Responses 请求翻译成内部 ChatRequest
// 再交给候选对应 tokenizer（桥接复用，无独立 Responses tokenizer）。
func (s *ResponsesService) prepareResponsesCandidates(ctx context.Context, req gatewayapi.ResponsesRequest, candidates []routing.ChatRouteCandidate, stream bool) (lifecycle.CandidatePlan, error) {
	capabilities := []lifecycle.AdapterCapability{
		lifecycle.AdapterCapabilityInputTokenizer,
	}
	if stream {
		capabilities = append(capabilities, lifecycle.AdapterCapabilityStream)
	} else {
		capabilities = append(capabilities, lifecycle.AdapterCapabilityNonStream)
	}

	return s.candidates.PrepareCandidates(ctx, lifecycle.PrepareCandidatesParams{
		Protocol:            routing.ProtocolOpenAI,
		Candidates:          candidates,
		Capabilities:        capabilities,
		Available:           s.lifecycle.CandidateAvailable,
		EstimateInputTokens: s.responsesInputTokenEstimator(req),
	})
}

// responsesInputTokenEstimator 构造 Responses 候选级 tokenizer closure。
//
// closure 持有 Responses HTTP DTO，按 candidate 的 adapter_key 与 upstream model 查找 tokenizer，
// 并先用 mapResponsesRequestToChat 把请求翻译成内部 ChatRequest 后估算输入 token。
func (s *ResponsesService) responsesInputTokenEstimator(req gatewayapi.ResponsesRequest) lifecycle.CandidateInputTokenEstimator {
	return func(_ context.Context, candidate routing.ChatRouteCandidate) (int64, error) {
		tokenizer, ok := s.registry.ChatInputTokenizer(candidate.AdapterKey)
		if !ok {
			return 0, failure.New(
				failure.CodeGatewayAdapterNotRegistered,
				failure.WithMessage("openai chat input tokenizer is not registered"),
				failure.WithField("protocol", routing.ProtocolOpenAI),
				failure.WithField("adapter_key", candidate.AdapterKey),
			)
		}

		chatReq, _ := mapResponsesRequestToChat(req, candidate.UpstreamModel)
		inputTokens, err := tokenizer.CountChatInputTokens(chatReq)
		if err != nil {
			return 0, failure.Wrap(
				failure.CodeAdapterTokenizeFailed,
				err,
				failure.WithMessage("count responses-bridged chat input tokens"),
				failure.WithField("protocol", routing.ProtocolOpenAI),
				failure.WithField("adapter_key", candidate.AdapterKey),
				failure.WithField("upstream_model", candidate.UpstreamModel),
			)
		}

		return inputTokens, nil
	}
}

// estimateMaxCompletionTokens 估算 authorization 用的最大输出 token。
func estimateMaxCompletionTokens(req gatewayapi.ResponsesRequest) int64 {
	if req.MaxOutputTokens != nil && *req.MaxOutputTokens > 0 {
		return int64(*req.MaxOutputTokens)
	}
	return lifecycle.DefaultAuthorizationMaxCompletionTokens
}
