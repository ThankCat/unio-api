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
