package bootstrap

import (
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/app/adminapi"
	"github.com/ThankCat/unio-api/internal/app/adminapi/middleware"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
)

// NewAdminHTTPHandler 创建 admin-server 进程使用的 HTTP handler。
func NewAdminHTTPHandler(
	logger *slog.Logger,
	authenticator middleware.AdminAuthenticator,
	providerService adminapi.ProviderService,
	channelService adminapi.ChannelService,
	modelService adminapi.ModelService,
	channelModelService adminapi.ChannelModelService,
	costPriceService adminapi.CostPriceService,
	priceService adminapi.PriceService,
	metricsRecorder *metrics.Metrics,
) http.Handler {
	deps := adminapi.RouterDeps{
		Logger:              logger,
		AdminAuthenticator:  authenticator,
		ProviderService:     providerService,
		ChannelService:      channelService,
		ModelService:        modelService,
		ChannelModelService: channelModelService,
		CostPriceService:    costPriceService,
		PriceService:        priceService,
	}

	if metricsRecorder != nil {
		deps.HTTPMetrics = metricsRecorder
		deps.MetricsHandler = metricsRecorder.Handler()
	}

	return adminapi.NewRouter(deps)
}
