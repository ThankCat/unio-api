package bootstrap

import (
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	anthropicmessages "github.com/ThankCat/unio-gateway/internal/service/gateway/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
	gateway "github.com/ThankCat/unio-gateway/internal/service/gateway/openai/chatcompletions"
	responsesgateway "github.com/ThankCat/unio-gateway/internal/service/gateway/openai/responses"
)

// NewChatGateway 创建当前 server 进程使用的 chat gateway service。
// metricsRecorder 可为 nil，表示不采集业务指标。
// channelBreaker 为三协议共享的熔断器单实例（运行时配置驱动，enabled 为内部原子开关）；nil 表示不启用熔断。
func NewChatGateway(
	db lifecycle.ChatTxBeginner,
	queries *sqlc.Queries,
	router gateway.ChatRouter,
	registry *lifecycle.AdapterRegistry,
	workerConfig config.WorkerConfig,
	gatewayConfig config.GatewayConfig,
	metricsRecorder *metrics.Metrics,
	rateLimitGuard lifecycle.RateLimitGuard,
	channelBreaker lifecycle.ChannelBreaker,
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
		workerConfig.SettlementRecoveryMaxAttempts,
	)
	chatSettlementExecutor := lifecycle.NewRecoverableChatSettlementExecutor(
		chatSettlementService,
		chatSettlementRecoveryStore,
		workerConfig.SettlementRecoverySettleTimeout,
	)
	chatAuthorizationServer := lifecycle.NewChatAuthorizationService(
		billing.Service{},
		ledgerService,
		gatewayConfig.MaxOutputTokensFallback,
	)
	candidatePreparer := lifecycle.NewExecutor(registry)

	// 避免 typed-nil 接口陷阱：nil *metrics.Metrics 必须以 nil 接口传入，
	// 否则 service 内的 nil 判断会失效并在调用时 panic。
	var chatMetrics lifecycle.MetricsRecorder
	if metricsRecorder != nil {
		chatMetrics = metricsRecorder
	}

	service := gateway.NewChatCompletionService(
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
	service.SetRateLimitGuard(rateLimitGuard)
	return service
}

// NewResponsesGateway 创建 OpenAI Responses API gateway service（DEC-014 responses-to-chat 桥接）。
//
// 复用与 chat 相同的 OpenAI routing / adapter / settlement / authorization；只把 ingress operation
// 落为 responses，候选 fallback 计费循环走共享 lifecycle.AttemptRunner。本阶段不牵扯 Anthropic Messages。
// metricsRecorder 可为 nil，表示不采集业务指标。channelBreaker 为三协议共享单实例；nil 表示不启用熔断。
func NewResponsesGateway(
	db lifecycle.ChatTxBeginner,
	queries *sqlc.Queries,
	router responsesgateway.ChatRouter,
	registry *lifecycle.AdapterRegistry,
	workerConfig config.WorkerConfig,
	gatewayConfig config.GatewayConfig,
	metricsRecorder *metrics.Metrics,
	rateLimitGuard lifecycle.RateLimitGuard,
	channelBreaker lifecycle.ChannelBreaker,
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
		workerConfig.SettlementRecoveryMaxAttempts,
	)
	chatSettlementExecutor := lifecycle.NewRecoverableChatSettlementExecutor(
		chatSettlementService,
		chatSettlementRecoveryStore,
		workerConfig.SettlementRecoverySettleTimeout,
	)
	chatAuthorizationServer := lifecycle.NewChatAuthorizationService(
		billing.Service{},
		ledgerService,
		gatewayConfig.MaxOutputTokensFallback,
	)
	candidatePreparer := lifecycle.NewExecutor(registry)

	var chatMetrics lifecycle.MetricsRecorder
	if metricsRecorder != nil {
		chatMetrics = metricsRecorder
	}

	service := responsesgateway.NewResponsesService(
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
	service.SetRateLimitGuard(rateLimitGuard)
	return service
}

// NewMessagesGateway 创建 Anthropic Messages gateway service。
// channelBreaker 为三协议共享的熔断器单实例；nil 表示不启用熔断。
func NewMessagesGateway(
	db lifecycle.ChatTxBeginner,
	queries *sqlc.Queries,
	router anthropicmessages.MessagesRouter,
	registry *lifecycle.AdapterRegistry,
	workerConfig config.WorkerConfig,
	gatewayConfig config.GatewayConfig,
	metricsRecorder *metrics.Metrics,
	rateLimitGuard lifecycle.RateLimitGuard,
	channelBreaker lifecycle.ChannelBreaker,
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
		workerConfig.SettlementRecoveryMaxAttempts,
	)
	chatSettlementExecutor := lifecycle.NewRecoverableChatSettlementExecutor(
		chatSettlementService,
		chatSettlementRecoveryStore,
		workerConfig.SettlementRecoverySettleTimeout,
	)
	chatAuthorizationServer := lifecycle.NewChatAuthorizationService(
		billing.Service{},
		ledgerService,
		gatewayConfig.MaxOutputTokensFallback,
	)
	candidatePreparer := lifecycle.NewExecutor(registry)

	var chatMetrics lifecycle.MetricsRecorder
	if metricsRecorder != nil {
		chatMetrics = metricsRecorder
	}

	service := anthropicmessages.NewMessagesService(
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
	service.SetRateLimitGuard(rateLimitGuard)
	return service
}
