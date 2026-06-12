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

	ProviderService     ProviderService
	ChannelService      ChannelService
	ModelService        ModelService
	ChannelModelService ChannelModelService
	CostPriceService    CostPriceService
	PriceService        PriceService

	// M6 只读查询台
	RequestQueryService RequestQueryService
	UsageQueryService   UsageQueryService
	LedgerQueryService  LedgerQueryService

	// M7 客户管理：用户/项目（工作空间）/API Key（费用上限）/手工调额
	UserService       UserService
	ProjectService    ProjectService
	APIKeyService     APIKeyService
	AdjustmentService AdjustmentService

	// M5 能力管理：模型能力/渠道收紧 CRUD、models.dev 同步、adapter 画像、enforce 只读
	CapabilityService            CapabilityService
	CapabilitySyncService        CapabilitySyncService
	CapabilitySeedService        CapabilitySeedService
	CapabilityEnforcementService CapabilityEnforcementService

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

		if deps.ChannelModelService != nil {
			cmh := &channelModelsHandler{service: deps.ChannelModelService}
			// channel↔model 绑定是 channel 的子资源，用 {modelId} 定位 Unio 模型。
			r.Get("/channels/{id}/models", cmh.list)
			r.Post("/channels/{id}/models", cmh.create)
			r.Patch("/channels/{id}/models/{modelId}", cmh.update)
			r.Delete("/channels/{id}/models/{modelId}", cmh.delete)
		}

		if deps.CostPriceService != nil {
			cph := &costPricesHandler{service: deps.CostPriceService}
			// 成本价挂在 channel 下；价格不可删，PATCH 调窗口/启停用价格 id 定位。
			r.Get("/channels/{id}/cost-prices", cph.list)
			r.Post("/channels/{id}/cost-prices", cph.create)
			r.Patch("/cost-prices/{id}", cph.update)
		}

		if deps.PriceService != nil {
			prh := &pricesHandler{service: deps.PriceService}
			// 客户售价挂在 model 下；价格不可删，PATCH 调窗口/启停用价格 id 定位。
			r.Get("/models/{id}/prices", prh.list)
			r.Post("/models/{id}/prices", prh.create)
			r.Patch("/prices/{id}", prh.update)
		}

		if deps.ModelService != nil {
			mh := &modelsHandler{service: deps.ModelService}
			r.Get("/models", mh.list)
			r.Post("/models", mh.create)
			r.Get("/models/{id}", mh.get)
			r.Patch("/models/{id}", mh.update)
		}

		// M6 只读查询台：请求记录（含详情聚合）、用量、账本流水、计费异常。全部只读。
		if deps.RequestQueryService != nil {
			rqh := &requestsHandler{service: deps.RequestQueryService}
			r.Get("/requests", rqh.list)
			// 详情按对外 request_id 定位；?include_internal=true 才回显内部错误详情。
			r.Get("/requests/{requestId}", rqh.get)
		}

		if deps.UsageQueryService != nil {
			uh := &usageHandler{service: deps.UsageQueryService}
			r.Get("/usage", uh.list)
		}

		if deps.LedgerQueryService != nil {
			lh := &ledgerHandler{service: deps.LedgerQueryService}
			r.Get("/ledger/entries", lh.listEntries)
			r.Get("/ledger/billing-exceptions", lh.listBillingExceptions)
		}

		// M7 客户管理：用户、项目（工作空间）、API Key（费用上限）、手工调额。
		if deps.UserService != nil {
			uh := &usersHandler{service: deps.UserService}
			r.Get("/users", uh.list)
			r.Get("/users/{id}", uh.get)

			// 手工调额是用户的子资源：充值/扣款一律走账本留痕。
			if deps.AdjustmentService != nil {
				ah := &adjustmentsHandler{service: deps.AdjustmentService}
				r.Post("/users/{id}/balance-adjustments", ah.create)
			}
		}

		if deps.ProjectService != nil {
			pjh := &projectsHandler{service: deps.ProjectService}
			r.Get("/projects", pjh.list)
			r.Get("/projects/{id}", pjh.get)
		}

		if deps.APIKeyService != nil {
			akh := &apiKeysHandler{service: deps.APIKeyService}
			// 列表/创建挂在项目（工作空间）下；单把操作用扁平 /api-keys/{id} 定位。
			r.Get("/projects/{id}/api-keys", akh.listByProject)
			r.Post("/projects/{id}/api-keys", akh.create)
			r.Get("/api-keys/{id}", akh.get)
			// PATCH 调启停/费用上限；DELETE 永久吊销（不可逆）。
			r.Patch("/api-keys/{id}", akh.update)
			r.Delete("/api-keys/{id}", akh.revoke)
		}

		// M5 能力管理：模型能力（手工覆盖）/渠道收紧（只能减）CRUD + 能力 key 注册表。
		if deps.CapabilityService != nil {
			cah := &capabilitiesHandler{service: deps.CapabilityService}
			r.Get("/capability/keys", cah.listKeys)
			// 模型能力挂在 model 下；写入用 PUT {key} 幂等 upsert，DELETE 撤销。
			r.Get("/models/{id}/capabilities", cah.listModelCapabilities)
			r.Put("/models/{id}/capabilities/{key}", cah.setModelCapability)
			r.Delete("/models/{id}/capabilities/{key}", cah.deleteModelCapability)
			// 渠道收紧挂在 channel 下；只能减（limited/unsupported）。
			r.Get("/channels/{id}/capability-overrides", cah.listChannelOverrides)
			r.Put("/channels/{id}/capability-overrides/{key}", cah.setChannelOverride)
			r.Delete("/channels/{id}/capability-overrides/{key}", cah.deleteChannelOverride)
		}

		// M5 models.dev 同步：内联触发（dry-run 预览/实际应用）+ 最近任务展示。
		if deps.CapabilitySyncService != nil {
			csh := &capabilitySyncHandler{service: deps.CapabilitySyncService}
			r.Get("/capability/sync-jobs", csh.listJobs)
			r.Post("/capability/sync-jobs", csh.trigger)
		}

		// M5 adapter 画像：列出可物化画像 + 对指定模型物化（source=adapter_seed）。
		if deps.CapabilitySeedService != nil {
			cseh := &capabilitySeedHandler{service: deps.CapabilitySeedService}
			r.Get("/capability/adapter-profiles", cseh.listProfiles)
			r.Post("/capability/adapter-seed-jobs", cseh.materialize)
		}

		// M5 enforce 只读：展示各表面 observe/enforce + observe 期判定分布。
		if deps.CapabilityEnforcementService != nil {
			ceh := &capabilityEnforcementHandler{service: deps.CapabilityEnforcementService}
			r.Get("/capability/enforcement", ceh.get)
			r.Get("/capability/observe-summary", ceh.observeSummary)
		}
	})

	return r
}

// handlePing 在通过 admin 认证后返回探针结果。
func handlePing(w http.ResponseWriter, _ *http.Request) {
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
