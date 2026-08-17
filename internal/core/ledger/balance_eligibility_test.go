package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

type positiveBalanceStoreStub struct {
	state  string
	err    error
	userID int64
}

func (s *positiveBalanceStoreStub) GetUserBalanceEligibility(_ context.Context, userID int64) (string, error) {
	s.userID = userID
	return s.state, s.err
}

func TestBalanceEligibilityServiceReturnsStoreResult(t *testing.T) {
	tests := []struct {
		name  string
		state BalanceEligibility
	}{
		{name: "positive available balance", state: BalanceEligibilityPositiveAvailable},
		{name: "temporarily reserved balance", state: BalanceEligibilityTemporarilyReserved},
		{name: "insufficient balance", state: BalanceEligibilityInsufficient},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &positiveBalanceStoreStub{state: string(tt.state)}
			service := NewBalanceEligibilityService(store)

			got, err := service.GetBalanceEligibility(context.Background(), 42)
			if err != nil {
				t.Fatalf("check balance eligibility: %v", err)
			}
			if got != tt.state {
				t.Fatalf("eligibility=%q, want %q", got, tt.state)
			}
			if store.userID != 42 {
				t.Fatalf("user id=%d, want 42", store.userID)
			}
		})
	}
}

func TestBalanceEligibilityServiceWrapsStoreFailure(t *testing.T) {
	storeErr := errors.New("postgres unavailable")
	service := NewBalanceEligibilityService(&positiveBalanceStoreStub{err: storeErr})

	state, err := service.GetBalanceEligibility(context.Background(), 42)
	if err == nil {
		t.Fatal("expected store failure")
	}
	if state != "" {
		t.Fatalf("store failure must not report eligibility, got %q", state)
	}
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected wrapped store error, got %v", err)
	}
	if code := failure.CodeOf(err); code != failure.CodeLedgerStoreFailed {
		t.Fatalf("failure code=%q, want %q", code, failure.CodeLedgerStoreFailed)
	}
}

func TestBalanceEligibilityServiceRejectsUnknownState(t *testing.T) {
	service := NewBalanceEligibilityService(&positiveBalanceStoreStub{state: "future_state"})

	state, err := service.GetBalanceEligibility(context.Background(), 42)
	if err == nil {
		t.Fatal("expected unknown state failure")
	}
	if state != "" {
		t.Fatalf("unknown state must not be returned, got %q", state)
	}
	if code := failure.CodeOf(err); code != failure.CodeLedgerStoreFailed {
		t.Fatalf("failure code=%q, want %q", code, failure.CodeLedgerStoreFailed)
	}
}
