package calibration

import (
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

func testThresholds() Thresholds {
	return Thresholds{MinSuccess: 20, MinEvidenceRatio: 0.8, Lookback: 168 * time.Hour}
}

// TestBuildPlanDecisionPaths 覆盖单 (模型, 能力) 的各判定路径：自动补 vs 建议 vs 跳过。
func TestBuildPlanDecisionPaths(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Hour)

	cases := []struct {
		name        string
		obs         Observation
		mc          ModelContext
		wantAuto    int
		wantSuggest int
		wantKind    EvidenceKind
	}{
		{
			name:        "auto single-channel strong tool -> auto",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsCustom, Success: 30, Evidence: 30, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeAuto, EnabledChannels: 1},
			wantAuto:    1,
			wantSuggest: 0,
			wantKind:    EvidenceStrong,
		},
		{
			name:        "suggest mode strong tool -> suggest",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsCustom, Success: 30, Evidence: 30, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeSuggest, EnabledChannels: 1},
			wantAuto:    0,
			wantSuggest: 1,
			wantKind:    EvidenceStrong,
		},
		{
			name:        "auto multi-channel strong tool -> suggest (not auto)",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsCustom, Success: 30, Evidence: 30, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeAuto, EnabledChannels: 2},
			wantAuto:    0,
			wantSuggest: 1,
			wantKind:    EvidenceStrong,
		},
		{
			name:        "auto single-channel prompt_cache strong -> auto",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyPromptCache, Success: 50, Evidence: 45, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeAuto, EnabledChannels: 1},
			wantAuto:    1,
			wantSuggest: 0,
			wantKind:    EvidenceStrong,
		},
		{
			name:        "auto reasoning.effort has-limits -> suggest (not auto)",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyReasoningEffort, Success: 50, Evidence: 50, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeAuto, EnabledChannels: 1},
			wantAuto:    0,
			wantSuggest: 1,
			wantKind:    EvidenceStrong,
		},
		{
			name:        "auto builtin web_search weak -> suggest weak",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsBuiltinWebSearch, Success: 30, Evidence: 0, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeAuto, EnabledChannels: 1},
			wantAuto:    0,
			wantSuggest: 1,
			wantKind:    EvidenceWeak,
		},
		{
			name:        "auto strong key but low ratio -> suggest weak (not auto)",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsCustom, Success: 40, Evidence: 10, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeAuto, EnabledChannels: 1},
			wantAuto:    0,
			wantSuggest: 1,
			wantKind:    EvidenceWeak,
		},
		{
			name:        "already declared -> skip",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsCustom, Success: 30, Evidence: 30, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeAuto, EnabledChannels: 1, Declared: map[capability.Key]struct{}{capability.KeyToolsCustom: {}}},
			wantAuto:    0,
			wantSuggest: 0,
		},
		{
			name:        "dismissed -> skip",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsCustom, Success: 30, Evidence: 30, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeAuto, EnabledChannels: 1, Dismissed: map[capability.Key]struct{}{capability.KeyToolsCustom: {}}},
			wantAuto:    0,
			wantSuggest: 0,
		},
		{
			name:        "below MinSuccess -> skip",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsCustom, Success: 5, Evidence: 5, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeAuto, EnabledChannels: 1},
			wantAuto:    0,
			wantSuggest: 0,
		},
		{
			name:        "mode off -> skip",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsCustom, Success: 30, Evidence: 30, LastSeen: fresh},
			mc:          ModelContext{Mode: ModeOff, EnabledChannels: 1},
			wantAuto:    0,
			wantSuggest: 0,
		},
		{
			name:        "stale (outside lookback) -> skip",
			obs:         Observation{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsCustom, Success: 30, Evidence: 30, LastSeen: now.Add(-200 * time.Hour)},
			mc:          ModelContext{Mode: ModeAuto, EnabledChannels: 1},
			wantAuto:    0,
			wantSuggest: 0,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			plan := BuildPlan([]Observation{tc.obs}, map[int64]ModelContext{tc.obs.ModelID: tc.mc}, testThresholds(), now)
			if len(plan.AutoApply) != tc.wantAuto {
				t.Fatalf("AutoApply = %d, want %d (%+v)", len(plan.AutoApply), tc.wantAuto, plan.AutoApply)
			}
			if len(plan.Suggestions) != tc.wantSuggest {
				t.Fatalf("Suggestions = %d, want %d (%+v)", len(plan.Suggestions), tc.wantSuggest, plan.Suggestions)
			}
			if tc.wantKind != "" {
				decisions := append(append([]Decision{}, plan.AutoApply...), plan.Suggestions...)
				if len(decisions) != 1 {
					t.Fatalf("expected 1 decision, got %d", len(decisions))
				}
				if decisions[0].EvidenceKind != tc.wantKind {
					t.Fatalf("EvidenceKind = %q, want %q", decisions[0].EvidenceKind, tc.wantKind)
				}
			}
		})
	}
}

