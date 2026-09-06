package bootstrap

import (
	"context"
	"fmt"
	"net/http"
	"os"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/workers"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/fx"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/core/modelcatalog"
	"github.com/ThankCat/unio-gateway/internal/core/providerledger"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/proxyclient"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/adminmessage"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodelinventory"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channeltest"
	"github.com/ThankCat/unio-gateway/internal/service/admin/exchangerate"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
	"github.com/ThankCat/unio-gateway/internal/service/subscription"
	subscriptionhealth "github.com/ThankCat/unio-gateway/internal/service/subscription/health"
	subscriptionquota "github.com/ThankCat/unio-gateway/internal/service/subscription/quota"
)

// WorkerServerAppDB 定义 worker server app 构建时需要的数据库能力。
type WorkerServerAppDB interface {
	sqlc.DBTX
	lifecycle.ChatTxBeginner
}

// WorkerServerAppDeps 表示构建 worker server app 需要的进程级依赖。
type WorkerServerAppDeps struct {
	Logger *zap.Logger
	Config config.Config
	DB     WorkerServerAppDB
	Redis  redis.Cmdable
}

// WorkerServerApp 表示当前 worker-server 进程已经装配完成的后台任务应用。
//
// 后台任务分成两个并行 Runner：
//   - settlement：结算补偿与预授权清扫，是资金一致性的关键路径，必须按自身节奏尽快 claim；
//   - maintenance：令牌保活、渠道探测、模型发现/验证、目录与汇率同步、工单维护等外呼或周期任务，
//     单轮可能长达数十秒，不允许推迟 settlement 组的下一轮。
type WorkerServerApp struct {
	Runner *workers.Group
}

