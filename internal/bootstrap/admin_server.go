package bootstrap

import (
	"context"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	apichannel "github.com/ThankCat/unio-gateway/internal/app/adminapi/channel"
	apiproviderendpoint "github.com/ThankCat/unio-gateway/internal/app/adminapi/providerendpoint"
	anthropicdeepseek "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/deepseek/messages"
	openaideepseek "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/deepseek/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/adminauth"
	"github.com/ThankCat/unio-gateway/internal/core/capability"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	capabilityadmin "github.com/ThankCat/unio-gateway/internal/service/admin/capability"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelcostmultiplier"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelops"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelprice"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelrechargefactor"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channeltest"
	"github.com/ThankCat/unio-gateway/internal/service/admin/customer"
	"github.com/ThankCat/unio-gateway/internal/service/admin/customerops"
	"github.com/ThankCat/unio-gateway/internal/service/admin/dashboard"
	"github.com/ThankCat/unio-gateway/internal/service/admin/model"
	modelcatalogadmin "github.com/ThankCat/unio-gateway/internal/service/admin/modelcatalog"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelops"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelprice"
	"github.com/ThankCat/unio-gateway/internal/service/admin/provider"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerendpoint"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerops"
	"github.com/ThankCat/unio-gateway/internal/service/admin/query"
	adminroute "github.com/ThankCat/unio-gateway/internal/service/admin/route"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routeops"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routeruntime"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routingtrace"
	"github.com/ThankCat/unio-gateway/internal/service/admin/runtimediagnostics"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/readiness"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
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
// 启动期校验：ADMIN_API_TOKEN 不能为空（缺失即失败，避免 admin 表面带病启动）。
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

	// JSON 请求体上限为进程级 ingress 安全配置；admin 表面与 gateway 共用同一全局限制（启动期设置一次）。
	httpx.SetMaxJSONBodyBytes(deps.Config.HTTP.MaxJSONBodyBytes)

	authenticator, err := adminauth.NewStaticTokenAuthenticator(deps.Config.Admin.APIToken)
	if err != nil {
		return nil, err
	}

	adapterRegistry, err := NewAdapterRegistry(http.DefaultClient, deps.Logger)
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

	providerService := provider.NewService(queries)
	providerOpsService := providerops.NewService(queries)
	// P4 §8.1：ProviderEndpoint CRUD。create 需初始化可恢复的 Redis Endpoint control（§4.2.18）；
	// Redis 缺失时 control 为 nil，create 跳过初始化（Endpoint 在 control 恢复前不可被准入，fail-closed）。
	var endpointControl providerendpoint.ControlInitializer
	var endpointFencer *providerendpoint.EndpointFencer
	var endpointBreakerRuntime apiproviderendpoint.BreakerRuntime
	var channelBreakerRuntime apichannel.BreakerRuntime
	var settingsRuntimePublisher appsettings.RuntimeControlPublisher
	var settingsRuntimeStore appsettings.RuntimeControlStore
	var channelRuntimePublisher channel.RuntimeControlPublisher
	var channelRuntimeStore channel.AdmissionControlStore
	var sharedBreakerStore *breakerstore.Store
	var runtimeReconcilerCancel context.CancelFunc
	if deps.Redis != nil {
		breakerStore := breakerstore.NewStore(deps.Redis, deps.Config.Redis.KeyNamespace, metricsRecorder)
		sharedBreakerStore = breakerStore
		if pool, ok := deps.DB.(*pgxpool.Pool); ok {
			if err := reconcileAllRuntimeControls(
				ctx, pool, settingsStore, breakerStore, runtimeTelemetry,
			); err != nil {
				return nil, err
			}
			reconcileCtx, cancel := context.WithCancel(context.Background())
			runtimeReconcilerCancel = cancel
			go runRuntimeControlReconciler(reconcileCtx, pool, settingsStore, breakerStore, runtimeTelemetry)
		}
		endpointControl = breakerStore
		endpointBreakerRuntime = breakerStore
		channelBreakerRuntime = breakerStore
		// status/base_url 热更新走可恢复围栏（endpoint_routing_operations + Redis fence）；需要 DB pool。
		if pool, ok := deps.DB.(*pgxpool.Pool); ok {
			endpointFencer = providerendpoint.NewEndpointFencer(
				runtimecontrol.NewEndpointFencePublisher(pool), breakerStore,
			)
			publisher := runtimecontrol.NewPublisher(pool, breakerStore)
			settingsRuntimePublisher = publisher
			settingsRuntimeStore = breakerStore
			channelRuntimePublisher = publisher
			channelRuntimeStore = breakerStore
		}
	}
	providerEndpointService := providerendpoint.NewService(queries, endpointControl)
	if pool, ok := deps.DB.(*pgxpool.Pool); ok {
		providerEndpointService.WithTransactionalDB(pool)
	}
	if endpointFencer != nil {
		providerEndpointService = providerEndpointService.WithFencer(endpointFencer)
		if pool, ok := deps.DB.(*pgxpool.Pool); ok && sharedBreakerStore != nil {
			providerService.WithStatusFencer(
				provider.NewStatusFencer(runtimecontrol.NewEndpointFencePublisher(pool), sharedBreakerStore),
				func(ctx context.Context) int {
					return appsettings.GatewayCircuitBreaker(ctx, settingsStore).EndpointStatusBatchMax
				},
			)
		}
	}
	channelService := channel.NewService(queries, adapterRegistry)
	if channelRuntimePublisher != nil && channelRuntimeStore != nil {
		channelService.WithRuntimeControl(channelRuntimePublisher, channelRuntimeStore)
	}
	// 渠道检测复用 gateway adapter registry（同一份 adapter/HTTP 链路，检测结果=真实行为）。
	// 探测超时取自运行时配置 admin_backend.channel_test（与用户请求渠道超时正交）。
	channelTestService := channeltest.NewService(queries, adapterRegistry, settingsStore)
	channelTestService.SetMetrics(metricsRecorder)
	channelService.WithCredentialRotator(channelTestService)
	channelOpsService := channelops.NewService(queries)
	modelService := model.NewService(queries)
	modelOpsService := modelops.NewService(queries)
	channelModelService := channelmodel.NewService(queries)
	channelPriceService := channelprice.NewService(queries)
	modelPriceService := modelprice.NewService(queries)
	// DEC-027 渠道成本倍率：渠道价格倍率 / 渠道充值倍率，均复用同一 sqlc Queries。
	channelCostMultiplierService := channelcostmultiplier.NewService(queries)
	channelRechargeFactorService := channelrechargefactor.NewService(queries)
	routeService := adminroute.NewService(deps.DB, queries)
	routeOpsService := routeops.NewService(queries)
	routingTraceService := routingtrace.NewService(queries)
	routeRuntimeService := routeruntime.NewService(queries, runtimefacts.NewReader(queries), sharedBreakerStore)

	// M6 只读查询台：请求记录 / 账本，只读 service 共用同一 sqlc Queries。
	requestQueryService := query.NewRequestService(queries)
	costExposureQueryService := query.NewCostExposureService(queries)
	ledgerQueryService := query.NewLedgerService(queries)

	// M7 客户管理：用户/项目只读 + API Key 管理；手工调额经由 ledger 写 adjustment_* 流水。
	ledgerService := ledger.NewService(deps.DB, queries)
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

	// M9 工作台看板：复用同一 sqlc Queries 做只读聚合（KPI 概览 + 时间序列）。
	dashboardService := dashboard.NewService(queries)

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

	handler := NewAdminHTTPHandler(adminHTTPDeps{
		Logger:                  deps.Logger,
		Authenticator:           authenticator,
		ProviderService:         providerService,
		ProviderOpsService:      providerOpsService,
		ProviderEndpointService: providerEndpointService,
		ProviderEndpointBreaker: endpointBreakerRuntime,
		ChannelService:          channelService,
		ChannelBreaker:          channelBreakerRuntime,
		ChannelTestService:      channelTestService,
		ChannelOpsService:       channelOpsService,
		ModelService:            modelService,
		ModelOpsService:         modelOpsService,
		ChannelModelService:     channelModelService,
		ChannelPriceService:     channelPriceService,
		ModelPriceService:       modelPriceService,

		ChannelCostMultiplierService: channelCostMultiplierService,
		ChannelRechargeFactorService: channelRechargeFactorService,

		RouteService:        routeService,
		RouteOpsService:     routeOpsService,
		RoutingTraceService: routingTraceService,
		RouteRuntimeService: routeRuntimeService,
		RequestQueryService: requestQueryService,
		LedgerQueryService:  ledgerQueryService,

		CostExposureQueryService: costExposureQueryService,
		UserService:              userService,
		APIKeyService:            apiKeyService,
		AdjustmentService:        adjustmentService,
		CustomerOpsService:       customerOpsService,

		CapabilityService:     capabilityService,
		CapabilitySyncService: capabilitySyncService,
		CapabilitySeedService: capabilitySeedService,

		CatalogService: modelCatalogAdminService,

		DashboardService: dashboardService,

		RecoveryJobQueryService:   recoveryJobQueryService,
		RuntimeDiagnosticsService: runtimeDiagnosticsService,
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
