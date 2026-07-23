package bootstrap

import (
	"go.uber.org/zap"

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
func NewChatGateway(
	db lifecycle.ChatTxBeginner,
	queries *sqlc.Queries,
	router gateway.ChatRouter,
	registry *lifecycle.AdapterRegistry,
	workerConfig config.WorkerConfig,
	gatewayConfig config.GatewayConfig,
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
	)
	return service
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
	gatewayConfig config.GatewayConfig,
	metricsRecorder *metrics.Metrics,
	logger *zap.Logger,
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
		logger,
	)
	return service
}

// NewMessagesGateway 创建 Anthropic Messages gateway service。
func NewMessagesGateway(
	db lifecycle.ChatTxBeginner,
	queries *sqlc.Queries,
	router anthropicmessages.MessagesRouter,
	registry *lifecycle.AdapterRegistry,
	workerConfig config.WorkerConfig,
	gatewayConfig config.GatewayConfig,
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
	)
	return service
}
