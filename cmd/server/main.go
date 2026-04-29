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

	"github.com/ThankCat/unio-api/internal/config"
	"github.com/ThankCat/unio-api/internal/httpapi"
	"github.com/ThankCat/unio-api/internal/redis"
	"github.com/ThankCat/unio-api/internal/store"
)

func main() {
	bootstrapLogger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg, err := config.Load()
	if err != nil {
		bootstrapLogger.Error("load config failed", "error", err)

		os.Exit(1)
	}

	logger := slog.New(slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: cfg.Log.Level}))

	startupCtx, startupCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer startupCancel()

	// DB 启动期先检查数据库可用，避免服务带病启动。
	pgPool, err := store.OpenPostgres(startupCtx, cfg.DB.URL)
	if err != nil {
		logger.Error("open postgres failed", "error", err)
		os.Exit(1)
	}
	defer pgPool.Close()
	logger.Info("postgres connected")

	// Redis
	redisClient, err := redis.OpenRedis(startupCtx, cfg.Redis)
	if err != nil {
		logger.Error("open redis failed", "error", err)
		os.Exit(1)
	}
	defer redisClient.Close()
	logger.Info("redis connected", "addr", cfg.Redis.Addr, "db", cfg.Redis.DB)

	handler := httpapi.NewRouter(logger)
	server := &http.Server{
		Addr:         cfg.HTTP.Addr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)

	go func() {
		logger.Info("server starting", "addr", cfg.HTTP.Addr)

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
			logger.Error("server failed", "error", err)
			os.Exit(1)
		}
	case sig := <-shutdownCh:
		// 收到 Ctrl+C / SIGTERM 时走这里
		logger.Info("shutdown signal received", "signal", sig.String())
	}

	// 给服务最多 10 秒时间处理完正在进行的请求，然后再退出。
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Shutdown 会停止接收新请求，并等待已有请求在 ctx 超时前完成。
	if err := server.Shutdown(ctx); err != nil {
		logger.Error("server shutdown failed", "error", err)
		os.Exit(1)
	}

	logger.Info("server stopped")
}
