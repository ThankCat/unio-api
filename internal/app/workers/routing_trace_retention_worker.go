package workers

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

type RoutingTraceRetentionStore interface {
	DeleteExpiredRoutingDecisionTraces(context.Context, sqlc.DeleteExpiredRoutingDecisionTracesParams) (int64, error)
}

type RoutingTraceRetentionWorker struct {
	store     RoutingTraceRetentionStore
	settings  *appsettings.SettingsStore
	now       func() time.Time
	nextRunAt time.Time
	draining  bool
}

func NewRoutingTraceRetentionWorker(store RoutingTraceRetentionStore, settings *appsettings.SettingsStore) *RoutingTraceRetentionWorker {
	if store == nil {
		panic("workers: routing trace retention store is required")
	}
	return &RoutingTraceRetentionWorker{store: store, settings: settings, now: time.Now}
}

func (w *RoutingTraceRetentionWorker) Name() string {
	return "routing_trace_retention"
}

func (w *RoutingTraceRetentionWorker) RunOnce(ctx context.Context) (bool, error) {
	now := w.now()
	if !w.draining && now.Before(w.nextRunAt) {
		return false, nil
	}
	settings := appsettings.GatewayRoutingTrace(ctx, w.settings)
	w.draining = true
	deleted, err := w.store.DeleteExpiredRoutingDecisionTraces(ctx, sqlc.DeleteExpiredRoutingDecisionTracesParams{
		Cutoff:     pgtype.Timestamptz{Time: now.Add(-settings.Retention), Valid: true},
		BatchLimit: settings.CleanupBatchSize,
	})
	if err != nil {
		w.draining = false
		w.nextRunAt = now.Add(settings.CleanupInterval)
		return false, err
	}
	if deleted >= int64(settings.CleanupBatchSize) {
		return true, nil
	}
	w.draining = false
	w.nextRunAt = now.Add(settings.CleanupInterval)
	return deleted > 0, nil
}
