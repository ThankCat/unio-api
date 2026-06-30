package bootstrap

import (
	"context"
	"log/slog"
	"net/http"

	anthropicdeepseek "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/deepseek/messages"
	openaideepseek "github.com/ThankCat/unio-api/internal/core/adapter/openai/deepseek/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/adminauth"
	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	capabilityadmin "github.com/ThankCat/unio-api/internal/service/admin/capability"
	"github.com/ThankCat/unio-api/internal/service/admin/channel"
	"github.com/ThankCat/unio-api/internal/service/admin/channelmodel"
	"github.com/ThankCat/unio-api/internal/service/admin/channelops"
	"github.com/ThankCat/unio-api/internal/service/admin/channelprice"
	"github.com/ThankCat/unio-api/internal/service/admin/customer"
	"github.com/ThankCat/unio-api/internal/service/admin/customerops"
	"github.com/ThankCat/unio-api/internal/service/admin/dashboard"
	"github.com/ThankCat/unio-api/internal/service/admin/model"
	modelcatalogadmin "github.com/ThankCat/unio-api/internal/service/admin/modelcatalog"
	"github.com/ThankCat/unio-api/internal/service/admin/modelops"
	"github.com/ThankCat/unio-api/internal/service/admin/modelprice"
	"github.com/ThankCat/unio-api/internal/service/admin/provider"
	"github.com/ThankCat/unio-api/internal/service/admin/providerops"
	"github.com/ThankCat/unio-api/internal/service/admin/query"
	adminroute "github.com/ThankCat/unio-api/internal/service/admin/route"
	"github.com/ThankCat/unio-api/internal/service/admin/routeops"
)

// AdminServerAppDB 定义 admin server app 构建时需要的数据库能力。
// 既要 sqlc 查询能力，也要事务能力（M7 手工调额经由 ledger 需要 Begin）。
type AdminServerAppDB interface {
	sqlc.DBTX
	ledger.TxBeginner
}

// AdminServerAppDeps 表示构建 admin server app 需要的进程级依赖。
type AdminServerAppDeps struct {
	Logger *slog.Logger
	Config config.Config
	DB     AdminServerAppDB
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

	providerService := provider.NewService(queries)
	providerOpsService := providerops.NewService(queries)
	channelService := channel.NewService(queries, adapterRegistry)
	channelOpsService := channelops.NewService(queries)
	modelService := model.NewService(queries)
	modelOpsService := modelops.NewService(queries)
	channelModelService := channelmodel.NewService(queries)
	channelPriceService := channelprice.NewService(queries)
	modelPriceService := modelprice.NewService(queries)
	routeService := adminroute.NewService(deps.DB, queries)
	routeOpsService := routeops.NewService(queries)

	// M6 只读查询台：请求记录 / 用量 / 账本，三个只读 service 共用同一 sqlc Queries。
	requestQueryService := query.NewRequestService(queries)
	usageQueryService := query.NewUsageService(queries)
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

	// M8 系统/任务/健康：结算补偿任务只读视图 + 系统级 channel 健康（派生），复用同一 sqlc Queries。
	recoveryJobQueryService := query.NewRecoveryService(queries)
	channelHealthQueryService := query.NewChannelHealthService(queries)

	metricsRecorder := metrics.New()

	handler := NewAdminHTTPHandler(adminHTTPDeps{
		Logger:              deps.Logger,
		Authenticator:       authenticator,
		ProviderService:     providerService,
		ProviderOpsService:  providerOpsService,
		ChannelService:      channelService,
		ChannelOpsService:   channelOpsService,
		ModelService:        modelService,
		ModelOpsService:     modelOpsService,
		ChannelModelService: channelModelService,
		ChannelPriceService: channelPriceService,
		ModelPriceService:   modelPriceService,
		RouteService:        routeService,
		RouteOpsService:     routeOpsService,
		RequestQueryService: requestQueryService,
		UsageQueryService:   usageQueryService,
		LedgerQueryService:  ledgerQueryService,
		UserService:         userService,
		APIKeyService:       apiKeyService,
		AdjustmentService:   adjustmentService,
		CustomerOpsService:  customerOpsService,

		CapabilityService:     capabilityService,
		CapabilitySyncService: capabilitySyncService,
		CapabilitySeedService: capabilitySeedService,

		CatalogService: modelCatalogAdminService,

		DashboardService: dashboardService,

		RecoveryJobQueryService:   recoveryJobQueryService,
		ChannelHealthQueryService: channelHealthQueryService,

		GatewayConfig:        deps.Config.Gateway,
		RateLimitConfig:      deps.Config.RateLimit,
		CircuitBreakerConfig: deps.Config.CircuitBreaker,
		WorkerConfig:         deps.Config.Worker,
		HTTPConfig:           deps.Config.HTTP,

		MetricsRecorder: metricsRecorder,
	})

	return &AdminServerApp{
		Handler: handler,
		tracer:  tracerProvider,
	}, nil
}
