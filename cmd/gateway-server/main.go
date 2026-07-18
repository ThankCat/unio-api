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

	// TODO(阶段1/production): [GAP-1-001] 启动超时仍硬编码且 readiness 尚未独立；公网部署前；将 startup timeout 纳入 config，并增加 readiness/metrics 暴露运行状态。
	// TODO(阶段2/production): [GAP-2-001] 启动前接入 migration runner（迁移执行器）或 schema 版本检查，避免服务连接到未迁移数据库。
	// DB 启动期先检查数据库可用，避免服务带病启动。
	pgPool, err := store.OpenPostgres(startupCtx, cfg.DB)
	if err != nil {
		logger.Error("open postgres failed", failure.LogFields(err)...)
		os.Exit(1)
	}
	defer pgPool.Close()
	logger.Info("postgres connected")

	// Redis
	redisClient, err := redis.OpenRedis(startupCtx, cfg.Redis)
	if err != nil {
		logger.Error("open redis failed", failure.LogFields(err)...)
		os.Exit(1)
	}
	defer redisClient.Close()
	logger.Info("redis connected", zap.String("addr", cfg.Redis.Addr), zap.Int("db", cfg.Redis.DB))

	// APP
	app, err := bootstrap.NewGatewayServerApp(startupCtx, bootstrap.GatewayServerAppDeps{
		Logger: logger,
		Config: cfg,
		DB:     pgPool,
		Redis:  redisClient,
	})
	if err != nil {
		logger.Error("server app failed", failure.LogFields(err)...)
		os.Exit(1)
	}

	server := &http.Server{
		Addr:    cfg.Gateway.HTTPAddr,
		Handler: app.Handler,

		ReadTimeout: cfg.HTTP.ReadTimeout,
		// 网关要透传 LLM 流式（SSE）与长补全：Go 的 WriteTimeout 是「从读完请求头起算的绝对
		// 截止时间」，心跳无法续期，>WriteTimeout 的响应（如 Codex 触发图像生成耗时数分钟）会被
		// 服务端中途掐断，客户端报 "error decoding response body"。故网关不设绝对写超时，改由
		// ReadTimeout（读请求）+ IdleTimeout（空闲 keep-alive）+ 每次上游调用的 context 超时
		// （渠道 timeout_ms）兜底。admin-server 仍按 cfg.HTTP.WriteTimeout 设置（纯 CRUD 无长响应）。
		WriteTimeout: 0,
		IdleTimeout:  cfg.HTTP.IdleTimeout,
	}

	errCh := make(chan error, 1)

	go func() {
		logger.Info("server starting", zap.String("addr", cfg.Gateway.HTTPAddr))

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
			// 服务启动失败时走这里
			logger.Error("server failed", failure.LogFields(err)...)
			os.Exit(1)
		}
	case sig := <-shutdownCh:
		// 收到 Ctrl+C / SIGTERM 时走这里
		logger.Info("shutdown signal received", zap.String("signal", sig.String()))
	}

	// 给服务最多 cfg.HTTP.ShutdownTimeout 时间处理完正在进行的请求，然后再退出。
	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	// Shutdown 会停止接收新请求，并等待已有请求在 ctx 超时前完成。
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("server shutdown failed", failure.LogFields(err)...)
		os.Exit(1)
	}

	// 关闭可观测性资源（flush 未导出的 trace span）。
	if err := app.Shutdown(ctx); err != nil {
		logger.Error("app shutdown failed", failure.LogFields(err)...)
	}

	logger.Info("server stopped")
}
