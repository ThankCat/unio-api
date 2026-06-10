// Package adminapi 提供 admin 管理端（/admin/v1）的 HTTP 路由、handler 与 DTO。
//
// admin 表面只服务平台管理员，认证走 admin 静态 token（core/adminauth），
// 与客户 Gateway（/v1）严格隔离，不复用其认证、限流或 DTO。
package adminapi

import (
	"log/slog"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/app/adminapi/middleware"
	"github.com/ThankCat/unio-api/internal/platform/httpmw"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// RouterDeps 保存构建 admin HTTP router 所需的外部依赖。
type RouterDeps struct {
	Logger             *slog.Logger
	AdminAuthenticator middleware.AdminAuthenticator

	ProviderService ProviderService
	ChannelService  ChannelService

	// HTTPMetrics 记录 HTTP 层请求指标；nil 表示不采集。
	HTTPMetrics httpmw.MetricsRecorder

	// MetricsHandler 暴露 Prometheus /metrics；nil 表示不挂载该端点。
	MetricsHandler http.Handler
}

// NewRouter 创建 admin-server 使用的 HTTP handler。
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(httpmw.CORS)
	r.Use(httpmw.RequestID)
	r.Use(httpmw.Tracing)
	r.Use(httpmw.Metrics(deps.HTTPMetrics))
	r.Use(httpmw.Logger(deps.Logger))
	r.Use(httpmw.Recoverer(deps.Logger))

	if deps.MetricsHandler != nil {
		r.Handle("/metrics", deps.MetricsHandler)
	}

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteError(w, http.StatusNotFound, "not_found", "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/admin/v1", func(r chi.Router) {
		r.Use(middleware.AdminAuth(deps.AdminAuthenticator))

		// ping 是受保护探针：用于校验 admin token 是否有效（认证后回 200）。
		r.Get("/ping", handlePing)

		if deps.ProviderService != nil {
			ph := &providersHandler{service: deps.ProviderService}
			r.Get("/providers", ph.list)
			r.Post("/providers", ph.create)
			r.Get("/providers/{id}", ph.get)
			r.Patch("/providers/{id}", ph.update)
		}

		if deps.ChannelService != nil {
			ch := &channelsHandler{service: deps.ChannelService}
			r.Get("/channels", ch.list)
			r.Post("/channels", ch.create)
			r.Get("/channels/{id}", ch.get)
			r.Patch("/channels/{id}", ch.update)
			// credential 只写不回：用子资源 PUT 轮换，成功返回 204。
			r.Put("/channels/{id}/credential", ch.rotateCredential)
		}
	})

	return r
}

// handlePing 在通过 admin 认证后返回探针结果。
func handlePing(w http.ResponseWriter, _ *http.Request) {
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
