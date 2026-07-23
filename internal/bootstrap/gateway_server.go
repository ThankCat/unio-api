package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/gatewayapi"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/core/tokenest"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-gateway/internal/platform/stickysession"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/readiness"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
	"github.com/redis/go-redis/v9"
)

// GatewayServerAppDB 定义 gateway server app 构建时需要的数据库能力。
type GatewayServerAppDB interface {
	sqlc.DBTX
	lifecycle.ChatTxBeginner
}

// GatewayServerAppDeps 表示构建 gateway server app 需要的进程级依赖。
type GatewayServerAppDeps struct {
	Logger *zap.Logger
	Config config.Config
	DB     GatewayServerAppDB
	Redis  redis.Cmdable
}

// GatewayServerApp 表示当前 gateway-server 进程已经装配完成的 HTTP 应用。
type GatewayServerApp struct {
	Handler http.Handler

	tracer      *tracing.Provider
	stopApplier context.CancelFunc
}

// Shutdown 停止后台配置 applier/runtime-control reconciler，并释放可观测性资源。
// 未启用 tracing 时为安全空操作。
func (a *GatewayServerApp) Shutdown(ctx context.Context) error {
	if a == nil {
		return nil
	}
	if a.stopApplier != nil {
		a.stopApplier()
	}

	return a.tracer.Shutdown(ctx)
}