// NewWorkerServerApp 装配当前 worker-server 进程的后台任务应用。
func NewWorkerServerApp(ctx context.Context, deps WorkerServerAppDeps) (*WorkerServerApp, error) {
	if deps.Redis == nil {
		return nil, fmt.Errorf("worker-server: redis is required")
	}
	queries := sqlc.New(deps.DB)
	permitStore := breakerstore.NewStore(deps.Redis, deps.Config.Redis.KeyNamespace)
	ledgerService := ledger.NewService(deps.DB, queries)
	providerLedgerService := providerledger.NewService(deps.DB, queries).WithFxRates(fx.NewService(queries, 0))
	chatSettlementService := lifecycle.NewChatSettlementService(
		deps.DB,
		queries,
		billing.Service{},
		ledgerService,
		providerLedgerService,
	).WithFxRates(fx.NewService(queries, 0)).WithLogger(deps.Logger)
	chatSettlementRecoveryService := lifecycle.NewChatSettlementRecoveryService(queries, chatSettlementService)

	settlementRecoveryWorker := workers.NewSettlementRecoveryWorker(
		queries,
		chatSettlementRecoveryService,
		defaultWorkerID("settlement-recovery"),
		deps.Config.Worker.SettlementRecoveryLockTTL,
		deps.Config.Worker.SettlementRecoveryBackoffCap,
	)
	// P2-5：积压时单轮批量排空，摊薄每轮 dead 收口 + exhausted 扫描的固定开销。
	settlementRecoveryWorker.SetBatchSize(int(deps.Config.Worker.SettlementRecoveryBatchSize))

	// 孤儿预授权清扫 worker：兜底进程崩溃遗留的「永久冻结 + 永久 running」请求（与 settlement_recovery 互补）。
	orphanReservationSweeperWorker := workers.NewOrphanReservationSweeperWorker(
		queries,
		chatSettlementService,
		permitStore,
		deps.Logger,
		deps.Config.Worker.OrphanReservationSweepAgeThreshold,
		deps.Config.Worker.OrphanReservationSweepBatchSize,
	)

	// 搁浅预授权清扫 worker：兜底「请求已终态 + 冻结仍 authorized」，即 release 失败而审计写入成功留下的
	// 残留。与孤儿清扫按请求状态互斥，共用同一组年龄阈值与批量配置。
	strandedReservationSweeperWorker := workers.NewStrandedReservationSweeperWorker(
		queries,
		chatSettlementService,
		deps.Logger,
		deps.Config.Worker.OrphanReservationSweepAgeThreshold,
		deps.Config.Worker.OrphanReservationSweepBatchSize,
	)

	// 运行时配置中枢：与 gateway/admin 同一注册表；worker 既是消费者（巡检开关、阈值），
	// 也是 Codex 客户端版本自动同步值的唯一写入方。
	settingsStore := appsettings.NewSettingsStore(
		queries, deps.Redis, deps.Config.Redis.KeyNamespace, appsettings.DefaultRegistry(), deps.Logger,
	)
	_ = settingsStore.SeedDefaults(ctx)
	codexVersion := codexVersionSource(settingsStore)

	// 订阅账号令牌保活（第六节）：扫描将过期账号并刷新；分布式锁防多实例重复刷。
	// 出站（令牌端点）与正式请求共用按账号代理解析器与 Codex 出站身份。
	accountProxyClients := proxyclient.NewResolver(upstreamHTTPClient(nil))
	subscriptionOutbound := subscription.NewOutbound(
		queries,
		subscription.NewTokenClient(accountProxyClients.ClientFor, codexVersion),
		permitStore,
		deps.Redis,
		deps.Logger,
	)
	tokenRefreshWorker := subscription.NewRefreshWorker(queries, subscriptionOutbound, deps.Logger, subscription.RefreshWorkerOptions{})

	settlementUnits := []workers.Unit{
		settlementRecoveryWorker, orphanReservationSweeperWorker, strandedReservationSweeperWorker,
	}
	units := []workers.Unit{tokenRefreshWorker}

	if deps.Config.ModelCatalogSync.Enabled {
		syncer, store := buildModelCatalogSync(deps.Config.ModelCatalogSync, queries)
		units = append(units, workers.NewModelCatalogSyncWorker(
			syncer,
			store,
			deps.Logger,
			deps.Config.ModelCatalogSync.Interval,
		))
		deps.Logger.Info("model catalog sync worker enabled", zap.String("interval", deps.Config.ModelCatalogSync.Interval.String()))
	}

	// 渠道自动检测复用与网关一致的 adapter/HTTP 探测链路（不走计费/请求记录），
	// 故 worker-server 需自建一份 adapter registry 供 channeltest 使用。
	// 开关 / 间隔 / 保留 / 探测超时均走运行时配置（系统设置 → 运营判定），始终注册 worker，
	// 由 RunOnce 现读 enabled 决定是否巡检（可热关停，无需重启）。
	adapterRegistry, err := NewAdapterRegistry(http.DefaultClient, deps.Logger, codexVersion)
	if err != nil {
		return nil, err
	}
	channelTestService := channeltest.NewService(queries, adapterRegistry, settingsStore, providerLedgerService)
	channelModelInventoryService := channelmodelinventory.NewService(
		deps.DB, queries, adapterRegistry, adapterRegistry, providerLedgerService, settingsStore,
	)
	// 池型渠道的巡检/发现/验证/403 复检同样以账号身份出站（与 admin 同一解析器实现）。
	probeIdentity := subscription.NewProbeIdentityResolver(queries, subscriptionOutbound)
	channelTestService.WithAccountResolver(probeIdentity)
	channelModelInventoryService.WithAccountResolver(probeIdentity)
	// 验证成功回填账号观测（水位 + LRU），与请求路径同一 Recorder。
	probeHealth := subscriptionhealth.NewRecorder(queries, permitStore, deps.Logger, 0).
		WithThresholdProvider(func(ctx context.Context) int32 {
			return appsettings.GatewayAccountUsagePauseThreshold(ctx, settingsStore)
		})
	channelTestService.WithAccountHealth(probeHealth)
	channelTestService.WithAccountRuntime(permitStore)
	channelModelInventoryService.WithAccountHealth(probeHealth)
	// 自动使用重置卡：按账号阈值主动查用量（/wham/usage，同一 Recorder 落快照并评估暂停），
	// 触顶且有卡时消费最早到期的一张。单实例 worker，幂等由确定性 redeem id + 状态指纹保证。
	quotaService := subscriptionquota.NewService(
		queries, probeIdentity,
		subscriptionquota.NewClient(accountProxyClients.ClientFor, codexVersion),
		probeHealth, deps.Logger,
	)
	units = append(units, subscriptionquota.NewAutoResetWorker(queries, quotaService, deps.Logger, subscriptionquota.AutoResetOptions{}))
	// 令牌刷新成功后顺带刷新账号画像（套餐 / 订阅到期 / 上游状态），让手工录入的到期日随之自动更正。
	tokenRefreshWorker.WithAfterRefresh(func(ctx context.Context, accountID int64) {
		if _, err := quotaService.Refresh(ctx, accountID); err != nil {
			deps.Logger.Warn("account profile refresh after token refresh failed",
				zap.Int64("account_id", accountID), zap.String("error_message", err.Error()))
		}
	})
	permissionStore := permitStore
	if err := permissionStore.VerifySingleNodeDeployment(ctx); err != nil {
		return nil, err
	}
	if err := permissionStore.Ping(ctx); err != nil {
		return nil, err
	}
	units = append(units, workers.NewPermissionRecheckWorker(
		permissionStore,
		channelTestService,
		settingsStore,
		defaultWorkerID("permission-recheck"),
		deps.Logger,
	))
	units = append(units, workers.NewChannelTestWorker(
		queries,
		workerChannelTester{svc: channelTestService},
		settingsStore,
		deps.Logger,
	))
	units = append(units,
		workers.NewChannelModelDiscoveryWorker(channelModelInventoryService, settingsStore, deps.Logger),
		workers.NewChannelModelVerificationWorker(channelModelInventoryService),
		// Codex 出站身份的版本跟随：每 6h 从 GitHub 同步官方最新正式版到 settings，
		// gateway/admin/worker 的出站身份随之热更新（Admin 覆写优先）。
		workers.NewCodexClientVersionSyncWorker(http.DefaultClient, settingsStore, deps.Logger),
	)

	// 多货币三件套（PLAN 5.6）：汇率日常同步（多源+合理性校验）、汇率变动后的存量毛利复查、
	// 每日账务对账。告警统一写 admin 消息中心（§12.C.3）。
	adminMessageService := adminmessage.NewService(queries)
	fxFetcher := &fx.Fetcher{Sources: []fx.Source{
		&fx.ExchangeRateAPISource{
			Client: http.DefaultClient,
			APIKey: func(ctx context.Context) string {
				return appsettings.GatewayExchangeRateAPIKey(ctx, settingsStore, deps.Config.ExchangeRate.APIKey)
			},
		},
		&fx.OpenERAPISource{Client: http.DefaultClient},
		&fx.FrankfurterSource{Client: http.DefaultClient},
	}}
	units = append(units,
		workers.NewFxRateSyncWorker(queries, fxFetcher, adminMessageService, deps.Logger, deps.Config.ExchangeRate.SyncInterval, exchangerate.SupportedQuotes),
		workers.NewFxMarginRecheckWorker(deps.DB, adminMessageService, deps.Logger),
		workers.NewFxReconciliationWorker(queries, adminMessageService, deps.Logger),
	)

	// 工单维护：resolved 超期自动关闭 + 孤儿附件清理（不依赖附件签名密钥，始终注册）。
	units = append(units, workers.NewTicketMaintenanceWorker(
		queries,
		deps.Logger,
		deps.Config.Ticket.MaintenanceInterval,
		deps.Config.Ticket.AutoCloseAfter,
		deps.Config.Ticket.AttachmentOrphanTTL,
	))
	channelTestCfg := appsettings.AdminBackendChannelTest(ctx, settingsStore)
	deps.Logger.Info("channel test worker registered",
		zap.Bool("enabled", channelTestCfg.Enabled),
		zap.String("interval", channelTestCfg.Interval.String()),
		zap.Int("log_retention_per_channel", channelTestCfg.LogRetentionPerChannel),
	)
	settlementRunner := workers.NewRunner(
		deps.Logger,
		deps.Config.Worker.RunnerIdleInterval,
		settlementUnits...,
	).WithName("settlement")
	maintenanceRunner := workers.NewRunner(
		deps.Logger,
		deps.Config.Worker.RunnerIdleInterval,
		units...,
	).WithName("maintenance")
	deps.Logger.Info("worker runners assembled",
		zap.Strings("settlement", unitNames(settlementUnits)),
		zap.Strings("maintenance", unitNames(units)),
	)

	return &WorkerServerApp{Runner: workers.NewGroup(settlementRunner, maintenanceRunner)}, nil
}

