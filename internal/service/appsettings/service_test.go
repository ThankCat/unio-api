package appsettings

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

type fakeRuntimeControlPublisher struct {
	requests []runtimecontrol.PublishRequest
	result   runtimecontrol.PublishResult
	err      error
}

func (p *fakeRuntimeControlPublisher) Publish(_ context.Context, req runtimecontrol.PublishRequest) (runtimecontrol.PublishResult, error) {
	p.requests = append(p.requests, req)
	return p.result, p.err
}

type fakeRuntimeControlStore struct {
	snapshot           breakerstore.ControlSnapshot
	err                error
	restored           []string
	routeTargetCalls   int
	channelTargetCalls int
}

func (s *fakeRuntimeControlStore) SettingControl(string) breakerstore.ControlTarget {
	return breakerstore.ControlTarget{}
}

func (s *fakeRuntimeControlStore) RouteRateLimitControl() breakerstore.ControlTarget {
	s.routeTargetCalls++
	return breakerstore.ControlTarget{}
}

func (s *fakeRuntimeControlStore) ChannelRateLimitControl() breakerstore.ControlTarget {
	s.channelTargetCalls++
	return breakerstore.ControlTarget{}
}

func (s *fakeRuntimeControlStore) GlobalConcurrencyControl() breakerstore.ControlTarget {
	return breakerstore.ControlTarget{}
}

func (s *fakeRuntimeControlStore) ReadControl(context.Context, breakerstore.ControlTarget, int64) (breakerstore.ControlSnapshot, error) {
	return s.snapshot, s.err
}

func (s *fakeRuntimeControlStore) RestoreMissingControl(_ context.Context, _ breakerstore.ControlTarget, revision int64, payload string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	s.restored = append(s.restored, payload)
	s.snapshot = breakerstore.ControlSnapshot{
		ActiveRevision: revision,
		ActivePayload:  payload,
		SyncState:      "active",
	}
	return true, nil
}

func TestCriticalSettingUsesDurablePublisher(t *testing.T) {
	q := newFakeQueries()
	q.data[GatewayRouteRateLimitDefaultsKey] = encodeRateLimitDefaultsSettings(DefaultRateLimitDefaultsSettings())
	store := newTestStore(q)
	publisher := &fakeRuntimeControlPublisher{result: runtimecontrol.PublishResult{
		State: runtimecontrol.PublishCommitted, ActiveRevision: 2,
	}}
	runtimeStore := &fakeRuntimeControlStore{}
	service := NewServiceWithRuntimeControl(store, publisher, runtimeStore)

	result, err := service.SetRawWithResult(context.Background(), GatewayRouteRateLimitDefaultsKey, json.RawMessage(`{"rpd":5,"rpm":120,"tpm":9000}`))
	if err != nil {
		t.Fatalf("set critical setting: %v", err)
	}
	if result.State != "active" || result.Revision != 2 || result.ActiveRevision != 2 {
		t.Fatalf("unexpected result: %+v", result)
	}
	if len(publisher.requests) != 1 {
		t.Fatalf("publisher calls = %d, want 1", len(publisher.requests))
	}
	req := publisher.requests[0]
	if req.CurrentRevision != 1 || req.NextRevision != 2 || req.Kind != runtimecontrol.KindAppSetting {
		t.Fatalf("unexpected publish request: %+v", req)
	}
	if req.SettingKey == nil || *req.SettingKey != GatewayRouteRateLimitDefaultsKey ||
		runtimeStore.routeTargetCalls != 1 || runtimeStore.channelTargetCalls != 0 {
		t.Fatalf("route setting used wrong runtime control: request=%+v store=%+v", req, runtimeStore)
	}
	if req.Payload != `{"rpm":120,"tpm":9000,"rpd":5}` {
		t.Fatalf("payload must be canonical and omit failure_policy: %s", req.Payload)
	}
}

func TestChannelRateLimitSettingUsesIndependentRuntimeControl(t *testing.T) {
	q := newFakeQueries()
	q.data[GatewayChannelRateLimitDefaultsKey] = encodeRateLimitDefaultsSettings(DefaultRateLimitDefaultsSettings())
	publisher := &fakeRuntimeControlPublisher{result: runtimecontrol.PublishResult{
		State: runtimecontrol.PublishCommitted, ActiveRevision: 2,
	}}
	runtimeStore := &fakeRuntimeControlStore{}
	service := NewServiceWithRuntimeControl(newTestStore(q), publisher, runtimeStore)

	_, err := service.SetRawWithResult(
		context.Background(),
		GatewayChannelRateLimitDefaultsKey,
		json.RawMessage(`{"rpm":240,"tpm":18000,"rpd":10}`),
	)
	if err != nil {
		t.Fatalf("set channel rate limit defaults: %v", err)
	}
	if len(publisher.requests) != 1 {
		t.Fatalf("publisher calls = %d, want 1", len(publisher.requests))
	}
	req := publisher.requests[0]
	if req.SettingKey == nil || *req.SettingKey != GatewayChannelRateLimitDefaultsKey ||
		runtimeStore.routeTargetCalls != 0 || runtimeStore.channelTargetCalls != 1 {
		t.Fatalf("channel setting used wrong runtime control: request=%+v store=%+v", req, runtimeStore)
	}
}

