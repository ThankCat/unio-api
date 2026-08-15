package ledger

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

type positiveBalanceStoreStub struct {
	positive bool
	err      error
	userID   int64
}

func (s *positiveBalanceStoreStub) HasPositiveAvailableUserBalance(_ context.Context, userID int64) (bool, error) {
	s.userID = userID
	return s.positive, s.err
}

func TestBalanceEligibilityServiceReturnsStoreResult(t *testing.T) {
	tests := []struct {
		name     string
		positive bool
	}{
		{name: "positive balance", positive: true},
		{name: "no positive balance", positive: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := &positiveBalanceStoreStub{positive: tt.positive}
			service := NewBalanceEligibilityService(store)

			got, err := service.HasPositiveAvailableBalance(context.Background(), 42)
			if err != nil {
				t.Fatalf("check positive balance: %v", err)
			}
			if got != tt.positive {
				t.Fatalf("positive=%v, want %v", got, tt.positive)
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

	positive, err := service.HasPositiveAvailableBalance(context.Background(), 42)
	if err == nil {
		t.Fatal("expected store failure")
	}
	if positive {
		t.Fatal("store failure must not report positive balance")
	}
	if !errors.Is(err, storeErr) {
		t.Fatalf("expected wrapped store error, got %v", err)
	}
	if code := failure.CodeOf(err); code != failure.CodeLedgerStoreFailed {
		t.Fatalf("failure code=%q, want %q", code, failure.CodeLedgerStoreFailed)
	}
}
