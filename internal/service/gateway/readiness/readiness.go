// Package readiness 实现 Gateway 动态就绪门禁。liveness 不依赖本包，Redis/PostgreSQL
// 故障时进程仍保持存活，但每次 /readyz 都会重新强读权威事实。
package readiness

import (
	"context"
	"sync"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"go.uber.org/zap"
)

type Queries interface {
	GetGatewayRuntimeReadinessSnapshot(ctx context.Context) (sqlc.GetGatewayRuntimeReadinessSnapshotRow, error)
}

type Store interface {
	Ping(ctx context.Context) error
	CheckRuntimeReadiness(ctx context.Context, in breakerstore.RuntimeReadinessInput) (breakerstore.RuntimeReadinessResult, error)
	ClearRuntimeInfrastructureFaultAfterReconciliation(ctx context.Context, in breakerstore.RuntimeReadinessInput, proof breakerstore.RuntimeReconciliationProof) (breakerstore.RuntimeReadinessResult, error)
}

type Metrics interface {
	SetBreakerStoreHealth(ready, unavailable bool)
	SetRuntimeStateIntegrity(state string)
}

type Checker struct {
	queries Queries
	store   Store
	logger  *zap.Logger
	metrics Metrics

	mu            sync.Mutex
	observed      bool
	lastReady     bool
	lastReason    string
	lastIntegrity string
}

func NewChecker(queries Queries, store Store) *Checker {
	return NewCheckerWithObservability(queries, store, nil, nil)
}

// NewCheckerWithObservability attaches transition-only structured logs and current-state gauges.
// Metrics are refreshed on every probe; logs are emitted only when readiness or its reason changes.
func NewCheckerWithObservability(queries Queries, store Store, logger *zap.Logger, recorder Metrics) *Checker {
	if queries == nil || store == nil {
		panic("readiness: queries and store are required")
	}
	return &Checker{queries: queries, store: store, logger: logger, metrics: recorder}
}

// Check 只返回稳定的内部 reason code；HTTP 层不向外暴露 epoch、revision 或 payload。
func (c *Checker) Check(ctx context.Context) (bool, string) {
	in, reason, integrity, _, ok := c.expectedRuntime(ctx, false)
	if !ok {
		return c.finish(false, reason, false, integrity)
	}
	if err := c.store.Ping(ctx); err != nil {
		return c.finish(false, "redis_unavailable", true, "lost")
	}
	result, err := c.store.CheckRuntimeReadiness(ctx, in)
	if err != nil {
		return c.finish(false, "redis_unavailable", true, "lost")
	}
	if !result.Ready {
		reason := result.Reason
		if reason == "" {
			reason = "runtime_not_ready"
		}
		unavailable := reason == breakerstore.RuntimeReadinessReasonStoreFaultLatched
		return c.finish(false, reason, unavailable, runtimeIntegrityForReason(reason))
	}
	return c.finish(true, "ready", false, "ready")
}

// ClearStoreFaultAfterReconciliation is called only by the background reconciler after it has
// strictly reconciled every Origin fence, Channel admission control, critical setting, and
// durable operation. A regular /readyz probe never invokes this mutation.
func (c *Checker) ClearStoreFaultAfterReconciliation(
	ctx context.Context,
	proof breakerstore.RuntimeReconciliationProof,
) (bool, string) {
	in, reason, integrity, maintenanceSmoke, ok := c.expectedRuntime(ctx, true)
	if !ok {
		return c.finish(false, reason, false, integrity)
	}
	if err := c.store.Ping(ctx); err != nil {
		return c.finish(false, "redis_unavailable", true, "lost")
	}
	result, err := c.store.ClearRuntimeInfrastructureFaultAfterReconciliation(ctx, in, proof)
	if err != nil {
		return c.finish(false, "redis_unavailable", true, "lost")
	}
	if !result.Ready {
		reason := result.Reason
		if reason == "" {
			reason = "runtime_not_ready"
		}
		unavailable := reason == breakerstore.RuntimeReadinessReasonStoreFaultLatched || reason == "fault_changed"
		return c.finish(false, reason, unavailable, runtimeIntegrityForReason(reason))
	}
	if maintenanceSmoke {
		// The reconciliation proof is sufficient to clear the infrastructure latch so an
		// ingress-isolated post-commit smoke can run. The durable awaiting_release lock
		// still keeps ordinary /readyz probes closed until ReleaseRecovery succeeds.
		c.finish(false, "runtime_operation_pending", false, "ready")
		return true, "maintenance_smoke_ready"
	}
	return c.finish(true, "ready", false, "ready")
}

