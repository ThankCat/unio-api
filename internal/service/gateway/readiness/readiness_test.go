package readiness_test

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/readiness"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

type queryStub struct {
	row sqlc.GetGatewayRuntimeReadinessSnapshotRow
	err error
}

func (q queryStub) GetGatewayRuntimeReadinessSnapshot(context.Context) (sqlc.GetGatewayRuntimeReadinessSnapshotRow, error) {
	return q.row, q.err
}

type storeStub struct {
	pingErr     error
	result      breakerstore.RuntimeReadinessResult
	err         error
	input       breakerstore.RuntimeReadinessInput
	clearResult breakerstore.RuntimeReadinessResult
	clearErr    error
	clearInput  breakerstore.RuntimeReadinessInput
	clearCalls  int
}

type metricsStub struct {
	ready       bool
	unavailable bool
	integrity   string
	healthCalls int
}

func (m *metricsStub) SetBreakerStoreHealth(ready, unavailable bool) {
	m.ready = ready
	m.unavailable = unavailable
	m.healthCalls++
}

func (m *metricsStub) SetRuntimeStateIntegrity(state string) { m.integrity = state }

func (s *storeStub) Ping(context.Context) error { return s.pingErr }

func (s *storeStub) CheckRuntimeReadiness(_ context.Context, input breakerstore.RuntimeReadinessInput) (breakerstore.RuntimeReadinessResult, error) {
	s.input = input
	return s.result, s.err
}

func (s *storeStub) ClearRuntimeInfrastructureFaultAfterReconciliation(
	_ context.Context,
	input breakerstore.RuntimeReadinessInput,
	_ breakerstore.RuntimeReconciliationProof,
) (breakerstore.RuntimeReadinessResult, error) {
	s.clearInput = input
	s.clearCalls++
	return s.clearResult, s.clearErr
}

func TestCheckerRequiresMatchingReadyEpochAndControls(t *testing.T) {
	row := readyRow(t)
	store := &storeStub{result: breakerstore.RuntimeReadinessResult{Ready: true, Reason: "ready"}}
	checker := readiness.NewChecker(queryStub{row: row}, store)

	ready, reason := checker.Check(t.Context())
	if !ready || reason != "ready" {
		t.Fatalf("ready=%v reason=%q", ready, reason)
	}
	if store.input.Epoch != "00112233445566778899aabbccddeeff" || store.input.EpochRevision != 7 ||
		store.input.RouteRateLimitRevision != 2 || store.input.ChannelRateLimitRevision != 6 ||
		store.input.ConcurrencyRevision != 3 ||
		store.input.CircuitBreakerRevision != 4 || store.input.RoutingBalanceRevision != 5 {
		t.Fatalf("unexpected readiness input: %+v", store.input)
	}
}

func TestCheckerFailsClosedByLayer(t *testing.T) {
	tests := []struct {
		name   string
		row    sqlc.GetGatewayRuntimeReadinessSnapshotRow
		qerr   error
		store  *storeStub
		reason string
	}{
		{name: "postgres", qerr: errors.New("db down"), store: &storeStub{}, reason: "postgres_unavailable"},
		{name: "recovering epoch", row: recoveringRow(t), store: &storeStub{}, reason: "epoch_not_ready"},
		{name: "invalid route rate revision", row: func() sqlc.GetGatewayRuntimeReadinessSnapshotRow {
			row := readyRow(t)
			row.RouteRateLimitDefaultsRevision = 0
			return row
		}(), store: &storeStub{}, reason: "control_revision_invalid"},
		{name: "invalid channel rate revision", row: func() sqlc.GetGatewayRuntimeReadinessSnapshotRow {
			row := readyRow(t)
			row.ChannelRateLimitDefaultsRevision = 0
			return row
		}(), store: &storeStub{}, reason: "control_revision_invalid"},
		{name: "pending operation", row: func() sqlc.GetGatewayRuntimeReadinessSnapshotRow {
			row := readyRow(t)
			row.RuntimeOperationsReconciled = false
			return row
		}(), store: &storeStub{}, reason: "runtime_operation_pending"},
		{name: "redis ping", row: readyRow(t), store: &storeStub{pingErr: errors.New("redis down")}, reason: "redis_unavailable"},
		{name: "marker mismatch", row: readyRow(t), store: &storeStub{result: breakerstore.RuntimeReadinessResult{Reason: "marker_mismatch"}}, reason: "marker_mismatch"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			checker := readiness.NewChecker(queryStub{row: tc.row, err: tc.qerr}, tc.store)
			ready, reason := checker.Check(t.Context())
			if ready || reason != tc.reason {
				t.Fatalf("ready=%v reason=%q want=%q", ready, reason, tc.reason)
			}
		})
	}
}

