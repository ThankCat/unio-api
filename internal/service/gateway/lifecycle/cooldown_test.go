package lifecycle

import (
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

func newTestCooldown(defaultCooldown, cap time.Duration, now *time.Time) *ChannelCooldownRegistry {
	r := NewChannelCooldownRegistry(defaultCooldown, cap)
	r.now = func() time.Time { return *now }
	return r
}

func TestCooldownRecordsRetryAfterAndExpires(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(5*time.Second, time.Minute, &now)

	if !r.Allowed("7") {
		t.Fatal("channel should start allowed")
	}

	until, ok := r.RecordRateLimit("7", 30*time.Second)
	if !ok || !until.Equal(now.Add(30*time.Second)) {
		t.Fatalf("record: ok=%v until=%s", ok, until)
	}
	if r.Allowed("7") {
		t.Fatal("channel should be cooling down")
	}

	// 还差 1s 到期：仍冷却。
	now = now.Add(29 * time.Second)
	if r.Allowed("7") {
		t.Fatal("channel should still be cooling down")
	}

	// 到期后自动恢复。
	now = now.Add(2 * time.Second)
	if !r.Allowed("7") {
		t.Fatal("channel should recover after cooldown expires")
	}
}

func TestCooldownFallsBackToDefaultWhenNoRetryAfter(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(5*time.Second, time.Minute, &now)

	until, ok := r.RecordRateLimit("9", 0)
	if !ok || !until.Equal(now.Add(5*time.Second)) {
		t.Fatalf("default cooldown: ok=%v until=%s", ok, until)
	}
}

func TestCooldownDisabledWhenDefaultZeroAndNoRetryAfter(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(0, time.Minute, &now)

	if _, ok := r.RecordRateLimit("9", 0); ok {
		t.Fatal("no cooldown expected when default is 0 and no retry-after")
	}
	if !r.Allowed("9") {
		t.Fatal("channel should remain allowed")
	}
}

func TestCooldownCapsRetryAfter(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(5*time.Second, 10*time.Second, &now)

	until, ok := r.RecordRateLimit("9", time.Hour)
	if !ok || !until.Equal(now.Add(10*time.Second)) {
		t.Fatalf("cap: ok=%v until=%s", ok, until)
	}
}

func TestCooldownKeepsLaterExpiry(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(5*time.Second, time.Minute, &now)

	r.RecordRateLimit("9", 40*time.Second)
	// 之后一个更短的 Retry-After 不应缩短已登记冷却。
	until, _ := r.RecordRateLimit("9", 5*time.Second)
	if !until.Equal(now.Add(40 * time.Second)) {
		t.Fatalf("expected later expiry retained, got %s", until)
	}
}

func TestCooldownSetCooldownTakesEffect(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(5*time.Second, time.Minute, &now)

	// 热改默认冷却与封顶:之后的登记用新值。
	r.SetCooldown(20*time.Second, 25*time.Second)

	until, ok := r.RecordRateLimit("9", 0)
	if !ok || !until.Equal(now.Add(20*time.Second)) {
		t.Fatalf("new default cooldown: ok=%v until=%s", ok, until)
	}
	until, ok = r.RecordRateLimit("10", time.Hour)
	if !ok || !until.Equal(now.Add(25*time.Second)) {
		t.Fatalf("new cap: ok=%v until=%s", ok, until)
	}

	// 热改为 0:关闭默认冷却(无 Retry-After 时不再登记)。
	r.SetCooldown(0, 0)
	if _, ok := r.RecordRateLimit("11", 0); ok {
		t.Fatal("zero default cooldown should not record")
	}
}

// TestCooldownConcurrentReload 在 -race 下验证热改与热路径读写无竞态。
func TestCooldownConcurrentReload(t *testing.T) {
	r := NewChannelCooldownRegistry(5*time.Second, time.Minute)
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			r.SetCooldown(time.Duration(i)*time.Millisecond, time.Minute)
		}
	}()
	for i := 0; i < 500; i++ {
		r.RecordRateLimit("1", 0)
		r.Allowed("1")
		r.Until("1")
	}
	<-done
}

func TestNilCooldownRegistryAllowsAll(t *testing.T) {
	var r *ChannelCooldownRegistry
	if !r.Allowed("1") {
		t.Fatal("nil registry should allow")
	}
	if _, ok := r.RecordRateLimit("1", time.Second); ok {
		t.Fatal("nil registry should not record")
	}
}

func TestChannelRateLimitRetryAfterExtraction(t *testing.T) {
	rateErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorRateLimit,
		adapter.UpstreamMetadata{StatusCode: 429, RetryAfter: 20 * time.Second},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)
	d, ok := channelRateLimitRetryAfter(rateErr)
	if !ok || d != 20*time.Second {
		t.Fatalf("rate limit extraction: ok=%v d=%s", ok, d)
	}

	// 非 rate_limit 分类不参与冷却。
	serverErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorServer,
		adapter.UpstreamMetadata{StatusCode: 500},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)
	if _, ok := channelRateLimitRetryAfter(serverErr); ok {
		t.Fatal("server error should not yield retry-after")
	}

	// 非上游错误链返回 false。
	if _, ok := channelRateLimitRetryAfter(failure.New(failure.CodeAdapterUpstreamStatus)); ok {
		t.Fatal("plain failure should not yield retry-after")
	}
}

