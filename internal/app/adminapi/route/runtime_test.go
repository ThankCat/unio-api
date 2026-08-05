package route

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/service/admin/routeruntime"
)

func TestRouteRuntimeDTOUsesPartitionedObjectiveContract(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	value := routeruntime.Runtime{
		RouteID: 9, Mode: "balanced", RouteStatus: "enabled", ModelID: "openai/gpt", Protocol: "openai",
		ObservedAt: now, RuntimeSyncState: "active", BreakerStoreAdmission: "normal",
		RouteUsage: &routeruntime.RouteUsage{Concurrency: 3, RPM: 15, RPD: 40, ObservedTPM: 1200, ActiveUsers: 2},
		Sources:    []routeruntime.Source{{Name: "breaker_store", Available: true, ObservedAt: now}},
		ScoreConfig: routeruntime.ScoreConfig{
			AlgorithmVersion: "objective_v1", Revision: 7,
			CostWeightPct: 25, ConcurrencyWeightPct: 20, TTFTWeightPct: 25,
			ErrorRateWeightPct: 20, PriorityWeightPct: 10,
			TTFTPenaltyUnitMs: 1000, TTFTPenaltyPointsPerUnit: 2.5, ErrorPenaltyPointsPerPercent: 2.5,
		},
		SampleWindow: routeruntime.SampleWindow{
			TTFTWindowMs: 1_800_000, ErrorWindowMs: 1_800_000,
			StartedAt: now.Add(-30 * time.Minute), EndedAt: now, Available: true,
		},
		Channels: []routeruntime.Channel{{
			ChannelID: 3, ChannelName: "primary", ChannelStatus: "enabled",
			ProviderID: 4, ProviderName: "provider", ProviderStatus: "enabled",
			Pricing: routeruntime.ChannelPricing{
				Source: "multiplier", CostMultiplier: stringPointer("0.8"), RechargeFactor: stringPointer("0.9"),
			},
			Eligible: true, RuntimeRevisionCurrent: true, RuntimeSyncState: "active", BreakerStoreAdmission: "normal",
			ConcurrencyUsed: 2, ConcurrencyLimit: 10, ConcurrencyRemaining: float64Pointer(0.8),
			RPMUsed: 12, GlobalRPDUsed: 80, ObservedTPM: 900, TokenCoveredCount: 10, TokenCoveragePct: 83.33,
			AlgorithmVersion: "objective_v1", CostRatio: float64Pointer(0.4), Priority: 10,
			CostScore: 60, ConcurrencyScore: 80, TTFTScore: 97.5, ErrorScore: 100, PriorityScore: 90,
			CostWeightPct: 25, ConcurrencyWeightPct: 20, TTFTWeightPct: 25,
			ErrorRateWeightPct: 20, PriorityWeightPct: 10, FinalScore: 87.375,
			AvgTTFTMs: float64Pointer(1000), TTFTSampleCount: 12,
			ErrorRatePct: float64Pointer(0), ErrorSampleCount: 30,
			CurrentOrder: 1, MarginStatus: "safe",
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
	for _, key := range []string{"source_status", "route_summary", "filters", "channels", "score_config", "sample_window"} {
		if _, ok := decoded[key]; !ok {
			t.Errorf("missing route runtime partition %q: %s", key, body)
		}
	}
	for _, legacy := range []string{"runtime_sync_state", "route_usage", "rpm_limit", "candidate_scores"} {
		if _, ok := decoded[legacy]; ok {
			t.Errorf("legacy flat runtime field %q is still present: %s", legacy, body)
		}
	}
	channels := decoded["channels"].([]any)
	channel := channels[0].(map[string]any)
	for _, key := range []string{"pricing", "eligibility", "runtime", "concurrency", "quality", "traffic", "score", "distribution", "internal_diagnostics"} {
		if _, ok := channel[key]; !ok {
			t.Errorf("missing structured channel section %q: %s", key, body)
		}
	}
	traffic := channel["traffic"].(map[string]any)
	if traffic["rpm"] != float64(12) || traffic["rpd"] != float64(80) || traffic["tpm"] != float64(900) {
		t.Fatalf("traffic must expose observations, got %#v", traffic)
	}
	score := channel["score"].(map[string]any)
	if score["total"] != 87.375 {
		t.Fatalf("unexpected total score: %#v", score)
	}
	pricing := channel["pricing"].(map[string]any)
	if pricing["source"] != "multiplier" || pricing["cost_multiplier"] != "0.8" || pricing["recharge_factor"] != "0.9" {
		t.Fatalf("unexpected pricing facts: %#v", pricing)
	}
}

func TestEligibilityOmitsMarginWhenPricingWasNotEvaluated(t *testing.T) {
	dto := eligibilityOf("enabled", routeruntime.Channel{
		ProviderStatus: "enabled", ChannelStatus: "enabled", MarginStatus: "not_evaluated",
		ExcludedReason: "channel_cost_missing", RuntimeRevisionCurrent: true,
	})
	for _, item := range dto.Checks {
		if item.Key == "margin" {
			t.Fatalf("margin check must be omitted when not evaluated: %#v", dto.Checks)
		}
	}
}

func float64Pointer(value float64) *float64 { return &value }
func stringPointer(value string) *string    { return &value }
