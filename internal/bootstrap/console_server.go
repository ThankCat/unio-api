package bootstrap

import (
	"context"
	"net/http"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/consoleapi"
	consoleticketapp "github.com/ThankCat/unio-gateway/internal/app/consoleapi/ticket"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	coreticket "github.com/ThankCat/unio-gateway/internal/core/ticket"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/adminmessage"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consoleapikeys "github.com/ThankCat/unio-gateway/internal/service/console/apikeys"
	consoleauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
	consolecdkey "github.com/ThankCat/unio-gateway/internal/service/console/cdkey"
	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
	consoleticket "github.com/ThankCat/unio-gateway/internal/service/console/ticket"
	consoleusage "github.com/ThankCat/unio-gateway/internal/service/console/usage"
	consolewallet "github.com/ThankCat/unio-gateway/internal/service/console/wallet"
	emailsvc "github.com/ThankCat/unio-gateway/internal/service/email"
	"github.com/ThankCat/unio-gateway/internal/service/publicmodels"
	"github.com/redis/go-redis/v9"
)

// ConsoleServerAppDB 定义 console-server 所需的数据库能力。
type ConsoleServerAppDB interface {
	consoleservice.DB
	consoleticket.TxBeginner
}

// ConsoleServerAppDeps 包含 console-server 的启动依赖。
type ConsoleServerAppDeps struct {
	Logger *zap.Logger
	Config config.Config
	DB     ConsoleServerAppDB
	Redis  *redis.Client
}

// ConsoleServerApp 管理 Console HTTP 处理器和链路追踪生命周期。
type ConsoleServerApp struct {
	Handler http.Handler
	tracer  *tracing.Provider
}

// Shutdown 刷新并释放 Console 链路追踪资源。
func (a *ConsoleServerApp) Shutdown(ctx context.Context) error {
	if a == nil || a.tracer == nil {
		return nil
	}
	return a.tracer.Shutdown(ctx)
}

// NewConsoleServerApp 装配 Console 配置、认证服务和 HTTP 路由。
func NewConsoleServerApp(ctx context.Context, deps ConsoleServerAppDeps) (*ConsoleServerApp, error) {
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
	ledgerService := ledger.NewService(deps.DB, queries)
	cdkeyService := consolecdkey.NewService(deps.DB, queries, ledgerService)
	if deps.Redis != nil {
		cdkeyLimiter, limiterErr := consolecdkey.NewRateLimiter(
			deps.Redis,
			deps.Config.Redis.KeyNamespace,
			deps.Config.Console.AuthSecret,
		)
		if limiterErr != nil {
			_ = tracerProvider.Shutdown(ctx)
			return nil, limiterErr
		}
		cdkeyService.WithRateLimiter(cdkeyLimiter)
	}
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
		"",
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
	loginLimiter, err := consoleauth.NewPasswordLoginLimiter(
		deps.Redis,
		deps.Config.Redis.KeyNamespace,
		deps.Config.Console.AuthSecret,
		settingsStore,
	)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		return nil, err
	}
	authService, err := consoleauth.NewService(
		deps.DB,
		verificationStore,
		sessions,
		loginLimiter,
		deps.Logger,
	)
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		return nil, err
	}
	// 验证码邮件同步发送器：现读系统设置里的 SMTP 配置（热更新），发送后写 email_messages 记录。
	authService = authService.WithCodeMailer(emailsvc.NewMailer(settingsStore, queries, deps.Logger))
	// 工单模块：签名密钥未配置时整体不启用（路由不挂载），不阻塞其余 Console 功能。
	var ticketService consoleticketapp.Service
	if secret := deps.Config.Ticket.AttachmentSecret; secret != "" {
		signer, signerErr := coreticket.NewAttachmentSigner(secret)
		if signerErr != nil {
			_ = tracerProvider.Shutdown(ctx)
			return nil, signerErr
		}
		// 用户创建/回复工单时写入 Admin 站内消息中心（顶栏铃铛提醒运营）。
		ticketService = consoleticket.NewService(deps.DB, queries, signer).
			WithAdminNotifier(adminmessage.NewService(queries), deps.Logger)
	} else {
		deps.Logger.Warn("console ticket module disabled: TICKET_ATTACHMENT_SECRET is not set")
	}
	handler, err := consoleapi.NewRouter(consoleapi.Deps{
		Logger:         deps.Logger,
		Config:         deps.Config.Console,
		AuthService:    authService,
		RequestService: consolerequests.NewService(queries),
		UsageService:   consoleusage.NewService(queries),
		APIKeyService:  consoleapikeys.NewService(queries),
		WalletService:  consolewallet.NewService(queries),
		CDKeyService:   cdkeyService,
		ModelsService:  publicmodels.NewService(queries),
		LabLogos:       queries,
		TicketService:  ticketService,
	})
	if err != nil {
		_ = tracerProvider.Shutdown(ctx)
		return nil, err
	}
	return &ConsoleServerApp{Handler: handler, tracer: tracerProvider}, nil
}