// TestFailureCooldownRecordsAndExpires 验证失败软冷却：登记 → FailurePreferred=false → 到期自动恢复；
// 与 429 冷却相互独立（软冷却不影响 Allowed）。
func TestFailureCooldownRecordsAndExpires(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(5*time.Second, time.Minute, &now)
	r.SetFailureCooldown(8 * time.Second)

	if !r.FailurePreferred("7") {
		t.Fatal("channel should start preferred")
	}

	until, ok := r.RecordFailure("7")
	if !ok || !until.Equal(now.Add(8*time.Second)) {
		t.Fatalf("record failure: ok=%v until=%s", ok, until)
	}
	if r.FailurePreferred("7") {
		t.Fatal("channel should be soft-cooled after failure")
	}
	// 软冷却不影响 429 硬冷却判定。
	if !r.Allowed("7") {
		t.Fatal("soft cooldown must not affect hard 429 cooldown gate")
	}

	now = now.Add(9 * time.Second)
	if !r.FailurePreferred("7") {
		t.Fatal("channel should recover after failure cooldown expires")
	}
}

// TestFailureCooldownDisabledByDefault 验证未设置失败软冷却时长（0）时不登记。
func TestFailureCooldownDisabledByDefault(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(5*time.Second, time.Minute, &now)

	if _, ok := r.RecordFailure("7"); ok {
		t.Fatal("failure cooldown should be disabled when duration is 0")
	}
	if !r.FailurePreferred("7") {
		t.Fatal("channel should remain preferred")
	}
}

// TestFailureCooldownKeepsLaterExpiry 连续失败续期，不缩短已登记的软冷却。
func TestFailureCooldownKeepsLaterExpiry(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(5*time.Second, time.Minute, &now)
	r.SetFailureCooldown(10 * time.Second)

	r.RecordFailure("7")
	now = now.Add(4 * time.Second)
	until, ok := r.RecordFailure("7")
	if !ok || !until.Equal(now.Add(10*time.Second)) {
		t.Fatalf("consecutive failure should extend expiry: ok=%v until=%s", ok, until)
	}
}

// TestIsFailureCooldownError 验证只有 timeout/server 分类触发软冷却。
func TestIsFailureCooldownError(t *testing.T) {
	mk := func(cat adapter.UpstreamErrorCategory) error {
		return adapter.NewUpstreamError(cat, adapter.UpstreamMetadata{}, failure.New(failure.CodeAdapterUpstreamStatus))
	}
	if !isFailureCooldownError(mk(adapter.UpstreamErrorTimeout)) {
		t.Fatal("timeout should trigger failure cooldown")
	}
	if !isFailureCooldownError(mk(adapter.UpstreamErrorServer)) {
		t.Fatal("server error should trigger failure cooldown")
	}
	for _, cat := range []adapter.UpstreamErrorCategory{
		adapter.UpstreamErrorRateLimit,
		adapter.UpstreamErrorAuth,
		adapter.UpstreamErrorPermission,
		adapter.UpstreamErrorBadRequest,
		adapter.UpstreamErrorCanceled,
	} {
		if isFailureCooldownError(mk(cat)) {
			t.Fatalf("category %v should not trigger failure cooldown", cat)
		}
	}
	if isFailureCooldownError(failure.New(failure.CodeAdapterUpstreamStatus)) {
		t.Fatal("error without upstream category should not trigger failure cooldown")
	}
}

// TestRequestLifecycleRecordChannelFailureCooldown 验证 lifecycle 入口：timeout 错误登记软冷却，
// CandidateFailurePreferred 随之翻 false，到期恢复。
func TestRequestLifecycleRecordChannelFailureCooldown(t *testing.T) {
	now := time.Date(2026, 7, 10, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(5*time.Second, time.Minute, &now)
	r.SetFailureCooldown(6 * time.Second)

	lc := &RequestLifecycle{}
	lc.SetChannelCooldownRegistry(r)

	timeoutErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorTimeout,
		adapter.UpstreamMetadata{},
		failure.New(failure.CodeAdapterSendRequestFailed),
	)

	if !lc.RecordChannelFailureCooldown("42", timeoutErr) {
		t.Fatal("expected failure cooldown to be recorded")
	}
	if lc.CandidateFailurePreferred(candidateRoute(42, "openai")) {
		t.Fatal("channel should not be preferred during failure cooldown")
	}
	// 软冷却不把渠道从硬闸门里摘掉。
	if !lc.BreakerAllow("42") {
		t.Fatal("failure cooldown must not hard-block the channel")
	}

	now = now.Add(7 * time.Second)
	if !lc.CandidateFailurePreferred(candidateRoute(42, "openai")) {
		t.Fatal("channel should be preferred again after cooldown expires")
	}
}

func TestRequestLifecycleBreakerAllowHonorsCooldown(t *testing.T) {
	now := time.Date(2026, 6, 28, 12, 0, 0, 0, time.UTC)
	r := newTestCooldown(5*time.Second, time.Minute, &now)

	lc := &RequestLifecycle{}
	lc.SetChannelCooldownRegistry(r)

	rateErr := adapter.NewUpstreamError(
		adapter.UpstreamErrorRateLimit,
		adapter.UpstreamMetadata{StatusCode: 429, RetryAfter: 15 * time.Second},
		failure.New(failure.CodeAdapterUpstreamStatus),
	)

	if !lc.BreakerAllow("42") {
		t.Fatal("channel should start allowed")
	}
	if !lc.RecordChannelRateLimit("42", rateErr) {
		t.Fatal("expected cooldown to be recorded")
	}
	if lc.BreakerAllow("42") {
		t.Fatal("channel should be skipped during cooldown")
	}

	now = now.Add(16 * time.Second)
	if !lc.BreakerAllow("42") {
		t.Fatal("channel should recover after cooldown")
	}
}
