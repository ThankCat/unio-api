// website-server 是营销站（unio-website）的公开只读 API 进程。
//
// 独立于 console-server：website 面是无鉴权、可公共缓存的高流量面（搜索爬虫、营销页），
// 与承载用户会话的 console 面隔离，缓存与限流策略互不牵连。
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

	app, err := bootstrap.NewWebsiteServerApp(startupCtx, bootstrap.WebsiteServerAppDeps{
		Logger: logger,
		Config: cfg,
		DB:     pgPool,
	})
	if err != nil {
		logger.Error("website server app failed", zap.Error(err))
		os.Exit(1)
	}
	server := &http.Server{
		Addr:              cfg.Website.HTTPAddr,
		Handler:           app.Handler,
		ReadHeaderTimeout: cfg.HTTP.ReadHeaderTimeout,
		ReadTimeout:       cfg.HTTP.AdminReadTimeout,
		WriteTimeout:      cfg.HTTP.WriteTimeout,
		IdleTimeout:       cfg.HTTP.IdleTimeout,
	}
	errCh := make(chan error, 1)
	go func() {
		logger.Info("website server starting", zap.String("addr", cfg.Website.HTTPAddr))
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
			logger.Error("website server failed", zap.Error(err))
			os.Exit(1)
		}
	case sig := <-shutdownCh:
		logger.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("website server shutdown failed", zap.Error(err))
		os.Exit(1)
	}
	if err := app.Shutdown(ctx); err != nil {
		logger.Error("website app shutdown failed", zap.Error(err))
	}
	logger.Info("website server stopped")
}
