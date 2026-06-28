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
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/redis/go-redis/v9"
)

// NewHTTPHandler 创建当前 server 进程使用的 HTTP handler。
func NewHTTPHandler(
	logger *slog.Logger,
	queries *sqlc.Queries,
	redisClient redis.Cmdable,
	cfg config.Config,
	chatCompletionService gatewayopenai.ChatCompletionService,
	responsesService gatewayresponses.ResponsesService,
	messagesService gatewayanthropic.MessagesService,
	metricsRecorder *metrics.Metrics,
) http.Handler {
	apiKeyAuthenticator := auth.NewAPIKeyAuthenticator(queries)
	modelCatalogService := modelcatalog.NewService(queries)

	rateLimitGuard := NewRateLimitGuard(redisClient, cfg, logger)

	deps := gatewayapi.RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: apiKeyAuthenticator,
		RateLimiter:         rateLimitGuard,

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
// gateway HTTP 中间件（key 级 RPM/RPD）与 attempt runner（key TPM / 渠道级 RPM/TPM/RPD）共用同一口径。
func NewRateLimitGuard(redisClient redis.Cmdable, cfg config.Config, logger *slog.Logger) *ratelimit.Guard {
	store := ratelimit.NewSlidingWindowStore(redisClient, cfg.Redis.KeyNamespace)
	defaults := ratelimit.DefaultLimits{
		RPM: cfg.RateLimit.DefaultRPM,
		TPM: cfg.RateLimit.DefaultTPM,
		RPD: cfg.RateLimit.DefaultRPD,
	}
	failOpen := cfg.RateLimit.FailurePolicy == "fail_open"
	return ratelimit.NewGuard(store, defaults, failOpen, logger)
}
