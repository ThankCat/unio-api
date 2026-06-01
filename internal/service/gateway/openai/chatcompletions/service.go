package chatcompletions

import (
	"context"

	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
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
	Chat(adapterKey string) (openai.ChatAdapter, bool)
	StreamChat(adapterKey string) (openai.StreamChatAdapter, bool)
	ChatInputTokenizer(adapterKey string) (openai.ChatInputTokenizer, bool)
}

// RetryClassifier 定义 gateway 判断一次上游错误是否允许尝试下一个同模型 channel 的能力。
type RetryClassifier interface {
	IsRetryable(err error) bool
}

// ChatSettlementExecutor 定义 chat 成功后提交 usage、price snapshot 和 ledger 结算事务的能力。
type ChatSettlementExecutor interface {
	SettleSuccessfulChat(ctx context.Context, params ChatSettlementParams) error
}

// ChatCompletionService 编排 chat completion 请求的 routing、adapter 调用、request log 和结算。
type ChatCompletionService struct {
	router          ChatRouter
	registry        AdapterRegistry
	candidates      lifecycle.CandidatePreparer
	retryClassifier RetryClassifier
	requestLog      requestlog.Service
	chatSettlement  ChatSettlementExecutor
	chatAuthorizer  ChatAuthorizer
	metrics         MetricsRecorder
	breaker         ChannelBreaker
}

// NewChatCompletionService 创建聊天补全 gateway service。
// metricsRecorder 和 breaker 均可为 nil，分别表示不采集业务指标、不启用 channel 熔断。
func NewChatCompletionService(
	router ChatRouter,
	registry AdapterRegistry,
	candidates lifecycle.CandidatePreparer,
	retryClassifier RetryClassifier,
	requestLog requestlog.Service,
	chatSettlement ChatSettlementExecutor,
	chatAuthorizer ChatAuthorizer,
	metricsRecorder MetricsRecorder,
	breaker ChannelBreaker,
) *ChatCompletionService {
	if retryClassifier == nil {
		retryClassifier = NeverRetryClassifier{}
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
	}
}
