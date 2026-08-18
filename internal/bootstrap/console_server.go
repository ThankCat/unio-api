package bootstrap

import (
	"context"
	"errors"
	"net/http"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consoleauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
	"github.com/redis/go-redis/v9"
)

// ConsoleServerAppDB is the database contract required by console-server.
type ConsoleServerAppDB interface {
	consoleservice.DB
}

// ConsoleServerAppDeps contains console-server startup dependencies.
type ConsoleServerAppDeps struct {
	Logger *zap.Logger
	Config config.Config
	DB     ConsoleServerAppDB
	Redis  *redis.Client
}

// ConsoleServerApp owns the Console HTTP handler and tracing lifecycle.
type ConsoleServerApp struct {
	Handler http.Handler
	tracer  *tracing.Provider
}

// Shutdown flushes Console tracing resources.
func (a *ConsoleServerApp) Shutdown(ctx context.Context) error {
	if a == nil || a.tracer == nil {
		return nil
	}
	return a.tracer.Shutdown(ctx)
}

// NewConsoleServerApp wires Console settings, authentication, and HTTP routing.
func NewConsoleServerApp(ctx context.Context, deps ConsoleServerAppDeps) (*ConsoleServerApp, error) {
	if deps.Config.Console.Environment == config.GatewayEnvironmentProduction && deps.Config.Console.FixedVerificationCode == "" {
		return nil, errors.New("console email delivery is not configured for production")
	}
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

	httpx.SetMaxJSONBodyBytes(deps.Config.HTTP.AdminMaxJSONBodyBytes)
	httpx.SetResponseWriteTimeout(deps.Config.HTTP.WriteTimeout)
	queries := sqlc.New(deps.DB)
	settingsStore := appsettings.NewSettingsStore(
		queries,
		deps.Redis,
		deps.Config.Redis.KeyNamespace,
		appsettings.DefaultRegistry(),
		deps.Logger,
	)
	if err := settingsStore.SeedDefaults(ctx); err != nil {
		deps.Logger.Warn("seed console authentication settings failed", zap.Error(err))
	}
	verificationStore, err := consoleauth.NewVerificationStore(
		deps.Redis,
		deps.Config.Redis.KeyNamespace,
		deps.Config.Console.AuthSecret,
		deps.Config.Console.FixedVerificationCode,
		settingsStore,
	)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		return nil, err
	}
	sessions, err := consoleauth.NewSessionManager(
		deps.Redis,
		deps.Config.Redis.KeyNamespace,
		deps.Config.Console.AuthSecret,
		deps.Config.Console.AccessTokenTTL,
		deps.Config.Console.RefreshTokenTTL,
	)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		return nil, err
	}
	authService, err := consoleauth.NewService(
		deps.DB,
		verificationStore,
		sessions,
		deps.Logger,
	)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		return nil, err
	}
	handler, err := consoleapi.NewRouter(consoleapi.Deps{
		Logger:      deps.Logger,
		Config:      deps.Config.Console,
		AuthService: authService,
	})
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		return nil, err
	}
	return &ConsoleServerApp{Handler: handler, tracer: tracerProvider}, nil
}
