package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/ThankCat/unio-api/internal/bootstrap"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store"
)

func main() {
	preLogger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		preLogger.Error("load config failed", failure.LogArgs(err)...)
		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.Log.Level}))

	// 子命令分发：sync-models 手动触发一次目录同步（含 --dry-run 预演），其余进入常驻 runner。
	if len(os.Args) > 1 && os.Args[1] == "sync-models" {
		if err := runSyncModels(cfg, logger, os.Args[2:]); err != nil {
			logger.Error("sync-models failed", failure.LogArgs(err)...)
			os.Exit(1)
		}
		return
	}

	runWorkerServer(cfg, logger)
}

func runWorkerServer(cfg config.Config, logger *slog.Logger) {
	startupCtx, startupCancel := context.WithTimeout(context.Background(), cfg.Worker.StartupTimeout)
	defer startupCancel()

	pgPool, err := store.OpenPostgres(startupCtx, cfg.DB)
	if err != nil {
		logger.Error("open postgres failed", failure.LogArgs(err)...)
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
		logger.Error("worker app failed", failure.LogArgs(err)...)
		os.Exit(1)
	}

	runCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Info("worker server starting")
	if err := app.Runner.Run(runCtx); err != nil {
		logger.Error("worker server failed", failure.LogArgs(err)...)
		os.Exit(1)
	}
	logger.Info("worker server stopped")
}

// runSyncModels 解析 sync-models 子命令并手动执行一次 models.dev 同步。
func runSyncModels(cfg config.Config, logger *slog.Logger, args []string) error {
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
		"dry_run", result.DryRun,
		"feed_models", result.FeedModels,
		"inserted", result.Inserted,
		"updated", result.Updated,
		"skipped", result.Skipped,
		"removed", result.Removed,
		"capabilities_seeded", result.CapabilitiesSeeded,
		"manual_conflicts", len(result.ManualConflicts),
		"source_fingerprint", result.Fingerprint,
	)

	return nil
}
