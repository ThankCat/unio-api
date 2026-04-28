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
)

func main() {
	logger := slog.New(slog.NewTextHandler(os.Stdout, nil))

	cfg := config.Load()
	handler := httpapi.NewRouter(logger)

	server := &http.Server{
		Addr:         cfg.HTTPAddr,
		Handler:      handler,
		ReadTimeout:  10 * time.Second,
		WriteTimeout: 30 * time.Second,
		IdleTimeout:  60 * time.Second,
	}

	errCh := make(chan error, 1)

	go func() {
		logger.Info("server starting", "addr", cfg.HTTPAddr)

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
