package bootstrap

import (
	"context"
	"encoding/json"
	"sync"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// fakeSettingsReader 是 applier 单测用的内存配置源。
type fakeSettingsReader struct {
	mu     sync.Mutex
	values map[string]json.RawMessage
	reads  map[string]int
}

type fakeChannel429PolicyTarget struct {
	defaultCooldown time.Duration
	cap             time.Duration
	calls           int
}

func (f *fakeChannel429PolicyTarget) SetChannel429CooldownPolicy(defaultCooldown, cap time.Duration) {
	f.defaultCooldown = defaultCooldown
	f.cap = cap
	f.calls++
}

func (f *fakeSettingsReader) Raw(_ context.Context, key string) json.RawMessage {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reads[key]++
	return f.values[key]
}

func (f *fakeSettingsReader) set(key, value string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.values[key] = json.RawMessage(value)
}

func newApplierFixture() (*settingsApplier, *fakeSettingsReader, *fakeChatRouteStore) {
	store := &fakeSettingsReader{values: map[string]json.RawMessage{}, reads: map[string]int{}}
	routeStore := &fakeChatRouteStore{}
	a := &settingsApplier{
		store:  store,
		logger: zap.NewNop(),
		gate:   lifecycle.NewChannelCredentialGate(3, nil),
		router: routing.NewRouter(routeStore, 30*time.Second),
	}
	return a, store, routeStore
}

// TestSettingsApplierAppliesLocalSettings 验证非准入类本机配置仍能热更新。
func TestSettingsApplierAppliesLocalSettings(t *testing.T) {
	t.Cleanup(func() { adapter.SetStreamIdleTimeout(0) })
	a, store, _ := newApplierFixture()
	channel429 := &fakeChannel429PolicyTarget{}
	a.channel429 = channel429

	store.set(appsettings.GatewayStreamIdleTimeoutKey, `900000`)
	store.set(appsettings.GatewayCredential401ThresholdKey, `7`)
	store.set(appsettings.GatewayDefaultChannelTimeoutKey, `42000`)
	store.set(appsettings.GatewayChannelCooldownKey, `{"cooldown_ms":5000,"cap_ms":300000}`)

	a.applyOnce(context.Background())

	if got := adapter.StreamIdleTimeout(); got != 15*time.Minute {
		t.Errorf("stream idle timeout = %v, want 15m", got)
	}
	if channel429.calls != 1 || channel429.defaultCooldown != 5*time.Second || channel429.cap != 5*time.Minute {
		t.Errorf("channel 429 policy = calls:%d default:%v cap:%v", channel429.calls, channel429.defaultCooldown, channel429.cap)
	}

	store.set(appsettings.GatewayChannelCooldownKey, `{"cooldown_ms":7000,"cap_ms":90000}`)
	a.applyOnce(context.Background())
	if channel429.calls != 2 || channel429.defaultCooldown != 7*time.Second || channel429.cap != 90*time.Second {
		t.Errorf("hot-reloaded channel 429 policy = calls:%d default:%v cap:%v", channel429.calls, channel429.defaultCooldown, channel429.cap)
	}
}

func TestSettingsApplierDoesNotReadRuntimeControlSettings(t *testing.T) {
	a, store, _ := newApplierFixture()
	a.applyOnce(context.Background())

	for _, key := range []string{
		appsettings.GatewayCircuitBreakerKey,
		appsettings.GatewayRouteRateLimitDefaultsKey,
		appsettings.GatewayChannelRateLimitDefaultsKey,
		appsettings.GatewayConcurrencyDefaultsKey,
		appsettings.GatewayRoutingBalanceKey,
	} {
		if store.reads[key] != 0 {
			t.Errorf("settings applier must not read runtime control key %q", key)
		}
	}
}

// TestSettingsApplierKeepsCurrentOnDecodeError 验证坏数据不推送、保持当前值。
func TestSettingsApplierKeepsCurrentOnDecodeError(t *testing.T) {
	t.Cleanup(func() { adapter.SetStreamIdleTimeout(0) })
	a, store, _ := newApplierFixture()
	channel429 := &fakeChannel429PolicyTarget{defaultCooldown: 5 * time.Second, cap: time.Minute}
	a.channel429 = channel429

	adapter.SetStreamIdleTimeout(10 * time.Minute)
	store.set(appsettings.GatewayStreamIdleTimeoutKey, `"not-an-int"`)
	store.set(appsettings.GatewayCredential401ThresholdKey, `-1`)
	store.set(appsettings.GatewayChannelCooldownKey, `{"cooldown_ms":-1,"cap_ms":60000}`)

	a.applyOnce(context.Background())

	if got := adapter.StreamIdleTimeout(); got != 10*time.Minute {
		t.Errorf("stream idle timeout should keep current 10m on decode error, got %v", got)
	}
	if channel429.calls != 0 || channel429.defaultCooldown != 5*time.Second || channel429.cap != time.Minute {
		t.Errorf("invalid channel 429 policy changed current value: %+v", channel429)
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
