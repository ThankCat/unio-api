package bootstrap

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

const runtimeControlFailureLogInterval = 30 * time.Second

var runtimeControlMetricTargets = [...]string{
	"channel_capacity",
	"route_rate",
	"global_concurrency",
	"circuit_breaker",
	"routing_balance",
}

type providerOperationObservation struct {
	operation sqlc.ProviderRoutingOperation
	envelope  runtimecontrol.ProviderRoutingEnvelope
	age       time.Duration
}

type runtimeControlReconcileObservation struct {
	runtimeOperations  []sqlc.RuntimeControlOperation
	providerOperations []providerOperationObservation
}

type runtimeControlTelemetry struct {
	metrics *metrics.Metrics
	logger  *zap.Logger
	now     func() time.Time

	mu                   sync.Mutex
	lastFailureLog       time.Time
	lastFailureSignature string
	suppressedFailures   int
}

func newRuntimeControlTelemetry(recorder *metrics.Metrics, logger *zap.Logger) *runtimeControlTelemetry {
	if recorder == nil && logger == nil {
		return nil
	}
	return &runtimeControlTelemetry{metrics: recorder, logger: logger, now: time.Now}
}

func observeRuntimeStateEpochEnsure(
	recorder *metrics.Metrics,
	logger *zap.Logger,
	result runtimecontrol.StateEpochEnsureResult,
) {
	integrity := "lost"
	if result.State == runtimecontrol.StateEpochEnsureReady &&
		result.Record.Value.State == runtimecontrol.StateEpochReady {
		integrity = "ready"
	}
	if recorder != nil {
		recorder.SetRuntimeStateIntegrity(integrity)
		recorder.SetBreakerStoreHealth(false, false)
		if result.OperationToken != "" &&
			(result.Record.Value.Reason == runtimecontrol.StateEpochReasonStateLoss ||
				result.Record.Value.Reason == runtimecontrol.StateEpochReasonRestore) {
			recorder.IncRuntimeStateLossRecovery(stateEpochRecoveryResult(result.State))
		}
	}
	if logger == nil {
		return
	}
	fields := []zap.Field{
		zap.String("epoch", result.Record.Value.Epoch),
		zap.Int64("revision", result.Record.Revision),
		zap.Bool("created", result.Created),
		zap.String("runtime_state_integrity", integrity),
	}
	if integrity == "ready" {
		logging.Info(logger, "runtime", "state", "runtime state epoch ensured", fields...)
	} else {
		fields = append(fields, zap.String("reason", string(result.Record.Value.Reason)))
		logging.Warn(logger, "runtime", "state", "runtime state epoch not ready", fields...)
	}
}

func stateEpochRecoveryResult(state runtimecontrol.StateEpochEnsureState) string {
	if state == runtimecontrol.StateEpochEnsureReady {
		return "committed"
	}
	return "not_ready"
}

func (t *runtimeControlTelemetry) capture(
	ctx context.Context,
	pool *pgxpool.Pool,
) runtimeControlReconcileObservation {
	if t == nil {
		return runtimeControlReconcileObservation{}
	}
	q := sqlc.New(pool)
	observation := runtimeControlReconcileObservation{}

	runtimeOperations, err := q.ListNonterminalRuntimeControlOperations(ctx)
	if err != nil {
		t.logFailure("observe_runtime_operations", err)
	} else {
		observation.runtimeOperations = runtimeOperations
		t.observeRuntimePending(runtimeOperations)
	}

	providerOperations, err := q.ListNonterminalProviderRoutingOperations(ctx)
	if err != nil {
		t.logFailure("observe_origin_operations", err)
		return observation
	}
	for _, operation := range providerOperations {
		envelope, parseErr := runtimecontrol.ParseProviderRoutingEnvelope(operation.Transitions)
		if parseErr != nil {
			t.logFailure("observe_origin_operation", parseErr)
			continue
		}
		age := t.age(operation.CreatedAt.Time, operation.CreatedAt.Valid)
		observation.providerOperations = append(observation.providerOperations, providerOperationObservation{
			operation: operation,
			envelope:  envelope,
			age:       age,
		})
		t.observeProviderPending(envelope, age)
	}
	return observation
}

