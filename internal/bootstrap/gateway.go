package bootstrap

import (
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	anthropicmessages "github.com/ThankCat/unio-api/internal/service/gateway/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
	gateway "github.com/ThankCat/unio-api/internal/service/gateway/openai/chatcompletions"
	responsesgateway "github.com/ThankCat/unio-api/internal/service/gateway/openai/responses"
)

// NewChatGateway 创建当前 server 进程使用的 chat gateway service。
// metricsRecorder 可为 nil，表示不采集业务指标。
func NewChatGateway(
	db lifecycle.ChatTxBeginner,
	queries *sqlc.Queries,
	router gateway.ChatRouter,
	registry *lifecycle.AdapterRegistry,
	workerConfig config.WorkerConfig,
	breakerConfig config.CircuitBreakerConfig,
	metricsRecorder *metrics.Metrics,
) *gateway.ChatCompletionService {
	if registry == nil {
		panic("bootstrap: lifecycle adapter registry is required")
	}

	requestLogStore := requestlog.NewStore(queries)
	ledgerService := ledger.NewService(db, queries)
	chatSettlementService := lifecycle.NewChatSettlementService(
		db,
		queries,
		billing.Service{},
		ledgerService,
	)
	chatSettlementRecoveryStore := lifecycle.NewChatSettlementRecoveryStore(
		queries,
		workerConfig.SettlementRecoveryInitialDelay,
	)
	chatSettlementExecutor := lifecycle.NewRecoverableChatSettlementExecutor(
		chatSettlementService,
		chatSettlementRecoveryStore,
		workerConfig.SettlementRecoverySettleTimeout,
	)
	chatAuthorizationServer := lifecycle.NewChatAuthorizationService(
		queries,
		billing.Service{},
		ledgerService,
	)
	candidatePreparer := lifecycle.NewExecutor(registry)

	// 避免 typed-nil 接口陷阱：nil *metrics.Metrics 必须以 nil 接口传入，
	// 否则 service 内的 nil 判断会失效并在调用时 panic。
	var chatMetrics lifecycle.MetricsRecorder
	if metricsRecorder != nil {
		chatMetrics = metricsRecorder
	}

	// 熔断器默认启用；未启用时以 nil 接口传入，service 内退化为始终放行。
	var channelBreaker lifecycle.ChannelBreaker
	if breakerConfig.Enabled {
		channelBreaker = lifecycle.NewChannelCircuitBreaker(lifecycle.ChannelCircuitBreakerConfig{
			Window:       breakerConfig.Window,
			MinRequests:  breakerConfig.MinRequests,
			FailureRatio: breakerConfig.FailureRatio,
			OpenDuration: breakerConfig.OpenDuration,
		})
	}

	return gateway.NewChatCompletionService(
		router,
		registry.OpenAI,
		candidatePreparer,
		lifecycle.ProviderErrorClassifier{},
		requestLogStore,
		chatSettlementExecutor,
		chatAuthorizationServer,
		chatMetrics,
		channelBreaker,
	)
}

