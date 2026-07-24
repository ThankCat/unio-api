// Package runtimediagnostics provides the read-only maintenance view for the
// Gateway's durable and Redis runtime-control state.
package runtimediagnostics

import (
	"context"
	"fmt"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type Store interface {
	GetAppSettingRecord(ctx context.Context, key string) (sqlc.GetAppSettingRecordRow, error)
	ListNonterminalOriginRoutingOperations(ctx context.Context) ([]sqlc.OriginRoutingOperation, error)
	ListNonterminalRuntimeControlOperations(ctx context.Context) ([]sqlc.RuntimeControlOperation, error)
}

type ReadinessChecker interface {
	Check(ctx context.Context) (bool, string)
}

type IntegrityStore interface {
	StateIntegrity(ctx context.Context) (breakerstore.StateIntegritySnapshot, error)
}

type Readiness struct {
	Ready  bool
	Reason string
}

// StateEpoch intentionally omits the random epoch identity.
type StateEpoch struct {
	State    string
	Revision int64
	Match    bool
}

type OperationSummary struct {
	NonterminalCount int64
	OldestAgeSeconds *int64
}

type Operations struct {
	OriginRouting OperationSummary
	RuntimeControl  OperationSummary
}

type Diagnostics struct {
	Readiness         Readiness
	RuntimeStateEpoch StateEpoch
	Operations        Operations
}

type Service struct {
	store     Store
	readiness ReadinessChecker
	integrity IntegrityStore
	now       func() time.Time
}

func NewService(store Store, readiness ReadinessChecker, integrity IntegrityStore) *Service {
	if store == nil || readiness == nil || integrity == nil {
		panic("runtimediagnostics: store, readiness, and integrity are required")
	}
	return &Service{store: store, readiness: readiness, integrity: integrity, now: time.Now}
}

func (s *Service) Get(ctx context.Context) (Diagnostics, error) {
	ready, reason := s.readiness.Check(ctx)

	epochRow, err := s.store.GetAppSettingRecord(ctx, runtimecontrol.RuntimeStateEpochKey)
	if err != nil {
		return Diagnostics{}, fmt.Errorf("runtime diagnostics: read state epoch: %w", err)
	}
	originOperations, err := s.store.ListNonterminalOriginRoutingOperations(ctx)
	if err != nil {
		return Diagnostics{}, fmt.Errorf("runtime diagnostics: list origin operations: %w", err)
	}
	runtimeOperations, err := s.store.ListNonterminalRuntimeControlOperations(ctx)
	if err != nil {
		return Diagnostics{}, fmt.Errorf("runtime diagnostics: list runtime operations: %w", err)
	}

	epochState := "invalid"
	epochMatch := false
	if epoch, decodeErr := runtimecontrol.DecodeStateEpoch(epochRow.Value); decodeErr == nil {
		epochState = string(epoch.State)
		if marker, markerErr := s.integrity.StateIntegrity(ctx); markerErr == nil {
			epochMatch = marker.Ready(epoch.Epoch, epochRow.Revision)
		}
	}

	now := s.now()
	return Diagnostics{
		Readiness: Readiness{Ready: ready, Reason: reason},
		RuntimeStateEpoch: StateEpoch{
			State: epochState, Revision: epochRow.Revision, Match: epochMatch,
		},
		Operations: Operations{
			OriginRouting: summarizeOriginOperations(now, originOperations),
			RuntimeControl:  summarizeRuntimeOperations(now, runtimeOperations),
		},
	}, nil
}

func summarizeOriginOperations(now time.Time, operations []sqlc.OriginRoutingOperation) OperationSummary {
	times := make([]time.Time, 0, len(operations))
	for _, operation := range operations {
		if operation.CreatedAt.Valid {
			times = append(times, operation.CreatedAt.Time)
		}
	}
	return OperationSummary{NonterminalCount: int64(len(operations)), OldestAgeSeconds: oldestAgeSeconds(now, times)}
}

func summarizeRuntimeOperations(now time.Time, operations []sqlc.RuntimeControlOperation) OperationSummary {
	times := make([]time.Time, 0, len(operations))
	for _, operation := range operations {
		if operation.CreatedAt.Valid {
			times = append(times, operation.CreatedAt.Time)
		}
	}
	return OperationSummary{NonterminalCount: int64(len(operations)), OldestAgeSeconds: oldestAgeSeconds(now, times)}
}

func oldestAgeSeconds(now time.Time, createdAt []time.Time) *int64 {
	if len(createdAt) == 0 {
		return nil
	}
	oldest := createdAt[0]
	for _, candidate := range createdAt[1:] {
		if candidate.Before(oldest) {
			oldest = candidate
		}
	}
	age := now.Sub(oldest)
	if age < 0 {
		age = 0
	}
	seconds := int64(age / time.Second)
	return &seconds
}