// TestBuildPlanAggregatesAcrossChannels 验证同 (模型, 能力) 跨渠道观测聚合后再决策。
func TestBuildPlanAggregatesAcrossChannels(t *testing.T) {
	now := time.Now()
	fresh := now.Add(-time.Hour)
	obs := []Observation{
		{ModelID: 7, ChannelID: 1, Key: capability.KeyToolsFunction, Success: 12, Evidence: 12, LastSeen: fresh},
		{ModelID: 7, ChannelID: 2, Key: capability.KeyToolsFunction, Success: 12, Evidence: 12, LastSeen: fresh},
	}
	// EnabledChannels=1 表示当前只剩一个启用渠道（历史观测含已停用渠道），允许 auto。
	models := map[int64]ModelContext{7: {Mode: ModeAuto, EnabledChannels: 1}}

	plan := BuildPlan(obs, models, testThresholds(), now)
	if len(plan.AutoApply) != 1 {
		t.Fatalf("expected aggregated success 24 >= 20 to auto-apply, got AutoApply=%d suggestions=%d", len(plan.AutoApply), len(plan.Suggestions))
	}
	if plan.AutoApply[0].Rationale.SuccessCount != 24 || plan.AutoApply[0].Rationale.EvidenceCount != 24 {
		t.Fatalf("rationale aggregation wrong: %+v", plan.AutoApply[0].Rationale)
	}
	if len(plan.AutoApply[0].Rationale.ChannelIDs) != 2 {
		t.Fatalf("expected 2 channel ids in rationale, got %+v", plan.AutoApply[0].Rationale.ChannelIDs)
	}
}

