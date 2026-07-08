package main

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/ThankCat/unio-api/internal/bootstrap"
	"github.com/ThankCat/unio-api/internal/platform/config"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/redis"
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

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startupCancel()

	// DB 启动期先检查数据库可用，避免服务带病启动。
	pgPool, err := store.OpenPostgres(startupCtx, cfg.DB)
	if err != nil {
		logger.Error("open postgres failed", failure.LogArgs(err)...)
		os.Exit(1)
	}
	defer pgPool.Close()
	logger.Info("postgres connected")

	// Redis：运行时配置中枢(app_settings 实时缓存)需要;与 gateway 共享同一 Redis 实现跨进程秒级生效。
	redisClient, err := redis.OpenRedis(startupCtx, cfg.Redis)
	if err != nil {
		logger.Error("open redis failed", failure.LogArgs(err)...)
		os.Exit(1)
	}
	defer redisClient.Close()
	logger.Info("redis connected", "addr", cfg.Redis.Addr, "db", cfg.Redis.DB)

	// APP：装配时校验 ADMIN_API_TOKEN 与 CREDENTIAL_MASTER_KEY，缺失/非法在此启动期失败。
	app, err := bootstrap.NewAdminServerApp(startupCtx, bootstrap.AdminServerAppDeps{
		Logger: logger,
		Config: cfg,
		DB:     pgPool,
		Redis:  redisClient,
	})
	if err != nil {
		logger.Error("admin server app failed", failure.LogArgs(err)...)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:    cfg.Admin.HTTPAddr,
		Handler: app.Handler,

		ReadTimeout:  cfg.HTTP.ReadTimeout,
		WriteTimeout: cfg.HTTP.WriteTimeout,
		IdleTimeout:  cfg.HTTP.IdleTimeout,
	}

	errCh := make(chan error, 1)

	go func() {
		logger.Info("admin server starting", "addr", cfg.Admin.HTTPAddr)

		if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}

		close(errCh)
	}()

	shutdownCh := make(chan os.Signal, 1)
	signal.Notify(shutdownCh, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(shutdownCh)

	select {
	case err, ok := <-errCh:
		if ok && err != nil {
			logger.Error("admin server failed", failure.LogArgs(err)...)
			os.Exit(1)
		}
	case sig := <-shutdownCh:
		logger.Info("shutdown signal received", "signal", sig.String())
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("admin server shutdown failed", failure.LogArgs(err)...)
		os.Exit(1)
	}

	if err := app.Shutdown(ctx); err != nil {
		logger.Error("admin app shutdown failed", failure.LogArgs(err)...)
	}

	logger.Info("admin server stopped")
}
