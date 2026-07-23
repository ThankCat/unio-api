package chatcompletions

import (
	"context"

	"go.uber.org/zap"

	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
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
// 协议无关基础设施（request log / metrics / chat authorizer 的 release 流程 + ad-hoc
// code 文案 + ingress 协议常量）由 lifecycle.RequestLifecycle 统一承担，在构造时立即 bundle。
type ChatCompletionService struct {
	router          ChatRouter
	registry        AdapterRegistry
	candidates      lifecycle.CandidatePreparer
	retryClassifier lifecycle.RetryClassifier
	requestLog      requestlog.Service
	chatSettlement  lifecycle.ChatSettlementExecutor
	chatAuthorizer  lifecycle.ChatAuthorizer
	metrics         lifecycle.MetricsRecorder
	lifecycle       *lifecycle.RequestLifecycle
	attemptRunner   *lifecycle.AttemptRunner

	// sticky 是会话粘性路由核心（大 uncache 缺口 P0）；nil 表示未启用（Resolve nil-safe 不粘）。
	sticky *lifecycle.StickyRouter
}

// NewChatCompletionService 创建聊天补全 gateway service。
// metricsRecorder 可为 nil，表示不采集业务指标。
func NewChatCompletionService(
	router ChatRouter,
	registry AdapterRegistry,
	candidates lifecycle.CandidatePreparer,
	retryClassifier lifecycle.RetryClassifier,
	requestLog requestlog.Service,
	chatSettlement lifecycle.ChatSettlementExecutor,
	chatAuthorizer lifecycle.ChatAuthorizer,
	metricsRecorder lifecycle.MetricsRecorder,
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
		lifecycle:       requestLifecycle,
		attemptRunner:   lifecycle.NewAttemptRunner(requestLifecycle, retryClassifier, chatSettlement),
	}
}

// SetAttemptPermitManager 注入三协议共享的候选级全局准入管理器。
func (s *ChatCompletionService) SetAttemptPermitManager(manager *lifecycle.AttemptPermitManager) {
	s.attemptRunner.SetAttemptPermitManager(manager)
}

// SetCostExposureRecorder 注入成本敞口记录器（DESIGN-bill-on-cancel 阶段一）；nil 表示不启用。
func (s *ChatCompletionService) SetCostExposureRecorder(recorder lifecycle.CostExposureRecorder, assumedOutputFallback int64) {
	s.lifecycle.SetCostExposureRecorder(recorder, assumedOutputFallback)
}

// SetCredentialGate 注入凭据失效闸门（连续 401 翻 credential_valid=false，阶段二）；nil 表示不启用。
func (s *ChatCompletionService) SetCredentialGate(gate lifecycle.CredentialGate) {
	s.lifecycle.SetCredentialGate(gate)
}

// SetStickyRouter 注入会话粘性路由核心（大 uncache 缺口 P0）；nil 表示不启用 sticky。
// 同时把同一 StickyRouter 作为队首短等配置源交给 AttemptRunner（P1，与系统设置热更新同源）。
func (s *ChatCompletionService) SetStickyRouter(sticky *lifecycle.StickyRouter) {
	s.sticky = sticky
	s.attemptRunner.SetHeadWaitSource(sticky)
}

// SetRoutingLogger 注入 sticky/skip/wait/failover 结构化日志；nil 表示不打日志。
func (s *ChatCompletionService) SetRoutingLogger(logger *zap.Logger) {
	s.attemptRunner.SetLogger(logger)
}

func (s *ChatCompletionService) SetRoutingTraceRecorder(recorder *lifecycle.RoutingTraceRecorder) {
	s.lifecycle.SetRoutingTraceRecorder(recorder)
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
