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
	snapshot         breakerstore.ControlSnapshot
	err              error
	restored         []string
	routeTargetCalls int
}

func (s *fakeRuntimeControlStore) SettingControl(string) breakerstore.ControlTarget {
	return breakerstore.ControlTarget{}
}

func (s *fakeRuntimeControlStore) RouteRateLimitControl() breakerstore.ControlTarget {
	s.routeTargetCalls++
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

func (s *fakeRuntimeControlStore) ReconcileControl(ctx context.Context, target breakerstore.ControlTarget, revision int64, payload string) (bool, error) {
	return s.RestoreMissingControl(ctx, target, revision, payload)
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

	result, err := service.SetRawWithResult(context.Background(), GatewayRouteRateLimitDefaultsKey, json.RawMessage(`{"rpd":5,"rpm":120}`))
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
		runtimeStore.routeTargetCalls != 1 {
		t.Fatalf("route setting used wrong runtime control: request=%+v store=%+v", req, runtimeStore)
	}
	if req.Payload != `{"rpm":120,"rpd":5}` {
		t.Fatalf("payload must be canonical and omit failure_policy: %s", req.Payload)
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

	// 同一语义值（仅键顺序不同）不得推进 revision，也不得发布。
	result, err := service.SetRawWithResult(context.Background(), GatewayRoutingBalanceKey, json.RawMessage(`{
		"priority_weight_pct": 10,
		"error_penalty_points_per_percent": 2.5,
		"concurrency_weight_pct": 20,
		"ttft_penalty_points_per_unit": 2.5,
		"error_rate_weight_pct": 20,
		"ttft_weight_pct": 25,
		"cost_weight_pct": 25,
		"ttft_window_ms": 1800000,
		"ttft_penalty_unit_ms": 1000,
		"error_window_ms": 1800000
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

func TestCriticalSettingChangedWithoutPublisherFailsClosed(t *testing.T) {
	q := newFakeQueries()
	q.data[GatewayConcurrencyDefaultsKey] = encodeConcurrencyDefaultsSettings(DefaultConcurrencyDefaultsSettings())
	service := NewService(newTestStore(q))

	_, err := service.SetRawWithResult(context.Background(), GatewayConcurrencyDefaultsKey, json.RawMessage(`{"key_limit":2,"channel_limit":3}`))
	if failure.CodeOf(err) != failure.CodeGatewayBreakerStoreUnavailable {
		t.Fatalf("code = %q, want %q (err=%v)", failure.CodeOf(err), failure.CodeGatewayBreakerStoreUnavailable, err)
	}
}

func TestDedicatedControlSettingIsHiddenAndRejectsGenericWrite(t *testing.T) {
	q := newFakeQueries()
	definition, ok := DefaultRegistry().Get(GatewayLoggingDebugSessionKey)
	if !ok {
		t.Fatal("gateway logging control definition is missing")
	}
	q.data[GatewayLoggingDebugSessionKey] = append([]byte(nil), definition.Default...)
	service := NewService(newTestStore(q))

	for _, item := range service.List(context.Background()) {
		if item.Key == GatewayLoggingDebugSessionKey {
			t.Fatal("dedicated gateway logging control leaked into generic settings list")
		}
	}

	before := append([]byte(nil), q.data[GatewayLoggingDebugSessionKey]...)
	_, err := service.SetRawWithResult(
		context.Background(),
		GatewayLoggingDebugSessionKey,
		json.RawMessage(`{"session_id":"bypass","started_at":"2026-08-01T00:00:00Z","expires_at":"2026-08-01T01:00:00Z","reason":"bypass","enabled_by_user_id":0,"revision":2}`),
	)
	if err == nil {
		t.Fatal("generic write unexpectedly accepted dedicated gateway logging control")
	}
	if string(q.data[GatewayLoggingDebugSessionKey]) != string(before) {
		t.Fatal("rejected generic write changed the stored gateway logging control")
	}
}

func TestRestoreCriticalRuntimeControlsInstallsAllValidatedSettings(t *testing.T) {
	q := newFakeQueries()
	q.data[GatewayRouteRateLimitDefaultsKey] = encodeRateLimitDefaultsSettings(DefaultRateLimitDefaultsSettings())
	q.data[GatewayConcurrencyDefaultsKey] = encodeConcurrencyDefaultsSettings(DefaultConcurrencyDefaultsSettings())
	q.data[GatewayCircuitBreakerKey] = encodeCircuitBreakerSettings(DefaultCircuitBreakerSettings())
	q.data[GatewayRoutingBalanceKey] = encodeRoutingBalanceSettings(DefaultRoutingBalanceSettings())
	controls := &fakeRuntimeControlStore{}

	if err := RestoreCriticalRuntimeControls(context.Background(), newTestStore(q), controls); err != nil {
		t.Fatalf("restore controls: %v", err)
	}
	if len(controls.restored) != 4 {
		t.Fatalf("restored controls = %d, want 4", len(controls.restored))
	}
}

func TestRestoreCriticalRuntimeControlsRejectsLegacyShape(t *testing.T) {
	q := newFakeQueries()
	q.data[GatewayRouteRateLimitDefaultsKey] = []byte(`{"rpm":60,"rpd":0,"failure_policy":"fail_open"}`)
	q.data[GatewayConcurrencyDefaultsKey] = encodeConcurrencyDefaultsSettings(DefaultConcurrencyDefaultsSettings())
	q.data[GatewayCircuitBreakerKey] = encodeCircuitBreakerSettings(DefaultCircuitBreakerSettings())
	q.data[GatewayRoutingBalanceKey] = encodeRoutingBalanceSettings(DefaultRoutingBalanceSettings())

	err := RestoreCriticalRuntimeControls(context.Background(), newTestStore(q), &fakeRuntimeControlStore{})
	if failure.CodeOf(err) != failure.CodeConfigInvalid {
		t.Fatalf("code = %q, want config_invalid (err=%v)", failure.CodeOf(err), err)
	}
}
