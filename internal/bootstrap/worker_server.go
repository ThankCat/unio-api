package bootstrap

import (
	"context"
	"fmt"
	"log/slog"
	"os"

	"github.com/ThankCat/unio-api/internal/app/workers"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
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
	)

	runner := workers.NewRunner(
		deps.Logger,
		deps.Config.Worker.RunnerIdleInterval,
		settlementRecoveryWorker,
	)

	return &WorkerServerApp{Runner: runner}, nil
}

func defaultWorkerID(prefix string) string {
	hostname, err := os.Hostname()
	if err != nil || hostname == "" {
		hostname = "unknown-host"
	}

	return fmt.Sprintf("%s:%s:%d", prefix, hostname, os.Getpid())
}
