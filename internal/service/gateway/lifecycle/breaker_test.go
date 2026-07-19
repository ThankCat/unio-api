package lifecycle

import (
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// newTestBreaker 创建一个使用可控时钟的熔断器。
func newTestBreaker(cfg ChannelCircuitBreakerConfig) (*ChannelCircuitBreaker, *time.Time) {
	b := NewChannelCircuitBreaker(cfg)
	clock := time.Now()
	b.now = func() time.Time { return clock }

	return b, &clock
}

func TestChannelCircuitBreakerTripsAfterThreshold(t *testing.T) {
	b, _ := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  4,
		FailureRatio: 0.5,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	// 未达到 MinRequests 时不熔断。
	b.RecordFailure(key)
	b.RecordFailure(key)
	b.RecordFailure(key)
	if !b.Allow(key) {
		t.Fatal("breaker should stay closed below MinRequests")
	}

	// 第 4 次失败：total=4 >= MinRequests，ratio=1.0 >= 0.5 → 熔断。
	b.RecordFailure(key)
	if b.Allow(key) {
		t.Fatal("breaker should be open after crossing failure threshold")
	}
}

func TestChannelCircuitBreakerHalfOpenRecovers(t *testing.T) {
	b, clock := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  2,
		FailureRatio: 0.5,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	b.RecordFailure(key)
	b.RecordFailure(key)
	if b.Allow(key) {
		t.Fatal("breaker should be open")
	}

	// 冷却未到，仍然拒绝。
	*clock = clock.Add(5 * time.Second)
	if b.Allow(key) {
		t.Fatal("breaker should stay open within cooldown")
	}

	// 冷却到达，放行一次半开探测，且并发探测被拒绝。
	*clock = clock.Add(6 * time.Second)
	if !b.Allow(key) {
		t.Fatal("breaker should allow a half-open probe after cooldown")
	}
	if b.Allow(key) {
		t.Fatal("breaker should deny concurrent half-open probe")
	}

	// 探测成功 → 恢复闭合。
	b.RecordSuccess(key)
	if !b.Allow(key) {
		t.Fatal("breaker should close after successful probe")
	}
}

func TestChannelCircuitBreakerAvailableDoesNotReserveHalfOpenProbe(t *testing.T) {
	b, clock := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  2,
		FailureRatio: 0.5,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	b.RecordFailure(key)
	b.RecordFailure(key)

	*clock = clock.Add(11 * time.Second)
	if !b.Available(key) {
		t.Fatal("read-only availability should report the cooled-down channel without reserving its probe")
	}
	if !b.Allow(key) {
		t.Fatal("first real attempt should reserve the half-open probe")
	}
	if b.Allow(key) {
		t.Fatal("concurrent real attempt should not reserve a second half-open probe")
	}
}

func TestChannelCircuitBreakerReopensOnProbeFailure(t *testing.T) {
	b, clock := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  2,
		FailureRatio: 0.5,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	b.RecordFailure(key)
	b.RecordFailure(key)

	*clock = clock.Add(11 * time.Second)
	if !b.Allow(key) {
		t.Fatal("breaker should allow a half-open probe")
	}

	// 探测失败 → 重新熔断。
	b.RecordFailure(key)
	if b.Allow(key) {
		t.Fatal("breaker should re-open after a failed probe")
	}
}

func TestChannelCircuitBreakerWindowResetsCounts(t *testing.T) {
	b, clock := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  3,
		FailureRatio: 0.9,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	b.RecordFailure(key)
	b.RecordFailure(key)

	// 跨过窗口后计数清零，旧失败不再累计触发熔断。
	*clock = clock.Add(2 * time.Minute)
	b.RecordFailure(key)
	if !b.Allow(key) {
		t.Fatal("breaker should stay closed after window reset clears old failures")
	}
}

func TestChannelCircuitBreakerSetConfigTakesEffect(t *testing.T) {
	b, _ := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  10,
		FailureRatio: 0.9,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	// 旧阈值下 2 次失败远不足以熔断。
	b.RecordFailure(key)
	b.RecordFailure(key)
	if !b.Allow(key) {
		t.Fatal("breaker should stay closed under old thresholds")
	}

	// 热改为更敏感的阈值:窗口计数保留(2 failures),第 3 次失败即触发。
	b.SetConfig(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  3,
		FailureRatio: 0.5,
		OpenDuration: 10 * time.Second,
	})
	b.RecordFailure(key)
	if b.Allow(key) {
		t.Fatal("breaker should trip using hot-reloaded thresholds")
	}
}

func TestChannelCircuitBreakerSetConfigNormalizesInvalid(t *testing.T) {
	b, _ := newTestBreaker(ChannelCircuitBreakerConfig{})
	b.SetConfig(ChannelCircuitBreakerConfig{Window: -1, MinRequests: -1, FailureRatio: 2, OpenDuration: -1})

	b.mu.Lock()
	cfg := b.cfg
	b.mu.Unlock()
	want := ChannelCircuitBreakerConfig{Window: 30 * time.Second, MinRequests: 20, FailureRatio: 0.5, OpenDuration: 30 * time.Second}
	if cfg != want {
		t.Fatalf("normalized cfg = %+v, want %+v", cfg, want)
	}
}

