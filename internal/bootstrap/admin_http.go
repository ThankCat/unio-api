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
	metricsRecorder *metrics.Metrics,
) http.Handler {
	deps := adminapi.RouterDeps{
		Logger:             logger,
		AdminAuthenticator: authenticator,
		ProviderService:    providerService,
		ChannelService:     channelService,
	}

	if metricsRecorder != nil {
		deps.HTTPMetrics = metricsRecorder
		deps.MetricsHandler = metricsRecorder.Handler()
	}

	return adminapi.NewRouter(deps)
}
