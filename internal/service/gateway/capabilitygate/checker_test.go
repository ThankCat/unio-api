package capabilitygate

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/routing"
)

type fakeStore struct {
	modelCaps     map[int64][]capability.ModelCapability
	overrides     map[int64][]capability.ChannelOverride
	modelCapsErr  error
	overridesErr  error
	overrideCalls []int64
}

func (f *fakeStore) ListModelCapabilities(_ context.Context, modelID int64) ([]capability.ModelCapability, error) {
	if f.modelCapsErr != nil {
		return nil, f.modelCapsErr
	}
	return f.modelCaps[modelID], nil
}

func (f *fakeStore) ListChannelOverrides(_ context.Context, channelID int64) ([]capability.ChannelOverride, error) {
	f.overrideCalls = append(f.overrideCalls, channelID)
	if f.overridesErr != nil {
		return nil, f.overridesErr
	}
	return f.overrides[channelID], nil
}

type fakeMetrics struct {
	results   []string
	protocols []string
	required  []string
	missing   []string
}

func (f *fakeMetrics) IncCapabilityCheck(protocol string, result string) {
	f.protocols = append(f.protocols, protocol)
	f.results = append(f.results, result)
}

func (f *fakeMetrics) IncCapabilityRequired(_ string, capability string) {
	f.required = append(f.required, capability)
}

func (f *fakeMetrics) IncCapabilityMissing(_ string, capability string, scope string) {
	f.missing = append(f.missing, scope+":"+capability)
}

func mc(modelID int64, key capability.Key, level capability.SupportLevel) capability.ModelCapability {
	return capability.ModelCapability{ModelID: modelID, Key: key, SupportLevel: level}
}

func TestCheckerProvisionedModelOK(t *testing.T) {
	store := &fakeStore{
		modelCaps: map[int64][]capability.ModelCapability{
			10: {
				mc(10, capability.KeyTextInput, capability.SupportLevelFull),
				mc(10, capability.KeyTextOutput, capability.SupportLevelFull),
			},
		},
	}
	metrics := &fakeMetrics{}
	checker := NewChecker(store, metrics, nil)

	got := checker.Check(context.Background(), routing.CapabilityCheckInput{
		Protocol:   "openai",
		ModelDBID:  10,
		ChannelIDs: []int64{1, 2},
		Required:   capability.NewSet(capability.KeyTextInput, capability.KeyTextOutput),
	})

	if got.Result != capability.GateResultOK {
		t.Fatalf("result = %q, want ok", got.Result)
	}
	if len(metrics.results) != 1 || metrics.results[0] != string(capability.GateResultOK) {
		t.Fatalf("metrics = %v, want [ok]", metrics.results)
	}
	if len(metrics.protocols) != 1 || metrics.protocols[0] != "openai" {
		t.Fatalf("metric protocols = %v, want [openai]", metrics.protocols)
	}
	if len(metrics.required) != 2 {
		t.Fatalf("required metrics = %v, want 2 entries", metrics.required)
	}
	if len(metrics.missing) != 0 {
		t.Fatalf("missing metrics = %v, want none on ok", metrics.missing)
	}
	if want := []int64{1, 2}; !equalInt64(store.overrideCalls, want) {
		t.Fatalf("override calls = %v, want %v", store.overrideCalls, want)
	}
}

func TestCheckerUnprovisionedSkips(t *testing.T) {
	store := &fakeStore{}
	metrics := &fakeMetrics{}
	checker := NewChecker(store, metrics, nil)

	got := checker.Check(context.Background(), routing.CapabilityCheckInput{
		ModelDBID:  99,
		ChannelIDs: []int64{1},
		Required:   capability.NewSet(capability.KeyTextInput, capability.KeyTextOutput),
	})

	if got.Result != capability.GateResultUnprovisioned {
		t.Fatalf("result = %q, want unprovisioned", got.Result)
	}
	if got.Provisioned {
		t.Fatalf("provisioned = true, want false")
	}
}

