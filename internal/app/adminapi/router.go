// Package adminapi 组装 admin 管理端（/admin/v1）的 HTTP 路由。
//
// admin 表面只服务平台管理员，认证走 admin 静态 token（core/adminauth），与客户 Gateway（/v1）
// 严格隔离。各业务模块的 handler / DTO / service 接口按模块拆到子包（overview/provider/channel/
// model/route/capability/user/requests/ledger/system，镜像 internal/service/admin 的目录结构），
// 共用的响应/请求/分页/排序小工具在 adminapi/adminhttp 叶子包。本文件只做依赖聚合与路由编排。
package adminapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/capability"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/channel"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/ledger"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/model"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/overview"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/provider"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/providerendpoint"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/requests"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/route"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/system"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/user"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/httpmw"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

type RoutingTraceService interface {
	route.RoutingTraceService
	requests.RoutingTraceService
}

// RouterDeps 保存构建 admin HTTP router 所需的外部依赖（扁平聚合，按模块分派到各子包 Register）。
type RouterDeps struct {
	Logger             *zap.Logger
	AdminAuthenticator middleware.AdminAuthenticator

	ProviderService         provider.ProviderService
	ProviderOpsService      provider.ProviderOpsService
	ProviderEndpointService providerendpoint.ProviderEndpointService
	ProviderEndpointBreaker providerendpoint.BreakerRuntime
	ChannelService          channel.ChannelService
	ChannelBreaker          channel.BreakerRuntime
	ChannelTestService      channel.ChannelTestService
	ChannelOpsService       channel.ChannelOpsService
	ModelService            model.ModelService
	ModelOpsService         model.ModelOpsService
	ChannelModelService     channel.ChannelModelService

	// 渠道-模型成本价（绝对覆盖）+ 线路（渠道商品）。
	ChannelPriceService channel.ChannelPriceService
	RouteService        route.RouteService
	RouteOpsService     route.RouteOpsService
	RoutingTraceService RoutingTraceService
	RouteRuntimeService route.RuntimeService

	// DEC-026：模型基准售价（客户售价 = 模型基准价 × 线路倍率）。
	ModelPriceService model.ModelPriceService

	// DEC-027：渠道成本倍率（渠道真实成本 = 模型上游参考成本 × 价格倍率 × 充值倍率）。
	ChannelCostMultiplierService channel.ChannelCostMultiplierService
	ChannelRechargeFactorService channel.ChannelRechargeFactorService

	// M6 只读查询台
	RequestQueryService requests.RequestQueryService
	LedgerQueryService  ledger.LedgerQueryService

	// bill-on-cancel 渠道成本敞口只读视图。
	CostExposureQueryService ledger.CostExposureQueryService

	// M7 客户管理：用户（只读）/API Key（费用上限 + 必填线路）/手工调额
	UserService        user.UserService
	APIKeyService      user.APIKeyService
	AdjustmentService  user.AdjustmentService
	CustomerOpsService user.CustomerOpsService

	// M5 能力管理：模型能力 CRUD、models.dev 同步、adapter 画像
	CapabilityService     capability.CapabilityService
	CapabilitySyncService capability.CapabilitySyncService
	CapabilitySeedService capability.CapabilitySeedService

	// 模型目录：models.dev 目录浏览 + 从目录采纳/刷新/更新提醒
	CatalogService model.CatalogService

	// M9 工作台看板：首屏概览雷达 + 时间序列（只读聚合）
	DashboardService overview.DashboardService

	// M8 系统/任务/健康（横切）：结算补偿任务只读视图
	RecoveryJobQueryService   system.RecoveryJobQueryService
	RuntimeDiagnosticsService system.RuntimeDiagnosticsService

	// Provider 全局设置（可编辑）：起步 Anthropic beta 转发策略（app_settings）。
	ProviderSettingsService system.ProviderSettingsService

	// 系统配置只读面板（进程级 env 生效值，脱敏）。
	GatewayConfig config.GatewayConfig
	WorkerConfig  config.WorkerConfig
	HTTPConfig    config.HTTPConfig

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

		// 各业务模块自注册路由（chi 按静态优先于通配匹配，模块注册顺序不影响正确性）。
		overview.Register(r, overview.Deps{Service: deps.DashboardService})
		provider.Register(r, provider.Deps{
			Service:    deps.ProviderService,
			OpsService: deps.ProviderOpsService,
		})
		providerendpoint.Register(r, providerendpoint.Deps{
			Service: deps.ProviderEndpointService,
			Breaker: deps.ProviderEndpointBreaker,
		})
		channel.Register(r, channel.Deps{
			Service:               deps.ChannelService,
			OpsService:            deps.ChannelOpsService,
			TestService:           deps.ChannelTestService,
			ModelService:          deps.ChannelModelService,
			PriceService:          deps.ChannelPriceService,
			CostMultiplierService: deps.ChannelCostMultiplierService,
			RechargeFactorService: deps.ChannelRechargeFactorService,
			Breaker:               deps.ChannelBreaker,
		})
		model.Register(r, model.Deps{
			Service:        deps.ModelService,
			OpsService:     deps.ModelOpsService,
			PriceService:   deps.ModelPriceService,
			CatalogService: deps.CatalogService,
		})
		route.Register(r, route.Deps{
			Service:             deps.RouteService,
			OpsService:          deps.RouteOpsService,
			RoutingTraceService: deps.RoutingTraceService,
			RuntimeService:      deps.RouteRuntimeService,
		})
		capability.Register(r, capability.Deps{
			Service:     deps.CapabilityService,
			SyncService: deps.CapabilitySyncService,
			SeedService: deps.CapabilitySeedService,
		})
		user.Register(r, user.Deps{
			Service:           deps.UserService,
			APIKeyService:     deps.APIKeyService,
			AdjustmentService: deps.AdjustmentService,
			OpsService:        deps.CustomerOpsService,
		})
		requests.Register(r, requests.Deps{
			Service:             deps.RequestQueryService,
			RoutingTraceService: deps.RoutingTraceService,
		})
		ledger.Register(r, ledger.Deps{
			Service:             deps.LedgerQueryService,
			CostExposureService: deps.CostExposureQueryService,
		})
		system.Register(r, system.Deps{
			RecoveryJobService:        deps.RecoveryJobQueryService,
			ProviderSettingsService:   deps.ProviderSettingsService,
			RuntimeDiagnosticsService: deps.RuntimeDiagnosticsService,
			GatewayConfig:             deps.GatewayConfig,
			WorkerConfig:              deps.WorkerConfig,
			HTTPConfig:                deps.HTTPConfig,
		})
	})

	return r
}

// handlePing 在通过 admin 认证后返回探针结果。
func handlePing(w http.ResponseWriter, _ *http.Request) {
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
