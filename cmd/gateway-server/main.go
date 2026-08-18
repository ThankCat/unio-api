package main

import (
	"context"
	"errors"
	"net"
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
	startupStartedAt := time.Now()
	preLogger := logging.MustNewEmergency()

	cfg, err := config.Load()
	if err != nil {
		preLogger.Error("gateway logger initialization failed", append(
			[]zap.Field{zap.String("phase", "config_load")}, failure.LogFields(err)...,
		)...)

		os.Exit(1)
	}

	logRuntime, err := logging.NewGateway(cfg.GatewayLog, cfg.Gateway.InstanceID)
	if err != nil {
		preLogger.Error("gateway logger initialization failed", append(
			[]zap.Field{zap.String("phase", "logger_init")}, failure.LogFields(err)...,
		)...)
		os.Exit(1)
	}
	defer func() { _ = logRuntime.Close() }()
	logger := logRuntime.Logger
	logging.Info(logger, "system", "service", "service starting",
		zap.String("addr", cfg.Gateway.HTTPAddr),
		zap.String("environment", cfg.GatewayLog.Environment),
		zap.String("environment_source", gatewayLogConfigSource("GATEWAY_ENV")),
		zap.String("baseline_level", cfg.GatewayLog.BaselineLevel.String()),
		zap.String("baseline_level_source", gatewayLogConfigSource("GATEWAY_LOG_LEVEL")),
		zap.Bool("console_enabled", cfg.GatewayLog.ConsoleEnabled),
		zap.String("console_enabled_source", gatewayLogConfigSource("GATEWAY_CONSOLE_ENABLED")),
	)

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startupCancel()

	// TODO(阶段1/production): [GAP-1-001] 启动超时仍硬编码且 readiness 尚未独立；公网部署前；将 startup timeout 纳入 config，并增加 readiness/metrics 暴露运行状态。
	// TODO(阶段2/production): [GAP-2-001] 启动前接入 migration runner（迁移执行器）或 schema 版本检查，避免服务连接到未迁移数据库。
	// DB 启动期先检查数据库可用，避免服务带病启动。
	pgPool, err := store.OpenPostgres(startupCtx, cfg.DB)
	if err != nil {
		logging.Error(logger, "system", "dependency", "dependency connection failed", append(
			[]zap.Field{zap.String("dependency", "postgres")}, failure.LogFields(err)...,
		)...)
		os.Exit(1)
	}
	defer pgPool.Close()
	logging.Info(logger, "system", "dependency", "dependency connected",
		zap.String("dependency", "postgres"),
	)

	// Redis
	redisClient, err := redis.OpenRedis(startupCtx, cfg.Redis)
	if err != nil {
		logging.Error(logger, "system", "dependency", "dependency connection failed", append(
			[]zap.Field{zap.String("dependency", "redis"), zap.String("addr", cfg.Redis.Addr), zap.Int("database", cfg.Redis.DB)},
			failure.LogFields(err)...,
		)...)
		os.Exit(1)
	}
	defer redisClient.Close()
	logging.Info(logger, "system", "dependency", "dependency connected",
		zap.String("dependency", "redis"), zap.String("addr", cfg.Redis.Addr), zap.Int("database", cfg.Redis.DB),
	)

	// APP
	app, err := bootstrap.NewGatewayServerApp(startupCtx, bootstrap.GatewayServerAppDeps{
		Logger:  logger,
		Logging: logRuntime,
		Config:  cfg,
		DB:      pgPool,
		Redis:   redisClient,
	})
	if err != nil {
		logging.Error(logger, "system", "service", "service start failed", append(
			[]zap.Field{zap.String("phase", "app_build")}, failure.LogFields(err)...,
		)...)
		os.Exit(1)
	}

	server := newGatewayHTTPServer(cfg.Gateway.HTTPAddr, app.Handler, cfg.HTTP)

	listener, err := net.Listen("tcp", cfg.Gateway.HTTPAddr)
	if err != nil {
		logging.Error(logger, "system", "service", "service start failed", append(
			[]zap.Field{zap.String("phase", "http_listen")}, failure.LogFields(err)...,
		)...)
		os.Exit(1)
	}
	logging.Info(logger, "system", "service", "service started",
		zap.String("addr", listener.Addr().String()),
		zap.Int64("startup_duration_ms", time.Since(startupStartedAt).Milliseconds()),
	)

	errCh := make(chan error, 1)

	go func() {
		if err := server.Serve(listener); err != nil && !errors.Is(err, http.ErrServerClosed) {
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
			logging.Error(logger, "system", "service", "service start failed", append(
				[]zap.Field{zap.String("phase", "http_serve")}, failure.LogFields(err)...,
			)...)
			os.Exit(1)
		}
	case sig := <-shutdownCh:
		// 收到 Ctrl+C / SIGTERM 时走这里
		logging.Info(logger, "system", "service", "shutdown signal received", zap.String("signal", sig.String()))
	}
	shutdownStartedAt := time.Now()

	// 给服务最多 cfg.HTTP.ShutdownTimeout 时间处理完正在进行的请求，然后再退出。
	ctx, cancel := context.WithTimeout(context.Background(), cfg.HTTP.ShutdownTimeout)
	defer cancel()

	// Shutdown 会停止接收新请求，并等待已有请求在 ctx 超时前完成。
	if err := server.Shutdown(ctx); err != nil {
		logging.Error(logger, "system", "service", "service shutdown failed", append(
			[]zap.Field{zap.String("phase", "http_server")}, failure.LogFields(err)...,
		)...)
		os.Exit(1)
	}

	// 关闭可观测性资源（flush 未导出的 trace span）。
	if err := app.Shutdown(ctx); err != nil {
		logging.Error(logger, "system", "service", "service shutdown failed", append(
			[]zap.Field{zap.String("phase", "app")}, failure.LogFields(err)...,
		)...)
	}

	logging.Info(logger, "system", "service", "service stopped",
		zap.Int64("shutdown_duration_ms", time.Since(shutdownStartedAt).Milliseconds()),
	)
}

func newGatewayHTTPServer(addr string, handler http.Handler, httpConfig config.HTTPConfig) *http.Server {
	return &http.Server{
		Addr:              addr,
		Handler:           handler,
		ReadHeaderTimeout: httpConfig.ReadHeaderTimeout,
		// 网关要透传 LLM 流式（SSE）与长补全：Go 的 WriteTimeout 是「从读完请求头起算的绝对
		// 截止时间」，心跳无法续期，>WriteTimeout 的响应（如 Codex 触发图像生成耗时数分钟）会被
		// 服务端中途掐断，客户端报 "error decoding response body"。故网关不设绝对读写超时，改由
		// Nginx 的请求头/请求体空闲超时、IdleTimeout（空闲 keep-alive）+ 每次上游调用的 context 超时
		// （渠道 response_timeout_ms / first_token_timeout_ms）兜底。实际下游写入由 httpx 设置
		// 单次 JSON deadline / SSE 滑动 deadline，避免慢客户端无限占用连接。
		ReadTimeout:  0,
		WriteTimeout: 0,
		IdleTimeout:  httpConfig.IdleTimeout,
	}
}

func gatewayLogConfigSource(key string) string {
	if os.Getenv(key) == "" {
		return "default"
	}
	return "environment"
}
