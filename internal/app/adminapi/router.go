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

	// 阶段 15：渠道-模型价（售价+成本合并）+ 线路（渠道商品）。
	ChannelPriceService ChannelPriceService
	RouteService        RouteService

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

	// 阶段 14 模型目录：models.dev 目录浏览 + 从目录采纳/刷新/更新提醒
	CatalogService CatalogService

	// M9 工作台看板：首屏 KPI 概览 + 时间序列（只读聚合）
	DashboardService DashboardService

	// M8 系统/任务/健康（横切）：结算补偿任务只读视图 + 系统级 channel 健康（派生）
	RecoveryJobQueryService   RecoveryJobQueryService
	ChannelHealthQueryService ChannelHealthQueryService

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
			// DELETE 物理删除录错的脏数据：名下有渠道或已被请求/账务引用时返回 409，提示改用停用。
			r.Delete("/providers/{id}", ph.delete)
		}

		if deps.ChannelService != nil {
			ch := &channelsHandler{service: deps.ChannelService}
			// adapter_key 可选枚举（供前端下拉）：静态路径，置于 /channels/{id} 之前避免被通配吞掉。
			r.Get("/channels/adapter-keys", ch.adapterKeys)
			r.Get("/channels", ch.list)
			r.Post("/channels", ch.create)
			r.Get("/channels/{id}", ch.get)
			r.Patch("/channels/{id}", ch.update)
			// credential 只写不回：用子资源 PUT 轮换，成功返回 204。
			r.Put("/channels/{id}/credential", ch.rotateCredential)
			// DELETE 物理删除录错的脏数据，级联清理自身绑定/价格；已被请求/账务引用时返回 409。
			r.Delete("/channels/{id}", ch.delete)
		}

		if deps.ChannelModelService != nil {
			cmh := &channelModelsHandler{service: deps.ChannelModelService}
			// channel↔model 绑定是 channel 的子资源，用 {modelId} 定位 Unio 模型。
			r.Get("/channels/{id}/models", cmh.list)
			r.Post("/channels/{id}/models", cmh.create)
			r.Patch("/channels/{id}/models/{modelId}", cmh.update)
			r.Delete("/channels/{id}/models/{modelId}", cmh.delete)
		}

		if deps.ChannelPriceService != nil {
			cph := &channelPricesHandler{service: deps.ChannelPriceService}
			// 阶段 15：渠道-模型价（售价+成本同表）挂在 channel 下；价格不可删，PATCH 调窗口/启停用价格 id 定位。
			r.Get("/channels/{id}/prices", cph.list)
			r.Post("/channels/{id}/models/{modelId}/prices", cph.create)
			r.Patch("/channel-prices/{id}", cph.update)
		}

		if deps.RouteService != nil {
			rh := &routesHandler{service: deps.RouteService}
			// 线路（渠道商品）：内置经济/稳定只读，自定义线路 CRUD + 渠道池设置。
			r.Get("/routes", rh.list)
			r.Post("/routes", rh.create)
			r.Get("/routes/{id}", rh.get)
			r.Patch("/routes/{id}", rh.update)
			r.Delete("/routes/{id}", rh.delete)
			r.Put("/routes/{id}/channels", rh.setChannels)
		}

		if deps.ModelService != nil {
			mh := &modelsHandler{service: deps.ModelService}
			r.Get("/models", mh.list)
			r.Post("/models", mh.create)
			r.Get("/models/{id}", mh.get)
			r.Patch("/models/{id}", mh.update)
			// DELETE 物理删除录错的脏数据，级联清理自身价格/绑定/能力；已被请求/账务引用时返回 409。
			r.Delete("/models/{id}", mh.delete)
		}

		// 阶段 14 模型目录：浏览 models.dev 目录 + 从目录采纳/刷新/更新提醒（采纳/刷新/提醒回读完整模型）。
		if deps.CatalogService != nil && deps.ModelService != nil {
			ch := &catalogHandler{catalog: deps.CatalogService, models: deps.ModelService}
			r.Get("/model-catalog", ch.list)
			// canonical_id 含 '/'，用通配段承载（如 /model-catalog/openai/gpt-4o）。
			r.Get("/model-catalog/*", ch.get)
			r.Post("/models/from-catalog", ch.adopt)
			r.Post("/models/{id}/catalog-refresh", ch.refresh)
			r.Post("/models/{id}/catalog-reminder", ch.reminder)
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
			// 阶段 15：设置项目默认线路。
			r.Patch("/projects/{id}", pjh.update)
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

			// 能力自动校正建议（DESIGN-capability-autocalibration）：列待采纳 / 一键采纳 / 忽略。
			r.Get("/capability/suggestions", cah.listSuggestions)
			r.Post("/models/{id}/capability-suggestions/{key}/accept", cah.acceptSuggestion)
			r.Post("/models/{id}/capability-suggestions/{key}/dismiss", cah.dismissSuggestion)
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

		// M9 工作台看板：运营首页只读聚合（KPI 概览 + 时间序列）。
		if deps.DashboardService != nil {
			dh := &dashboardHandler{service: deps.DashboardService}
			r.Get("/dashboard/overview", dh.overview)
			r.Get("/dashboard/timeseries", dh.timeseries)
		}

		// M8 系统/任务/健康：结算补偿任务只读视图（列表脱敏内部详情，详情按 ?include_internal 回显）。
		if deps.RecoveryJobQueryService != nil {
			rjh := &recoveryJobsHandler{service: deps.RecoveryJobQueryService}
			r.Get("/system/settlement-recovery-jobs", rjh.list)
			r.Get("/system/settlement-recovery-jobs/{id}", rjh.get)
		}

		// M8 系统级 channel 健康：从 request_attempts 派生（非熔断器实时态，仅运营观测近似）。
		if deps.ChannelHealthQueryService != nil {
			chh := &channelHealthHandler{service: deps.ChannelHealthQueryService}
			r.Get("/system/channel-health", chh.list)
		}
	})

	return r
}

// handlePing 在通过 admin 认证后返回探针结果。
func handlePing(w http.ResponseWriter, _ *http.Request) {
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
