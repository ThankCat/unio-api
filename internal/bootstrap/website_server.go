package bootstrap

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/websiteapi"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/publicmodels"
)

// WebsiteServerAppDeps 包含 website-server 的启动依赖（只读查询，无 Redis）。
type WebsiteServerAppDeps struct {
	Logger *zap.Logger
	Config config.Config
	DB     sqlc.DBTX
}

// WebsiteServerApp 管理 website HTTP 处理器和链路追踪生命周期。
type WebsiteServerApp struct {
	Handler http.Handler
	tracer  *tracing.Provider
}

// Shutdown 刷新并释放 website 链路追踪资源。
func (a *WebsiteServerApp) Shutdown(ctx context.Context) error {
	if a == nil || a.tracer == nil {
		return nil
	}
	return a.tracer.Shutdown(ctx)
}

// NewWebsiteServerApp 装配 website 公开只读 API 的路由。
func NewWebsiteServerApp(ctx context.Context, deps WebsiteServerAppDeps) (*WebsiteServerApp, error) {
	tracerProvider, err := tracing.Setup(ctx, tracing.Options{
		Enabled:     deps.Config.Tracing.Enabled,
		Endpoint:    deps.Config.Tracing.Endpoint,
		Insecure:    deps.Config.Tracing.Insecure,
		ServiceName: deps.Config.Tracing.ServiceName,
		SampleRatio: deps.Config.Tracing.SampleRatio,
	})
	if err != nil {
		return nil, err
	}

	queries := sqlc.New(deps.DB)
	handler := websiteapi.NewRouter(websiteapi.Deps{
		Logger:   deps.Logger,
		Models:   publicmodels.NewService(queries),
		LabLogos: queries,
	})

	return &WebsiteServerApp{Handler: handler, tracer: tracerProvider}, nil
}