func TestCheckerUpdatesMetricsAndLogsOnlyReadinessTransitions(t *testing.T) {
	core, logs := observer.New(zap.DebugLevel)
	store := &storeStub{result: breakerstore.RuntimeReadinessResult{Reason: "marker_mismatch"}}
	recorder := &metricsStub{}
	checker := readiness.NewCheckerWithObservability(
		queryStub{row: readyRow(t)}, store, zap.New(core), recorder,
	)

	for range 2 {
		ready, reason := checker.Check(t.Context())
		if ready || reason != "marker_mismatch" {
			t.Fatalf("ready=%v reason=%q", ready, reason)
		}
	}
	if recorder.ready || recorder.unavailable || recorder.integrity != "lost" || recorder.healthCalls != 2 {
		t.Fatalf("unexpected not-ready metrics: %+v", recorder)
	}
	if logs.Len() != 1 {
		t.Fatalf("repeated unchanged readiness must log once, got %d", logs.Len())
	}

	store.result = breakerstore.RuntimeReadinessResult{Ready: true, Reason: "ready"}
	ready, reason := checker.Check(t.Context())
	if !ready || reason != "ready" {
		t.Fatalf("ready=%v reason=%q", ready, reason)
	}
	if !recorder.ready || recorder.unavailable || recorder.integrity != "ready" {
		t.Fatalf("unexpected ready metrics: %+v", recorder)
	}
	if logs.Len() != 2 || logs.All()[1].Level != zap.InfoLevel {
		t.Fatalf("readiness recovery must emit one info transition: %+v", logs.All())
	}
}

func TestCheckerMarksBreakerStoreUnavailableOnRedisFailure(t *testing.T) {
	recorder := &metricsStub{}
	checker := readiness.NewCheckerWithObservability(
		queryStub{row: readyRow(t)},
		&storeStub{pingErr: errors.New("redis down")},
		nil,
		recorder,
	)
	ready, reason := checker.Check(t.Context())
	if ready || reason != "redis_unavailable" {
		t.Fatalf("ready=%v reason=%q", ready, reason)
	}
	if recorder.ready || !recorder.unavailable || recorder.integrity != "lost" {
		t.Fatalf("unexpected redis failure metrics: %+v", recorder)
	}
}

func TestCheckerDoesNotClearLatchedStoreFaultDuringOrdinaryProbe(t *testing.T) {
	store := &storeStub{result: breakerstore.RuntimeReadinessResult{
		Reason: breakerstore.RuntimeReadinessReasonStoreFaultLatched,
	}}
	checker := readiness.NewChecker(queryStub{row: readyRow(t)}, store)

	for range 2 {
		ready, reason := checker.Check(t.Context())
		if ready || reason != breakerstore.RuntimeReadinessReasonStoreFaultLatched {
			t.Fatalf("ready=%v reason=%q", ready, reason)
		}
	}
	if store.clearCalls != 0 {
		t.Fatalf("ordinary readiness probes cleared the latch %d times", store.clearCalls)
	}
}

