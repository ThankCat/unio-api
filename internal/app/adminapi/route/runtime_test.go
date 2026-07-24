package route

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/service/admin/routeruntime"
)

func TestRouteRuntimeDTOUsesP4Contract(t *testing.T) {
	value := routeruntime.Runtime{
		RouteID: 9, Mode: "balanced", RouteStatus: "enabled", ModelID: "openai/gpt",
		ObservedAt:       time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC),
		RuntimeSyncState: "active", BreakerStoreAdmission: "normal",
		Sources: []routeruntime.Source{{Name: "breaker_store", Available: true}},
		Channels: []routeruntime.Channel{{
			ChannelID: 7, ProviderOriginID: 21, ProviderOriginName: "primary",
			ProviderOriginStatus: "enabled", TTFTSampleSource: "stream_only",
			CostRatio: float64Pointer(0.4), CostWeight: 0.5, CostFactor: 0.8,
			RuntimeSyncState: "active", BreakerStoreAdmission: "normal",
		}},
	}
	body, err := json.Marshal(toRouteRuntimeDTO(value))
	if err != nil {
		t.Fatalf("marshal route runtime: %v", err)
	}
	var decoded map[string]any
	if err := json.Unmarshal(body, &decoded); err != nil {
		t.Fatalf("decode route runtime: %v", err)
	}
	for _, key := range []string{"runtime_sync_state", "breaker_store_admission", "sources", "channels"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing route runtime field %q: %s", key, body)
		}
	}
	for _, key := range []string{"gateway_sources", "health_factor", "latency_ewma_ms", "instance_snapshots"} {
		if _, ok := decoded[key]; ok {
			t.Errorf("legacy route runtime field %q is still present: %s", key, body)
		}
	}
	channels, ok := decoded["channels"].([]any)
	if !ok || len(channels) != 1 {
		t.Fatalf("unexpected channels: %#v", decoded["channels"])
	}
	channel, ok := channels[0].(map[string]any)
	if !ok {
		t.Fatalf("unexpected channel DTO: %#v", channels[0])
	}
	for _, key := range []string{
		"provider_origin_id", "provider_origin_name", "provider_origin_status",
		"origin_breaker_state", "channel_breaker_state", "error_samples",
		"ttft_ewma_ms", "ttft_samples", "ttft_sample_source",
		"cost_ratio", "cost_weight", "cost_factor", "final_weight",
		"runtime_sync_state", "breaker_store_admission",
	} {
		if _, ok := channel[key]; !ok {
			t.Errorf("missing P4 channel field %q: %s", key, body)
		}
	}
	for _, key := range []string{"health_factor", "breaker_state", "latency_ewma_ms", "instance_snapshots"} {
		if _, ok := channel[key]; ok {
			t.Errorf("legacy channel field %q is still present: %s", key, body)
		}
	}
}

func float64Pointer(value float64) *float64 { return &value }