// NewGatewayServerApp 装配当前 gateway-server 进程的业务应用。
func NewGatewayServerApp(ctx context.Context, deps GatewayServerAppDeps) (*GatewayServerApp, error) {
	if deps.Redis == nil {
		return nil, errors.New("gateway-server: redis is required")
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

	// P2-6：生产构建不读结算故障注入 env；若运维误设则启动期显式告警（构建标签 billing_e2e 时为激活提示）。
	lifecycle.WarnIfSettlementFaultInjectionConfigured(deps.Logger)

	// JSON 请求体上限为进程级网关 ingress 安全配置（防 OOM / zip bomb）；启动期设置一次，全 DecodeJSON 生效。
	httpx.SetMaxJSONBodyBytes(deps.Config.HTTP.MaxJSONBodyBytes)

	// 非流式上游响应体上限为进程级 egress 安全配置（防 OOM）；启动期设置一次，全 adapter 非流式读 body 生效。
	adapter.SetMaxUpstreamResponseBytes(deps.Config.Gateway.MaxUpstreamResponseBytes)

	// 输入 token 估算的媒体处理配置（图片 tile 数学 / 是否抓取远程图片）；启动期设置一次，全 tokenizer 生效。
	tokenest.Configure(tokenest.Options{
		CountMedia:        deps.Config.TokenEstimate.CountMedia,
		FetchRemoteImages: deps.Config.TokenEstimate.FetchRemoteImages,
		FetchTimeout:      deps.Config.TokenEstimate.FetchTimeout,
		FetchMaxBytes:     deps.Config.TokenEstimate.FetchMaxBytes,
	})

	// 无真实 usage 的流式 partial 结算的假定缓存率（临时口径）；启动期设置一次，全 partial 结算生效。
	lifecycle.SetPartialAssumedCacheReadRatio(deps.Config.Gateway.PartialAssumedCacheReadRatio)

	queries := sqlc.New(deps.DB)
	metricsRecorder := metrics.New()
	runtimeTelemetry := newRuntimeControlTelemetry(metricsRecorder, deps.Logger)

	// P4-D19 / §5.5：Redis 全局 breaker 是上游准入的必需基础设施；启动期校验部署形态与可达性。
	// 检测到 Redis Cluster 时拒绝启动（多 key 原子 Lua 不能拆步降级）；Redis 不可达时启动失败（fail-closed）。
	var requestAdmissionManager *requestadmission.Manager
	var attemptPermitManager *lifecycle.AttemptPermitManager
	var sharedBreakerStore *breakerstore.Store
	var runtimeReadinessChecker *readiness.Checker
	breakerStore := breakerstore.NewStore(deps.Redis, deps.Config.Redis.KeyNamespace, metricsRecorder)
	sharedBreakerStore = breakerStore
	if err := breakerStore.VerifySingleNodeDeployment(ctx); err != nil {
		metricsRecorder.SetBreakerStoreHealth(false, true)
		if deps.Logger != nil {
			deps.Logger.Error("breaker store deployment verification failed", zap.Error(err))
		}
		return nil, err
	}
	if err := breakerStore.Ping(ctx); err != nil {
		metricsRecorder.SetBreakerStoreHealth(false, true)
		if deps.Logger != nil {
			deps.Logger.Error("breaker store startup ping failed", zap.Error(err))
		}
		return nil, err
	}
	epochResult, err := runtimecontrol.EnsureStateEpochSeed(
		ctx,
		runtimecontrol.NewStateEpochCoordinator(deps.DB, breakerStore),
	)
	if err != nil {
		metricsRecorder.SetBreakerStoreHealth(false, true)
		metricsRecorder.SetRuntimeStateIntegrity("lost")
		if deps.Logger != nil {
			deps.Logger.Error("runtime state epoch ensure failed", zap.Error(err))
		}
		return nil, err
	}
	observeRuntimeStateEpochEnsure(metricsRecorder, deps.Logger, epochResult)
	runtimeReadinessChecker = readiness.NewCheckerWithObservability(
		queries, breakerStore, deps.Logger, metricsRecorder,
	)
	runtimeFactsReader := runtimefacts.NewReader(queries)
	requestAdmissionManager = requestadmission.NewManager(
		breakerStore,
		runtimeFactsReader,
		requestadmission.ManagerOptions{Logger: deps.Logger, Metrics: metricsRecorder},
	)
	attemptPermitManager = lifecycle.NewAttemptPermitManager(
		breakerStore,
		runtimeFactsReader,
		lifecycle.AttemptPermitManagerOptions{Logger: deps.Logger, Metrics: metricsRecorder},
	)

	// 运行时配置中枢（app_settings + Redis 实时缓存 + 本地短缓存）：admin 改动经 Redis 跨进程秒级生效、无需重启。
	settingsStore := appsettings.NewSettingsStore(
		queries, deps.Redis, deps.Config.Redis.KeyNamespace, appsettings.DefaultRegistry(), deps.Logger,
	)
	// 启动 seed（DEC §11.2）：把注册表默认值写入 DB 缺行（DO NOTHING,绝不覆盖已改值）。
	// 失败不阻断启动——读侧本就有注册表默认兜底。
	_ = settingsStore.SeedDefaults(ctx)
	var runtimeControlPool *pgxpool.Pool
	if sharedBreakerStore != nil {
		// Endpoint 围栏、普通 runtime control、四个关键 setting 与全部 Channel admission
		// 必须按顺序先收口，Gateway-only 部署也不能依赖 Admin 进程恢复运行态。
		// marker/epoch 不匹配时即使 control 已重建，/readyz 仍会保持 fail-closed。
		pool, ok := deps.DB.(*pgxpool.Pool)
		if !ok {
			return nil, errors.New("bootstrap: gateway runtime control recovery requires pgxpool")
		}
		reconciliationGeneration, err := sharedBreakerStore.BeginRuntimeReconciliation(ctx)
		if err != nil {
			return nil, err
		}
		if err := reconcileAllRuntimeControls(
			ctx, pool, settingsStore, sharedBreakerStore, runtimeTelemetry,
		); err != nil {
			return nil, err
		}
		reconciliationProof, err := captureRuntimeReconciliationProof(ctx, pool, reconciliationGeneration)
		if err != nil {
			return nil, err
		}
		// Only a successful full Endpoint/Channel/critical-control reconciliation may clear a
		// request-time infrastructure fault latch. /readyz itself remains read-only.
		if reconciled, reason := runtimeReadinessChecker.ClearStoreFaultAfterReconciliation(ctx, reconciliationProof); !reconciled {
			return nil, fmt.Errorf("bootstrap: gateway runtime reconciliation proof was rejected: %s", reason)
		}
		runtimeControlPool = pool
	}
	if attemptPermitManager != nil {
		cooldown := appsettings.GatewayChannelCooldown(ctx, settingsStore)
		attemptPermitManager.SetChannel429CooldownPolicy(cooldown.Cooldown, cooldown.Cap)
	}

	// Anthropic beta 转发策略：adapter 每请求经 provider 现读（策略读取本身足够轻）。
	messagesadapter.SetBetaPolicyProvider(appsettings.NewBetaPolicyProvider(settingsStore))

	// 非准入类 gateway 热路径配置：启动期从配置中枢取初值构造消费方，之后由
	// settingsApplier 周期推送热更新。breaker、admission 与 balanced 参数只读 Redis committed control。
	adapter.SetStreamIdleTimeout(appsettings.GatewayStreamIdleTimeout(ctx, settingsStore))

	chatRouter := NewChatRouter(queries, appsettings.GatewayDefaultChannelTimeout(ctx, settingsStore), deps.Logger)

	adapterRegistry, err := NewAdapterRegistry(http.DefaultClient, deps.Logger)
	if err != nil {
		return nil, err
	}

	// 启动期对所有 enabled channel 的 (protocol, adapter_key) 复合键做 adapter registry preflight。
	// admin provider/channel CRUD 写入路径已同样用 registry 校验复合键（关闭 GAP-6-003），
	// 不可运行绑定无法被写入业务数据。
	providerAdapterPreflight := NewProviderAdapterPreflight(queries, adapterRegistry)
	if err := providerAdapterPreflight.ValidateEnabledChannelBindings(ctx); err != nil {
		return nil, err
	}

	chatCompletionService := NewChatGateway(
		deps.DB,
		queries,
		chatRouter,
		adapterRegistry,
		deps.Config.Worker,
		deps.Config.Gateway,
		metricsRecorder,
	)
	responsesService := NewResponsesGateway(
		deps.DB,
		queries,
		chatRouter,
		adapterRegistry,
		deps.Config.Worker,
		deps.Config.Gateway,
		metricsRecorder,
		deps.Logger,
	)
	messagesService := NewMessagesGateway(
		deps.DB,
		queries,
		chatRouter,
		adapterRegistry,
		deps.Config.Worker,
		deps.Config.Gateway,
		metricsRecorder,
	)
	chatCompletionService.SetAttemptPermitManager(attemptPermitManager)
	responsesService.SetAttemptPermitManager(attemptPermitManager)
	messagesService.SetAttemptPermitManager(attemptPermitManager)
	routingTraceRecorder := lifecycle.NewRoutingTraceRecorder(queries, deps.Logger)
	routingTraceRecorder.SetMetrics(metricsRecorder)
	routingTraceRecorder.SetSampleRate(appsettings.GatewayRoutingTrace(ctx, settingsStore).SampleRate)
	chatCompletionService.SetRoutingTraceRecorder(routingTraceRecorder)
	responsesService.SetRoutingTraceRecorder(routingTraceRecorder)
	messagesService.SetRoutingTraceRecorder(routingTraceRecorder)
	// 成本敞口记录器（DESIGN-bill-on-cancel 阶段一）：bill-on-disconnect 渠道的失败/取消路径
	// 记平台成本敞口；假定输出兜底与 authorization 的进程级兜底同源，保证敞口与冻结上界口径一致。
	costExposureRecorder := newCostExposureStore(queries, deps.Logger)
	chatCompletionService.SetCostExposureRecorder(costExposureRecorder, deps.Config.Gateway.MaxOutputTokensFallback)
	responsesService.SetCostExposureRecorder(costExposureRecorder, deps.Config.Gateway.MaxOutputTokensFallback)
	messagesService.SetCostExposureRecorder(costExposureRecorder, deps.Config.Gateway.MaxOutputTokensFallback)

	// 凭据失效闸门（阶段二）：三协议共享一份进程内「连续 401」计数器；达阈值时异步把
	// channels.credential_valid 翻 false + 写 runtime_401 日志，后续请求在路由候选层直接跳过该渠道。
	credentialGate := lifecycle.NewChannelCredentialGate(
		appsettings.GatewayCredential401Threshold(ctx, settingsStore),
		newCredentialInvalidator(queries, deps.Logger),
	)
	chatCompletionService.SetCredentialGate(credentialGate)
	responsesService.SetCredentialGate(credentialGate)
	messagesService.SetCredentialGate(credentialGate)

	// 会话粘性路由（大 uncache 缺口 P0）：三协议共享一份 sticky 核心，同会话请求钉住上次成功渠道
	// 以保上游 prompt cache。绑定存 Redis（fail-open，故障只丢粘性不伤主链路）；全局默认/TTL 由
	// 系统设置热更新，线路行 sticky_enabled 可覆盖开关。无 Redis（测试装配）时不启用 sticky。
	var stickyRouter *lifecycle.StickyRouter
	if deps.Redis != nil {
		stickySettings := appsettings.GatewayRoutingSticky(ctx, settingsStore)
		stickyRouter = lifecycle.NewStickyRouter(stickysession.NewStore(deps.Redis, deps.Config.Redis.KeyNamespace, deps.Logger))
		stickyRouter.SetConfig(stickySettings.EnabledDefault, stickySettings.TTL, stickySettings.TPMWait, stickySettings.TPMWaitJitter)
		stickyRouter.SetMetrics(metricsRecorder)
		stickyRouter.SetLogger(deps.Logger)
		chatCompletionService.SetStickyRouter(stickyRouter)
		responsesService.SetStickyRouter(stickyRouter)
		messagesService.SetStickyRouter(stickyRouter)
		chatCompletionService.SetRoutingLogger(deps.Logger)
		responsesService.SetRoutingLogger(deps.Logger)
		messagesService.SetRoutingLogger(deps.Logger)
	}

	// 配置 applier：周期性推送非准入类本机配置。breaker、限额和 balanced 参数由
	// Redis committed runtime control 驱动，不得再由本机 settings cache 覆盖。
	// 配置 applier 与 runtime-control reconciler 共用独立 background context
	// （传入的 ctx 是启动期短时 ctx），随 app.Shutdown 一起停止。
	applier := &settingsApplier{
		store:        settingsStore,
		logger:       deps.Logger,
		gate:         credentialGate,
		router:       chatRouter,
		sticky:       stickyRouter,
		routingTrace: routingTraceRecorder,
		channel429:   attemptPermitManager,
	}
	applierCtx, stopApplier := context.WithCancel(context.Background())
	go applier.run(applierCtx, settingsApplyInterval)
	if runtimeControlPool != nil {
		go runRuntimeControlReconciler(
			applierCtx, runtimeControlPool, settingsStore, sharedBreakerStore, runtimeTelemetry,
			func(reconcileCtx context.Context, proof breakerstore.RuntimeReconciliationProof) {
				runtimeReadinessChecker.ClearStoreFaultAfterReconciliation(reconcileCtx, proof)
			},
		)
	}

	var readinessProbe gatewayapi.ReadinessProbe
	if runtimeReadinessChecker != nil {
		readinessProbe = runtimeReadinessChecker
	}
	handler := NewHTTPHandler(
		deps.Logger,
		queries,
		requestAdmissionManager,
		chatCompletionService,
		responsesService,
		messagesService,
		metricsRecorder,
		readinessProbe,
	)

	return &GatewayServerApp{
		Handler:     handler,
		tracer:      tracerProvider,
		stopApplier: stopApplier,
	}, nil
}