func unitNames(units []workers.Unit) []string {
	names := make([]string, 0, len(units))
	for _, unit := range units {
		if unit != nil {
			names = append(names, unit.Name())
		}
	}
	return names
}

// NewModelCatalogSyncer 装配一个独立的 models.dev 同步编排器，供 worker-server 子命令（如 sync-models）使用。
func NewModelCatalogSyncer(cfg config.ModelCatalogSyncConfig, db sqlc.DBTX) *modelcatalog.Syncer {
	syncer, _ := buildModelCatalogSync(cfg, sqlc.New(db))
	return syncer
}

func buildModelCatalogSync(cfg config.ModelCatalogSyncConfig, queries *sqlc.Queries) (*modelcatalog.Syncer, modelcatalog.SyncStore) {
	store := modelcatalog.NewSyncStore(queries)
	fetcher := modelcatalog.NewHTTPFetcher(cfg.BaseURL, cfg.HTTPTimeout, cfg.MaxResponseBytes)

	return modelcatalog.NewSyncer(fetcher, store), store
}

// workerChannelTester 把 channeltest.Service 适配成 workers.ChannelCredentialTester：
// worker 只需触发一次 source=worker 的检测（翻牌 + 写日志在 Service 内完成），不关心 TestResult。
type workerChannelTester struct {
	svc *channeltest.Service
}

func (t workerChannelTester) TestChannel(ctx context.Context, channelID int64) error {
	_, err := t.svc.Test(ctx, channeltest.TestInput{ChannelID: channelID, Source: channeltest.SourceWorker})
	return err
}

func defaultWorkerID(prefix string) string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}

	return fmt.Sprintf("%s:%s:%d", prefix, hostname, os.Getpid())
}
