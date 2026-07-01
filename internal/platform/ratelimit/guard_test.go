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

// CheckThenAdd 复刻 admitThenAddScript 的准入语义：门槛只看进入前已用量（下探 0），未达上限即放行并占用 amount。
func (f *fakeStore) CheckThenAdd(_ context.Context, subject string, limit int64, _ time.Duration, _ time.Duration, amount int64) (CountResult, error) {
	f.calls++
	if f.err != nil {
		return CountResult{}, f.err
	}
	current := f.counts[subject]
	if current < 0 {
		current = 0
	}
	if limit > 0 && current >= limit {
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
		decision, err := guard.AllowRouteUserRequest(context.Background(), 1, 7, limits)
		if err != nil {
			t.Fatalf("call %d unexpected err: %v", i, err)
		}
		if !decision.Allowed {
			t.Fatalf("call %d expected allowed", i)
		}
	}

	decision, err := guard.AllowRouteUserRequest(context.Background(), 1, 7, limits)
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

	decision, err := guard.AllowRouteUserRequest(context.Background(), 1, 1, Limits{})
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
		decision, err := guard.AllowRouteUserRequest(context.Background(), 1, 1, Limits{RPM: ptr(0), RPD: ptr(0)})
		if err != nil {
			t.Fatalf("unexpected err: %v", err)
		}
		if !decision.Allowed {
			t.Fatalf("iteration %d expected allowed under explicit unlimited", i)
		}
	}
}

// TestGuardChannelTokensRespectTPM 验证渠道 TPM 的准入门槛：窗口未达上限前放行（允许最后一条冲过上限一次），
// 一旦已用量达到/超过上限，后续请求被拒（DEC-028 准入语义，替代旧的 sum+amount>limit 严格门槛）。
func TestGuardChannelTokensRespectTPM(t *testing.T) {
	store := newFakeStore()
	guard := NewGuard(store, DefaultLimits{}, false, nil)
	limits := Limits{TPM: ptr(100)}

	// 已用量 0 < 100 → 放行，占用 80。
	if decision, err := guard.AllowChannel(context.Background(), 3, limits, 80); err != nil || !decision.Allowed {
		t.Fatalf("first 80 tokens should pass: decision=%+v err=%v", decision, err)
	}
	// 已用量 80 < 100 → 仍放行（准入门槛不看本次 amount），占用后窗口冲到 110。
	if decision, err := guard.AllowChannel(context.Background(), 3, limits, 30); err != nil || !decision.Allowed {
		t.Fatalf("second request should be admitted while window (80) is under limit: decision=%+v err=%v", decision, err)
	}
	// 已用量 110 >= 100 → 拒绝。
	decision, err := guard.AllowChannel(context.Background(), 3, limits, 1)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if decision.Allowed {
		t.Fatalf("expected tpm rejection when window already at/over limit")
	}
	if decision.Dimension != DimensionTPM {
		t.Fatalf("expected tpm dimension, got %s", decision.Dimension)
	}
}

// TestGuardRouteUserTokensAdmitsOversizedSingleRequest 锁定 DEC-028 准入门槛：
// 空窗口下单条预估远超上限的请求也应放行（Codex 每轮重发大缓存上下文的核心场景），
// 不再「预估超上限即 429」。
func TestGuardRouteUserTokensAdmitsOversizedSingleRequest(t *testing.T) {
	store := newFakeStore()
	guard := NewGuard(store, DefaultLimits{}, false, nil)
	limits := Limits{TPM: ptr(300_000)}

	// 单条预估 330k > 上限 300k，但窗口为空 → 准入门槛应放行。
	decision, err := guard.AllowRouteUserTokens(context.Background(), 3, 1, limits, 330_000)
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !decision.Allowed {
		t.Fatalf("oversized single request must be admitted on an empty window (new-api admission gate)")
	}
}

// TestGuardRouteUserTokensRejectsWhenAlreadyOverLimit 验证准入门槛在「窗口已用量达/超上限」时才拒绝，
// 且回填把用量降回上限内后立即恢复放行（对应结算 backfill 退回 cache_read）。
func TestGuardRouteUserTokensRejectsWhenAlreadyOverLimit(t *testing.T) {
	store := newFakeStore()
	guard := NewGuard(store, DefaultLimits{}, false, nil)
	limits := Limits{TPM: ptr(300_000)}

	// 第一条把窗口冲到 330k（> 上限），准入放行。
	if d, err := guard.AllowRouteUserTokens(context.Background(), 3, 1, limits, 330_000); err != nil || !d.Allowed {
		t.Fatalf("first request should be admitted: decision=%+v err=%v", d, err)
	}
	// 此时已用量 330k >= 300k → 下一条应被拒。
	if d, err := guard.AllowRouteUserTokens(context.Background(), 3, 1, limits, 1); err != nil || d.Allowed {
		t.Fatalf("second request should be rejected while window is over limit: decision=%+v err=%v", d, err)
	}
	// 结算回填把预估退回真实小额（如真实仅 5k，delta = 5k-330k）。
	guard.BackfillRouteUserTokens(context.Background(), 3, 1, 5_000-330_000)
	// 窗口回落到 5k < 300k → 恢复放行。
	if d, err := guard.AllowRouteUserTokens(context.Background(), 3, 1, limits, 330_000); err != nil || !d.Allowed {
		t.Fatalf("request should be admitted again after backfill drains the window: decision=%+v err=%v", d, err)
	}
}

// TestGuardChannelTokensAdmitOnEmptyWindow 验证渠道级 TPM 同样用准入门槛：单条大请求不因自身预估超渠道上限被跳过。
func TestGuardChannelTokensAdmitOnEmptyWindow(t *testing.T) {
	store := newFakeStore()
	guard := NewGuard(store, DefaultLimits{}, false, nil)
	limits := Limits{TPM: ptr(219_000)}

	if d, err := guard.AllowChannel(context.Background(), 1, limits, 300_000); err != nil || !d.Allowed {
		t.Fatalf("oversized single request must be admitted on empty channel window: decision=%+v err=%v", d, err)
	}
}

func TestGuardBackfillAdjustsTokens(t *testing.T) {
	store := newFakeStore()
	guard := NewGuard(store, DefaultLimits{}, false, nil)
	limits := Limits{TPM: ptr(1000)}

	// 预占 100。
	if _, err := guard.AllowRouteUserTokens(context.Background(), 1, 5, limits, 100); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	// 实际用了 250，回填 +150。
	guard.BackfillRouteUserTokens(context.Background(), 1, 5, 150)

	subject := routeUserSubject(1, 5, DimensionTPM)
	if got := store.counts[subject]; got != 250 {
		t.Fatalf("expected tpm counter 250 after backfill, got %d", got)
	}
}

func TestGuardFailOpenOnStoreError(t *testing.T) {
	store := newFakeStore()
	store.err = errors.New("redis down")
	guard := NewGuard(store, DefaultLimits{RPM: 10}, true, nil)

	decision, err := guard.AllowRouteUserRequest(context.Background(), 1, 9, Limits{})
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

	decision, err := guard.AllowRouteUserRequest(context.Background(), 1, 9, Limits{})
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
