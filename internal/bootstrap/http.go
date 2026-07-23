package bootstrap

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi"
	gatewayanthropic "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/anthropic/messages"
	gatewayopenai "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	gatewayresponses "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/modelcatalog"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
)

// NewHTTPHandler 创建当前 server 进程使用的 HTTP handler。
func NewHTTPHandler(
	logger *zap.Logger,
	queries *sqlc.Queries,
	requestAdmission *requestadmission.Manager,
	chatCompletionService gatewayopenai.ChatCompletionService,
	responsesService gatewayresponses.ResponsesService,
	messagesService gatewayanthropic.MessagesService,
	metricsRecorder *metrics.Metrics,
	readiness gatewayapi.ReadinessProbe,
) http.Handler {
	apiKeyAuthenticator := auth.NewAPIKeyAuthenticator(queries)
	modelCatalogService := modelcatalog.NewService(queries)

	deps := gatewayapi.RouterDeps{
		Logger:              logger,
		APIKeyAuthenticator: apiKeyAuthenticator,
		RequestAdmission:    requestAdmission,
		Readiness:           readiness,

		ChatCompletionService: chatCompletionService,
		ResponsesService:      responsesService,
		MessagesService:       messagesService,
		ModelCatalogService:   modelCatalogService,
	}

	if metricsRecorder != nil {
		deps.HTTPMetrics = metricsRecorder
		deps.MetricsHandler = metricsRecorder.Handler()
	}

	return gatewayapi.NewRouter(deps)
}
