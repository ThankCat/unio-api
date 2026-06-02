package gatewayapi

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi/middleware"
	gatewayanthropic "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	gatewaychat "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	gatewaymodels "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/models"
	"github.com/ThankCat/unio-api/internal/platform/httpmw"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// RouterDeps 保存构建 HTTP router 所需的外部依赖。
type RouterDeps struct {
	Logger                 *slog.Logger
	APIKeyAuthenticator    middleware.APIKeyAuthenticator
	ChatCompletionService  gatewaychat.ChatCompletionService
	MessagesService        gatewayanthropic.MessagesService
	RateLimiter            middleware.RateLimiter
	RateLimitLimit         int64
	RateLimitWindow        time.Duration
	ModelCatalogService    gatewaymodels.ModelCatalogService
	RateLimitFailurePolicy string

	// HTTPMetrics 记录 HTTP 层请求指标；nil 表示不采集。
	HTTPMetrics httpmw.MetricsRecorder

	// RateLimitMetrics 记录限流判定指标；nil 表示不采集。
	RateLimitMetrics middleware.RateLimitMetricsRecorder

	// MetricsHandler 暴露 Prometheus /metrics；nil 表示不挂载该端点。
	MetricsHandler http.Handler
}

// NewRouter 创建 API server 使用的 HTTP handler。
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(httpmw.RequestID)
	r.Use(httpmw.Tracing)
	r.Use(httpmw.Metrics(deps.HTTPMetrics))
	r.Use(httpmw.Logger(deps.Logger))
	r.Use(httpmw.Recoverer(deps.Logger))

	if deps.MetricsHandler != nil {
		r.Handle("/metrics", deps.MetricsHandler)
	}

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteError(
			w,
			http.StatusNotFound,
			"not_found",
			"route not found",
		)
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteError(
			w,
			http.StatusMethodNotAllowed,
			"method_not_allowed",
			"method not allowed",
		)
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{
			"status": "ok",
		})
	})

	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(deps.APIKeyAuthenticator))
		r.Use(middleware.RateLimit(deps.RateLimiter, middleware.RateLimitOptions{
			Limit:         deps.RateLimitLimit,
			Window:        deps.RateLimitWindow,
			FailurePolicy: middleware.RateLimitFailurePolicy(deps.RateLimitFailurePolicy),
			Logger:        deps.Logger,
			Metrics:       deps.RateLimitMetrics,
		}))

		r.Get("/models", gatewaymodels.NewModelsHandler(deps.ModelCatalogService))

		r.Method(http.MethodPost, "/chat/completions", gatewaychat.NewChatCompletionsHandler(deps.ChatCompletionService))
		r.Method(http.MethodPost, "/messages", gatewayanthropic.NewMessagesHandler(deps.MessagesService, deps.Logger))
	})

	return r
}
