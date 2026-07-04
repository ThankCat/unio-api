package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"os"

	"github.com/ThankCat/unio-api/internal/app/workers"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-api/internal/service/admin/channeltest"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

// WorkerServerAppDB 定义 worker server app 构建时需要的数据库能力。
type WorkerServerAppDB interface {
	sqlc.DBTX
	lifecycle.ChatTxBeginner
}

// WorkerServerAppDeps 表示构建 worker server app 需要的进程级依赖。
type WorkerServerAppDeps struct {
	Logger *slog.Logger
	Config config.Config
	DB     WorkerServerAppDB
}

// WorkerServerApp 表示当前 worker-server 进程已经装配完成的后台任务应用。
type WorkerServerApp struct {
	Runner *workers.Runner
}

// NewWorkerServerApp 装配当前 worker-server 进程的后台任务应用。
func NewWorkerServerApp(_ context.Context, deps WorkerServerAppDeps) (*WorkerServerApp, error) {
	queries := sqlc.New(deps.DB)
	ledgerService := ledger.NewService(deps.DB, queries)
	chatSettlementService := lifecycle.NewChatSettlementService(
		deps.DB,
		queries,
		billing.Service{},
		ledgerService,
	)
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
		deps.Logger,
		deps.Config.Worker.OrphanReservationSweepAgeThreshold,
		deps.Config.Worker.OrphanReservationSweepBatchSize,
	)

	units := []workers.Unit{settlementRecoveryWorker, orphanReservationSweeperWorker}

	if deps.Config.ModelCatalogSync.Enabled {
		syncer, store := buildModelCatalogSync(deps.Config.ModelCatalogSync, queries)
		units = append(units, workers.NewModelCatalogSyncWorker(
			syncer,
			store,
			deps.Logger,
			deps.Config.ModelCatalogSync.Interval,
		))
		deps.Logger.Info("model catalog sync worker enabled", "interval", deps.Config.ModelCatalogSync.Interval.String())
	}

	if deps.Config.ChannelTestWorker.Enabled {
		// 渠道自动检测复用与网关一致的 adapter/HTTP 探测链路（不走计费/请求记录），
		// 故 worker-server 需自建一份 adapter registry 供 channeltest 使用。
		adapterRegistry, err := NewAdapterRegistry(http.DefaultClient, deps.Logger)
		if err != nil {
			return nil, err
		}
		channelTestService := channeltest.NewService(queries, adapterRegistry)
		units = append(units, workers.NewChannelTestWorker(
			queries,
			workerChannelTester{svc: channelTestService},
			deps.Logger,
			deps.Config.ChannelTestWorker.Interval,
			deps.Config.ChannelTestWorker.LogRetentionPerChannel,
		))
		deps.Logger.Info("channel test worker enabled",
			"interval", deps.Config.ChannelTestWorker.Interval.String(),
			"log_retention_per_channel", deps.Config.ChannelTestWorker.LogRetentionPerChannel)
	}

	runner := workers.NewRunner(
		deps.Logger,
		deps.Config.Worker.RunnerIdleInterval,
		units...,
	)

	return &WorkerServerApp{Runner: runner}, nil
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
