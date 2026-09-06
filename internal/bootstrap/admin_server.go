package bootstrap

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	apichannel "github.com/ThankCat/unio-gateway/internal/app/adminapi/channel"
	adminticketapp "github.com/ThankCat/unio-gateway/internal/app/adminapi/ticket"
	anthropicdeepseek "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/deepseek/messages"
	openaideepseek "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/deepseek/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/core/capability"
	"github.com/ThankCat/unio-gateway/internal/core/fx"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/core/providerledger"
	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	coreticket "github.com/ThankCat/unio-gateway/internal/core/ticket"
	"github.com/ThankCat/unio-gateway/internal/platform/adminlogin"
	"github.com/ThankCat/unio-gateway/internal/platform/adminsession"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-gateway/internal/platform/proxyclient"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/adminmessage"
	capabilityadmin "github.com/ThankCat/unio-gateway/internal/service/admin/capability"
	admincdkey "github.com/ThankCat/unio-gateway/internal/service/admin/cdkey"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelcostmultiplier"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodelinventory"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelops"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelprice"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channeltest"
	"github.com/ThankCat/unio-gateway/internal/service/admin/customer"
	"github.com/ThankCat/unio-gateway/internal/service/admin/customerops"
	"github.com/ThankCat/unio-gateway/internal/service/admin/dashboard"
	"github.com/ThankCat/unio-gateway/internal/service/admin/emaillog"
	"github.com/ThankCat/unio-gateway/internal/service/admin/exchangerate"
	admingatewaylogging "github.com/ThankCat/unio-gateway/internal/service/admin/gatewaylogging"
	"github.com/ThankCat/unio-gateway/internal/service/admin/model"
	modelcatalogadmin "github.com/ThankCat/unio-gateway/internal/service/admin/modelcatalog"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelops"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelprice"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelrouting"
	"github.com/ThankCat/unio-gateway/internal/service/admin/provider"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerbalance"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerops"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerrechargerate"
	adminproxy "github.com/ThankCat/unio-gateway/internal/service/admin/proxy"
	"github.com/ThankCat/unio-gateway/internal/service/admin/query"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routingtrace"
	"github.com/ThankCat/unio-gateway/internal/service/admin/runtimediagnostics"
	subscriptionaccountadmin "github.com/ThankCat/unio-gateway/internal/service/admin/subscriptionaccount"
	adminticket "github.com/ThankCat/unio-gateway/internal/service/admin/ticket"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	emailsvc "github.com/ThankCat/unio-gateway/internal/service/email"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/readiness"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
	"github.com/ThankCat/unio-gateway/internal/service/subscription"
	subscriptionhealth "github.com/ThankCat/unio-gateway/internal/service/subscription/health"
	"github.com/redis/go-redis/v9"
)

// AdminServerAppDB 定义 admin server app 构建时需要的数据库能力。
// 既要 sqlc 查询能力，也要事务能力（M7 手工调额经由 ledger 需要 Begin）。
type AdminServerAppDB interface {
	sqlc.DBTX
	ledger.TxBeginner
}

// AdminServerAppDeps 表示构建 admin server app 需要的进程级依赖。
type AdminServerAppDeps struct {
	Logger *zap.Logger
	Config config.Config
	DB     AdminServerAppDB
	// Redis 供运行时配置中枢(app_settings 实时缓存)写透与读取;nil 时降级为 DB + 本地缓存。
	Redis redis.Cmdable
}

// AdminServerApp 表示当前 admin-server 进程已经装配完成的 HTTP 应用。
type AdminServerApp struct {
	Handler http.Handler

	tracer         *tracing.Provider
	stopReconciler context.CancelFunc
}

// Shutdown 释放 app 持有的可观测性资源（flush trace exporter）。
// 未启用 tracing 时为安全空操作。
func (a *AdminServerApp) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if a.stopReconciler != nil {
		a.stopReconciler()
	}

	return a.tracer.Shutdown(ctx)
}