func (t *runtimeControlTelemetry) observeRuntimePending(operations []sqlc.RuntimeControlOperation) {
	if t.metrics == nil {
		return
	}
	type pendingState struct {
		pending bool
		age     time.Duration
	}
	states := make(map[string]pendingState, len(runtimeControlMetricTargets))
	for _, target := range runtimeControlMetricTargets {
		states[target] = pendingState{}
	}
	for _, operation := range operations {
		target := runtimeControlMetricTarget(operation)
		if target == "" {
			continue
		}
		state := states[target]
		age := t.age(operation.CreatedAt.Time, operation.CreatedAt.Valid)
		if !state.pending || age > state.age {
			state.age = age
		}
		state.pending = true
		states[target] = state
	}
	for _, target := range runtimeControlMetricTargets {
		state := states[target]
		t.metrics.SetRuntimeControlPending(target, state.pending, state.age)
	}
}

func (t *runtimeControlTelemetry) observeProviderPending(
	envelope runtimecontrol.ProviderRoutingEnvelope,
	age time.Duration,
) {
	if t.metrics == nil {
		return
	}
	providerID := strconv.FormatInt(envelope.ProviderID, 10)
	switch envelope.Kind {
	case runtimecontrol.ProviderFenceKindOrigin:
		t.metrics.SetOriginRevisionFence(providerID, "pending", age)
	case runtimecontrol.ProviderFenceKindStatus:
		t.metrics.SetProviderStatusRevisionFence(providerID, "pending", age)
	}
}

func (t *runtimeControlTelemetry) passFailed(
	phase string,
	err error,
	observation runtimeControlReconcileObservation,
) {
	if t == nil {
		return
	}
	if t.metrics != nil {
		for _, operation := range observation.runtimeOperations {
			target := runtimeControlMetricTarget(operation)
			if target == "" {
				continue
			}
			t.metrics.IncRuntimeControlOperation(target, "reconcile", "failed")
			t.metrics.IncRuntimeControlRecovery(target, "failed")
		}
	}
	t.logFailure(phase, err)
}

func (t *runtimeControlTelemetry) passSucceeded(observation runtimeControlReconcileObservation) {
	if t == nil {
		return
	}
	for _, operation := range observation.runtimeOperations {
		target := runtimeControlMetricTarget(operation)
		if target == "" {
			continue
		}
		result := recoveredOperationResult(operation.State)
		if t.metrics != nil {
			t.metrics.IncRuntimeControlOperation(target, "reconcile", result)
			t.metrics.IncRuntimeControlRecovery(target, result)
			t.metrics.SetRuntimeControlPending(target, false, 0)
		}
		if t.logger != nil {
			fields := []zap.Field{
				zap.String("target", target),
				zap.String("kind", operation.Kind),
				zap.String("operation_state", operation.State),
				zap.String("result", result),
				zap.Int64("revision", operation.NextRevision),
			}
			if operation.ChannelID.Valid {
				fields = append(fields, zap.Int64("channel_id", operation.ChannelID.Int64))
			}
			logging.Info(t.logger, "runtime", "state", "runtime control reconciled", fields...)
		}
	}
	for _, observed := range observation.providerOperations {
		if t.logger == nil {
			continue
		}
		logging.Info(t.logger, "runtime", "state", "runtime control reconciled",
			zap.String("target", "provider"),
			zap.String("kind", observed.operation.Kind),
			zap.String("operation_state", observed.operation.State),
			zap.String("result", recoveredOperationResult(observed.operation.State)),
			zap.Int64("provider_id", observed.envelope.ProviderID),
			zap.Int64("revision", providerOperationRevision(observed.envelope)),
		)
	}
	t.mu.Lock()
	t.lastFailureSignature = ""
	t.suppressedFailures = 0
	t.mu.Unlock()
}

func (t *runtimeControlTelemetry) ProviderControlReconciled(
	providerID, originRevision, statusRevision int64,
	status string,
	restored bool,
) {
	if t == nil {
		return
	}
	id := strconv.FormatInt(providerID, 10)
	if t.metrics != nil {
		t.metrics.SetOriginRevisionFence(id, "active", 0)
		t.metrics.SetProviderStatusRevisionFence(id, "active", 0)
	}
	if restored && t.logger != nil {
		logging.Info(t.logger, "runtime", "state", "runtime control restored",
			zap.String("target", "provider"),
			zap.Int64("provider_id", providerID),
			zap.Int64("origin_revision", originRevision),
			zap.Int64("status_revision", statusRevision),
			zap.String("status", status),
		)
	}
}