func TestChannelCircuitBreakerSetEnabled(t *testing.T) {
	b, _ := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  2,
		FailureRatio: 0.5,
		OpenDuration: 10 * time.Second,
	})

	key := "1"
	b.RecordFailure(key)
	b.RecordFailure(key)
	if b.Allow(key) {
		t.Fatal("breaker should be open")
	}

	// 运行期禁用:放行全部、健康分归零、不记状态。
	b.SetEnabled(false)
	if b.Enabled() {
		t.Fatal("Enabled() should be false after disable")
	}
	if !b.Allow(key) || !b.Available(key) {
		t.Fatal("disabled breaker must allow everything")
	}
	if score := b.HealthScore(key); score != 0 {
		t.Fatalf("disabled breaker health score = %v, want 0", score)
	}
	b.RecordFailure(key)
	b.RecordFailure(key)

	// 重新启用:禁用边沿已清空状态,旧熔断/禁用期间的失败都不追溯。
	b.SetEnabled(true)
	if !b.Enabled() {
		t.Fatal("Enabled() should be true after enable")
	}
	if !b.Allow(key) {
		t.Fatal("re-enabled breaker should start from clean state")
	}

	// 重新启用后照常工作。
	b.RecordFailure(key)
	b.RecordFailure(key)
	if b.Allow(key) {
		t.Fatal("re-enabled breaker should trip on fresh failures")
	}
}

func TestChannelCircuitBreakerHealthScoreIncludesLatency(t *testing.T) {
	fast := NewChannelCircuitBreaker(ChannelCircuitBreakerConfig{Window: time.Minute})
	slow := NewChannelCircuitBreaker(ChannelCircuitBreakerConfig{Window: time.Minute})
	for range 5 {
		fast.RecordLatency("1", 100*time.Millisecond)
		fast.RecordSuccess("1")
		slow.RecordLatency("1", 5*time.Second)
		slow.RecordSuccess("1")
	}
	fastScore := fast.HealthScore("1")
	slowScore := slow.HealthScore("1")
	if fastScore <= 0 || slowScore <= fastScore || slowScore >= 1 {
		t.Fatalf("expected latency to monotonically lower health without hard exclusion: fast=%v slow=%v", fastScore, slowScore)
	}
}

// TestChannelCircuitBreakerConcurrentReload 在 -race 下验证热改与热路径读写无竞态。
func TestChannelCircuitBreakerConcurrentReload(t *testing.T) {
	b := NewChannelCircuitBreaker(ChannelCircuitBreakerConfig{})
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 500; i++ {
			b.SetConfig(ChannelCircuitBreakerConfig{Window: time.Minute, MinRequests: i%10 + 1, FailureRatio: 0.5, OpenDuration: time.Second})
			b.SetEnabled(i%2 == 0)
		}
	}()
	for i := 0; i < 500; i++ {
		b.Allow("1")
		b.RecordFailure("1")
		b.RecordSuccess("1")
		b.HealthScore("1")
		b.Available("1")
	}
	<-done
}

func TestIsChannelFaultError(t *testing.T) {
	faulty := []adapter.UpstreamErrorCategory{
		adapter.UpstreamErrorTimeout,
		adapter.UpstreamErrorServer,
		adapter.UpstreamErrorRateLimit,
		adapter.UpstreamErrorPermission,
	}
	for _, category := range faulty {
		err := adapter.NewUpstreamError(category, adapter.UpstreamMetadata{}, failure.New(failure.CodeAdapterUpstreamStatus))
		if !IsChannelFaultError(err) {
			t.Errorf("category %q should be channel fault", category)
		}
	}

	// auth（401）改由凭据闸门专管，不再计入进程内熔断（DEC 2026-07 C-4）。
	notFaulty := []error{
		nil,
		adapter.NewUpstreamError(adapter.UpstreamErrorAuth, adapter.UpstreamMetadata{}, failure.New(failure.CodeAdapterUpstreamStatus)),
		adapter.NewUpstreamError(adapter.UpstreamErrorBadRequest, adapter.UpstreamMetadata{}, failure.New(failure.CodeAdapterUpstreamStatus)),
		adapter.NewUpstreamError(adapter.UpstreamErrorCanceled, adapter.UpstreamMetadata{}, failure.New(failure.CodeAdapterUpstreamStatus)),
		errors.New("non-upstream error"),
	}
	for _, err := range notFaulty {
		if IsChannelFaultError(err) {
			t.Errorf("error %v should not be channel fault", err)
		}
	}
}

func TestChannelCircuitBreakerSnapshotOpenRemaining(t *testing.T) {
	b, clock := newTestBreaker(ChannelCircuitBreakerConfig{
		Window:       time.Minute,
		MinRequests:  2,
		FailureRatio: 0.5,
		OpenDuration: 10 * time.Second,
	})

	key := "14"
	b.RecordFailure(key)
	b.RecordFailure(key)
	if b.Allow(key) {
		t.Fatal("expected open")
	}

	*clock = clock.Add(4 * time.Second)
	snap := b.Snapshot()
	if !snap.Enabled {
		t.Fatal("expected enabled")
	}
	if len(snap.Channels) != 1 {
		t.Fatalf("expected 1 channel, got %d", len(snap.Channels))
	}
	ch := snap.Channels[0]
	if ch.ChannelID != 14 || ch.State != CircuitStateOpen {
		t.Fatalf("unexpected entry: %+v", ch)
	}
	if ch.OpenRemainingMs == nil || *ch.OpenRemainingMs != 6000 {
		t.Fatalf("expected remaining 6000ms, got %v", ch.OpenRemainingMs)
	}

	// Snapshot must not advance open → half-open.
	snap2 := b.Snapshot()
	if snap2.Channels[0].State != CircuitStateOpen {
		t.Fatal("snapshot must not mutate open into half-open")
	}
}
