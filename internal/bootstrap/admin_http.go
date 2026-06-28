package bootstrap

import (
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/app/adminapi"
	"github.com/ThankCat/unio-api/internal/app/adminapi/middleware"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
)

// adminHTTPDeps 收拢 admin-server HTTP handler 构建所需的全部 service 依赖。
type adminHTTPDeps struct {
	Logger        *slog.Logger
	Authenticator middleware.AdminAuthenticator

	ProviderService     adminapi.ProviderService
	ProviderOpsService  adminapi.ProviderOpsService
	ChannelService      adminapi.ChannelService
	ChannelOpsService   adminapi.ChannelOpsService
	ModelService        adminapi.ModelService
	ModelOpsService     adminapi.ModelOpsService
	ChannelModelService adminapi.ChannelModelService
	ChannelPriceService adminapi.ChannelPriceService
	RouteService        adminapi.RouteService
	RouteOpsService     adminapi.RouteOpsService

	RequestQueryService adminapi.RequestQueryService
	UsageQueryService   adminapi.UsageQueryService
	LedgerQueryService  adminapi.LedgerQueryService

	UserService        adminapi.UserService
	ProjectService     adminapi.ProjectService
	APIKeyService      adminapi.APIKeyService
	AdjustmentService  adminapi.AdjustmentService
	CustomerOpsService adminapi.CustomerOpsService

	CapabilityService     adminapi.CapabilityService
	CapabilitySyncService adminapi.CapabilitySyncService
	CapabilitySeedService adminapi.CapabilitySeedService

	CatalogService adminapi.CatalogService

	DashboardService adminapi.DashboardService

	RecoveryJobQueryService   adminapi.RecoveryJobQueryService
	ChannelHealthQueryService adminapi.ChannelHealthQueryService

	// 系统配置只读面板（进程级 env 生效值，脱敏）。
	GatewayConfig        config.GatewayConfig
	RateLimitConfig      config.RateLimitConfig
	CircuitBreakerConfig config.CircuitBreakerConfig
	WorkerConfig         config.WorkerConfig
	HTTPConfig           config.HTTPConfig

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
		ChannelOpsService:   deps.ChannelOpsService,
		ModelService:        deps.ModelService,
		ModelOpsService:     deps.ModelOpsService,
		ChannelModelService: deps.ChannelModelService,
		ChannelPriceService: deps.ChannelPriceService,
		RouteService:        deps.RouteService,
		RouteOpsService:     deps.RouteOpsService,
		RequestQueryService: deps.RequestQueryService,
		UsageQueryService:   deps.UsageQueryService,
		LedgerQueryService:  deps.LedgerQueryService,
		UserService:         deps.UserService,
		ProjectService:      deps.ProjectService,
		APIKeyService:       deps.APIKeyService,
		AdjustmentService:   deps.AdjustmentService,
		CustomerOpsService:  deps.CustomerOpsService,

		CapabilityService:     deps.CapabilityService,
		CapabilitySyncService: deps.CapabilitySyncService,
		CapabilitySeedService: deps.CapabilitySeedService,

		CatalogService: deps.CatalogService,

		DashboardService: deps.DashboardService,

		RecoveryJobQueryService:   deps.RecoveryJobQueryService,
		ChannelHealthQueryService: deps.ChannelHealthQueryService,

		GatewayConfig:        deps.GatewayConfig,
		RateLimitConfig:      deps.RateLimitConfig,
		CircuitBreakerConfig: deps.CircuitBreakerConfig,
		WorkerConfig:         deps.WorkerConfig,
		HTTPConfig:           deps.HTTPConfig,
	}

	if deps.MetricsRecorder != nil {
		routerDeps.HTTPMetrics = deps.MetricsRecorder
		routerDeps.MetricsHandler = deps.MetricsRecorder.Handler()
	}

	return adminapi.NewRouter(routerDeps)
}
