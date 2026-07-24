package gatewayapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	gatewayanthropic "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi/middleware"
	gatewaychat "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	gatewaymodels "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/models"
	gatewayresponses "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/platform/httpmw"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

type ReadinessProbe interface {
	Check(ctx context.Context) (ready bool, reason string)
}

// RouterDeps 保存构建 HTTP router 所需的外部依赖。
type RouterDeps struct {
	Logger                *zap.Logger
	APIKeyAuthenticator   middleware.APIKeyAuthenticator
	ChatCompletionService gatewaychat.ChatCompletionService
	ResponsesService      gatewayresponses.ResponsesService
	MessagesService       gatewayanthropic.MessagesService
	ModelCatalogService   gatewaymodels.ModelCatalogService
	RequestAdmission      middleware.RequestAdmissionAcquirer
	Readiness             ReadinessProbe

	// HTTPMetrics 记录 HTTP 层请求指标；nil 表示不采集。
	HTTPMetrics httpmw.MetricsRecorder

	// MetricsHandler 暴露 Prometheus /metrics；nil 表示不挂载该上游源站。
	MetricsHandler http.Handler
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
	r.Get("/readyz", func(w http.ResponseWriter, r *http.Request) {
		ready := false
		if deps.Readiness != nil {
			ready, _ = deps.Readiness.Check(r.Context())
		}
		status := http.StatusServiceUnavailable
		body := map[string]string{"status": "not_ready"}
		if ready {
			status = http.StatusOK
			body["status"] = "ready"
		}
		_ = httpx.WriteJSON(w, status, body)
	})

	r.Route("/v1", func(r chi.Router) {
		r.Use(middleware.APIKeyAuth(deps.APIKeyAuthenticator))
		// nil admission 只用于直接构造 Router 的单元测试，middleware 会原样返回 handler。
		// 生产 Gateway 必须注入 Manager，禁止回退到旧的本机限流或并发门禁。
		admitted := func(scope string, protocol middleware.RequestAdmissionProtocol, handler http.Handler) http.Handler {
			return middleware.RequestAdmission(deps.RequestAdmission, middleware.RequestAdmissionOptions{
				Scope:    scope,
				Protocol: protocol,
				Logger:   deps.Logger,
			})(handler)
		}

		r.Method(http.MethodGet, "/models", admitted("/v1/models", middleware.RequestAdmissionOpenAI,
			gatewaymodels.NewModelsHandler(deps.ModelCatalogService)))

		r.Method(http.MethodPost, "/chat/completions", admitted("/v1/chat/completions", middleware.RequestAdmissionOpenAI,
			gatewaychat.NewChatCompletionsHandler(deps.ChatCompletionService)))

		// OpenAI Responses API（Codex 兼容）。compact/input_tokens 是 /responses 子上游源站；
		// 有状态 origin（retrieve/delete/cancel/input_items）Unio 无服务端存储，统一 501。
		r.Method(http.MethodPost, "/responses", admitted("/v1/responses", middleware.RequestAdmissionOpenAI,
			gatewayresponses.NewResponsesHandler(deps.ResponsesService)))
		r.Method(http.MethodPost, "/responses/compact", admitted("/v1/responses/compact", middleware.RequestAdmissionOpenAI,
			gatewayresponses.NewResponsesCompactHandler(deps.ResponsesService)))
		r.Method(http.MethodPost, "/responses/input_tokens", admitted("/v1/responses/input_tokens", middleware.RequestAdmissionOpenAI,
			gatewayresponses.NewResponsesInputTokensHandler(deps.ResponsesService)))
		r.Method(http.MethodGet, "/responses/{response_id}", admitted("/v1/responses/{response_id}", middleware.RequestAdmissionOpenAI,
			gatewayresponses.NewResponsesStatelessUnsupportedHandler()))
		r.Method(http.MethodDelete, "/responses/{response_id}", admitted("/v1/responses/{response_id}", middleware.RequestAdmissionOpenAI,
			gatewayresponses.NewResponsesStatelessUnsupportedHandler()))
		r.Method(http.MethodGet, "/responses/{response_id}/input_items", admitted("/v1/responses/{response_id}/input_items", middleware.RequestAdmissionOpenAI,
			gatewayresponses.NewResponsesStatelessUnsupportedHandler()))
		r.Method(http.MethodPost, "/responses/{response_id}/cancel", admitted("/v1/responses/{response_id}/cancel", middleware.RequestAdmissionOpenAI,
			gatewayresponses.NewResponsesStatelessUnsupportedHandler()))

		r.Method(http.MethodPost, "/messages", admitted("/v1/messages", middleware.RequestAdmissionAnthropic,
			gatewayanthropic.NewMessagesHandler(deps.MessagesService, deps.Logger)))
	})

	return r
}
