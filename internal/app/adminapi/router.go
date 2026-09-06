// Package adminapi 组装 admin 管理端（/v1）的 HTTP 路由。
//
// admin 表面只服务平台管理员，登录使用固定用户名口令换取 Redis 会话 token，与客户 Gateway（/v1）
// 认证严格隔离。各业务模块的 handler / DTO / service 接口按模块拆到子包（overview/provider/channel/
// model/capability/user/requests/ledger/system，镜像 internal/service/admin 的目录结构），
// 共用的响应/请求/分页/排序小工具在 adminapi/adminhttp 叶子包。本文件只做依赖聚合与路由编排。
package adminapi

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/capability"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/cdkey"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/channel"
	adminemail "github.com/ThankCat/unio-gateway/internal/app/adminapi/email"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/exchangerate"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/ledger"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/message"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/model"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/overview"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/provider"
	proxyapi "github.com/ThankCat/unio-gateway/internal/app/adminapi/proxy"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/requests"
	subscriptionaccountapi "github.com/ThankCat/unio-gateway/internal/app/adminapi/subscriptionaccount"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/system"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/ticket"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/user"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/httpmw"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	proxyservice "github.com/ThankCat/unio-gateway/internal/service/admin/proxy"
	subscriptionaccountservice "github.com/ThankCat/unio-gateway/internal/service/admin/subscriptionaccount"
)

type RoutingTraceService interface {
	requests.RoutingTraceService
}

// RouterDeps 保存构建 admin HTTP router 所需的外部依赖（扁平聚合，按模块分派到各子包 Register）。
type RouterDeps struct {
	Logger             *zap.Logger
	AdminAuthenticator middleware.AdminAuthenticator

	// CredentialAuthenticator 校验登录口令，LoginAttemptLimiter 限制失败尝试，Sessions 签发与吊销会话 token。
	CredentialAuthenticator CredentialAuthenticator
	LoginAttemptLimiter     LoginAttemptLimiter
	Sessions                SessionIssuer
	SessionTTLSeconds       int64
	// LoginSourceResolver 给登录限速提供可信代理感知的来源 IP；nil 时按 RemoteAddr 分桶。
	LoginSourceResolver LoginSourceResolver

	ProviderService              provider.ProviderService
	ProviderOpsService           provider.ProviderOpsService
	ProviderBalanceService       provider.ProviderBalanceService
	ProviderBreaker              provider.BreakerRuntime
	ChannelService               channel.ChannelService
	ChannelBreaker               channel.BreakerRuntime
	ChannelTestService           channel.ChannelTestService
	ChannelOpsService            channel.ChannelOpsService
	ModelService                 model.ModelService
	ModelOpsService              model.ModelOpsService
	ModelRoutingService          model.ModelRoutingService
	ChannelModelService          channel.ChannelModelService
	ChannelModelInventoryService channel.ChannelModelInventoryService

	// 渠道-模型成本价（绝对覆盖）。
	ChannelPriceService channel.ChannelPriceService
	RoutingTraceService RoutingTraceService

	// 模型定价：客户售价 = 模型绝对售价，或基准价 × 该模型的售价折扣。
	ModelPriceService model.ModelPriceService

	// DEC-027：渠道价格倍率（渠道真实成本 = 模型基准价 × 价格倍率 × 服务商充值汇率）。
	ChannelCostMultiplierService channel.ChannelCostMultiplierService
	// SubscriptionAccountService 是订阅账号号池管理（第九节）；nil 时不注册账号路由。
	SubscriptionAccountService *subscriptionaccountservice.Service
	ProxyService               *proxyservice.Service
	// 服务商充值汇率（服务商级，全渠道共享）。
	ProviderRechargeRateService provider.ProviderRechargeRateService

	// M6 只读查询台
	RequestQueryService requests.RequestQueryService
	LedgerQueryService  ledger.LedgerQueryService
	CDKeyService        cdkey.Service

	// M7 客户管理：用户（只读）/API Key（费用上限 + 必填线路）/手工调额
	UserService        user.UserService
	APIKeyService      user.APIKeyService
	AdjustmentService  user.AdjustmentService
	CustomerOpsService user.CustomerOpsService

	// M5 能力管理：模型能力 CRUD、models.dev 同步、adapter 画像
	CapabilityService     capability.CapabilityService
	CapabilitySyncService capability.CapabilitySyncService
	CapabilitySeedService capability.CapabilitySeedService

	// 模型目录：models.dev 目录浏览 + 从目录采纳/刷新/更新提醒
	CatalogService model.CatalogService

	// LabLogos 提供模型出品方图标（models.dev 公开资产，挂鉴权之外）；nil 时不挂图标路由。
	LabLogos LabLogoStore

	// M9 工作台看板：首屏概览雷达 + 时间序列（只读聚合）
	DashboardService overview.DashboardService
	// 实时流量：Redis 运行态秒级快照，与上面的历史聚合不是同一时间尺度
	LiveTrafficService overview.LiveTrafficService

	// M8 系统/任务/健康（横切）：结算补偿任务只读视图
	RecoveryJobQueryService   system.RecoveryJobQueryService
	RuntimeDiagnosticsService system.RuntimeDiagnosticsService
	GatewayLoggingService     system.GatewayLoggingService

	// 站内消息中心（告警通道 MVP）：worker 写入，管理台查看/标记已读。
	MessageService message.MessageService

	// 邮件发送记录（客户中心「邮件」列表）与 SMTP 配置面板（含测试邮件）。
	EmailLogService  adminemail.EmailLogService
	EmailSMTPService system.EmailSMTPService
	EmailTestMailer  system.EmailTestMailer

	// 用户反馈工单：队列/详情/回复/状态流转；nil 时不挂载（未配置 TICKET_ATTACHMENT_SECRET）。
	TicketService ticket.TicketService

	// 汇率管理（多货币）：最新/历史查询、手工录入兜底、API Key 运行时验证。
	ExchangeRateService exchangerate.ExchangeRateService

	// Provider 全局设置（可编辑）：起步 Anthropic beta 转发策略（app_settings）。
	ProviderSettingsService system.ProviderSettingsService

	// 系统配置只读面板（进程级 env 生效值，脱敏）。
	GatewayConfig config.GatewayConfig
	WorkerConfig  config.WorkerConfig
	HTTPConfig    config.HTTPConfig

	// HTTPMetrics 记录 HTTP 层请求指标；nil 表示不采集。
	HTTPMetrics httpmw.MetricsRecorder

	// MetricsHandler 暴露 Prometheus /metrics；nil 表示不挂载该上游源站。
	MetricsHandler http.Handler
}