func TestCheckerModelUnavailableRecordsMissing(t *testing.T) {
	store := &fakeStore{
		modelCaps: map[int64][]capability.ModelCapability{
			10: {
				mc(10, capability.KeyTextInput, capability.SupportLevelFull),
				mc(10, capability.KeyTextOutput, capability.SupportLevelFull),
			},
		},
	}
	metrics := &fakeMetrics{}
	checker := NewChecker(store, metrics, nil)

	got := checker.Check(context.Background(), routing.CapabilityCheckInput{
		Protocol:   "openai",
		ModelDBID:  10,
		ChannelIDs: []int64{1},
		Required:   capability.NewSet(capability.KeyTextInput, capability.KeyTextOutput, capability.KeyImageInput),
	})

	if got.Result != capability.GateResultModelUnavailable {
		t.Fatalf("result = %q, want model_unavailable", got.Result)
	}
	if len(got.MissingModel) != 1 || got.MissingModel[0] != capability.KeyImageInput {
		t.Fatalf("missing model = %v, want [image.input]", got.MissingModel)
	}
	if metrics.results[0] != string(capability.GateResultModelUnavailable) {
		t.Fatalf("metric = %v, want model_unavailable", metrics.results)
	}
	if len(metrics.missing) != 1 || metrics.missing[0] != "model:"+string(capability.KeyImageInput) {
		t.Fatalf("missing metrics = %v, want [model:image.input]", metrics.missing)
	}
}

// TestCheckerLimitsThreadThrough 验证请求侧 reasoning.effort 档位值经 in.Limits 透传到 Evaluate（GAP-12-012）：
// 模型 reasoning.effort=limited{max_effort:medium}，请求 high → 超限 → model_unavailable。
func TestCheckerLimitsThreadThrough(t *testing.T) {
	store := &fakeStore{
		modelCaps: map[int64][]capability.ModelCapability{
			10: {
				mc(10, capability.KeyTextInput, capability.SupportLevelFull),
				mc(10, capability.KeyTextOutput, capability.SupportLevelFull),
				{
					ModelID:      10,
					Key:          capability.KeyReasoningEffort,
					SupportLevel: capability.SupportLevelLimited,
					Limits:       []byte(`{"max_effort":"medium"}`),
				},
			},
		},
	}
	metrics := &fakeMetrics{}
	checker := NewChecker(store, metrics, nil)

	base := routing.CapabilityCheckInput{
		Protocol:   "openai",
		ModelDBID:  10,
		ChannelIDs: []int64{1},
		Required:   capability.NewSet(capability.KeyTextInput, capability.KeyTextOutput, capability.KeyReasoningEffort),
	}

	// 不带档位值（空 Limits）→ limited 视为满足 → ok。
	if got := checker.Check(context.Background(), base); got.Result != capability.GateResultOK {
		t.Fatalf("empty limits result = %q, want ok", got.Result)
	}

	// 带 high 档位 → 超过 max_effort=medium → model_unavailable。
	withLimit := base
	withLimit.Limits = capability.RequestLimits{ReasoningEffort: "high"}
	got := checker.Check(context.Background(), withLimit)
	if got.Result != capability.GateResultModelUnavailable {
		t.Fatalf("high effort result = %q, want model_unavailable", got.Result)
	}
	if len(got.MissingModel) != 1 || got.MissingModel[0] != capability.KeyReasoningEffort {
		t.Fatalf("missing model = %v, want [reasoning.effort]", got.MissingModel)
	}
}

func TestCheckerStoreErrorFailsOpen(t *testing.T) {
	store := &fakeStore{modelCapsErr: errors.New("db down")}
	metrics := &fakeMetrics{}
	checker := NewChecker(store, metrics, nil)

	got := checker.Check(context.Background(), routing.CapabilityCheckInput{
		ModelDBID:  10,
		ChannelIDs: []int64{1},
		Required:   capability.NewSet(capability.KeyTextInput),
	})

	if got.Result != capability.GateResultError {
		t.Fatalf("result = %q, want error", got.Result)
	}
	if metrics.results[0] != string(capability.GateResultError) {
		t.Fatalf("metric = %v, want error", metrics.results)
	}
}

func TestCheckerChannelOverrideErrorFailsOpen(t *testing.T) {
	store := &fakeStore{
		modelCaps: map[int64][]capability.ModelCapability{
			10: {mc(10, capability.KeyTextInput, capability.SupportLevelFull)},
		},
		overridesErr: errors.New("override read failed"),
	}
	metrics := &fakeMetrics{}
	checker := NewChecker(store, metrics, nil)

	got := checker.Check(context.Background(), routing.CapabilityCheckInput{
		ModelDBID:  10,
		ChannelIDs: []int64{1},
		Required:   capability.NewSet(capability.KeyTextInput),
	})

	if got.Result != capability.GateResultError {
		t.Fatalf("result = %q, want error", got.Result)
	}
}

func equalInt64(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