// NewResponsesGateway 创建 OpenAI Responses API gateway service（DEC-014 responses-to-chat 桥接）。
//
// 复用与 chat 相同的 OpenAI routing / adapter / settlement / authorization；只把 ingress operation
// 落为 responses，候选 fallback 计费循环走共享 lifecycle.AttemptRunner。本阶段不牵扯 Anthropic Messages。
// metricsRecorder 可为 nil，表示不采集业务指标。
func NewResponsesGateway(
	db lifecycle.ChatTxBeginner,
	queries *sqlc.Queries,
	router responsesgateway.ChatRouter,
	registry *lifecycle.AdapterRegistry,
	workerConfig config.WorkerConfig,
	breakerConfig config.CircuitBreakerConfig,
	metricsRecorder *metrics.Metrics,
) *responsesgateway.ResponsesService {
	if registry == nil {
		panic("bootstrap: lifecycle adapter registry is required")
	}

	requestLogStore := requestlog.NewStore(queries)
	ledgerService := ledger.NewService(db, queries)
	chatSettlementService := lifecycle.NewChatSettlementService(
		db,
		queries,
		billing.Service{},
		ledgerService,
	)
	chatSettlementRecoveryStore := lifecycle.NewChatSettlementRecoveryStore(
		queries,
		workerConfig.SettlementRecoveryInitialDelay,
	)
	chatSettlementExecutor := lifecycle.NewRecoverableChatSettlementExecutor(
		chatSettlementService,
		chatSettlementRecoveryStore,
		workerConfig.SettlementRecoverySettleTimeout,
	)
	chatAuthorizationServer := lifecycle.NewChatAuthorizationService(
		queries,
		billing.Service{},
		ledgerService,
	)
	candidatePreparer := lifecycle.NewExecutor(registry)

	var chatMetrics lifecycle.MetricsRecorder
	if metricsRecorder != nil {
		chatMetrics = metricsRecorder
	}

	var channelBreaker lifecycle.ChannelBreaker
	if breakerConfig.Enabled {
		channelBreaker = lifecycle.NewChannelCircuitBreaker(lifecycle.ChannelCircuitBreakerConfig{
			Window:       breakerConfig.Window,
			MinRequests:  breakerConfig.MinRequests,
			FailureRatio: breakerConfig.FailureRatio,
			OpenDuration: breakerConfig.OpenDuration,
		})
	}

	return responsesgateway.NewResponsesService(
		router,
		registry.OpenAI,
		candidatePreparer,
		lifecycle.ProviderErrorClassifier{},
		requestLogStore,
		chatSettlementExecutor,
		chatAuthorizationServer,
		chatMetrics,
		channelBreaker,
	)
}

// NewMessagesGateway 创建 Anthropic Messages gateway service。
func NewMessagesGateway(
	db lifecycle.ChatTxBeginner,
	queries *sqlc.Queries,
	router anthropicmessages.MessagesRouter,
	registry *lifecycle.AdapterRegistry,
	workerConfig config.WorkerConfig,
	breakerConfig config.CircuitBreakerConfig,
	metricsRecorder *metrics.Metrics,
) *anthropicmessages.MessagesService {
	if registry == nil {
		panic("bootstrap: lifecycle adapter registry is required")
	}

	requestLogStore := requestlog.NewStore(queries)
	ledgerService := ledger.NewService(db, queries)
	chatSettlementService := lifecycle.NewChatSettlementService(
		db,
		queries,
		billing.Service{},
		ledgerService,
	)
	chatSettlementRecoveryStore := lifecycle.NewChatSettlementRecoveryStore(
		queries,
		workerConfig.SettlementRecoveryInitialDelay,
	)
	chatSettlementExecutor := lifecycle.NewRecoverableChatSettlementExecutor(
		chatSettlementService,
		chatSettlementRecoveryStore,
		workerConfig.SettlementRecoverySettleTimeout,
	)
	chatAuthorizationServer := lifecycle.NewChatAuthorizationService(
		queries,
		billing.Service{},
		ledgerService,
	)
	candidatePreparer := lifecycle.NewExecutor(registry)

	var chatMetrics lifecycle.MetricsRecorder
	if metricsRecorder != nil {
		chatMetrics = metricsRecorder
	}

	var channelBreaker lifecycle.ChannelBreaker
	if breakerConfig.Enabled {
		channelBreaker = lifecycle.NewChannelCircuitBreaker(lifecycle.ChannelCircuitBreakerConfig{
			Window:       breakerConfig.Window,
			MinRequests:  breakerConfig.MinRequests,
			FailureRatio: breakerConfig.FailureRatio,
			OpenDuration: breakerConfig.OpenDuration,
		})
	}

	return anthropicmessages.NewMessagesService(
		router,
		registry.Anthropic,
		candidatePreparer,
		lifecycle.ProviderErrorClassifier{},
		requestLogStore,
		chatSettlementExecutor,
		chatAuthorizationServer,
		chatMetrics,
		channelBreaker,
	)
}
