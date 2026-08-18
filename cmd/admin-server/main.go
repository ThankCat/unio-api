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
	"github.com/ThankCat/unio-gateway/internal/platform/redis"
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

	// DB 启动期先检查数据库可用，避免服务带病启动。
	pgPool, err := store.OpenPostgres(startupCtx, cfg.DB)
	if err != nil {
		logger.Error("open postgres failed", failure.LogFields(err)...)
		os.Exit(1)
	}
	defer pgPool.Close()
	logger.Info("postgres connected")

	// Redis：运行时配置中枢(app_settings 实时缓存)需要;与 gateway 共享同一 Redis 实现跨进程秒级生效。
	redisClient, err := redis.OpenRedis(startupCtx, cfg.Redis)
	if err != nil {
		logger.Error("open redis failed", failure.LogFields(err)...)
		os.Exit(1)
	}
	defer redisClient.Close()
	logger.Info("redis connected", zap.String("addr", cfg.Redis.Addr), zap.Int("db", cfg.Redis.DB))

	// APP：装配时校验 ADMIN_USERNAME / ADMIN_PASSWORD，缺失/非法在此启动期失败。
	app, err := bootstrap.NewAdminServerApp(startupCtx, bootstrap.AdminServerAppDeps{
		Logger: logger,
		Config: cfg,
		DB:     pgPool,
		Redis:  redisClient,
	})
	if err != nil {
		logger.Error("admin server app failed", failure.LogFields(err)...)
		os.Exit(1)
	}

	server := newAdminHTTPServer(cfg.Admin.HTTPAddr, app.Handler, cfg.HTTP)

	errCh := make(chan error, 1)

	go func() {
		logger.Info("admin server starting", zap.String("addr", cfg.Admin.HTTPAddr))

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
			logger.Error("admin server failed", failure.LogFields(err)...)
			os.Exit(1)
		}
	case sig := <-shutdownCh:
		logger.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	if err := server.Shutdown(ctx); err != nil {
		logger.Error("admin server shutdown failed", failure.LogFields(err)...)
		os.Exit(1)
	}

	if err := app.Shutdown(ctx); err != nil {
		logger.Error("admin app shutdown failed", failure.LogFields(err)...)
	}

	logger.Info("admin server stopped")
}

func newAdminHTTPServer(addr string, handler http.Handler, httpConfig config.HTTPConfig) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: httpConfig.ReadHeaderTimeout,
		ReadTimeout:       httpConfig.AdminReadTimeout,
		// 普通 Admin 请求使用绝对写超时；渠道手动检测 handler 会清除绝对 deadline，改由 probe
		// context 管理执行时长，最终 JSON 写出仍受 httpx 单次写窗口限制。
		WriteTimeout: httpConfig.WriteTimeout,
		IdleTimeout:  httpConfig.IdleTimeout,
	}
}
