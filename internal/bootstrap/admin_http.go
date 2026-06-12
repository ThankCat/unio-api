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
	ChannelService      adminapi.ChannelService
	ModelService        adminapi.ModelService
	ChannelModelService adminapi.ChannelModelService
	CostPriceService    adminapi.CostPriceService
	PriceService        adminapi.PriceService

	RequestQueryService adminapi.RequestQueryService
	UsageQueryService   adminapi.UsageQueryService
	LedgerQueryService  adminapi.LedgerQueryService

	UserService       adminapi.UserService
	ProjectService    adminapi.ProjectService
	APIKeyService     adminapi.APIKeyService
	AdjustmentService adminapi.AdjustmentService

	CapabilityService            adminapi.CapabilityService
	CapabilitySyncService        adminapi.CapabilitySyncService
	CapabilitySeedService        adminapi.CapabilitySeedService
	CapabilityEnforcementService adminapi.CapabilityEnforcementService

	DashboardService adminapi.DashboardService

	MetricsRecorder *metrics.Metrics
}

// NewAdminHTTPHandler 创建 admin-server 进程使用的 HTTP handler。
func NewAdminHTTPHandler(deps adminHTTPDeps) http.Handler {
	routerDeps := adminapi.RouterDeps{
		Logger:              deps.Logger,
		AdminAuthenticator:  deps.Authenticator,
		ProviderService:     deps.ProviderService,
		ChannelService:      deps.ChannelService,
		ModelService:        deps.ModelService,
		ChannelModelService: deps.ChannelModelService,
		CostPriceService:    deps.CostPriceService,
		PriceService:        deps.PriceService,
		RequestQueryService: deps.RequestQueryService,
		UsageQueryService:   deps.UsageQueryService,
		LedgerQueryService:  deps.LedgerQueryService,
		UserService:         deps.UserService,
		ProjectService:      deps.ProjectService,
		APIKeyService:       deps.APIKeyService,
		AdjustmentService:   deps.AdjustmentService,

		CapabilityService:            deps.CapabilityService,
		CapabilitySyncService:        deps.CapabilitySyncService,
		CapabilitySeedService:        deps.CapabilitySeedService,
		CapabilityEnforcementService: deps.CapabilityEnforcementService,

		DashboardService: deps.DashboardService,
	}

	if deps.MetricsRecorder != nil {
		routerDeps.HTTPMetrics = deps.MetricsRecorder
		routerDeps.MetricsHandler = deps.MetricsRecorder.Handler()
	}

	return adminapi.NewRouter(routerDeps)
}
