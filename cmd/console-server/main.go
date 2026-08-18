package main

import (
	"context"
	"errors"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/bootstrap"
	"github.com/ThankCat/unio-gateway/internal/platform/config"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	platformredis "github.com/ThankCat/unio-gateway/internal/platform/redis"
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

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startupCancel()
	pgPool, err := store.OpenPostgres(startupCtx, cfg.DB)
	if err != nil {
		logger.Error("open postgres failed", failure.LogFields(err)...)
		os.Exit(1)
	}
	defer pgPool.Close()
	redisClient, err := platformredis.OpenRedis(startupCtx, cfg.Redis)
	if err != nil {
		logger.Error("open redis failed", failure.LogFields(err)...)
		os.Exit(1)
	}
	defer redisClient.Close()

	app, err := bootstrap.NewConsoleServerApp(startupCtx, bootstrap.ConsoleServerAppDeps{
		Logger: logger,
		Config: cfg,
		DB:     pgPool,
		Redis:  redisClient,
	})
	if err != nil {
		logger.Error("console server app failed", zap.Error(err))
		os.Exit(1)
	}
	server := newConsoleHTTPServer(cfg.Console.HTTPAddr, app.Handler, cfg.HTTP)
	errCh := make(chan error, 1)
	go func() {
		logger.Info("console server starting", zap.String("addr", cfg.Console.HTTPAddr))
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
			logger.Error("console server failed", zap.Error(err))
			os.Exit(1)
		}
	case sig := <-shutdownCh:
		logger.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("console server shutdown failed", zap.Error(err))
		os.Exit(1)
	}
	if err := app.Shutdown(ctx); err != nil {
		logger.Error("console app shutdown failed", zap.Error(err))
	}
	logger.Info("console server stopped")
}

func newConsoleHTTPServer(addr string, handler http.Handler, httpConfig config.HTTPConfig) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: httpConfig.ReadHeaderTimeout,
		ReadTimeout:       httpConfig.AdminReadTimeout,
		WriteTimeout:      httpConfig.WriteTimeout,
		IdleTimeout:       httpConfig.IdleTimeout,
	}
}