// NewRouter 创建 admin-server 使用的 HTTP handler。
func NewRouter(deps RouterDeps) http.Handler {
	r := chi.NewRouter()

	r.Use(httpmw.CORS)
	r.Use(httpmw.RequestID)
	r.Use(httpmw.Tracing)
	r.Use(httpmw.Metrics(deps.HTTPMetrics))
	r.Use(httpmw.Logger(deps.Logger))
	r.Use(httpmw.Recoverer(deps.Logger))

	if deps.MetricsHandler != nil {
		r.Handle("/metrics", deps.MetricsHandler)
	}

	r.NotFound(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteError(w, http.StatusNotFound, "not_found", "route not found")
	})
	r.MethodNotAllowed(func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteError(w, http.StatusMethodNotAllowed, "method_not_allowed", "method not allowed")
	})

	r.Get("/healthz", func(w http.ResponseWriter, r *http.Request) {
		_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
	})

	r.Route("/v1", func(r chi.Router) {
		// login 与出品方图标不需要 token：前者没有它就无法取得 token；
		// 后者是 models.dev 公开资产且 <img> 无法携带 Bearer。
		// 单独分组挂载，确保 AdminAuth 只作用于其余全部端点。
		r.Group(func(r chi.Router) {
			r.Post("/login", handleLogin(
				deps.CredentialAuthenticator,
				deps.LoginAttemptLimiter,
				deps.Sessions,
				deps.SessionTTLSeconds,
				deps.LoginSourceResolver,
			))
			if deps.LabLogos != nil {
				r.Get("/labs/{slug}/logo.svg", handleLabLogo(deps.LabLogos))
			}
		})

		// 工单模块自行管理认证分组：附件下载是签名公开路由（<img> 带不了 Bearer），
		// 其余路由在模块内部套 AdminAuth。
		ticket.Register(r, ticket.Deps{
			Service: deps.TicketService,
			Auth:    middleware.AdminAuth(deps.AdminAuthenticator),
		})

		r.Group(func(r chi.Router) {
			r.Use(middleware.AdminAuth(deps.AdminAuthenticator))

			// ping 是受保护探针：用于校验会话 token 是否仍然有效（认证后回 200）。
			r.Get("/ping", handlePing)

			// logout 吊销当前会话；挂在受保护分组内，因此到达时 token 必然有效。
			r.Post("/logout", handleLogout(deps.Sessions))

			// 各业务模块自注册路由（chi 按静态优先于通配匹配，模块注册顺序不影响正确性）。
			overview.Register(r, overview.Deps{
				Service:     deps.DashboardService,
				LiveTraffic: deps.LiveTrafficService,
			})
			provider.Register(r, provider.Deps{
				Service:             deps.ProviderService,
				OpsService:          deps.ProviderOpsService,
				BalanceService:      deps.ProviderBalanceService,
				RechargeRateService: deps.ProviderRechargeRateService,
				Breaker:             deps.ProviderBreaker,
			})
			channel.Register(r, channel.Deps{
				Service:               deps.ChannelService,
				OpsService:            deps.ChannelOpsService,
				TestService:           deps.ChannelTestService,
				ModelService:          deps.ChannelModelService,
				ModelInventoryService: deps.ChannelModelInventoryService,
				PriceService:          deps.ChannelPriceService,
				CostMultiplierService: deps.ChannelCostMultiplierService,
				Breaker:               deps.ChannelBreaker,
			})
			subscriptionaccountapi.Register(r, deps.SubscriptionAccountService)
			proxyapi.Register(r, deps.ProxyService)
			model.Register(r, model.Deps{
				Service:        deps.ModelService,
				OpsService:     deps.ModelOpsService,
				PriceService:   deps.ModelPriceService,
				CatalogService: deps.CatalogService,
				RoutingService: deps.ModelRoutingService,
			})
			capability.Register(r, capability.Deps{
				Service:     deps.CapabilityService,
				SyncService: deps.CapabilitySyncService,
				SeedService: deps.CapabilitySeedService,
			})
			user.Register(r, user.Deps{
				Service:           deps.UserService,
				APIKeyService:     deps.APIKeyService,
				AdjustmentService: deps.AdjustmentService,
				OpsService:        deps.CustomerOpsService,
			})
			requests.Register(r, requests.Deps{
				Service:             deps.RequestQueryService,
				RoutingTraceService: deps.RoutingTraceService,
			})
			ledger.Register(r, ledger.Deps{Service: deps.LedgerQueryService})
			cdkey.Register(r, cdkey.Deps{Service: deps.CDKeyService, Logger: deps.Logger})
			message.Register(r, message.Deps{Service: deps.MessageService})
			adminemail.Register(r, adminemail.Deps{Service: deps.EmailLogService})
			exchangerate.Register(r, exchangerate.Deps{Service: deps.ExchangeRateService})
			system.Register(r, system.Deps{
				RecoveryJobService:        deps.RecoveryJobQueryService,
				ProviderSettingsService:   deps.ProviderSettingsService,
				RuntimeDiagnosticsService: deps.RuntimeDiagnosticsService,
				GatewayLoggingService:     deps.GatewayLoggingService,
				EmailSMTPService:          deps.EmailSMTPService,
				EmailTestMailer:           deps.EmailTestMailer,
				GatewayConfig:             deps.GatewayConfig,
				WorkerConfig:              deps.WorkerConfig,
				HTTPConfig:                deps.HTTPConfig,
			})
		})
	})

	return r
}

// handlePing 在通过 admin 认证后返回探针结果。
func handlePing(w http.ResponseWriter, _ *http.Request) {
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
