package bootstrap

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adminauth"
	"github.com/ThankCat/unio-api/internal/core/credential"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/channel"
	"github.com/ThankCat/unio-api/internal/service/admin/channelmodel"
	"github.com/ThankCat/unio-api/internal/service/admin/costprice"
	"github.com/ThankCat/unio-api/internal/service/admin/model"
	"github.com/ThankCat/unio-api/internal/service/admin/price"
	"github.com/ThankCat/unio-api/internal/service/admin/provider"
)

// AdminServerAppDeps 表示构建 admin server app 需要的进程级依赖。
type AdminServerAppDeps struct {
	Logger *slog.Logger
	Config config.Config
	DB     sqlc.DBTX
}

// AdminServerApp 表示当前 admin-server 进程已经装配完成的 HTTP 应用。
type AdminServerApp struct {
	Handler http.Handler

	tracer *tracing.Provider
}

// Shutdown 释放 app 持有的可观测性资源（flush trace exporter）。
// 未启用 tracing 时为安全空操作。
func (a *AdminServerApp) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}

	return a.tracer.Shutdown(ctx)
}

// NewAdminServerApp 装配当前 admin-server 进程的业务应用。
//
// 启动期校验：ADMIN_API_TOKEN 不能为空；CREDENTIAL_MASTER_KEY 必须可解析成 AES-256 key
// （channel 凭据落库要用它加密）。任一缺失/非法都在此失败，避免 admin 表面带病启动。
func NewAdminServerApp(ctx context.Context, deps AdminServerAppDeps) (*AdminServerApp, error) {
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

	authenticator, err := adminauth.NewStaticTokenAuthenticator(deps.Config.Admin.APIToken)
	if err != nil {
		return nil, err
	}

	masterKey, err := credential.ParseMasterKey(deps.Config.Credential.MasterKey)
	if err != nil {
		return nil, err
	}
	cipher, err := credential.NewAESGCMCipher(masterKey)
	if err != nil {
		return nil, err
	}

	adapterRegistry, err := NewAdapterRegistry(http.DefaultClient, deps.Logger)
	if err != nil {
		return nil, err
	}

	queries := sqlc.New(deps.DB)

	providerService := provider.NewService(queries)
	channelService := channel.NewService(queries, cipher, adapterRegistry)
	modelService := model.NewService(queries)
	channelModelService := channelmodel.NewService(queries)
	costPriceService := costprice.NewService(queries)
	priceService := price.NewService(queries)

	metricsRecorder := metrics.New()

	handler := NewAdminHTTPHandler(deps.Logger, authenticator, providerService, channelService, modelService, channelModelService, costPriceService, priceService, metricsRecorder)

	return &AdminServerApp{
		Handler: handler,
		tracer:  tracerProvider,
	}, nil
}
