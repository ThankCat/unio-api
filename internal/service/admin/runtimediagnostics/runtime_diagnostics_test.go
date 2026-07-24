package runtimediagnostics

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type storeStub struct {
	epoch              sqlc.GetAppSettingRecordRow
	epochErr           error
	originOperations []sqlc.OriginRoutingOperation
	originErr        error
	runtimeOperations  []sqlc.RuntimeControlOperation
	runtimeErr         error
}

func (s *storeStub) GetAppSettingRecord(context.Context, string) (sqlc.GetAppSettingRecordRow, error) {
	return s.epoch, s.epochErr
}

func (s *storeStub) ListNonterminalOriginRoutingOperations(context.Context) ([]sqlc.OriginRoutingOperation, error) {
	return s.originOperations, s.originErr
}

func (s *storeStub) ListNonterminalRuntimeControlOperations(context.Context) ([]sqlc.RuntimeControlOperation, error) {
	return s.runtimeOperations, s.runtimeErr
}

type readinessStub struct {
	ready  bool
	reason string
}

func (s readinessStub) Check(context.Context) (bool, string) { return s.ready, s.reason }

type integrityStub struct {
	marker breakerstore.StateIntegritySnapshot
	err    error
}

func (s integrityStub) StateIntegrity(context.Context) (breakerstore.StateIntegritySnapshot, error) {
	return s.marker, s.err
}

func TestServiceReturnsRedactedRuntimeDiagnostics(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	epochValue := readyEpochValue(t, now.Add(-time.Hour))
	store := &storeStub{
		epoch: sqlc.GetAppSettingRecordRow{Value: epochValue, Revision: 7},
		originOperations: []sqlc.OriginRoutingOperation{
			{CreatedAt: pgtype.Timestamptz{Time: now.Add(-20 * time.Second), Valid: true}},
		},
		runtimeOperations: []sqlc.RuntimeControlOperation{
			{CreatedAt: pgtype.Timestamptz{Time: now.Add(-10 * time.Second), Valid: true}},
			{CreatedAt: pgtype.Timestamptz{Time: now.Add(-30 * time.Second), Valid: true}},
		},
	}
	marker := matchingMarker(t, epochValue, 7)
	service := NewService(store, readinessStub{ready: true, reason: "ready"}, integrityStub{marker: marker})
	service.now = func() time.Time { return now }

	got, err := service.Get(t.Context())
	if err != nil {
		t.Fatalf("get diagnostics: %v", err)
	}
	if !got.Readiness.Ready || got.Readiness.Reason != "ready" {
		t.Fatalf("unexpected readiness: %+v", got.Readiness)
	}
	if got.RuntimeStateEpoch.State != "ready" || got.RuntimeStateEpoch.Revision != 7 || !got.RuntimeStateEpoch.Match {
		t.Fatalf("unexpected redacted epoch: %+v", got.RuntimeStateEpoch)
	}
	if got.Operations.OriginRouting.NonterminalCount != 1 ||
		got.Operations.OriginRouting.OldestAgeSeconds == nil ||
		*got.Operations.OriginRouting.OldestAgeSeconds != 20 {
		t.Fatalf("unexpected origin operations: %+v", got.Operations.OriginRouting)
	}
	if got.Operations.RuntimeControl.NonterminalCount != 2 ||
		got.Operations.RuntimeControl.OldestAgeSeconds == nil ||
		*got.Operations.RuntimeControl.OldestAgeSeconds != 30 {
		t.Fatalf("unexpected runtime operations: %+v", got.Operations.RuntimeControl)
	}
}

func TestServiceReportsEpochMismatchWithoutExposingIdentity(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	epochValue := readyEpochValue(t, now.Add(-time.Hour))
	service := NewService(
		&storeStub{epoch: sqlc.GetAppSettingRecordRow{Value: epochValue, Revision: 7}},
		readinessStub{reason: "marker_mismatch"},
		integrityStub{marker: breakerstore.StateIntegritySnapshot{
			Exists: true, State: "ready", Epoch: "ffeeddccbbaa99887766554433221100", Revision: 7,
			MarkerHash: breakerstore.StateIntegrityReadyMarkerHash("ffeeddccbbaa99887766554433221100", 7),
		}},
	)
	service.now = func() time.Time { return now }

	got, err := service.Get(t.Context())
	if err != nil {
		t.Fatalf("get diagnostics: %v", err)
	}
	if got.RuntimeStateEpoch.Match || got.Readiness.Reason != "marker_mismatch" {
		t.Fatalf("unexpected mismatch result: %+v", got)
	}
	if got.Operations.OriginRouting.OldestAgeSeconds != nil || got.Operations.RuntimeControl.OldestAgeSeconds != nil {
		t.Fatalf("empty operation sets must not report an age: %+v", got.Operations)
	}
}

func TestServiceKeepsRedisFailureDiagnosable(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	service := NewService(
		&storeStub{epoch: sqlc.GetAppSettingRecordRow{Value: readyEpochValue(t, now.Add(-time.Hour)), Revision: 7}},
		readinessStub{reason: "redis_unavailable"},
		integrityStub{err: errors.New("redis down")},
	)

	got, err := service.Get(t.Context())
	if err != nil {
		t.Fatalf("redis failure should remain a diagnostic result: %v", err)
	}
	if got.Readiness.Ready || got.Readiness.Reason != "redis_unavailable" || got.RuntimeStateEpoch.Match {
		t.Fatalf("unexpected redis failure diagnostics: %+v", got)
	}
}

func TestServiceReturnsStoreErrors(t *testing.T) {
	service := NewService(
		&storeStub{epochErr: errors.New("postgres down")},
		readinessStub{reason: "postgres_unavailable"},
		integrityStub{},
	)
	if _, err := service.Get(t.Context()); err == nil {
		t.Fatal("expected store error")
	}
}

func readyEpochValue(t *testing.T, activatedAt time.Time) []byte {
	t.Helper()
	raw, err := json.Marshal(runtimecontrol.StateEpoch{
		Epoch: "00112233445566778899aabbccddeeff", State: runtimecontrol.StateEpochReady,
		Reason: runtimecontrol.StateEpochReasonBootstrap, ActivatedAt: &activatedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func matchingMarker(t *testing.T, raw []byte, revision int64) breakerstore.StateIntegritySnapshot {
	t.Helper()
	epoch, err := runtimecontrol.DecodeStateEpoch(raw)
	if err != nil {
		t.Fatal(err)
	}
	return breakerstore.StateIntegritySnapshot{
		Exists: true, State: "ready", Epoch: epoch.Epoch, Revision: revision,
		MarkerHash: breakerstore.StateIntegrityReadyMarkerHash(epoch.Epoch, revision),
	}
}
