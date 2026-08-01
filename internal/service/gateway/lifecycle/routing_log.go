package lifecycle

import (
	"context"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/logging"
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
	switch msg {
	case "routing candidate skipped":
		logging.Debug(r.logger, "routing", "candidate", msg, fields...)
	case "routing candidate transparent fallback skipped":
		logging.Debug(r.logger, "routing", "fallback", "transparent fallback skipped", fields...)
	case "routing capacity wait":
		logging.Debug(r.logger, "admission", "capacity_wait", "capacity wait completed", fields...)
	case "attempt permit abort result unknown", "stream attempt permit abort result unknown":
		logging.Error(r.logger, "admission", "permit", "attempt permit abort result unknown", fields...)
	case "attempt permit finish result unknown", "stream attempt permit finish result unknown":
		logging.Error(r.logger, "admission", "permit", "attempt permit finish result unknown", fields...)
	case "attempt runtime feedback failed", "stream attempt runtime feedback failed":
		return
	default:
		logging.Debug(r.logger, "routing", "decision", msg, fields...)
	}
}

func (r *StickyRouter) logSticky(ctx context.Context, msg string, fields ...zap.Field) {
	if r == nil || r.logger == nil {
		return
	}
	if f, ok := logfields.FromContext(ctx); ok {
		fields = append(f.ZapFields(), fields...)
	}
	switch msg {
	case "sticky pin_lost":
		logging.Warn(r.logger, "routing", "sticky", "sticky pin lost", fields...)
	case "sticky pinned_non_preferred":
		logging.Debug(r.logger, "routing", "sticky", "sticky pinned to non-preferred candidate", fields...)
	case "sticky preserve_on_temporary_bypass":
		logging.Debug(r.logger, "routing", "sticky", "sticky binding preserved during temporary bypass", fields...)
	case "sticky clear_if_current":
		logging.Debug(r.logger, "routing", "sticky", "sticky binding cleared", fields...)
	case "sticky operation failed":
		logging.Warn(r.logger, "routing", "sticky", msg, fields...)
	case "sticky operation conflicted", "sticky binding created", "sticky binding refreshed":
		logging.Debug(r.logger, "routing", "sticky", msg, fields...)
	default:
		logging.Debug(r.logger, "routing", "sticky", msg, fields...)
	}
}
