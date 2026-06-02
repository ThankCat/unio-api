package messages

import (
	"context"

	"github.com/ThankCat/unio-api/internal/core/adapter/anthropic"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// MessagesRouter 定义 gateway 为 Anthropic Messages 请求生成有序 route plan 所需能力。
type MessagesRouter interface {
	PlanChat(ctx context.Context, req routing.ChatRouteRequest) (routing.ChatRoutePlan, error)
}

// AdapterRegistry 定义 gateway 根据 routing 返回的 adapter key 查找 Anthropic adapter 能力。
type AdapterRegistry interface {
	Messages(adapterKey string) (anthropic.MessagesAdapter, bool)
	StreamMessages(adapterKey string) (anthropic.StreamMessagesAdapter, bool)
	MessagesInputTokenizer(adapterKey string) (anthropic.MessagesInputTokenizer, bool)
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
		lifecycle: lifecycle.NewRequestLifecycle(lifecycle.RequestLifecycleParams{
			RequestLog:      requestLog,
			Authorizer:      chatAuthorizer,
			Metrics:         metricsRecorder,
			Breaker:         breaker,
			IngressProtocol: requestlog.ProtocolAnthropic,
			Operation:       requestlog.OperationMessages,
			SafeMessage:     messagesSafeMessage,
		}),
	}
}

// messagesSafeMessage 把 messages 编排专用 ad-hoc string code 映射成可展示文案；
// 返回空串表示「此 code 不在本协议族 ad-hoc 集合内」，由 lifecycle 兜底到 BaseSafeRequestLogErrorMessage。
func messagesSafeMessage(code string) string {
	switch code {
	case "messages_authorization_failed":
		return "Request authorization failed."
	case "messages_authorization_release_failed":
		return "Request billing cleanup failed."
	case "messages_settlement_failed", "stream_messages_settlement_failed":
		return "Request settlement failed."
	}
	return ""
}
