package ratelimit

import (
	"sync"
	"testing"
)

func TestConcurrencyLimiterChannelLimitAndRelease(t *testing.T) {
	l := NewConcurrencyLimiter(0, 2)

	r1, ok := l.AcquireChannel(7, nil)
	if !ok {
		t.Fatal("first acquire should pass")
	}
	r2, ok := l.AcquireChannel(7, nil)
	if !ok {
		t.Fatal("second acquire should pass")
	}
	if _, ok := l.AcquireChannel(7, nil); ok {
		t.Fatal("third acquire should hit the limit")
	}
	// 其它渠道不受影响。
	rOther, ok := l.AcquireChannel(8, nil)
	if !ok {
		t.Fatal("another channel should be independent")
	}
	rOther()

	r1()
	if _, ok := l.AcquireChannel(7, nil); !ok {
		t.Fatal("acquire should pass again after release")
	}
	r2()
}

func TestConcurrencyLimiterChannelOverride(t *testing.T) {
	l := NewConcurrencyLimiter(0, 2)

	// override=1：渠道行覆盖全局默认。
	one := int64(1)
	r1, ok := l.AcquireChannel(7, &one)
	if !ok {
		t.Fatal("first acquire should pass")
	}
	if _, ok := l.AcquireChannel(7, &one); ok {
		t.Fatal("override=1 should reject the second acquire")
	}
	r1()

	// override=0：显式不限（即使全局默认为 2）。
	zero := int64(0)
	releases := make([]func(), 0, 5)
	for i := 0; i < 5; i++ {
		r, ok := l.AcquireChannel(9, &zero)
		if !ok {
			t.Fatalf("override=0 must be unlimited, rejected at %d", i)
		}
		releases = append(releases, r)
	}
	for _, r := range releases {
		r()
	}
	if got := l.Inflight(ChannelInflightSubject(9)); got != 0 {
		t.Fatalf("inflight should drop to 0 after releases, got %d", got)
	}
}

func TestConcurrencyLimiterRouteUserLimit(t *testing.T) {
	l := NewConcurrencyLimiter(1, 0)

	r1, ok := l.AcquireRouteUser(3, 5)
	if !ok {
		t.Fatal("first acquire should pass")
	}
	if _, ok := l.AcquireRouteUser(3, 5); ok {
		t.Fatal("second acquire should hit key limit 1")
	}
	// 不同用户独立。
	rOther, ok := l.AcquireRouteUser(3, 6)
	if !ok {
		t.Fatal("different user should be independent")
	}
	rOther()
	r1()
}

func TestConcurrencyLimiterReleaseIdempotent(t *testing.T) {
	l := NewConcurrencyLimiter(1, 0)

	r1, _ := l.AcquireRouteUser(1, 1)
	r1()
	r1() // 重复调用只释放一次。
	if got := l.Inflight(RouteUserInflightSubject(1, 1)); got != 0 {
		t.Fatalf("inflight should be 0 (not negative), got %d", got)
	}

	// 计数没有被重复释放挖成负数：再占一个名额后应恰好到达上限。
	r2, ok := l.AcquireRouteUser(1, 1)
	if !ok {
		t.Fatal("acquire should pass on empty counter")
	}
	if _, ok := l.AcquireRouteUser(1, 1); ok {
		t.Fatal("limit 1 should reject the second in-flight request")
	}
	r2()
}

func TestConcurrencyLimiterSetDefaultsHotReload(t *testing.T) {
	l := NewConcurrencyLimiter(0, 0)

	// 默认 0=不限，但仍计数：热改上限后立即按当前在途量生效。
	r1, _ := l.AcquireChannel(7, nil)
	r2, _ := l.AcquireChannel(7, nil)
	l.SetDefaults(0, 2)
	if _, ok := l.AcquireChannel(7, nil); ok {
		t.Fatal("after hot reload to limit 2, third in-flight should be rejected")
	}
	r1()
	r2()
}

func TestConcurrencyLimiterNilReceiverAllowsAll(t *testing.T) {
	var l *ConcurrencyLimiter
	release, ok := l.AcquireRouteUser(1, 1)
	if !ok {
		t.Fatal("nil limiter should allow")
	}
	release()
	release2, ok := l.AcquireChannel(1, nil)
	if !ok {
		t.Fatal("nil limiter should allow channel acquire")
	}
	release2()
}

// TestConcurrencyLimiterConcurrentAccess 在 -race 下验证并发 acquire/release 无竞态且计数收敛为 0。
func TestConcurrencyLimiterConcurrentAccess(t *testing.T) {
	l := NewConcurrencyLimiter(0, 8)
	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if release, ok := l.AcquireChannel(7, nil); ok {
				release()
			}
			l.SetDefaults(0, 8)
		}()
	}
	wg.Wait()
	if got := l.Inflight(ChannelInflightSubject(7)); got != 0 {
		t.Fatalf("inflight should converge to 0, got %d", got)
	}
}
