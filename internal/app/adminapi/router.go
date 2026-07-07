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
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/httpmw"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// RouterDeps 保存构建 admin HTTP router 所需的外部依赖。
type RouterDeps struct {
	Logger             *slog.Logger
	AdminAuthenticator middleware.AdminAuthenticator

	ProviderService     ProviderService
	ProviderOpsService  ProviderOpsService
	ChannelService      ChannelService
	ChannelTestService  ChannelTestService
	ChannelOpsService   ChannelOpsService
	ModelService        ModelService
	ModelOpsService     ModelOpsService
	ChannelModelService ChannelModelService

	// 阶段 15：渠道-模型价（售价+成本合并）+ 线路（渠道商品）。
	ChannelPriceService ChannelPriceService
	RouteService        RouteService
	RouteOpsService     RouteOpsService

	// DEC-026：模型基准售价（客户售价 = 模型基准价 × 线路倍率）。
	ModelPriceService ModelPriceService

	// M6 只读查询台
	RequestQueryService RequestQueryService
	LedgerQueryService  LedgerQueryService

	// M7 客户管理：用户（只读）/API Key（费用上限 + 必填线路）/手工调额
	UserService        UserService
	APIKeyService      APIKeyService
	AdjustmentService  AdjustmentService
	CustomerOpsService CustomerOpsService

	// M5 能力管理：模型能力 CRUD、models.dev 同步、adapter 画像（能力闸门已移除，DEC-024）
	CapabilityService     CapabilityService
	CapabilitySyncService CapabilitySyncService
	CapabilitySeedService CapabilitySeedService

	// 阶段 14 模型目录：models.dev 目录浏览 + 从目录采纳/刷新/更新提醒
	CatalogService CatalogService

	// M9 工作台看板：首屏 KPI 概览 + 时间序列（只读聚合）
	DashboardService DashboardService

	// M8 系统/任务/健康（横切）：结算补偿任务只读视图 + 系统级 channel 健康（派生）
	RecoveryJobQueryService   RecoveryJobQueryService
	ChannelHealthQueryService ChannelHealthQueryService

	// Provider 全局设置（可编辑）：起步 Anthropic beta 转发策略（app_settings）。
	ProviderSettingsService ProviderSettingsService

	// 系统配置只读面板（进程级 env 生效值，脱敏）：网关兜底/熔断/限流默认/补偿/HTTP 阈值。
	// 这些是值类型快照，恒有效，故 /system/config 路由无条件注册。
	GatewayConfig        config.GatewayConfig
	RateLimitConfig      config.RateLimitConfig
	CircuitBreakerConfig config.CircuitBreakerConfig
	WorkerConfig         config.WorkerConfig
	HTTPConfig           config.HTTPConfig

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

		// §3.2 服务商聚合视图：静态 /providers/ops 必须在 /providers/{id} 之前注册。
		if deps.ProviderOpsService != nil {
			poh := &providerOpsHandler{service: deps.ProviderOpsService}
			r.Get("/providers/ops", poh.table)
			r.Get("/providers/{id}/ops/detail", poh.detail)
			r.Get("/providers/{id}/ops/channel-catalog", poh.channelCatalog)
			r.Get("/providers/{id}/ops/model-catalog", poh.modelCatalog)
			r.Get("/providers/{id}/ops/route-catalog", poh.routeCatalog)
			r.Get("/providers/{id}/ops/channels", poh.channels)
			r.Get("/providers/{id}/ops/performance", poh.performance)
			r.Get("/providers/{id}/ops/errors", poh.errors)
		}

		if deps.ProviderService != nil {
			ph := &providersHandler{service: deps.ProviderService}
			r.Get("/providers", ph.list)
			r.Post("/providers", ph.create)
			r.Post("/providers/{id}/archive", ph.archive)
			r.Post("/providers/{id}/restore", ph.restore)
			r.Patch("/providers/{id}", ph.update)
			// DELETE 物理删除录错的脏数据：名下有渠道或已被请求/账务引用时返回 409，提示改用停用。
			r.Delete("/providers/{id}", ph.delete)
		}

		// §3.3 渠道作战台只读运维聚合：静态 /channels/ops* 必须在 /channels/{id} 之前注册。
		if deps.ChannelOpsService != nil {
			coh := &channelOpsHandler{service: deps.ChannelOpsService}
			r.Get("/channels/ops/summary", coh.summary)
			r.Get("/channels/ops", coh.table)
			r.Get("/channels/{id}/ops/detail", coh.detail)
			r.Get("/channels/{id}/ops/performance", coh.performance)
			r.Get("/channels/{id}/ops/success-buckets", coh.successBuckets)
			r.Get("/channels/{id}/ops/errors", coh.errors)
			r.Get("/channels/{id}/ops/models", coh.models)
			r.Get("/channels/{id}/ops/routes", coh.routes)
		}

		if deps.ChannelService != nil {
			ch := &channelsHandler{service: deps.ChannelService}
			// adapter_key 可选枚举（供前端下拉）：静态路径，置于 /channels/{id} 之前避免被通配吞掉。
			r.Get("/channels/adapter-keys", ch.adapterKeys)
			r.Get("/channels", ch.list)
			r.Post("/channels", ch.create)
			r.Get("/channels/{id}", ch.get)
			r.Patch("/channels/{id}", ch.update)
			r.Delete("/channels/{id}", ch.delete)
			r.Post("/channels/{id}/archive", ch.archive)
			r.Post("/channels/{id}/restore", ch.restore)
			// credential 只写不回：用子资源 PUT 轮换，成功返回 204。
			r.Put("/channels/{id}/credential", ch.rotateCredential)
		}

		// 渠道主动检测（一键测渠道，阶段一）：向真实上游发一个最小请求验证连通/凭据/模型，只报告不摘除。
		if deps.ChannelTestService != nil {
			cth := &channelTestHandler{service: deps.ChannelTestService}
			r.Post("/channels/{id}/test", cth.test)
			r.Get("/channels/{id}/test-logs", cth.testLogs)
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

		// §3.5 线路路由作战台：静态 /routes/ops 必须在 /routes/{id} 之前注册。
		if deps.RouteOpsService != nil {
			roh := &routeOpsHandler{service: deps.RouteOpsService}
			r.Get("/routes/ops/summary", roh.summary)
			r.Get("/routes/ops", roh.table)
			r.Get("/routes/{id}/ops/detail", roh.detail)
			r.Get("/routes/{id}/ops/reachable-models", roh.reachableModels)
			r.Get("/routes/{id}/ops/channel-pool", roh.channelPool)
			r.Get("/routes/{id}/ops/bindings", roh.bindings)
			r.Get("/routes/{id}/ops/performance", roh.performance)
			r.Get("/routes/{id}/ops/models", roh.models)
			r.Get("/routes/{id}/ops/requests", roh.requests)
		}

		if deps.RouteService != nil {
			rh := &routesHandler{service: deps.RouteService}
			// 线路（渠道商品）CRUD + 渠道池设置。
			r.Get("/routes", rh.list)
			r.Post("/routes", rh.create)
			r.Post("/routes/{id}/archive", rh.archive)
			r.Post("/routes/{id}/restore", rh.restore)
			r.Post("/routes/{id}/migrate-keys", rh.migrateKeys)
			r.Get("/routes/{id}", rh.get)
			r.Patch("/routes/{id}", rh.update)
			r.Delete("/routes/{id}", rh.delete)
		}

		// §3.4 模型商品控制台：静态 /models/ops 必须在 /models/{id} 之前注册。
		if deps.ModelOpsService != nil {
			moh := &modelOpsHandler{service: deps.ModelOpsService}
			r.Get("/models/ops/summary", moh.summary)
			r.Get("/models/ops", moh.table)
			r.Get("/models/{id}/ops/detail", moh.detail)
			r.Get("/models/{id}/ops/channels", moh.channels)
			r.Get("/models/{id}/ops/performance", moh.performance)
			r.Get("/models/{id}/ops/requests", moh.requests)
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

		if deps.ModelPriceService != nil {
			mph := &modelPricesHandler{service: deps.ModelPriceService}
			// DEC-026：模型基准售价挂在 model 下；金额不可改，PATCH 调窗口/启停用价格 id 定位。
			r.Get("/models/{id}/prices", mph.list)
			r.Post("/models/{id}/prices", mph.create)
			r.Patch("/model-prices/{id}", mph.update)
		}

		// 阶段 14 模型目录：浏览 models.dev 目录 + 从目录采纳/刷新/更新提醒（采纳/刷新/提醒回读完整模型）。
		if deps.CatalogService != nil && deps.ModelService != nil {
			ch := &catalogHandler{catalog: deps.CatalogService, models: deps.ModelService}
			r.Get("/model-catalog", ch.list)
			// canonical_id 含 '/'，用通配段承载（如 /model-catalog/openai/gpt-4o）。
			r.Get("/model-catalog/*", ch.get)
			r.Post("/models/from-catalog", ch.adopt)
		}

		// M6 只读查询台：请求记录（含详情聚合）、用量、账本流水、计费异常。全部只读。
		if deps.RequestQueryService != nil {
			rqh := &requestsHandler{service: deps.RequestQueryService}
			r.Get("/requests", rqh.list)
			// 详情按对外 request_id 定位；?include_internal=true 才回显内部错误详情。
			r.Get("/requests/{requestId}", rqh.get)
		}

		if deps.LedgerQueryService != nil {
			lh := &ledgerHandler{service: deps.LedgerQueryService}
			r.Get("/ledger/entries", lh.listEntries)
			r.Get("/ledger/billing-exceptions", lh.listBillingExceptions)
		}

		// §3.7 客户中心只读运维聚合：静态 ops 路径在 {id} 之前注册。
		if deps.CustomerOpsService != nil {
			cuh := &customerOpsHandler{service: deps.CustomerOpsService}
			r.Get("/users/ops/summary", cuh.usersSummary)
			r.Get("/users/ops", cuh.usersTable)
			r.Get("/users/{id}/ops/detail", cuh.userDetail)
			r.Get("/users/{id}/api-keys/ops/summary", cuh.apiKeysSummary)
			r.Get("/users/{id}/api-keys/ops", cuh.apiKeysTable)
		}

		// M7 客户管理：用户（只读列表/详情）、API Key（费用上限/线路）、手工调额。
		if deps.UserService != nil {
			uh := &usersHandler{service: deps.UserService}
			r.Get("/users/{id}", uh.get)

			// 手工调额是用户的子资源：充值/扣款一律走账本留痕。
			if deps.AdjustmentService != nil {
				ah := &adjustmentsHandler{service: deps.AdjustmentService}
				r.Post("/users/{id}/balance-adjustments", ah.create)
			}
		}

		if deps.APIKeyService != nil {
			akh := &apiKeysHandler{service: deps.APIKeyService}
			// 创建挂在用户下；单把操作用扁平 /api-keys/{id} 定位。
			r.Post("/users/{id}/api-keys", akh.create)
			// PATCH 调启停/费用上限；DELETE 物理删除无调用历史的 Key（有历史→409 提示改用吊销）。
			r.Patch("/api-keys/{id}", akh.update)
			r.Delete("/api-keys/{id}", akh.delete)
			// 吊销是保留行与审计的软失效（不可逆），走子资源 POST，与硬删除区分。
			r.Post("/api-keys/{id}/revoke", akh.revoke)
		}

		// M5 能力管理：模型能力（手工覆盖）CRUD + 能力 key 注册表（渠道收紧已移除，DEC-023）。
		if deps.CapabilityService != nil {
			cah := &capabilitiesHandler{service: deps.CapabilityService}
			r.Get("/capability/keys", cah.listKeys)
			r.Post("/capability/keys", cah.createKey)
			r.Put("/capability/keys/{key}", cah.updateKey)
			r.Delete("/capability/keys/{key}", cah.deleteKey)
			// 模型能力挂在 model 下；写入用 PUT {key} 幂等 upsert，DELETE 撤销。
			r.Get("/models/{id}/capabilities", cah.listModelCapabilities)
			// 批量整表覆盖（一次保存多条，DEC-024 §6.2）；per-key PUT/DELETE 保留兼容。
			r.Put("/models/{id}/capabilities", cah.replaceModelCapabilities)
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

		// M9 工作台看板：运营首页只读聚合（雷达 / 分组表现 / 性能时序）。
		if deps.DashboardService != nil {
			dh := &dashboardHandler{service: deps.DashboardService}
			r.Get("/dashboard/timeseries", dh.timeseries)
			r.Get("/dashboard/radar", dh.radar)
			r.Get("/dashboard/breakdown", dh.breakdown)
			r.Get("/dashboard/errors", dh.topErrors)
			r.Get("/dashboard/timeseries/performance", dh.performanceTimeseries)
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

		// Provider 全局设置（可编辑）：起步 Anthropic beta 转发策略;将来 OpenAI/Gemini 各自配置在此扩展。
		if deps.ProviderSettingsService != nil {
			psh := &providerSettingsHandler{service: deps.ProviderSettingsService}
			r.Get("/provider-settings/anthropic/beta-policy", psh.getAnthropicBeta)
			r.Put("/provider-settings/anthropic/beta-policy", psh.putAnthropicBeta)
		}

		// 系统配置只读面板：进程级 env 生效阈值（脱敏），让运营在前端看到所有不可运行期改的阈值。
		systemConfig := &systemConfigHandler{
			gateway:        deps.GatewayConfig,
			rateLimit:      deps.RateLimitConfig,
			circuitBreaker: deps.CircuitBreakerConfig,
			worker:         deps.WorkerConfig,
			http:           deps.HTTPConfig,
		}
		r.Get("/system/config", systemConfig.get)
	})

	return r
}

// handlePing 在通过 admin 认证后返回探针结果。
func handlePing(w http.ResponseWriter, _ *http.Request) {
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
