package bootstrap

import (
	"net/http"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/capability"
	admincdkeyapp "github.com/ThankCat/unio-gateway/internal/app/adminapi/cdkey"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/channel"
	adminemail "github.com/ThankCat/unio-gateway/internal/app/adminapi/email"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/exchangerate"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/ledger"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/message"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/model"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/overview"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/provider"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/requests"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/system"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/ticket"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/user"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/httpmw"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	adminproxy "github.com/ThankCat/unio-gateway/internal/service/admin/proxy"
	subscriptionaccountadmin "github.com/ThankCat/unio-gateway/internal/service/admin/subscriptionaccount"
)

// adminHTTPDeps 收拢 admin-server HTTP handler 构建所需的全部 service 依赖。
type adminHTTPDeps struct {
	Logger        *zap.Logger
	Authenticator middleware.AdminAuthenticator

	// CredentialAuthenticator 校验登录口令，LoginAttemptLimiter 限制失败尝试，Sessions 签发与吊销会话 token。
	CredentialAuthenticator adminapi.CredentialAuthenticator
	LoginAttemptLimiter     adminapi.LoginAttemptLimiter
	LoginSourceResolver     adminapi.LoginSourceResolver
	Sessions                adminapi.SessionIssuer
	SessionTTLSeconds       int64

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
	ChannelPriceService          channel.ChannelPriceService
	ModelPriceService            model.ModelPriceService

	// DEC-027 渠道成本倍率。
	ChannelCostMultiplierService channel.ChannelCostMultiplierService
	SubscriptionAccountService   *subscriptionaccountadmin.Service
	ProxyService                 *adminproxy.Service
	ProviderRechargeRateService  provider.ProviderRechargeRateService

	RoutingTraceService adminapi.RoutingTraceService

	RequestQueryService requests.RequestQueryService
	LedgerQueryService  ledger.LedgerQueryService
	CDKeyService        admincdkeyapp.Service

	UserService        user.UserService
	APIKeyService      user.APIKeyService
	AdjustmentService  user.AdjustmentService
	CustomerOpsService user.CustomerOpsService

	CapabilityService     capability.CapabilityService
	CapabilitySyncService capability.CapabilitySyncService
	CapabilitySeedService capability.CapabilitySeedService

	CatalogService model.CatalogService

	// LabLogos 提供模型出品方图标（models.dev 公开资产）。
	LabLogos adminapi.LabLogoStore

	DashboardService   overview.DashboardService
	LiveTrafficService overview.LiveTrafficService

	RecoveryJobQueryService   system.RecoveryJobQueryService
	RuntimeDiagnosticsService system.RuntimeDiagnosticsService
	GatewayLoggingService     system.GatewayLoggingService

	// 站内消息中心（告警通道 MVP）。
	MessageService message.MessageService

	// 邮件发送记录（客户中心「邮件」列表）与 SMTP 配置面板（含测试邮件）。
	EmailLogService  adminemail.EmailLogService
	EmailSMTPService system.EmailSMTPService
	EmailTestMailer  system.EmailTestMailer

	// 用户反馈工单；nil 时不挂载（未配置 TICKET_ATTACHMENT_SECRET）。
	TicketService ticket.TicketService

	// 汇率管理（多货币）。
	ExchangeRateService exchangerate.ExchangeRateService

	ProviderSettingsService system.ProviderSettingsService

	// 系统配置只读面板（进程级 env 生效值，脱敏）；gateway 热路径配置已迁移为运行时配置，不在此列。
	GatewayConfig config.GatewayConfig
	WorkerConfig  config.WorkerConfig
	HTTPConfig    config.HTTPConfig

	MetricsRecorder *metrics.Metrics
	// GatewayInternalToken 非空时保护 /metrics（Bearer）；与 gateway 共用同一 token。
	GatewayInternalToken string
}

// NewAdminHTTPHandler 创建 admin-server 进程使用的 HTTP handler。
func NewAdminHTTPHandler(deps adminHTTPDeps) http.Handler {
	routerDeps := adminapi.RouterDeps{
		Logger:                  deps.Logger,
		AdminAuthenticator:      deps.Authenticator,
		CredentialAuthenticator: deps.CredentialAuthenticator,
		LoginAttemptLimiter:     deps.LoginAttemptLimiter,
		LoginSourceResolver:     deps.LoginSourceResolver,
		Sessions:                deps.Sessions,
		SessionTTLSeconds:       deps.SessionTTLSeconds,

		ProviderService:              deps.ProviderService,
		ProviderOpsService:           deps.ProviderOpsService,
		ProviderBalanceService:       deps.ProviderBalanceService,
		ProviderBreaker:              deps.ProviderBreaker,
		ChannelService:               deps.ChannelService,
		ChannelBreaker:               deps.ChannelBreaker,
		ChannelTestService:           deps.ChannelTestService,
		ChannelOpsService:            deps.ChannelOpsService,
		ModelService:                 deps.ModelService,
		ModelOpsService:              deps.ModelOpsService,
		ModelRoutingService:          deps.ModelRoutingService,
		ChannelModelService:          deps.ChannelModelService,
		ChannelModelInventoryService: deps.ChannelModelInventoryService,
		ChannelPriceService:          deps.ChannelPriceService,
		ModelPriceService:            deps.ModelPriceService,

		ChannelCostMultiplierService: deps.ChannelCostMultiplierService,
		SubscriptionAccountService:   deps.SubscriptionAccountService,
		ProxyService:                 deps.ProxyService,
		ProviderRechargeRateService:  deps.ProviderRechargeRateService,

		RoutingTraceService: deps.RoutingTraceService,
		RequestQueryService: deps.RequestQueryService,
		LedgerQueryService:  deps.LedgerQueryService,
		CDKeyService:        deps.CDKeyService,

		UserService:        deps.UserService,
		APIKeyService:      deps.APIKeyService,
		AdjustmentService:  deps.AdjustmentService,
		CustomerOpsService: deps.CustomerOpsService,

		CapabilityService:     deps.CapabilityService,
		CapabilitySyncService: deps.CapabilitySyncService,
		CapabilitySeedService: deps.CapabilitySeedService,

		CatalogService: deps.CatalogService,
		LabLogos:       deps.LabLogos,

		DashboardService:   deps.DashboardService,
		LiveTrafficService: deps.LiveTrafficService,

		RecoveryJobQueryService:   deps.RecoveryJobQueryService,
		RuntimeDiagnosticsService: deps.RuntimeDiagnosticsService,
		GatewayLoggingService:     deps.GatewayLoggingService,
		MessageService:            deps.MessageService,
		EmailLogService:           deps.EmailLogService,
		EmailSMTPService:          deps.EmailSMTPService,
		EmailTestMailer:           deps.EmailTestMailer,
		TicketService:             deps.TicketService,
		ExchangeRateService:       deps.ExchangeRateService,
		ProviderSettingsService:   deps.ProviderSettingsService,

		GatewayConfig: deps.GatewayConfig,
		WorkerConfig:  deps.WorkerConfig,
		HTTPConfig:    deps.HTTPConfig,
	}

	if deps.MetricsRecorder != nil {
		routerDeps.HTTPMetrics = deps.MetricsRecorder
		// 与 gateway 同一把 GATEWAY_INTERNAL_TOKEN 保护 /metrics（Admin 与 Gateway 本就共用该 token）。
		routerDeps.MetricsHandler = httpmw.RequireBearer(deps.GatewayInternalToken, deps.MetricsRecorder.Handler())
	}

	return adminapi.NewRouter(routerDeps)
}