func TestCriticalSettingSameSemanticValueDoesNotAdvanceRevision(t *testing.T) {
	q := newFakeQueries()
	current := encodeRoutingBalanceSettings(DefaultRoutingBalanceSettings())
	q.data[GatewayRoutingBalanceKey] = current
	store := newTestStore(q)
	publisher := &fakeRuntimeControlPublisher{}
	runtimeStore := &fakeRuntimeControlStore{snapshot: breakerstore.ControlSnapshot{
		ActiveRevision: 1,
		ActivePayload:  string(current),
		SyncState:      "active",
	}}
	service := NewServiceWithRuntimeControl(store, publisher, runtimeStore)

	result, err := service.SetRawWithResult(context.Background(), GatewayRoutingBalanceKey, json.RawMessage(`{
		"ttft_ewma_alpha": 0.2,
		"minimum_routing_factor": 0.05,
		"cost_weight": 0.5,
		"ttft_weight": 0.35,
		"ttft_target_ms": 2000
	}`))
	if err != nil {
		t.Fatalf("idempotent setting update: %v", err)
	}
	if result.State != "active" || result.Revision != 1 || result.ActiveRevision != 1 {
		t.Fatalf("unexpected idempotent result: %+v", result)
	}
	if len(publisher.requests) != 0 {
		t.Fatalf("idempotent update must not publish, calls=%d", len(publisher.requests))
	}
}

func TestLegacyRoutingBalanceSameSemanticValueDoesNotAdvanceRevision(t *testing.T) {
	legacy := json.RawMessage(`{"ttft_target_ms":2000,"ttft_weight":0.35,"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2}`)
	q := newFakeQueries()
	q.data[GatewayRoutingBalanceKey] = legacy
	store := newTestStore(q)
	publisher := &fakeRuntimeControlPublisher{}
	runtimeStore := &fakeRuntimeControlStore{snapshot: breakerstore.ControlSnapshot{
		ActiveRevision: 1,
		ActivePayload:  string(legacy),
		SyncState:      "active",
	}}
	service := NewServiceWithRuntimeControl(store, publisher, runtimeStore)

	result, err := service.SetRawWithResult(context.Background(), GatewayRoutingBalanceKey, legacy)
	if err != nil {
		t.Fatalf("idempotent legacy setting update: %v", err)
	}
	if result.State != "active" || result.Revision != 1 || result.ActiveRevision != 1 {
		t.Fatalf("legacy payload must remain active at the same revision: %+v", result)
	}
	if len(publisher.requests) != 0 {
		t.Fatalf("legacy no-op must not publish a cost behavior change, calls=%d", len(publisher.requests))
	}
}

func TestCriticalSettingChangedWithoutPublisherFailsClosed(t *testing.T) {
	q := newFakeQueries()
	q.data[GatewayConcurrencyDefaultsKey] = encodeConcurrencyDefaultsSettings(DefaultConcurrencyDefaultsSettings())
	service := NewService(newTestStore(q))

	_, err := service.SetRawWithResult(context.Background(), GatewayConcurrencyDefaultsKey, json.RawMessage(`{"key_limit":2,"channel_limit":3}`))
	if failure.CodeOf(err) != failure.CodeGatewayBreakerStoreUnavailable {
		t.Fatalf("code = %q, want %q (err=%v)", failure.CodeOf(err), failure.CodeGatewayBreakerStoreUnavailable, err)
	}
}

func TestRestoreCriticalRuntimeControlsInstallsAllFiveValidatedSettings(t *testing.T) {
	q := newFakeQueries()
	q.data[GatewayRouteRateLimitDefaultsKey] = encodeRateLimitDefaultsSettings(DefaultRateLimitDefaultsSettings())
	q.data[GatewayChannelRateLimitDefaultsKey] = encodeRateLimitDefaultsSettings(DefaultRateLimitDefaultsSettings())
	q.data[GatewayConcurrencyDefaultsKey] = encodeConcurrencyDefaultsSettings(DefaultConcurrencyDefaultsSettings())
	q.data[GatewayCircuitBreakerKey] = encodeCircuitBreakerSettings(DefaultCircuitBreakerSettings())
	q.data[GatewayRoutingBalanceKey] = encodeRoutingBalanceSettings(DefaultRoutingBalanceSettings())
	controls := &fakeRuntimeControlStore{}

	if err := RestoreCriticalRuntimeControls(context.Background(), newTestStore(q), controls); err != nil {
		t.Fatalf("restore controls: %v", err)
	}
	if len(controls.restored) != 5 {
		t.Fatalf("restored controls = %d, want 5", len(controls.restored))
	}
}

func TestRestoreCriticalRuntimeControlsRejectsLegacyShape(t *testing.T) {
	q := newFakeQueries()
	q.data[GatewayRouteRateLimitDefaultsKey] = []byte(`{"rpm":60,"tpm":0,"rpd":0,"failure_policy":"fail_open"}`)
	q.data[GatewayChannelRateLimitDefaultsKey] = encodeRateLimitDefaultsSettings(DefaultRateLimitDefaultsSettings())
	q.data[GatewayConcurrencyDefaultsKey] = encodeConcurrencyDefaultsSettings(DefaultConcurrencyDefaultsSettings())
	q.data[GatewayCircuitBreakerKey] = encodeCircuitBreakerSettings(DefaultCircuitBreakerSettings())
	q.data[GatewayRoutingBalanceKey] = encodeRoutingBalanceSettings(DefaultRoutingBalanceSettings())

	err := RestoreCriticalRuntimeControls(context.Background(), newTestStore(q), &fakeRuntimeControlStore{})
	if failure.CodeOf(err) != failure.CodeConfigInvalid {
		t.Fatalf("code = %q, want config_invalid (err=%v)", failure.CodeOf(err), err)
	}
}