func TestCheckerClearsFaultOnlyThroughExplicitPostReconciliationPath(t *testing.T) {
	store := &storeStub{
		result:      breakerstore.RuntimeReadinessResult{Reason: breakerstore.RuntimeReadinessReasonStoreFaultLatched},
		clearResult: breakerstore.RuntimeReadinessResult{Ready: true, Reason: "ready"},
	}
	checker := readiness.NewChecker(queryStub{row: readyRow(t)}, store)

	ready, reason := checker.ClearStoreFaultAfterReconciliation(
		t.Context(), breakerstore.RuntimeReconciliationProof{},
	)
	if !ready || reason != "ready" {
		t.Fatalf("ready=%v reason=%q", ready, reason)
	}
	if store.clearCalls != 1 || store.clearInput.Epoch != "00112233445566778899aabbccddeeff" ||
		store.clearInput.EpochRevision != 7 || store.clearInput.RoutingBalanceRevision != 5 {
		t.Fatalf("clear calls=%d input=%+v", store.clearCalls, store.clearInput)
	}
}

func TestCheckerRefusesClearWhileDurableOperationIsPending(t *testing.T) {
	row := readyRow(t)
	row.RuntimeOperationsReconciled = false
	store := &storeStub{clearResult: breakerstore.RuntimeReadinessResult{Ready: true, Reason: "ready"}}
	checker := readiness.NewChecker(queryStub{row: row}, store)

	ready, reason := checker.ClearStoreFaultAfterReconciliation(
		t.Context(), breakerstore.RuntimeReconciliationProof{},
	)
	if ready || reason != "runtime_operation_pending" || store.clearCalls != 0 {
		t.Fatalf("ready=%v reason=%q clear_calls=%d", ready, reason, store.clearCalls)
	}
}

func TestCheckerClearsFaultForMaintenanceSmokeWithoutOpeningReadiness(t *testing.T) {
	row := readyRow(t)
	row.RuntimeOperationsReconciled = false
	row.RuntimeMaintenanceSmokeAllowed = true
	store := &storeStub{
		result:      breakerstore.RuntimeReadinessResult{Ready: true, Reason: "ready"},
		clearResult: breakerstore.RuntimeReadinessResult{Ready: true, Reason: "ready"},
	}
	recorder := &metricsStub{}
	checker := readiness.NewCheckerWithObservability(queryStub{row: row}, store, nil, recorder)

	reconciled, reason := checker.ClearStoreFaultAfterReconciliation(
		t.Context(), breakerstore.RuntimeReconciliationProof{},
	)
	if !reconciled || reason != "maintenance_smoke_ready" || store.clearCalls != 1 {
		t.Fatalf("reconciled=%v reason=%q clear_calls=%d", reconciled, reason, store.clearCalls)
	}
	if recorder.ready || recorder.unavailable || recorder.integrity != "ready" {
		t.Fatalf("maintenance smoke reconciliation opened readiness metrics: %+v", recorder)
	}

	ready, reason := checker.Check(t.Context())
	if ready || reason != "runtime_operation_pending" {
		t.Fatalf("ordinary readiness during maintenance smoke: ready=%v reason=%q", ready, reason)
	}
}

func readyRow(t *testing.T) sqlc.GetGatewayRuntimeReadinessSnapshotRow {
	t.Helper()
	activatedAt := time.Date(2026, time.July, 22, 0, 0, 0, 0, time.UTC)
	raw, err := json.Marshal(map[string]any{
		"epoch": "00112233445566778899aabbccddeeff", "state": "ready",
		"reason": "bootstrap", "activated_at": activatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return sqlc.GetGatewayRuntimeReadinessSnapshotRow{
		RuntimeStateEpochValue: raw, RuntimeStateEpochRevision: 7,
		RouteRateLimitDefaultsRevision: 2, ChannelRateLimitDefaultsRevision: 6,
		ConcurrencyDefaultsRevision: 3,
		CircuitBreakerRevision:      4, RoutingBalanceRevision: 5,
		RuntimeOperationsReconciled: true,
	}
}

func recoveringRow(t *testing.T) sqlc.GetGatewayRuntimeReadinessSnapshotRow {
	t.Helper()
	row := readyRow(t)
	row.RuntimeStateEpochValue = []byte(`{"epoch":"00112233445566778899aabbccddeeff","state":"recovering","reason":"bootstrap","activated_at":null}`)
	return row
}
