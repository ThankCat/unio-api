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

	rateLimitStore := ratelimit.NewRedisStore(redisClient, cfg.Redis.KeyNamespace)
	rateLimiter := ratelimit.NewLimiter(rateLimitStore)

	deps := gatewayapi.RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: apiKeyAuthenticator,
		RateLimiter:         rateLimiter,

		RateLimitLimit:         cfg.RateLimit.DefaultLimit,
		RateLimitWindow:        cfg.RateLimit.DefaultWindow,
		RateLimitFailurePolicy: cfg.RateLimit.FailurePolicy,

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
