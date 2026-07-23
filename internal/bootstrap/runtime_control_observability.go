package bootstrap

import (
	"context"
	"strconv"
	"sync"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

const runtimeControlFailureLogInterval = 30 * time.Second

var runtimeControlMetricTargets = [...]string{
	"channel_admission",
	"route_rate",
	"channel_rate",
	"global_concurrency",
	"circuit_breaker",
	"routing_balance",
}

type endpointOperationObservation struct {
	operation sqlc.EndpointRoutingOperation
	envelope  runtimecontrol.EndpointRoutingEnvelope
	age       time.Duration
}

type runtimeControlReconcileObservation struct {
	runtimeOperations  []sqlc.RuntimeControlOperation
	endpointOperations []endpointOperationObservation
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
		zap.String("ensure_state", string(result.State)),
		zap.String("epoch_state", string(result.Record.Value.State)),
		zap.String("reason", string(result.Record.Value.Reason)),
		zap.Int64("revision", result.Record.Revision),
		zap.Bool("created", result.Created),
		zap.String("runtime_state_integrity", integrity),
	}
	if integrity == "ready" {
		logger.Info("runtime state epoch ensured", fields...)
	} else {
		logger.Warn("runtime state epoch not ready", fields...)
	}
}

func stateEpochRecoveryResult(state runtimecontrol.StateEpochEnsureState) string {
	switch state {
	case runtimecontrol.StateEpochEnsureReady:
		return "committed"
	case runtimecontrol.StateEpochEnsureAwaitingMaintenance:
		return "awaiting_maintenance"
	default:
		return "not_ready"
	}
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

	endpointOperations, err := q.ListNonterminalEndpointRoutingOperations(ctx)
	if err != nil {
		t.logFailure("observe_endpoint_operations", err)
		return observation
	}
	for _, operation := range endpointOperations {
		envelope, parseErr := runtimecontrol.ParseEndpointRoutingEnvelope(operation.Transitions, 1024)
		if parseErr != nil {
			t.logFailure("observe_endpoint_operation", parseErr)
			continue
		}
		age := t.age(operation.CreatedAt.Time, operation.CreatedAt.Valid)
		observation.endpointOperations = append(observation.endpointOperations, endpointOperationObservation{
			operation: operation,
			envelope:  envelope,
			age:       age,
		})
		t.observeEndpointPending(envelope, age)
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

func (t *runtimeControlTelemetry) observeEndpointPending(
	envelope runtimecontrol.EndpointRoutingEnvelope,
	age time.Duration,
) {
	if t.metrics == nil {
		return
	}
	for _, transition := range envelope.Transitions {
		endpointID := strconv.FormatInt(transition.EndpointID, 10)
		switch envelope.Kind {
		case runtimecontrol.EndpointFenceKindBaseURL:
			t.metrics.SetEndpointBaseURLRevisionFence(endpointID, "pending", age)
		case runtimecontrol.EndpointFenceKindStatus, runtimecontrol.EndpointFenceKindProviderStatusBatch:
			t.metrics.SetEndpointStatusRevisionFence(endpointID, "pending", age)
		case runtimecontrol.EndpointFenceKindBaseURLStatus:
			t.metrics.SetEndpointBaseURLRevisionFence(endpointID, "pending", age)
			t.metrics.SetEndpointStatusRevisionFence(endpointID, "pending", age)
		}
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
				zap.String("operation_state", operation.State),
				zap.String("result", result),
				zap.Int64("current_revision", operation.CurrentRevision),
				zap.Int64("next_revision", operation.NextRevision),
				zap.String("payload_hash_prefix", hashPrefix(operation.PayloadHash)),
			}
			if operation.ChannelID.Valid {
				fields = append(fields, zap.Int64("channel_id", operation.ChannelID.Int64))
			}
			t.logger.Info("runtime control operation reconciled", fields...)
		}
	}
	for _, observed := range observation.endpointOperations {
		if t.logger == nil {
			continue
		}
		t.logger.Info("endpoint routing operation reconciled",
			zap.String("kind", observed.operation.Kind),
			zap.String("operation_state", observed.operation.State),
			zap.String("result", recoveredOperationResult(observed.operation.State)),
			zap.Int64("provider_id", observed.envelope.ProviderID),
			zap.Int("endpoint_count", len(observed.envelope.Transitions)),
			zap.Duration("pending_age", observed.age),
			zap.String("payload_hash_prefix", hashPrefix(observed.operation.PayloadHash)),
		)
	}
	t.mu.Lock()
	t.lastFailureSignature = ""
	t.suppressedFailures = 0
	t.mu.Unlock()
}

func (t *runtimeControlTelemetry) EndpointControlReconciled(
	endpointID, baseURLRevision, statusRevision int64,
	effectiveStatus string,
	restored bool,
) {
	if t == nil {
		return
	}
	id := strconv.FormatInt(endpointID, 10)
	if t.metrics != nil {
		t.metrics.SetEndpointBaseURLRevisionFence(id, "active", 0)
		t.metrics.SetEndpointStatusRevisionFence(id, "active", 0)
	}
	if restored && t.logger != nil {
		t.logger.Info("endpoint runtime control restored",
			zap.Int64("endpoint_id", endpointID),
			zap.Int64("base_url_revision", baseURLRevision),
			zap.Int64("status_revision", statusRevision),
			zap.String("effective_status", effectiveStatus),
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
		t.logger.Info("critical runtime control restored",
			zap.String("target", target),
			zap.Int64("revision", revision),
		)
	}
}

func (t *runtimeControlTelemetry) channelControlReconciled(channelID, revision int64, restored bool) {
	if t == nil {
		return
	}
	if t.metrics != nil {
		t.metrics.SetRuntimeControlPending("channel_admission", false, 0)
		if restored {
			t.metrics.IncRuntimeControlOperation("channel_admission", "reconcile", "restored")
			t.metrics.IncRuntimeControlRecovery("channel_admission", "restored")
		}
	}
	if restored && t.logger != nil {
		t.logger.Info("channel admission control restored",
			zap.Int64("channel_id", channelID),
			zap.Int64("revision", revision),
		)
	}
}

func (t *runtimeControlTelemetry) logFailure(phase string, err error) {
	if t == nil || t.logger == nil || err == nil {
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

	t.logger.Error("runtime control reconciliation failed",
		zap.String("phase", phase),
		zap.Int("suppressed_failures", suppressed),
		zap.Error(err),
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
	case runtimecontrol.KindChannelAdmissionLimits:
		return "channel_admission"
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
	case appsettings.GatewayChannelRateLimitDefaultsKey:
		return "channel_rate"
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

func hashPrefix(hash string) string {
	if len(hash) <= 12 {
		return hash
	}
	return hash[:12]
}
