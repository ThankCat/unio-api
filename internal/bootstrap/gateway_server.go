package bootstrap

import (
	"context"
	"log/slog"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
	"github.com/ThankCat/unio-api/internal/core/tokenest"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-api/internal/platform/observability/tracing"
	"github.com/ThankCat/unio-api/internal/platform/ratelimit"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/appsettings"
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

	tracer      *tracing.Provider
	stopApplier context.CancelFunc
}

// Shutdown 停止后台配置 applier 并释放可观测性资源（flush trace exporter）。
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

	// 运行时配置中枢（app_settings + Redis 实时缓存 + 本地短缓存）：admin 改动经 Redis 跨进程秒级生效、无需重启。
	settingsStore := appsettings.NewSettingsStore(
		queries, deps.Redis, deps.Config.Redis.KeyNamespace, appsettings.DefaultRegistry(), deps.Logger,
	)
	// 启动 seed（DEC §11.2）：把注册表默认值写入 DB 缺行（DO NOTHING,绝不覆盖已改值）。
	// 失败不阻断启动——读侧本就有注册表默认兜底。
	_ = settingsStore.SeedDefaults(ctx)

	// Anthropic beta 转发策略：adapter 每请求经 provider 现读（策略读取本身足够轻）。
	messagesadapter.SetBetaPolicyProvider(appsettings.NewBetaPolicyProvider(settingsStore))

	// 6 组 gateway 热路径配置（DEC db_only）：启动期从配置中枢取初值构造消费方，
	// 之后由 settingsApplier 周期推送热更新（消费方内部 atomic/锁内字段，热路径零额外开销）。
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

	metricsRecorder := metrics.New()

	// 两层限流 Guard（P2-8）：进程内唯一实例（DEC §11.5），HTTP 中间件（线路+用户 RPM/RPD）与
	// attempt runner（TPM / 渠道级）共用同一 Redis 计数口径、同一默认上限与故障策略。
	rateLimitGuard := NewRateLimitGuard(
		deps.Redis, deps.Config.Redis.KeyNamespace,
		appsettings.GatewayRateLimitDefaults(ctx, settingsStore), deps.Logger,
	)

	// 渠道熔断器：三协议共享单实例（DEC §11.4），enabled 为内部原子开关（运行期启停免重启/免重建）。
	breakerSettings := appsettings.GatewayCircuitBreaker(ctx, settingsStore)
	channelBreaker := lifecycle.NewChannelCircuitBreaker(lifecycle.ChannelCircuitBreakerConfig{
		Window:       breakerSettings.Window,
		MinRequests:  breakerSettings.MinRequests,
		FailureRatio: breakerSettings.FailureRatio,
		OpenDuration: breakerSettings.OpenDuration,
	})
	channelBreaker.SetEnabled(breakerSettings.Enabled)

	chatCompletionService := NewChatGateway(
		deps.DB,
		queries,
		chatRouter,
		adapterRegistry,
		deps.Config.Worker,
		deps.Config.Gateway,
		metricsRecorder,
		rateLimitGuard,
		channelBreaker,
	)
	responsesService := NewResponsesGateway(
		deps.DB,
		queries,
		chatRouter,
		adapterRegistry,
		deps.Config.Worker,
		deps.Config.Gateway,
		metricsRecorder,
		rateLimitGuard,
		channelBreaker,
	)
	messagesService := NewMessagesGateway(
		deps.DB,
		queries,
		chatRouter,
		adapterRegistry,
		deps.Config.Worker,
		deps.Config.Gateway,
		metricsRecorder,
		rateLimitGuard,
		channelBreaker,
	)

	// 渠道级 429 冷却注册表（P2-7）：三协议 service 共享一份，使任一协议命中的 429 都能即时
	// 让该渠道在冷却窗口内被 routing fallback 跳过。冷却到期自动恢复。
	// 同一注册表还承载 timeout/5xx 失败软冷却（DEC-029）：软冷却只 demote 候选排序、不剔除。
	cooldownSettings := appsettings.GatewayChannelCooldown(ctx, settingsStore)
	channelCooldown := lifecycle.NewChannelCooldownRegistry(cooldownSettings.Cooldown, cooldownSettings.Cap)
	channelCooldown.SetFailureCooldown(appsettings.GatewayFailureCooldown(ctx, settingsStore))
	chatCompletionService.SetChannelCooldownRegistry(channelCooldown)
	responsesService.SetChannelCooldownRegistry(channelCooldown)
	messagesService.SetChannelCooldownRegistry(channelCooldown)

	// 在途并发限制器（DEC-029）：进程内单实例，两级共用——ingress 中间件（线路+用户）与
	// attempt runner（渠道级，渠道行 concurrency_limit 可覆盖默认）。
	concurrencySettings := appsettings.GatewayConcurrencyDefaults(ctx, settingsStore)
	concurrencyLimiter := ratelimit.NewConcurrencyLimiter(concurrencySettings.KeyLimit, concurrencySettings.ChannelLimit)
	chatCompletionService.SetConcurrencyLimiter(concurrencyLimiter)
	responsesService.SetConcurrencyLimiter(concurrencyLimiter)
	messagesService.SetConcurrencyLimiter(concurrencyLimiter)

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

	// 配置 applier：周期性把 6 组配置的最新生效值推给上述消费方（admin 改动 ≤ 应用周期内生效）。
	// 生命周期挂到独立 background context（传入的 ctx 是启动期短时 ctx），随 app.Shutdown 停止。
	applier := &settingsApplier{
		store:       settingsStore,
		logger:      deps.Logger,
		breaker:     channelBreaker,
		guard:       rateLimitGuard,
		cooldown:    channelCooldown,
		gate:        credentialGate,
		router:      chatRouter,
		concurrency: concurrencyLimiter,
	}
	applierCtx, stopApplier := context.WithCancel(context.Background())
	go applier.run(applierCtx, settingsApplyInterval)

	handler := NewHTTPHandler(
		deps.Logger,
		queries,
		rateLimitGuard,
		concurrencyLimiter,
		chatCompletionService,
		responsesService,
		messagesService,
		metricsRecorder,
	)

	return &GatewayServerApp{
		Handler:     handler,
		tracer:      tracerProvider,
		stopApplier: stopApplier,
	}, nil
}
