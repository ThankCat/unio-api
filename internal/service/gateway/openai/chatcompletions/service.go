package chatcompletions

import (
	"context"

	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// ChatRouter 定义 gateway 为 chat 请求生成有序 route plan 所需的 routing 能力。
type ChatRouter interface {
	PlanChat(ctx context.Context, req routing.ChatRouteRequest) (routing.ChatRoutePlan, error)
}

// AdapterRegistry 定义 gateway 根据 routing 返回的 adapter key 查找具体 adapter 的能力。
type AdapterRegistry interface {
	Chat(adapterKey string) (chatcompletionsadapter.ChatAdapter, bool)
	StreamChat(adapterKey string) (chatcompletionsadapter.StreamChatAdapter, bool)
	ChatInputTokenizer(adapterKey string) (chatcompletionsadapter.ChatInputTokenizer, bool)
}

// ChatCompletionService 编排 chat completion 请求的 routing、adapter 调用、request log 和结算。
//
// 协议无关基础设施（request log / metrics / breaker / chat authorizer 的 release 流程 + ad-hoc
// code 文案 + ingress 协议常量）由 lifecycle.RequestLifecycle 统一承担，在构造时立即 bundle，
// 由本文件内部 helper lc() 返回。两侧 service 的 thin wrapper（channel_breaker /
// chat_authorization / chat_metrics / chat_request_record）都改为 1-line forward 到 lifecycle。
type ChatCompletionService struct {
	router          ChatRouter
	registry        AdapterRegistry
	candidates      lifecycle.CandidatePreparer
	retryClassifier lifecycle.RetryClassifier
	requestLog      requestlog.Service
	chatSettlement  lifecycle.ChatSettlementExecutor
	chatAuthorizer  lifecycle.ChatAuthorizer
	metrics         lifecycle.MetricsRecorder
	breaker         lifecycle.ChannelBreaker
	lifecycle       *lifecycle.RequestLifecycle
	attemptRunner   *lifecycle.AttemptRunner
}

// NewChatCompletionService 创建聊天补全 gateway service。
// metricsRecorder 和 breaker 均可为 nil，分别表示不采集业务指标、不启用 channel 熔断。
func NewChatCompletionService(
	router ChatRouter,
	registry AdapterRegistry,
	candidates lifecycle.CandidatePreparer,
	retryClassifier lifecycle.RetryClassifier,
	requestLog requestlog.Service,
	chatSettlement lifecycle.ChatSettlementExecutor,
	chatAuthorizer lifecycle.ChatAuthorizer,
	metricsRecorder lifecycle.MetricsRecorder,
	breaker lifecycle.ChannelBreaker,
) *ChatCompletionService {
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
		Operation:       requestlog.OperationChatCompletions,
		SafeMessage:     chatCompletionsSafeMessage,
	})

	return &ChatCompletionService{
		router:          router,
		registry:        registry,
		candidates:      candidates,
		retryClassifier: retryClassifier,
		requestLog:      requestLog,
		chatSettlement:  chatSettlement,
		chatAuthorizer:  chatAuthorizer,
		metrics:         metricsRecorder,
		breaker:         breaker,
		lifecycle:       requestLifecycle,
		attemptRunner:   lifecycle.NewAttemptRunner(requestLifecycle, retryClassifier, chatSettlement),
	}
}

// chatCompletionsSafeMessage 把 chat-completion 编排专用 ad-hoc string code 映射成可展示文案；
// 返回空串表示「此 code 不在本协议族 ad-hoc 集合内」，由 lifecycle 兜底到 BaseSafeRequestLogErrorMessage。
func chatCompletionsSafeMessage(code string) string {
	switch code {
	case "chat_authorization_failed":
		return "Request authorization failed."
	case "chat_authorization_release_failed":
		return "Request billing cleanup failed."
	case "chat_settlement_failed", "stream_chat_settlement_failed":
		return "Request settlement failed."
	}
	return ""
}
