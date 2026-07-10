package bootstrap

import (
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi"
	gatewayanthropic "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	gatewayopenai "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	gatewayresponses "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/appsettings"
	"github.com/redis/go-redis/v9"
)

// NewHTTPHandler 创建当前 server 进程使用的 HTTP handler。
//
// rateLimitGuard 是进程内唯一的限流 Guard(与三协议 service 共用同一实例,DEC §11.5)：
// HTTP 中间件(线路+用户 RPM/RPD)与 attempt runner(TPM/渠道级)共用同一默认上限与故障策略,
// settingsApplier 热更新时只需更新这一个实例。
func NewHTTPHandler(
	logger *slog.Logger,
	queries *sqlc.Queries,
	rateLimitGuard *ratelimit.Guard,
	concurrencyLimiter *ratelimit.ConcurrencyLimiter,
	chatCompletionService gatewayopenai.ChatCompletionService,
	responsesService gatewayresponses.ResponsesService,
	messagesService gatewayanthropic.MessagesService,
	metricsRecorder *metrics.Metrics,
) http.Handler {
	apiKeyAuthenticator := auth.NewAPIKeyAuthenticator(queries)
	modelCatalogService := modelcatalog.NewService(queries)

	deps := gatewayapi.RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: apiKeyAuthenticator,
		RateLimiter:         rateLimitGuard,
		ConcurrencyLimiter:  concurrencyLimiter,

		ChatCompletionService: chatCompletionService,
		ResponsesService:      responsesService,
		MessagesService:       messagesService,
		ModelCatalogService:   modelCatalogService,
	}

	if metricsRecorder != nil {
		deps.HTTPMetrics = metricsRecorder
		deps.RateLimitMetrics = metricsRecorder
		deps.MetricsHandler = metricsRecorder.Handler()
	}

	return gatewayapi.NewRouter(deps)
}

// NewRateLimitGuard 构造两层限流 Guard（P2-8）：Redis 滑动窗口计数 + 全局默认上限 + 故障策略。
// 默认上限与故障策略来自运行时配置(gateway.rate_limit_defaults),之后由 settingsApplier 热更新。
func NewRateLimitGuard(redisClient redis.Cmdable, keyNamespace string, defaults appsettings.RateLimitDefaultsSettings, logger *slog.Logger) *ratelimit.Guard {
	store := ratelimit.NewSlidingWindowStore(redisClient, keyNamespace)
	return ratelimit.NewGuard(store, ratelimit.DefaultLimits{
		RPM: defaults.RPM,
		TPM: defaults.TPM,
		RPD: defaults.RPD,
	}, defaults.FailOpen(), logger)
}
