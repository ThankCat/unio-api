package bootstrap

import (
	"context"
	"log/slog"
	"net/http"

	anthropicdeepseek "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/deepseek/messages"
	openaideepseek "github.com/ThankCat/unio-api/internal/core/adapter/openai/deepseek/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/adminauth"
	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/credential"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	capabilityadmin "github.com/ThankCat/unio-api/internal/service/admin/capability"
	"github.com/ThankCat/unio-api/internal/service/admin/channel"
	"github.com/ThankCat/unio-api/internal/service/admin/channelmodel"
	"github.com/ThankCat/unio-api/internal/service/admin/channelprice"
	"github.com/ThankCat/unio-api/internal/service/admin/customer"
	"github.com/ThankCat/unio-api/internal/service/admin/dashboard"
	"github.com/ThankCat/unio-api/internal/service/admin/model"
	modelcatalogadmin "github.com/ThankCat/unio-api/internal/service/admin/modelcatalog"
	"github.com/ThankCat/unio-api/internal/service/admin/provider"
	"github.com/ThankCat/unio-api/internal/service/admin/query"
	adminroute "github.com/ThankCat/unio-api/internal/service/admin/route"
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
	channelPriceService := channelprice.NewService(queries)
	routeService := adminroute.NewService(deps.DB, queries)

	// M6 只读查询台：请求记录 / 用量 / 账本，三个只读 service 共用同一 sqlc Queries。
	requestQueryService := query.NewRequestService(queries)
	usageQueryService := query.NewUsageService(queries)
	ledgerQueryService := query.NewLedgerService(queries)

	// M7 客户管理：用户/项目只读 + API Key 管理；手工调额经由 ledger 写 adjustment_* 流水。
	ledgerService := ledger.NewService(deps.DB, queries)
	userService := customer.NewUserService(queries)
	projectService := customer.NewProjectService(queries)
	apiKeyService := customer.NewAPIKeyService(queries)
	adjustmentService := customer.NewAdjustmentService(ledgerService)

	// M5 能力管理：能力数据 CRUD / models.dev 同步 / adapter 画像物化 / enforce 只读。
	// capability store 复用 core 层（写入前做 key 注册表 + 支持级别校验，渠道层只能减）。
	capabilityStore := capability.NewStore(queries)
	capabilityService := capabilityadmin.NewCapabilityService(capabilityStore)
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
	// enforce 只读：读 admin 自身进程的 env 快照 + observe 期判定分布。
	capabilityEnforcementService := capabilityadmin.NewEnforcementService(queries, deps.Config.Capability)

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
		ChannelService:      channelService,
		ModelService:        modelService,
		ChannelModelService: channelModelService,
		ChannelPriceService: channelPriceService,
		RouteService:        routeService,
		RequestQueryService: requestQueryService,
		UsageQueryService:   usageQueryService,
		LedgerQueryService:  ledgerQueryService,
		UserService:         userService,
		ProjectService:      projectService,
		APIKeyService:       apiKeyService,
		AdjustmentService:   adjustmentService,

		CapabilityService:            capabilityService,
		CapabilitySyncService:        capabilitySyncService,
		CapabilitySeedService:        capabilitySeedService,
		CapabilityEnforcementService: capabilityEnforcementService,

		CatalogService: modelCatalogAdminService,

		DashboardService: dashboardService,

		RecoveryJobQueryService:   recoveryJobQueryService,
		ChannelHealthQueryService: channelHealthQueryService,

		MetricsRecorder: metricsRecorder,
	})

	return &AdminServerApp{
		Handler: handler,
		tracer:  tracerProvider,
	}, nil
}
