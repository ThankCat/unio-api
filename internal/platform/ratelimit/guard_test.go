package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

// fakeStore 是 Guard 单测用的内存滑动窗口计数器：按 subject 累加，忽略窗口滚动（单测窗口内不滚动）。
type fakeStore struct {
	counts map[string]int64
	err    error
	calls  int
}

func newFakeStore() *fakeStore {
	return &fakeStore{counts: map[string]int64{}}
}

func (f *fakeStore) CheckAndAdd(_ context.Context, subject string, limit int64, _ time.Duration, _ time.Duration, amount int64) (CountResult, error) {
	f.calls++
	if f.err != nil {
		return CountResult{}, f.err
	}
	current := f.counts[subject]
	if limit > 0 && current+amount > limit {
		return CountResult{Allowed: false, Count: current}, nil
	}
	f.counts[subject] = current + amount
	return CountResult{Allowed: true, Count: current + amount, ResetAt: time.Now()}, nil
}

func (f *fakeStore) Add(_ context.Context, subject string, _ time.Duration, _ time.Duration, delta int64) error {
	if f.err != nil {
		return f.err
	}
	f.counts[subject] += delta
	return nil
}

func ptr(v int64) *int64 { return &v }

func TestGuardKeyRequestAllowsUpToLimitThenDenies(t *testing.T) {
	store := newFakeStore()
	guard := NewGuard(store, DefaultLimits{}, false, nil)
	limits := Limits{RPM: ptr(2)}

	for i := 1; i <= 2; i++ {
		decision, err := guard.AllowKeyRequest(context.Background(), 7, limits)
		if err != nil {
			t.Fatalf("call %d unexpected err: %v", i, err)
		}
		if !decision.Allowed {
			t.Fatalf("call %d expected allowed", i)
		}
	}

	decision, err := guard.AllowKeyRequest(context.Background(), 7, limits)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("expected 3rd request to be denied")
	}
	if decision.Dimension != DimensionRPM {
		t.Fatalf("expected rpm dimension, got %s", decision.Dimension)
	}
}

func TestGuardUnlimitedSkipsStore(t *testing.T) {
	store := newFakeStore()
	// 全局默认 0 + 无覆盖 = 不限：不应触达 store。
	guard := NewGuard(store, DefaultLimits{}, false, nil)

	decision, err := guard.AllowKeyRequest(context.Background(), 1, Limits{})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("expected allowed under unlimited")
	}
	if store.calls != 0 {
		t.Fatalf("expected no store calls under unlimited, got %d", store.calls)
	}
}

func TestGuardZeroOverrideMeansUnlimited(t *testing.T) {
	store := newFakeStore()
	// 全局默认很小，但 Key 显式 0 = 不限，应覆盖默认。
	guard := NewGuard(store, DefaultLimits{RPM: 1, RPD: 1}, false, nil)

	for i := 0; i < 5; i++ {
		decision, err := guard.AllowKeyRequest(context.Background(), 1, Limits{RPM: ptr(0), RPD: ptr(0)})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !decision.Allowed {
			t.Fatalf("iteration %d expected allowed under explicit unlimited", i)
		}
	}
}

func TestGuardChannelTokensRespectTPM(t *testing.T) {
	store := newFakeStore()
	guard := NewGuard(store, DefaultLimits{}, false, nil)
	limits := Limits{TPM: ptr(100)}

	if decision, err := guard.AllowChannel(context.Background(), 3, limits, 80); err != nil || !decision.Allowed {
		t.Fatalf("first 80 tokens should pass: decision=%+v err=%v", decision, err)
	}
	// 已占 80，再来 30 超过 100 应拒绝。
	decision, err := guard.AllowChannel(context.Background(), 3, limits, 30)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("expected tpm rejection when 80+30 > 100")
	}
	if decision.Dimension != DimensionTPM {
		t.Fatalf("expected tpm dimension, got %s", decision.Dimension)
	}
}

func TestGuardBackfillAdjustsTokens(t *testing.T) {
	store := newFakeStore()
	guard := NewGuard(store, DefaultLimits{}, false, nil)
	limits := Limits{TPM: ptr(1000)}

	// 预占 100。
	if _, err := guard.AllowKeyTokens(context.Background(), 5, limits, 100); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// 实际用了 250，回填 +150。
	guard.BackfillKeyTokens(context.Background(), 5, 150)

	subject := subjectFor(ScopeKey, 5, DimensionTPM)
	if got := store.counts[subject]; got != 250 {
		t.Fatalf("expected tpm counter 250 after backfill, got %d", got)
	}
}

func TestGuardFailOpenOnStoreError(t *testing.T) {
	store := newFakeStore()
	store.err = errors.New("redis down")
	guard := NewGuard(store, DefaultLimits{RPM: 10}, true, nil)

	decision, err := guard.AllowKeyRequest(context.Background(), 9, Limits{})
	if err != nil {
		t.Fatalf("fail-open should not return error, got %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("fail-open should allow on store error")
	}
}

func TestGuardFailClosedOnStoreError(t *testing.T) {
	store := newFakeStore()
	store.err = errors.New("redis down")
	guard := NewGuard(store, DefaultLimits{RPM: 10}, false, nil)

	decision, err := guard.AllowKeyRequest(context.Background(), 9, Limits{})
	if err == nil {
		t.Fatalf("fail-closed should return error on store failure")
	}
	if decision.Allowed {
		t.Fatalf("fail-closed should not allow on store error")
	}
}

func TestTokensEnforced(t *testing.T) {
	guard := NewGuard(newFakeStore(), DefaultLimits{TPM: 0}, false, nil)
	if guard.TokensEnforced(Limits{}) {
		t.Fatalf("default tpm 0 should not be enforced")
	}
	if !guard.TokensEnforced(Limits{TPM: ptr(10)}) {
		t.Fatalf("override tpm 10 should be enforced")
	}
	if guard.TokensEnforced(Limits{TPM: ptr(0)}) {
		t.Fatalf("override tpm 0 means unlimited, not enforced")
	}
}