// NewAdminServerApp 装配当前 admin-server 进程的业务应用。
//
// 启动期校验：ADMIN_PASSWORD 不能为空（缺失即失败，避免以空口令开放登录入口）。
// 渠道上游凭据已改为明文存储（产品决策），admin 不再需要 master key / cipher。
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

	// Admin DTO 不承载模型上下文，使用独立且更小的进程级 ingress 上限。
	httpx.SetMaxJSONBodyBytes(deps.Config.HTTP.AdminMaxJSONBodyBytes)
	httpx.SetResponseWriteTimeout(deps.Config.HTTP.WriteTimeout)

	// 会话存储：登录后签发的随机 token 存于 Redis，可吊销、到期自动失效。
	sessions := adminsession.NewStore(deps.Redis, deps.Config.Redis.KeyNamespace, deps.Config.Admin.SessionTTL)
	loginAttemptLimiter := adminlogin.NewLimiter(
		deps.Redis,
		deps.Config.Redis.KeyNamespace,
		deps.Config.Admin.LoginSourceFailureLimit,
		deps.Config.Admin.LoginAccountFailureLimit,
		deps.Config.Admin.LoginFailureWindow,
	)
	// 登录限速的来源维度：只信任 ADMIN_TRUSTED_PROXY_CIDRS 内的对端回溯 X-Forwarded-For，
	// 否则按 RemoteAddr 分桶；不配置时行为与直连部署一致。
	loginSourceResolver, err := httpx.NewTrustedClientIPResolver(deps.Config.Admin.TrustedProxyCIDRs)
	if err != nil {
		return nil, failure.Wrap(failure.CodeConfigInvalid, err, failure.WithMessage("parse ADMIN_TRUSTED_PROXY_CIDRS"))
	}

	authenticator, err := adminauth.NewSessionAuthenticator(sessions)
	if err != nil {
		return nil, err
	}

	// 登录入口的用户名口令认证器；任一项未配置即启动失败，不以空口令对外开放登录。
	credentialAuthenticator, err := adminauth.NewStaticCredentialAuthenticator(
		deps.Config.Admin.Username,
		deps.Config.Admin.Password,
	)
	if err != nil {
		return nil, err
	}

	queries := sqlc.New(deps.DB)
	metricsRecorder := metrics.New()
	runtimeTelemetry := newRuntimeControlTelemetry(metricsRecorder, deps.Logger)

	// 运行时配置中枢:与 gateway 同一注册表;启动 seed 把默认值写入 DB 缺行(DO NOTHING,幂等,
	// 与 gateway 并发启动安全)。构造提前到各运维 service 之前——admin_backend 域(渠道健康分桶
	// 阈值)由 channelops/providerops/dashboard/channelhealth 四个 service 每请求现读。
	settingsStore := appsettings.NewSettingsStore(
		queries, deps.Redis, deps.Config.Redis.KeyNamespace, appsettings.DefaultRegistry(), deps.Logger,
	)
	_ = settingsStore.SeedDefaults(ctx)

	// adapter registry 依赖 settings store：Codex 出站身份的版本来源随设置热更新（渠道检测、模型发现、
	// 账号导入换码都以同一身份出站）。
	codexVersion := codexVersionSource(settingsStore)
	adapterRegistry, err := NewAdapterRegistry(http.DefaultClient, deps.Logger, codexVersion)
	if err != nil {
		return nil, err
	}

	providerService := provider.NewService(queries)
	providerOpsService := providerops.NewService(queries)
	providerLedgerService := providerledger.NewService(deps.DB, queries).WithFxRates(fx.NewService(queries, 0))
	providerBalanceService := providerbalance.NewService(queries, providerLedgerService)
	adminMessageService := adminmessage.NewService(queries)
	exchangeRateService := exchangerate.NewService(queries, settingsStore, deps.Config.ExchangeRate.APIKey)
	// 工单模块：签名密钥未配置时整体不启用（路由不挂载），不阻塞其余 Admin 功能。
	var adminTicketService adminticketapp.TicketService
	if secret := deps.Config.Ticket.AttachmentSecret; secret != "" {
		signer, signerErr := coreticket.NewAttachmentSigner(secret)
		if signerErr != nil {
			return nil, signerErr
		}
		adminTicketService = adminticket.NewService(deps.DB, queries, signer)
	} else {
		deps.Logger.Warn("admin ticket module disabled: TICKET_ATTACHMENT_SECRET is not set")
	}
	var providerBreakerRuntime *breakerstore.Store
	var channelBreakerRuntime apichannel.BreakerRuntime
	var settingsRuntimePublisher appsettings.RuntimeControlPublisher
	var settingsRuntimeStore appsettings.RuntimeControlStore
	var channelRuntimePublisher channel.RuntimeControlPublisher
	var channelRuntimeStore channel.CapacityControlStore
	var sharedBreakerStore *breakerstore.Store
	var runtimeReconcilerCancel context.CancelFunc
	if deps.Redis != nil {
		breakerStore := breakerstore.NewStore(deps.Redis, deps.Config.Redis.KeyNamespace, metricsRecorder)
		sharedBreakerStore = breakerStore
		if pool, ok := deps.DB.(*pgxpool.Pool); ok {
			if err := reconcileAllRuntimeControls(
				ctx, pool, settingsStore, breakerStore, runtimeTelemetry, runtimeControlStartupAuthority,
			); err != nil {
				return nil, err
			}
			reconcileCtx, cancel := context.WithCancel(context.Background())
			runtimeReconcilerCancel = cancel
			go runRuntimeControlReconciler(reconcileCtx, pool, settingsStore, breakerStore, runtimeTelemetry)
		}
		providerBreakerRuntime = breakerStore
		channelBreakerRuntime = breakerStore
		if pool, ok := deps.DB.(*pgxpool.Pool); ok {
			publisher := runtimecontrol.NewPublisher(pool, breakerStore)
			settingsRuntimePublisher = publisher
			settingsRuntimeStore = breakerStore
			channelRuntimePublisher = publisher
			channelRuntimeStore = breakerStore
		}
	}
	if pool, ok := deps.DB.(*pgxpool.Pool); ok && sharedBreakerStore != nil {
		providerService.WithRuntimeControl(
			provider.NewFencer(runtimecontrol.NewProviderFencePublisher(pool), sharedBreakerStore),
			sharedBreakerStore,
		)
	}
	// 全局账号用量暂停阈值（三层继承的最外层）与阈值变更后的运行态重算器：
	// 全局 setting / 渠道阈值 / 账号阈值任一改动，都按账号最近快照重写 Redis 暂停标记（展示缓存）；
	// 拦截本身由 gateway 按快照实时判定，不依赖这次重算。Redis 不可用时不重算，只影响展示。
	usagePauseThreshold := func(ctx context.Context) int32 {
		return appsettings.GatewayAccountUsagePauseThreshold(ctx, settingsStore)
	}
	var usagePauseReconciler *subscriptionhealth.Reconciler
	if sharedBreakerStore != nil {
		usagePauseReconciler = subscriptionhealth.NewReconciler(queries, sharedBreakerStore, usagePauseThreshold, deps.Logger)
	}

	channelService := channel.NewService(queries, adapterRegistry)
	channelService.WithSupplyLinkage(deps.DB, queries)
	if channelRuntimePublisher != nil && channelRuntimeStore != nil {
		channelService.WithRuntimeControl(channelRuntimePublisher, channelRuntimeStore)
	}
	if usagePauseReconciler != nil {
		channelService.WithUsagePauseReconciler(usagePauseReconciler)
	}
	// 渠道检测复用 gateway adapter registry（同一份 adapter/HTTP 链路，检测结果=真实行为）。
	// 探测超时取自运行时配置 admin_backend.channel_test（与用户请求渠道超时正交）。
	channelTestService := channeltest.NewService(queries, adapterRegistry, settingsStore, providerLedgerService)
	channelTestService.SetMetrics(metricsRecorder)
	channelService.WithCredentialRotator(channelTestService)
	channelOpsService := channelops.NewService(queries)
	modelService := model.NewService(queries, deps.DB, queries)
	modelOpsService := modelops.NewService(queries)
	channelModelService := channelmodel.NewService(queries, deps.DB, queries)
	channelModelInventoryService := channelmodelinventory.NewService(
		deps.DB, queries, adapterRegistry, adapterRegistry, providerLedgerService, settingsStore,
	)
	channelPriceService := channelprice.NewService(queries)
	modelPriceService := modelprice.NewService(queries, deps.DB, queries)
	// DEC-027 渠道价格倍率 + 服务商充值汇率，均复用同一 sqlc Queries。
	channelCostMultiplierService := channelcostmultiplier.NewService(queries)
	providerRechargeRateService := providerrechargerate.NewService(queries)

	// 订阅账号号池管理（第九节）：账号写入经渠道容量 control 两阶段发布传播到运行态围栏；
	// 出站（换码/刷新）与 gateway 同一按账号代理与令牌端点实现。
	var subscriptionAccountService *subscriptionaccountadmin.Service
	if pool, ok := deps.DB.(*pgxpool.Pool); ok && sharedBreakerStore != nil {
		accountProxyClients := proxyclient.NewResolver(upstreamHTTPClient(nil))
		accountTokens := subscription.NewTokenClient(accountProxyClients.ClientFor, codexVersion)
		accountOutbound := subscription.NewOutbound(queries, accountTokens, sharedBreakerStore, deps.Redis, deps.Logger)
		subscriptionAccountService = subscriptionaccountadmin.NewService(
			queries,
			sharedBreakerStore,
			runtimecontrol.NewPublisher(pool, sharedBreakerStore),
			sharedBreakerStore,
			accountOutbound,
			accountTokens,
			deps.Logger,
		).WithSupplyPreview(queries).
			WithUsagePausePolicy(usagePauseThreshold, usagePauseReconciler)
		// 池型渠道的检测/模型发现/验证以账号身份出站；不注入的话这些操作对池型渠道必然 401。
		probeIdentity := subscription.NewProbeIdentityResolver(queries, accountOutbound)
		channelTestService.WithAccountResolver(probeIdentity)
		channelModelInventoryService.WithAccountResolver(probeIdentity)
		// 探测/验证成功即回填账号观测（用量水位 + LRU）：与请求路径同一 Recorder，阈值同样热更新。
		// 探测 429 另写账号冷却（同一 Redis），否则检测已确认限流、列表仍显示「启用 · 正常」。
		probeHealth := subscriptionhealth.NewRecorder(queries, sharedBreakerStore, deps.Logger, 0).
			WithThresholdProvider(usagePauseThreshold)
		channelTestService.WithAccountHealth(probeHealth)
		channelTestService.WithAccountRuntime(sharedBreakerStore)
		channelModelInventoryService.WithAccountHealth(probeHealth)
	}
	proxyAdminService := adminproxy.NewService(queries)
	routingTraceService := routingtrace.NewService(queries)

	// M6 只读查询台：请求记录 / 账本，只读 service 共用同一 sqlc Queries。
	requestQueryService := query.NewRequestService(queries)
	ledgerQueryService := query.NewLedgerService(queries)

	// M7 客户管理：用户/项目只读 + API Key 管理；手工调额经由 ledger 写 adjustment_* 流水。
	ledgerService := ledger.NewService(deps.DB, queries)
	cdkeyService := admincdkey.NewService(deps.DB, queries)
	userService := customer.NewUserService(queries)
	apiKeyService := customer.NewAPIKeyService(queries)
	adjustmentService := customer.NewAdjustmentService(ledgerService)
	customerOpsService := customerops.NewService(queries)

	// M5 能力管理：能力数据 CRUD / models.dev 同步 / adapter 画像物化 / enforce 只读。
	// capability store 复用 core 层（写入前做 key 注册表 + 支持级别校验，渠道层只能减）。
	capabilityStore := capability.NewStore(queries)
	capabilityService := capabilityadmin.NewCapabilityService(capabilityStore, deps.DB, queries)
	// Syncer 与 worker-server 的 sync-models 子命令同构；admin 内联触发（支持 dry-run）。
	modelCatalogSyncer := NewModelCatalogSyncer(deps.Config.ModelCatalogSync, deps.DB)
	capabilitySyncService := capabilityadmin.NewSyncService(modelCatalogSyncer, capabilityStore)
	// adapter 画像注册表在装配期组装（目前仅 DeepSeek 的 openai/anthropic 两协议），避免 core 耦合 adapter。
	capabilitySeedService := capabilityadmin.NewSeedService(capabilityStore, []capability.AdapterProfile{
		openaideepseek.CapabilityProfile(),
		anthropicdeepseek.CapabilityProfile(),
	})
	// 阶段 14 模型目录：浏览 models.dev 目录 + 从目录采纳/刷新/更新提醒（采纳/刷新需事务，复用 deps.DB）。
	modelCatalogAdminService := modelcatalogadmin.NewService(deps.DB, queries)
	channelModelInventoryService.WithCatalogAdopter(modelCatalogAdminService)

	// M9 工作台看板：复用同一 sqlc Queries 做只读聚合（KPI 概览 + 时间序列）。
	dashboardService := dashboard.NewService(queries)

	// 选路观测：候选排序与流量分布视图（模型侧观测口径，ADR-0020）。
	// Redis 不可用时仍然构造：统计类接口只读 PG，实时视图会如实报告运行态不可用。
	var modelRoutingService *modelrouting.Service
	if sharedBreakerStore != nil {
		modelRoutingService = modelrouting.NewService(
			queries, runtimefacts.NewReader(queries), sharedBreakerStore, sharedBreakerStore,
		)
	} else {
		modelRoutingService = modelrouting.NewService(queries, nil, nil, nil)
	}

	// M8 系统/任务/健康：结算补偿任务只读视图，复用同一 sqlc Queries。
	recoveryJobQueryService := query.NewRecoveryService(queries)
	var runtimeDiagnosticsService *runtimediagnostics.Service
	if sharedBreakerStore != nil {
		runtimeDiagnosticsService = runtimediagnostics.NewService(
			queries, readiness.NewChecker(queries, sharedBreakerStore), sharedBreakerStore,
		)
	}

	adminhttp.SetRoutingMarginMetrics(metricsRecorder)
	providerSettingsService := appsettings.NewService(settingsStore)
	if settingsRuntimePublisher != nil && settingsRuntimeStore != nil {
		providerSettingsService = appsettings.NewServiceWithRuntimeControl(
			settingsStore, settingsRuntimePublisher, settingsRuntimeStore,
		)
	}
	if usagePauseReconciler != nil {
		// 全局阈值保存后立即按新阈值重算全部启用中的池内账号（本进程 Set 已写本地缓存，钩子读到的即新值）。
		providerSettingsService.WithWriteHook(appsettings.GatewayAccountUsagePauseThresholdKey, func(ctx context.Context) (any, error) {
			return usagePauseReconciler.ReconcileAll(ctx)
		})
	}
	gatewayLoggingService := admingatewaylogging.NewService(
		providerSettingsService,
		http.DefaultClient,
		deps.Config.Admin.GatewayInternalURLs,
		deps.Config.Admin.GatewayInternalToken,
		deps.Config.Admin.LokiURL,
	)

	handler := NewAdminHTTPHandler(adminHTTPDeps{
		Logger:                  deps.Logger,
		Authenticator:           authenticator,
		CredentialAuthenticator: credentialAuthenticator,
		LoginAttemptLimiter:     loginAttemptLimiter,
		LoginSourceResolver:     loginSourceResolver,
		Sessions:                sessions,
		SessionTTLSeconds:       int64(deps.Config.Admin.SessionTTL.Seconds()),

		ProviderService:              providerService,
		ProviderOpsService:           providerOpsService,
		ProviderBalanceService:       providerBalanceService,
		ProviderBreaker:              providerBreakerRuntime,
		ChannelService:               channelService,
		ChannelBreaker:               channelBreakerRuntime,
		ChannelTestService:           channelTestService,
		ChannelOpsService:            channelOpsService,
		ModelService:                 modelService,
		ModelOpsService:              modelOpsService,
		ModelRoutingService:          modelRoutingService,
		ChannelModelService:          channelModelService,
		ChannelModelInventoryService: channelModelInventoryService,
		ChannelPriceService:          channelPriceService,
		ModelPriceService:            modelPriceService,

		ChannelCostMultiplierService: channelCostMultiplierService,
		ProviderRechargeRateService:  providerRechargeRateService,
		ProxyService:                 proxyAdminService,
		SubscriptionAccountService:   subscriptionAccountService,

		RoutingTraceService: routingTraceService,
		RequestQueryService: requestQueryService,
		LedgerQueryService:  ledgerQueryService,
		CDKeyService:        cdkeyService,

		UserService:        userService,
		APIKeyService:      apiKeyService,
		AdjustmentService:  adjustmentService,
		CustomerOpsService: customerOpsService,

		CapabilityService:     capabilityService,
		CapabilitySyncService: capabilitySyncService,
		CapabilitySeedService: capabilitySeedService,

		CatalogService: modelCatalogAdminService,
		LabLogos:       queries,

		DashboardService:   dashboardService,
		LiveTrafficService: modelRoutingService,

		RecoveryJobQueryService:   recoveryJobQueryService,
		RuntimeDiagnosticsService: runtimeDiagnosticsService,
		GatewayLoggingService:     gatewayLoggingService,
		GatewayInternalToken:      deps.Config.Admin.GatewayInternalToken,
		MessageService:            adminMessageService,
		EmailLogService:           emaillog.NewService(queries),
		EmailSMTPService:          providerSettingsService,
		EmailTestMailer:           emailsvc.NewMailer(settingsStore, queries, deps.Logger),
		TicketService:             adminTicketService,
		ExchangeRateService:       exchangeRateService,
		ProviderSettingsService:   providerSettingsService,

		GatewayConfig: deps.Config.Gateway,
		WorkerConfig:  deps.Config.Worker,
		HTTPConfig:    deps.Config.HTTP,

		MetricsRecorder: metricsRecorder,
	})

	return &AdminServerApp{
		Handler:        handler,
		tracer:         tracerProvider,
		stopReconciler: runtimeReconcilerCancel,
	}, nil
}
