package gatewayapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	gatewayanthropic "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/app/gatewayapi/internalapi"
	"github.com/ThankCat/unio-api/internal/app/gatewayapi/middleware"
	gatewaychat "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	gatewaymodels "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/models"
	gatewayresponses "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/platform/httpmw"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// RouterDeps 保存构建 HTTP router 所需的外部依赖。
type RouterDeps struct {
	Logger                *slog.Logger
	APIKeyAuthenticator   middleware.APIKeyAuthenticator
	ChatCompletionService gatewaychat.ChatCompletionService
	ResponsesService      gatewayresponses.ResponsesService
	MessagesService       gatewayanthropic.MessagesService
	RateLimiter           middleware.KeyRateLimiter
	ConcurrencyLimiter    middleware.KeyConcurrencyLimiter
	ModelCatalogService   gatewaymodels.ModelCatalogService

	// HTTPMetrics 记录 HTTP 层请求指标；nil 表示不采集。
	HTTPMetrics httpmw.MetricsRecorder

	// RateLimitMetrics 记录限流判定指标；nil 表示不采集。
	RateLimitMetrics middleware.RateLimitMetricsRecorder

	// MetricsHandler 暴露 Prometheus /metrics；nil 表示不挂载该端点。
	MetricsHandler http.Handler

	// CircuitBreaker 供内部只读快照；nil 或 InternalToken 为空时不挂载 /internal 路由。
	CircuitBreaker internalapi.CircuitBreakerSnapshotter
	// InternalToken 来自 GATEWAY_INTERNAL_TOKEN；空表示关闭内部运维端点。
	InternalToken string
	// InstanceID 来自 GATEWAY_INSTANCE_ID；空则 handler 回退 hostname。
	InstanceID string
}

// NewRouter 创建 API server 使用的 HTTP handler。
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(httpmw.RequestID)
	r.Use(httpmw.ClientIP)
	r.Use(httpmw.Tracing)
	r.Use(httpmw.Metrics(deps.HTTPMetrics))
	r.Use(httpmw.Logger(deps.Logger))
	r.Use(httpmw.Recoverer(deps.Logger))

	// 版本前缀兼容：折叠多余的 /v1、补齐缺失的 /v1。置于日志/指标之后，故访问日志仍记录
	// 客户端真实发来的路径（便于定位 base_url 配错），而路由按规范化后的单个 /v1 前缀匹配。
	r.Use(v1PathCompat)

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

	if deps.CircuitBreaker != nil && deps.InternalToken != "" {
		r.Method(http.MethodGet, "/internal/v1/circuit-breaker", internalapi.CircuitBreakerHandler{
			Breaker:  deps.CircuitBreaker,
			Token:    deps.InternalToken,
			Instance: deps.InstanceID,
		})
	}

	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(deps.APIKeyAuthenticator))
		r.Use(middleware.RateLimit(deps.RateLimiter, middleware.RateLimitOptions{
			Logger:  deps.Logger,
			Metrics: deps.RateLimitMetrics,
		}))
		// 「线路+用户」在途并发限制（DEC-029）：多余的并发重试立即 429，不打上游、不冻结余额。
		r.Use(middleware.ConcurrencyLimit(deps.ConcurrencyLimiter))

		r.Get("/models", gatewaymodels.NewModelsHandler(deps.ModelCatalogService))

		r.Method(http.MethodPost, "/chat/completions", gatewaychat.NewChatCompletionsHandler(deps.ChatCompletionService))

		// OpenAI Responses API（Codex 兼容）。compact/input_tokens 是 /responses 子集协议；
		// 有状态 endpoint（retrieve/delete/cancel/input_items）Unio 无服务端存储，统一 501。
		r.Method(http.MethodPost, "/responses", gatewayresponses.NewResponsesHandler(deps.ResponsesService))
		r.Method(http.MethodPost, "/responses/compact", gatewayresponses.NewResponsesCompactHandler(deps.ResponsesService))
		r.Method(http.MethodPost, "/responses/input_tokens", gatewayresponses.NewResponsesInputTokensHandler(deps.ResponsesService))
		r.Method(http.MethodGet, "/responses/{response_id}", gatewayresponses.NewResponsesStatelessUnsupportedHandler())
		r.Method(http.MethodDelete, "/responses/{response_id}", gatewayresponses.NewResponsesStatelessUnsupportedHandler())
		r.Method(http.MethodGet, "/responses/{response_id}/input_items", gatewayresponses.NewResponsesStatelessUnsupportedHandler())
		r.Method(http.MethodPost, "/responses/{response_id}/cancel", gatewayresponses.NewResponsesStatelessUnsupportedHandler())

		r.Method(http.MethodPost, "/messages", gatewayanthropic.NewMessagesHandler(deps.MessagesService, deps.Logger))
	})

	return r
}
