package bootstrap

import (
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/app/adminapi"
	"github.com/ThankCat/unio-api/internal/app/adminapi/middleware"
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

	RequestQueryService adminapi.RequestQueryService
	UsageQueryService   adminapi.UsageQueryService
	LedgerQueryService  adminapi.LedgerQueryService

	UserService       adminapi.UserService
	ProjectService    adminapi.ProjectService
	APIKeyService     adminapi.APIKeyService
	AdjustmentService adminapi.AdjustmentService

	CapabilityService     adminapi.CapabilityService
	CapabilitySyncService adminapi.CapabilitySyncService
	CapabilitySeedService adminapi.CapabilitySeedService

	CatalogService adminapi.CatalogService

	DashboardService adminapi.DashboardService

	RecoveryJobQueryService   adminapi.RecoveryJobQueryService
	ChannelHealthQueryService adminapi.ChannelHealthQueryService

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
		RequestQueryService: deps.RequestQueryService,
		UsageQueryService:   deps.UsageQueryService,
		LedgerQueryService:  deps.LedgerQueryService,
		UserService:         deps.UserService,
		ProjectService:      deps.ProjectService,
		APIKeyService:       deps.APIKeyService,
		AdjustmentService:   deps.AdjustmentService,

		CapabilityService:     deps.CapabilityService,
		CapabilitySyncService: deps.CapabilitySyncService,
		CapabilitySeedService: deps.CapabilitySeedService,

		CatalogService: deps.CatalogService,

		DashboardService: deps.DashboardService,

		RecoveryJobQueryService:   deps.RecoveryJobQueryService,
		ChannelHealthQueryService: deps.ChannelHealthQueryService,
	}

	if deps.MetricsRecorder != nil {
		routerDeps.HTTPMetrics = deps.MetricsRecorder
		routerDeps.MetricsHandler = deps.MetricsRecorder.Handler()
	}

	return adminapi.NewRouter(routerDeps)
}
