package bootstrap

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/capability"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/channel"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/ledger"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/model"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/overview"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/provider"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/requests"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/route"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/system"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/user"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/service/admin/gatewayruntime"
)

// adminHTTPDeps 收拢 admin-server HTTP handler 构建所需的全部 service 依赖。
type adminHTTPDeps struct {
	Logger        *zap.Logger
	Authenticator middleware.AdminAuthenticator

	ProviderService     provider.ProviderService
	ProviderOpsService  provider.ProviderOpsService
	ChannelService      channel.ChannelService
	ChannelTestService  channel.ChannelTestService
	ChannelOpsService   channel.ChannelOpsService
	ModelService        model.ModelService
	ModelOpsService     model.ModelOpsService
	ChannelModelService channel.ChannelModelService
	ChannelPriceService channel.ChannelPriceService
	ModelPriceService   model.ModelPriceService

	// DEC-027 渠道成本倍率。
	ChannelCostMultiplierService channel.ChannelCostMultiplierService
	ChannelRechargeFactorService channel.ChannelRechargeFactorService

	// BreakerClient 可选：渠道列表挂载 gateway 熔断快照。
	BreakerClient *gatewayruntime.Client

	RouteService        route.RouteService
	RouteOpsService     route.RouteOpsService
	RoutingTraceService adminapi.RoutingTraceService
	RouteRuntimeService route.RuntimeService

	RequestQueryService requests.RequestQueryService
	LedgerQueryService  ledger.LedgerQueryService

	// bill-on-cancel 渠道成本敞口只读视图（DESIGN-bill-on-cancel 阶段一）。
	CostExposureQueryService ledger.CostExposureQueryService

	UserService        user.UserService
	APIKeyService      user.APIKeyService
	AdjustmentService  user.AdjustmentService
	CustomerOpsService user.CustomerOpsService

	CapabilityService     capability.CapabilityService
	CapabilitySyncService capability.CapabilitySyncService
	CapabilitySeedService capability.CapabilitySeedService

	CatalogService model.CatalogService

	DashboardService overview.DashboardService

	RecoveryJobQueryService system.RecoveryJobQueryService

	ProviderSettingsService system.ProviderSettingsService

	// 系统配置只读面板（进程级 env 生效值，脱敏）；6 组热路径配置已迁移为运行时配置，不在此列。
	GatewayConfig config.GatewayConfig
	WorkerConfig  config.WorkerConfig
	HTTPConfig    config.HTTPConfig

	MetricsRecorder *metrics.Metrics
}

// NewAdminHTTPHandler 创建 admin-server 进程使用的 HTTP handler。
func NewAdminHTTPHandler(deps adminHTTPDeps) http.Handler {
	routerDeps := adminapi.RouterDeps{
		Logger:              deps.Logger,
		AdminAuthenticator:  deps.Authenticator,
		ProviderService:     deps.ProviderService,
		ProviderOpsService:  deps.ProviderOpsService,
		ChannelService:      deps.ChannelService,
		ChannelTestService:  deps.ChannelTestService,
		ChannelOpsService:   deps.ChannelOpsService,
		ModelService:        deps.ModelService,
		ModelOpsService:     deps.ModelOpsService,
		ChannelModelService: deps.ChannelModelService,
		ChannelPriceService: deps.ChannelPriceService,
		ModelPriceService:   deps.ModelPriceService,

		ChannelCostMultiplierService: deps.ChannelCostMultiplierService,
		ChannelRechargeFactorService: deps.ChannelRechargeFactorService,

		BreakerClient: deps.BreakerClient,

		RouteService:        deps.RouteService,
		RouteOpsService:     deps.RouteOpsService,
		RoutingTraceService: deps.RoutingTraceService,
		RouteRuntimeService: deps.RouteRuntimeService,
		RequestQueryService: deps.RequestQueryService,
		LedgerQueryService:  deps.LedgerQueryService,

		CostExposureQueryService: deps.CostExposureQueryService,
		UserService:              deps.UserService,
		APIKeyService:            deps.APIKeyService,
		AdjustmentService:        deps.AdjustmentService,
		CustomerOpsService:       deps.CustomerOpsService,

		CapabilityService:     deps.CapabilityService,
		CapabilitySyncService: deps.CapabilitySyncService,
		CapabilitySeedService: deps.CapabilitySeedService,

		CatalogService: deps.CatalogService,

		DashboardService: deps.DashboardService,

		RecoveryJobQueryService: deps.RecoveryJobQueryService,
		ProviderSettingsService: deps.ProviderSettingsService,

		GatewayConfig: deps.GatewayConfig,
		WorkerConfig:  deps.WorkerConfig,
		HTTPConfig:    deps.HTTPConfig,
	}

	if deps.MetricsRecorder != nil {
		routerDeps.HTTPMetrics = deps.MetricsRecorder
		routerDeps.MetricsHandler = deps.MetricsRecorder.Handler()
	}

	return adminapi.NewRouter(routerDeps)
}