// TestDetectDegradations 覆盖上游退化检测的各判定路径（只看已声明的强证据键、近期证据塌陷才告警）。
func TestDetectDegradations(t *testing.T) {
	declared := func(keys ...capability.Key) map[capability.Key]struct{} {
		set := make(map[capability.Key]struct{}, len(keys))
		for _, k := range keys {
			set[k] = struct{}{}
		}
		return set
	}

	cases := []struct {
		name      string
		recent    []Observation
		mc        ModelContext
		wantCount int
		wantKey   capability.Key
	}{
		{
			name:      "declared strong key, evidence collapsed -> alert",
			recent:    []Observation{{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsFunction, Success: 30, Evidence: 1}},
			mc:        ModelContext{Mode: ModeSuggest, Declared: declared(capability.KeyToolsFunction)},
			wantCount: 1,
			wantKey:   capability.KeyToolsFunction,
		},
		{
			name:      "declared strong key, evidence healthy -> no alert",
			recent:    []Observation{{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsFunction, Success: 30, Evidence: 28}},
			mc:        ModelContext{Mode: ModeSuggest, Declared: declared(capability.KeyToolsFunction)},
			wantCount: 0,
		},
		{
			name:      "not declared -> no alert (BuildPlan's job, not degradation)",
			recent:    []Observation{{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsFunction, Success: 30, Evidence: 0}},
			mc:        ModelContext{Mode: ModeSuggest},
			wantCount: 0,
		},
		{
			name:      "weak-evidence key declared -> no alert (low ratio is natural)",
			recent:    []Observation{{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsBuiltinWebSearch, Success: 30, Evidence: 0}},
			mc:        ModelContext{Mode: ModeSuggest, Declared: declared(capability.KeyToolsBuiltinWebSearch)},
			wantCount: 0,
		},
		{
			name:      "below MinSuccess -> no alert (insufficient recent signal)",
			recent:    []Observation{{ModelID: 1, ChannelID: 1, Key: capability.KeyToolsFunction, Success: 5, Evidence: 0}},
			mc:        ModelContext{Mode: ModeSuggest, Declared: declared(capability.KeyToolsFunction)},
			wantCount: 0,
		},
		{
			name:      "mode off but declared strong collapsed -> still alert (degradation is observation, not action)",
			recent:    []Observation{{ModelID: 1, ChannelID: 1, Key: capability.KeyPromptCache, Success: 40, Evidence: 2}},
			mc:        ModelContext{Mode: ModeOff, Declared: declared(capability.KeyPromptCache)},
			wantCount: 1,
			wantKey:   capability.KeyPromptCache,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			models := map[int64]ModelContext{1: tc.mc}
			got := DetectDegradations(tc.recent, models, testThresholds())
			if len(got) != tc.wantCount {
				t.Fatalf("DetectDegradations = %d alerts, want %d (%+v)", len(got), tc.wantCount, got)
			}
			if tc.wantCount == 1 && got[0].Key != tc.wantKey {
				t.Fatalf("alert key = %q, want %q", got[0].Key, tc.wantKey)
			}
		})
	}
}

// TestDetectDegradationsAggregatesAcrossChannels 验证退化检测同 (模型, 能力) 跨渠道聚合后再判定。
func TestDetectDegradationsAggregatesAcrossChannels(t *testing.T) {
	recent := []Observation{
		{ModelID: 9, ChannelID: 1, Key: capability.KeyToolsFunction, Success: 12, Evidence: 1},
		{ModelID: 9, ChannelID: 2, Key: capability.KeyToolsFunction, Success: 12, Evidence: 0},
	}
	models := map[int64]ModelContext{9: {Mode: ModeSuggest, Declared: map[capability.Key]struct{}{capability.KeyToolsFunction: {}}}}

	got := DetectDegradations(recent, models, testThresholds())
	if len(got) != 1 {
		t.Fatalf("expected aggregated success 24 >= 20 to alert, got %d (%+v)", len(got), got)
	}
	if got[0].SuccessCount != 24 || got[0].EvidenceCount != 1 {
		t.Fatalf("aggregation wrong: %+v", got[0])
	}
	if len(got[0].ChannelIDs) != 2 {
		t.Fatalf("expected 2 channel ids, got %+v", got[0].ChannelIDs)
	}
}

// TestAttemptHasEvidence 验证按 key 的强证据归因。
func TestAttemptHasEvidence(t *testing.T) {
	if !AttemptHasEvidence(capability.KeyToolsCustom, "tool_use", 0, 0) {
		t.Fatal("tools.custom + finish_class=tool_use should be strong evidence")
	}
	if AttemptHasEvidence(capability.KeyToolsCustom, "stop", 0, 0) {
		t.Fatal("tools.custom + finish_class=stop should NOT be evidence")
	}
	if !AttemptHasEvidence(capability.KeyPromptCache, "stop", 10, 0) {
		t.Fatal("prompt_cache + cache_read>0 should be strong evidence")
	}
	if AttemptHasEvidence(capability.KeyPromptCache, "stop", 0, 0) {
		t.Fatal("prompt_cache + cache_read=0 should NOT be evidence")
	}
	if !AttemptHasEvidence(capability.KeyReasoningEffort, "stop", 0, 5) {
		t.Fatal("reasoning.effort + reasoning_tokens>0 should be strong evidence")
	}
	if AttemptHasEvidence(capability.KeyToolsBuiltinWebSearch, "tool_use", 0, 0) {
		t.Fatal("builtin web_search must never be strong evidence via tool_use")
	}
}
