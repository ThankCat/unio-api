// Package websiteapi 组装营销站（unio-website）的公开只读 HTTP API。
//
// 该 surface 无鉴权：消费方是 Next.js 的服务端渲染（ISR），内容是「公开模型目录」这类
// 本来就要印在营销页上的数据。所有响应带公共缓存头，浏览器与 CDN 均可缓存；
// 与 console（用户会话）、admin（管理员）两个面完全隔离，互不影响缓存与限流策略。
package websiteapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/httpmw"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// Deps 包含 website HTTP 路由所需的依赖。
type Deps struct {
	Logger *zap.Logger
	// Models 提供公开模型目录（internal/service/publicmodels）。
	Models ModelsService
	// Stats 提供营销首页用的运行指标（最快首字等）；nil 时不挂对应路由。
	Stats ProductionStats
	// LabLogos 提供出品方图标；nil 时不挂图标路由。
	LabLogos LabLogoStore
}

// NewRouter 构建 website API 路由。
func NewRouter(deps Deps) http.Handler {
	r := chi.NewRouter()
	r.Use(httpmw.CORS)
	r.Use(httpmw.RequestID)
	r.Use(httpmw.Tracing)
	r.Use(httpmw.Logger(deps.Logger))
	r.Use(httpmw.Recoverer(deps.Logger))

	r.NotFound(func(w http.ResponseWriter, _ *http.Request) {
		_ = httpx.WriteError(w, http.StatusNotFound, "not_found", "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		_ = httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})
	r.Get("/healthz", func(w http.ResponseWriter, _ *http.Request) {
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/v1", func(r chi.Router) {
		mh := &modelsHandler{service: deps.Models, logger: deps.Logger}
		r.Get("/models", mh.list)
		r.Get("/models/discount-history", mh.discountHistory)
		r.Get("/models/min-sale-ratio", mh.minSaleRatio)
		if deps.Stats != nil {
			sh := &statsHandler{service: deps.Stats, logger: deps.Logger}
			r.Get("/stats/min-first-token-ms", sh.minFirstTokenMs)
		}
		if deps.LabLogos != nil {
			r.Get("/labs/{slug}/logo.svg", handleLabLogo(deps.LabLogos))
		}
	})

	return r
}
