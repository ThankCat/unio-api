package bootstrap

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
	"github.com/redis/go-redis/v9"
)

// GatewayServerAppDB 定义 gateway server app 构建时需要的数据库能力。
type GatewayServerAppDB interface {
	sqlc.DBTX
	lifecycle.ChatTxBeginner
}

// GatewayServerAppDeps 表示构建 gateway server app 需要的进程级依赖。
type GatewayServerAppDeps struct {
	Logger *slog.Logger
	Config config.Config
	DB     GatewayServerAppDB
	Redis  redis.Cmdable
}

// GatewayServerApp 表示当前 gateway-server 进程已经装配完成的 HTTP 应用。
type GatewayServerApp struct {
	Handler http.Handler

	tracer *tracing.Provider
}

// Shutdown 释放 app 持有的可观测性资源（flush trace exporter）。
// 未启用 tracing 时为安全空操作。
func (a *GatewayServerApp) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}

	return a.tracer.Shutdown(ctx)
}

// NewGatewayServerApp 装配当前 gateway-server 进程的业务应用。
func NewGatewayServerApp(ctx context.Context, deps GatewayServerAppDeps) (*GatewayServerApp, error) {
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

	chatRouter, err := NewChatRouter(queries, deps.Config.Credential.MasterKey)
	if err != nil {
		return nil, err
	}

	adapterRegistry, err := NewAdapterRegistry(http.DefaultClient, deps.Logger)
	if err != nil {
		return nil, err
	}

	// TODO(阶段6/production): [GAP-6-003] 后台写入 channel.protocol + channel.adapter_key 时仍缺少 registry 校验，可能把不可运行的复合键写入业务数据；开放后台 provider/channel 管理前；在阶段 11 provider/channel CRUD 写入路径校验复合键必须存在于 adapter registry。
	providerAdapterPreflight := NewProviderAdapterPreflight(queries, adapterRegistry)
	if err := providerAdapterPreflight.ValidateEnabledChannelBindings(ctx); err != nil {
		return nil, err
	}

	metricsRecorder := metrics.New()

	chatCompletionService := NewChatGateway(
		deps.DB,
		queries,
		chatRouter,
		adapterRegistry,
		deps.Config.Worker,
		deps.Config.CircuitBreaker,
		metricsRecorder,
	)
	messagesService := NewMessagesGateway(
		deps.DB,
		queries,
		chatRouter,
		adapterRegistry,
		deps.Config.Worker,
		deps.Config.CircuitBreaker,
		metricsRecorder,
	)

	handler := NewHTTPHandler(
		deps.Logger,
		queries,
		deps.Redis,
		deps.Config,
		chatCompletionService,
		messagesService,
		metricsRecorder,
	)

	return &GatewayServerApp{
		Handler: handler,
		tracer:  tracerProvider,
	}, nil
}
