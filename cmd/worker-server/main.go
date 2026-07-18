package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/bootstrap"
	"github.com/ThankCat/unio-gateway/internal/core/modelcatalog"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/store"
)

func main() {
	preLogger := logging.MustNewConsole()

	cfg, err := config.Load()
	if err != nil {
		preLogger.Error("load config failed", failure.LogFields(err)...)
		os.Exit(1)
	}

	logger, err := logging.New(cfg.Log)
	if err != nil {
		preLogger.Error("init logger failed", failure.LogFields(err)...)
		os.Exit(1)
	}
	defer func() { _ = logger.Sync() }()

	// 子命令分发：sync-models 手动触发一次目录同步（含 --dry-run 预演），其余进入常驻 runner。
	if len(os.Args) > 1 && os.Args[1] == "sync-models" {
		if err := runSyncModels(cfg, logger, os.Args[2:]); err != nil {
			logger.Error("sync-models failed", failure.LogFields(err)...)
			os.Exit(1)
		}
		return
	}

	runWorkerServer(cfg, logger)
}

func runWorkerServer(cfg config.Config, logger *zap.Logger) {
	startupCtx, startupCancel := context.WithTimeout(context.Background(), cfg.Worker.StartupTimeout)
	defer startupCancel()

	pgPool, err := store.OpenPostgres(startupCtx, cfg.DB)
	if err != nil {
		logger.Error("open postgres failed", failure.LogFields(err)...)
		os.Exit(1)
	}
	defer pgPool.Close()
	logger.Info("postgres connected")

	app, err := bootstrap.NewWorkerServerApp(startupCtx, bootstrap.WorkerServerAppDeps{
		Logger: logger,
		Config: cfg,
		DB:     pgPool,
	})
	if err != nil {
		logger.Error("worker app failed", failure.LogFields(err)...)
		os.Exit(1)
	}

	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("worker server starting")
	if err := app.Runner.Run(runCtx); err != nil {
		logger.Error("worker server failed", failure.LogFields(err)...)
		os.Exit(1)
	}
	logger.Info("worker server stopped")
}

// runSyncModels 解析 sync-models 子命令并手动执行一次 models.dev 同步。
func runSyncModels(cfg config.Config, logger *zap.Logger, args []string) error {
	flags := flag.NewFlagSet("sync-models", flag.ContinueOnError)
	source := flags.String("source", "models-dev", "metadata source to sync (only models-dev is supported)")
	dryRun := flags.Bool("dry-run", false, "compute the merge plan without writing to the database")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *source != "models-dev" {
		return fmt.Errorf("unsupported source %q (only models-dev is supported)", *source)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	pgPool, err := store.OpenPostgres(ctx, cfg.DB)
	if err != nil {
		return err
	}
	defer pgPool.Close()

	syncer := bootstrap.NewModelCatalogSyncer(cfg.ModelCatalogSync, pgPool)

	result, err := syncer.Sync(ctx, modelcatalog.Options{DryRun: *dryRun})
	if err != nil {
		return err
	}

	logger.Info("sync-models completed",
		zap.Bool("dry_run", result.DryRun),
		zap.Int("feed_models", result.FeedModels),
		zap.Int("upserted", result.Upserted),
		zap.Int("removed", result.Removed),
		zap.Int("capability_hints", result.CapabilityHints),
		zap.String("source_fingerprint", result.Fingerprint),
	)

	return nil
}
