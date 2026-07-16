package bootstrap

import (
	"context"
	"encoding/json"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/ratelimit"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// fakeSettingsReader 是 applier 单测用的内存配置源。
type fakeSettingsReader struct {
	mu     sync.Mutex
	values map[string]json.RawMessage
}

func (f *fakeSettingsReader) Raw(_ context.Context, key string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.values[key]
}

func (f *fakeSettingsReader) set(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = json.RawMessage(value)
}

func newApplierFixture() (*settingsApplier, *fakeSettingsReader, *fakeChatRouteStore) {
	store := &fakeSettingsReader{values: map[string]json.RawMessage{}}
	routeStore := &fakeChatRouteStore{}
	a := &settingsApplier{
		store:       store,
		logger:      slog.Default(),
		breaker:     lifecycle.NewChannelCircuitBreaker(lifecycle.ChannelCircuitBreakerConfig{}),
		guard:       NewRateLimitGuard(nil, "test", appsettings.DefaultRateLimitDefaultsSettings(), slog.Default()),
		cooldown:    lifecycle.NewChannelCooldownRegistry(5*time.Second, 5*time.Minute),
		gate:        lifecycle.NewChannelCredentialGate(3, nil),
		router:      routing.NewRouter(routeStore, 30*time.Second),
		concurrency: ratelimit.NewConcurrencyLimiter(0, 0),
	}
	return a, store, routeStore
}

// TestSettingsApplierAppliesAllSixGroups 验证「改配置源 → applyOnce → 消费方生效」闭环。
func TestSettingsApplierAppliesAllSixGroups(t *testing.T) {
	t.Cleanup(func() { adapter.SetStreamIdleTimeout(0) })
	a, store, _ := newApplierFixture()

	store.set(appsettings.GatewayCircuitBreakerKey,
		`{"enabled":false,"window_ms":60000,"min_requests":5,"failure_ratio":0.9,"open_duration_ms":45000}`)
	store.set(appsettings.GatewayRateLimitDefaultsKey,
		`{"rpm":120,"tpm":90000,"rpd":5000,"failure_policy":"fail_open"}`)
	store.set(appsettings.GatewayStreamIdleTimeoutKey, `900000`)
	store.set(appsettings.GatewayChannelCooldownKey, `{"cooldown_ms":9000,"cap_ms":120000}`)
	store.set(appsettings.GatewayCredential401ThresholdKey, `7`)
	store.set(appsettings.GatewayDefaultChannelTimeoutKey, `42000`)
	store.set(appsettings.GatewayFailureCooldownKey, `7000`)
	store.set(appsettings.GatewayConcurrencyDefaultsKey, `{"key_limit":1,"channel_limit":2}`)

	a.applyOnce(context.Background())

	if a.breaker.Enabled() {
		t.Error("breaker should be disabled after apply")
	}
	if got := adapter.StreamIdleTimeout(); got != 15*time.Minute {
		t.Errorf("stream idle timeout = %v, want 15m", got)
	}
	// guard:热改后 TokensEnforced 反映新默认 TPM(>0 即 enforced)。
	if !a.guard.TokensEnforced(ratelimit.Limits{}) {
		t.Error("guard TPM default should be enforced after apply")
	}
	// cooldown:无 Retry-After 时用新默认 9s。
	until, ok := a.cooldown.RecordRateLimit("c", 0)
	if !ok || time.Until(until) > 10*time.Second {
		t.Errorf("cooldown not applied: ok=%v until=%v", ok, until)
	}
	// 失败软冷却:热改后 RecordFailure 生效(7s)。
	if _, ok := a.cooldown.RecordFailure("c"); !ok {
		t.Error("failure cooldown should be enabled after apply")
	}
	// 并发默认:key_limit=1 生效,第二个在途请求被拒。
	release, ok := a.concurrency.AcquireRouteUser(1, 1)
	if !ok {
		t.Fatal("first in-flight should be allowed")
	}
	if _, ok := a.concurrency.AcquireRouteUser(1, 1); ok {
		t.Error("second in-flight should be rejected after key_limit=1 applied")
	}
	release()
}

// TestSettingsApplierKeepsCurrentOnDecodeError 验证坏数据不推送、保持当前值。
func TestSettingsApplierKeepsCurrentOnDecodeError(t *testing.T) {
	t.Cleanup(func() { adapter.SetStreamIdleTimeout(0) })
	a, store, _ := newApplierFixture()

	adapter.SetStreamIdleTimeout(10 * time.Minute)
	store.set(appsettings.GatewayStreamIdleTimeoutKey, `"not-an-int"`)
	store.set(appsettings.GatewayCredential401ThresholdKey, `-1`)

	a.applyOnce(context.Background())

	if got := adapter.StreamIdleTimeout(); got != 10*time.Minute {
		t.Errorf("stream idle timeout should keep current 10m on decode error, got %v", got)
	}
}

// TestSettingsApplierRunStopsOnContextCancel 验证 applier 随 shutdown 退出。
func TestSettingsApplierRunStopsOnContextCancel(t *testing.T) {
	t.Cleanup(func() { adapter.SetStreamIdleTimeout(0) })
	a, _, _ := newApplierFixture()

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		a.run(ctx, time.Millisecond)
		close(done)
	}()

	time.Sleep(10 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("applier did not stop after context cancel")
	}
}
