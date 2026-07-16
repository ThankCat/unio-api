package messages

import (
	"context"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// MessagesRouter 定义 gateway 为 Anthropic Messages 请求生成有序 route plan 所需能力。
type MessagesRouter interface {
	PlanChat(ctx context.Context, req routing.ChatRouteRequest) (routing.ChatRoutePlan, error)
}

// AdapterRegistry 定义 gateway 根据 routing 返回的 adapter key 查找 Anthropic adapter 能力。
type AdapterRegistry interface {
	Messages(adapterKey string) (messagesadapter.MessagesAdapter, bool)
	StreamMessages(adapterKey string) (messagesadapter.StreamMessagesAdapter, bool)
	MessagesInputTokenizer(adapterKey string) (messagesadapter.MessagesInputTokenizer, bool)
}

// MessagesService 编排 Anthropic Messages 请求的 routing、adapter 调用、request log 和结算。
//
// 协议无关基础设施（request log / metrics / breaker / chat authorizer 的 release 流程 + ad-hoc
// code 文案 + ingress 协议常量）由 lifecycle.RequestLifecycle 统一承担，在构造时立即 bundle，
// 由本文件内部 helper lc() 返回。两侧 service 的 thin wrapper（channel_breaker /
// message_authorization / message_metrics / message_request_record）都改为 1-line forward 到 lifecycle。
type MessagesService struct {
	router          MessagesRouter
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

// NewMessagesService 创建 Anthropic Messages gateway service。
func NewMessagesService(
	router MessagesRouter,
	registry AdapterRegistry,
	candidates lifecycle.CandidatePreparer,
	retryClassifier lifecycle.RetryClassifier,
	requestLog requestlog.Service,
	chatSettlement lifecycle.ChatSettlementExecutor,
	chatAuthorizer lifecycle.ChatAuthorizer,
	metricsRecorder lifecycle.MetricsRecorder,
	breaker lifecycle.ChannelBreaker,
) *MessagesService {
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
		panic("gateway: chat settlement executor is required")
	}
	if chatAuthorizer == nil {
		panic("gateway: chat authorizer service is required")
	}

	requestLifecycle := lifecycle.NewRequestLifecycle(lifecycle.RequestLifecycleParams{
		RequestLog:      requestLog,
		Authorizer:      chatAuthorizer,
		Metrics:         metricsRecorder,
		Breaker:         breaker,
		IngressProtocol: requestlog.ProtocolAnthropic,
		Operation:       requestlog.OperationMessages,
		SafeMessage:     messagesSafeMessage,
	})

	return &MessagesService{
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

// SetRateLimitGuard 注入两层限流 Guard（P2-8），转发给候选循环驱动；nil 表示不启用限流。
func (s *MessagesService) SetRateLimitGuard(guard lifecycle.RateLimitGuard) {
	s.attemptRunner.SetRateLimitGuard(guard)
}

// SetConcurrencyLimiter 注入渠道在途并发限制器（DEC-029），转发给候选循环驱动；nil 表示不启用。
func (s *MessagesService) SetConcurrencyLimiter(limiter lifecycle.ChannelConcurrencyLimiter) {
	s.attemptRunner.SetConcurrencyLimiter(limiter)
}

// SetCostExposureRecorder 注入成本敞口记录器（DESIGN-bill-on-cancel 阶段一）；nil 表示不启用。
func (s *MessagesService) SetCostExposureRecorder(recorder lifecycle.CostExposureRecorder, assumedOutputFallback int64) {
	s.lifecycle.SetCostExposureRecorder(recorder, assumedOutputFallback)
}

// SetChannelCooldownRegistry 注入渠道级 429 冷却注册表（P2-7），转发给共享 lifecycle；nil 表示不启用冷却。
func (s *MessagesService) SetChannelCooldownRegistry(registry *lifecycle.ChannelCooldownRegistry) {
	s.lifecycle.SetChannelCooldownRegistry(registry)
}

// SetCredentialGate 注入凭据失效闸门（连续 401 翻 credential_valid=false，阶段二）；nil 表示不启用。
func (s *MessagesService) SetCredentialGate(gate lifecycle.CredentialGate) {
	s.lifecycle.SetCredentialGate(gate)
}

// messagesSafeMessage 把 messages 编排专用 ad-hoc string code 映射成可展示文案；
// 返回空串表示「此 code 不在本协议族 ad-hoc 集合内」，由 lifecycle 兜底到 BaseSafeRequestLogErrorMessage。
func messagesSafeMessage(code string) string {
	switch code {
	case "messages_authorization_failed":
		return "Request authorization failed."
	case "messages_authorization_release_failed":
		return "Request billing cleanup failed."
	case "chat_authorization_release_failed":
		return "Request billing cleanup failed."
	case "stream_adapter_error":
		return "Upstream stream failed."
	case "chat_settlement_failed", "stream_chat_settlement_failed":
		return "Request settlement failed."
	case "messages_settlement_failed", "stream_messages_settlement_failed":
		return "Request settlement failed."
	}
	return ""
}
