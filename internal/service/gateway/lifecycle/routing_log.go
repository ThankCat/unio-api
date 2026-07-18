package lifecycle

import (
	"context"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/observability/logfields"
)

// SetLogger 注入路由可观测日志器（sticky / skip / wait / failover）。nil 表示不打日志。
func (r *AttemptRunner) SetLogger(logger *zap.Logger) {
	if r == nil {
		return
	}
	if logger == nil {
		r.logger = nil
		return
	}
	r.logger = logger
}

// SetLogger 注入 sticky 决策日志器。nil 表示不打日志。
func (r *StickyRouter) SetLogger(logger *zap.Logger) {
	if r == nil {
		return
	}
	if logger == nil {
		r.logger = nil
		return
	}
	r.logger = logger
}

func (r *AttemptRunner) logRouting(ctx context.Context, msg string, fields ...zap.Field) {
	if r == nil || r.logger == nil {
		return
	}
	if f, ok := logfields.FromContext(ctx); ok {
		fields = append(f.ZapFields(), fields...)
	}
	r.logger.Info(msg, fields...)
}

func (r *StickyRouter) logSticky(ctx context.Context, msg string, fields ...zap.Field) {
	if r == nil || r.logger == nil {
		return
	}
	if f, ok := logfields.FromContext(ctx); ok {
		fields = append(f.ZapFields(), fields...)
	}
	r.logger.Info(msg, fields...)
}