func (c *Checker) expectedRuntime(
	ctx context.Context,
	allowMaintenanceSmoke bool,
) (breakerstore.RuntimeReadinessInput, string, string, bool, bool) {
	row, err := c.queries.GetGatewayRuntimeReadinessSnapshot(ctx)
	if err != nil {
		return breakerstore.RuntimeReadinessInput{}, "postgres_unavailable", "lost", false, false
	}
	epoch, err := runtimecontrol.DecodeStateEpoch(row.RuntimeStateEpochValue)
	if err != nil || epoch.State != runtimecontrol.StateEpochReady || row.RuntimeStateEpochRevision < 1 {
		return breakerstore.RuntimeReadinessInput{}, "epoch_not_ready", "lost", false, false
	}
	if row.RouteRateLimitDefaultsRevision < 1 || row.ChannelRateLimitDefaultsRevision < 1 ||
		row.ConcurrencyDefaultsRevision < 1 ||
		row.CircuitBreakerRevision < 1 || row.RoutingBalanceRevision < 1 {
		return breakerstore.RuntimeReadinessInput{}, "control_revision_invalid", "ready", false, false
	}
	maintenanceSmoke := !row.RuntimeOperationsReconciled && row.RuntimeMaintenanceSmokeAllowed
	if !row.RuntimeOperationsReconciled && (!allowMaintenanceSmoke || !maintenanceSmoke) {
		return breakerstore.RuntimeReadinessInput{}, "runtime_operation_pending", "ready", false, false
	}
	return breakerstore.RuntimeReadinessInput{
		Epoch:                    epoch.Epoch,
		EpochRevision:            row.RuntimeStateEpochRevision,
		RouteRateLimitRevision:   row.RouteRateLimitDefaultsRevision,
		ChannelRateLimitRevision: row.ChannelRateLimitDefaultsRevision,
		ConcurrencyRevision:      row.ConcurrencyDefaultsRevision,
		CircuitBreakerRevision:   row.CircuitBreakerRevision,
		RoutingBalanceRevision:   row.RoutingBalanceRevision,
	}, "", "", maintenanceSmoke, true
}

func (c *Checker) finish(ready bool, reason string, unavailable bool, integrity string) (bool, string) {
	if c.metrics != nil {
		c.metrics.SetBreakerStoreHealth(ready, unavailable)
		c.metrics.SetRuntimeStateIntegrity(integrity)
	}

	c.mu.Lock()
	changed := !c.observed || c.lastReady != ready || c.lastReason != reason || c.lastIntegrity != integrity
	c.observed = true
	c.lastReady = ready
	c.lastReason = reason
	c.lastIntegrity = integrity
	c.mu.Unlock()

	if changed && c.logger != nil {
		fields := []zap.Field{
			zap.Bool("ready", ready),
			zap.String("reason", reason),
			zap.Bool("breaker_store_unavailable", unavailable),
			zap.String("runtime_state_integrity", integrity),
		}
		if ready {
			c.logger.Info("gateway readiness changed", fields...)
		} else {
			c.logger.Warn("gateway readiness changed", fields...)
		}
	}
	return ready, reason
}

func runtimeIntegrityForReason(reason string) string {
	switch reason {
	case "marker_absent", "marker_not_ready", "marker_mismatch", "runtime_not_ready",
		breakerstore.RuntimeReadinessReasonStoreFaultLatched, "fault_changed":
		return "lost"
	default:
		return "ready"
	}
}
