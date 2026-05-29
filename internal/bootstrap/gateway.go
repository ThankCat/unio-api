package bootstrap

import (
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/gateway"
)

// NewChatGateway 创建当前 server 进程使用的 chat gateway service。
// metricsRecorder 可为 nil，表示不采集业务指标。
func NewChatGateway(
	db gateway.ChatTxBeginner,
	queries *sqlc.Queries,
	router gateway.ChatRouter,
	registry gateway.AdapterRegistry,
	workerConfig config.WorkerConfig,
	breakerConfig config.CircuitBreakerConfig,
	metricsRecorder *metrics.Metrics,
) *gateway.ChatCompletionService {
	requestLogStore := requestlog.NewStore(queries)
	ledgerService := ledger.NewService(db, queries)
	chatSettlementService := gateway.NewChatSettlementService(
		db,
		queries,
		billing.Service{},
		ledgerService,
	)
	chatSettlementRecoveryStore := gateway.NewChatSettlementRecoveryStore(
		queries,
		workerConfig.SettlementRecoveryInitialDelay,
	)
	chatSettlementExecutor := gateway.NewRecoverableChatSettlementExecutor(
		chatSettlementService,
		chatSettlementRecoveryStore,
		workerConfig.SettlementRecoverySettleTimeout,
	)
	chatAuthorizationServer := gateway.NewChatAuthorizationService(
		queries,
		billing.Service{},
		ledgerService,
		registry,
	)

	// 避免 typed-nil 接口陷阱：nil *metrics.Metrics 必须以 nil 接口传入，
	// 否则 service 内的 nil 判断会失效并在调用时 panic。
	var chatMetrics gateway.MetricsRecorder
	if metricsRecorder != nil {
		chatMetrics = metricsRecorder
	}

	// 熔断器默认启用；未启用时以 nil 接口传入，service 内退化为始终放行。
	var channelBreaker gateway.ChannelBreaker
	if breakerConfig.Enabled {
		channelBreaker = gateway.NewChannelCircuitBreaker(gateway.ChannelCircuitBreakerConfig{
			Window:       breakerConfig.Window,
			MinRequests:  breakerConfig.MinRequests,
			FailureRatio: breakerConfig.FailureRatio,
			OpenDuration: breakerConfig.OpenDuration,
		})
	}

	return gateway.NewChatCompletionService(
		router,
		registry,
		gateway.ProviderErrorClassifier{},
		requestLogStore,
		chatSettlementExecutor,
		chatAuthorizationServer,
		chatMetrics,
		channelBreaker,
	)
}