func (t *runtimeControlTelemetry) criticalSettingReconciled(settingKey string, revision int64, restored bool) {
	if t == nil {
		return
	}
	target := runtimeControlMetricTargetForSetting(settingKey)
	if target == "" {
		return
	}
	if t.metrics != nil {
		t.metrics.SetRuntimeControlPending(target, false, 0)
		if restored {
			t.metrics.IncRuntimeControlOperation(target, "reconcile", "restored")
			t.metrics.IncRuntimeControlRecovery(target, "restored")
		}
	}
	if restored && t.logger != nil {
		logging.Info(t.logger, "runtime", "state", "runtime control restored",
			zap.String("target", "critical_control"),
			zap.String("control", target),
			zap.Int64("revision", revision),
		)
	}
}

func (t *runtimeControlTelemetry) channelControlReconciled(channelID, revision int64, restored bool) {
	if t == nil {
		return
	}
	if t.metrics != nil {
		t.metrics.SetRuntimeControlPending("channel_capacity", false, 0)
		if restored {
			t.metrics.IncRuntimeControlOperation("channel_capacity", "reconcile", "restored")
			t.metrics.IncRuntimeControlRecovery("channel_capacity", "restored")
		}
	}
	if restored && t.logger != nil {
		logging.Info(t.logger, "runtime", "state", "runtime control restored",
			zap.String("target", "channel_capacity"),
			zap.Int64("channel_id", channelID),
			zap.Int64("revision", revision),
		)
	}
}

func (t *runtimeControlTelemetry) logFailure(phase string, err error) {
	if t == nil || t.logger == nil || err == nil || errors.Is(err, context.Canceled) {
		return
	}
	now := t.now()
	signature := phase + ":" + err.Error()

	t.mu.Lock()
	if signature == t.lastFailureSignature && now.Sub(t.lastFailureLog) < runtimeControlFailureLogInterval {
		t.suppressedFailures++
		t.mu.Unlock()
		return
	}
	suppressed := t.suppressedFailures
	t.lastFailureLog = now
	t.lastFailureSignature = signature
	t.suppressedFailures = 0
	t.mu.Unlock()

	logging.Error(t.logger, "runtime", "state", "runtime control reconciliation failed",
		zap.String("phase", phase),
		zap.Int("suppressed_failures", suppressed),
		zap.String("error_code", "runtime_control_reconciliation_failed"),
		zap.String("error_category", "runtime_state"),
		zap.String("error_message", err.Error()),
	)
}

func (t *runtimeControlTelemetry) age(createdAt time.Time, valid bool) time.Duration {
	if !valid {
		return 0
	}
	age := t.now().Sub(createdAt)
	if age < 0 {
		return 0
	}
	return age
}

func runtimeControlMetricTarget(operation sqlc.RuntimeControlOperation) string {
	switch operation.Kind {
	case runtimecontrol.KindChannelCapacity:
		return "channel_capacity"
	case runtimecontrol.KindAppSetting:
		if operation.SettingKey.Valid {
			return runtimeControlMetricTargetForSetting(operation.SettingKey.String)
		}
	}
	return ""
}

func runtimeControlMetricTargetForSetting(settingKey string) string {
	switch settingKey {
	case appsettings.GatewayRouteRateLimitDefaultsKey:
		return "route_rate"
	case appsettings.GatewayConcurrencyDefaultsKey:
		return "global_concurrency"
	case appsettings.GatewayCircuitBreakerKey:
		return "circuit_breaker"
	case appsettings.GatewayRoutingBalanceKey:
		return "routing_balance"
	default:
		return ""
	}
}

func recoveredOperationResult(state string) string {
	if state == "db_committed" {
		return "committed"
	}
	return "aborted"
}

func providerOperationRevision(envelope runtimecontrol.ProviderRoutingEnvelope) int64 {
	if envelope.Kind == runtimecontrol.ProviderFenceKindOrigin {
		return envelope.NextOriginRevision
	}
	return envelope.NextStatusRevision
}
