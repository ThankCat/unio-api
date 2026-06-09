package capability

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

// recordingStore 只实现 MaterializeAdapterSeed 需要的 UpsertModelCapability，其余 Store 方法不被调用。
type recordingStore struct {
	Store
	upserts []UpsertModelCapabilityParams
	err     error
}

func (s *recordingStore) UpsertModelCapability(_ context.Context, params UpsertModelCapabilityParams) (ModelCapability, error) {
	if s.err != nil {
		return ModelCapability{}, s.err
	}
	s.upserts = append(s.upserts, params)
	return ModelCapability{
		ModelID:      params.ModelID,
		Key:          params.Key,
		SupportLevel: params.SupportLevel,
		Limits:       params.Limits,
		Source:       params.Source,
		UpdatedBy:    params.UpdatedBy,
	}, nil
}

func validProfile() AdapterProfile {
	return AdapterProfile{
		Provider: "deepseek",
		Protocol: "openai",
		Declarations: []Declaration{
			{Key: KeyTextInput, SupportLevel: SupportLevelFull},
			{Key: KeyReasoningEffort, SupportLevel: SupportLevelLimited, Limits: json.RawMessage(`{"effort":["high","max"]}`)},
			{Key: KeyImageInput, SupportLevel: SupportLevelUnsupported},
		},
	}
}

func TestAdapterProfileValidate(t *testing.T) {
	if err := validProfile().Validate(); err != nil {
		t.Fatalf("valid profile rejected: %v", err)
	}

	cases := []struct {
		name    string
		profile AdapterProfile
	}{
		{
			name:    "empty provider",
			profile: AdapterProfile{Protocol: "openai"},
		},
		{
			name:    "empty protocol",
			profile: AdapterProfile{Provider: "deepseek"},
		},
		{
			name: "unregistered key",
			profile: AdapterProfile{Provider: "deepseek", Protocol: "openai", Declarations: []Declaration{
				{Key: Key("bogus.key"), SupportLevel: SupportLevelFull},
			}},
		},
		{
			name: "invalid support level",
			profile: AdapterProfile{Provider: "deepseek", Protocol: "openai", Declarations: []Declaration{
				{Key: KeyTextInput, SupportLevel: SupportLevel("bogus")},
			}},
		},
		{
			name: "duplicate key",
			profile: AdapterProfile{Provider: "deepseek", Protocol: "openai", Declarations: []Declaration{
				{Key: KeyTextInput, SupportLevel: SupportLevelFull},
				{Key: KeyTextInput, SupportLevel: SupportLevelUnsupported},
			}},
		},
		{
			name: "limits on non-limited level",
			profile: AdapterProfile{Provider: "deepseek", Protocol: "openai", Declarations: []Declaration{
				{Key: KeyImageInput, SupportLevel: SupportLevelUnsupported, Limits: json.RawMessage(`{"x":1}`)},
			}},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.profile.Validate(); err == nil {
				t.Fatal("expected validation error")
			}
		})
	}
}

func TestMaterializeAdapterSeedUpsertsAllDeclarations(t *testing.T) {
	store := &recordingStore{}
	updatedBy := "adapter_seed_job"

	if err := MaterializeAdapterSeed(context.Background(), store, 42, validProfile(), &updatedBy); err != nil {
		t.Fatalf("materialize: %v", err)
	}

	if len(store.upserts) != 3 {
		t.Fatalf("expected 3 upserts, got %d", len(store.upserts))
	}
	for _, params := range store.upserts {
		if params.ModelID != 42 {
			t.Fatalf("expected model id 42, got %d", params.ModelID)
		}
		if params.Source != SourceAdapterSeed {
			t.Fatalf("expected source adapter_seed, got %q", params.Source)
		}
		if params.UpdatedBy == nil || *params.UpdatedBy != updatedBy {
			t.Fatalf("expected updated_by propagated, got %v", params.UpdatedBy)
		}
	}

	var sawLimited bool
	for _, params := range store.upserts {
		if params.Key == KeyReasoningEffort {
			sawLimited = true
			if params.SupportLevel != SupportLevelLimited || len(params.Limits) == 0 {
				t.Fatalf("expected limited reasoning.effort with limits, got level=%q limits=%s", params.SupportLevel, params.Limits)
			}
		}
	}
	if !sawLimited {
		t.Fatal("expected reasoning.effort declaration materialized")
	}
}

func TestMaterializeAdapterSeedIsIdempotent(t *testing.T) {
	store := &recordingStore{}

	for i := 0; i < 2; i++ {
		if err := MaterializeAdapterSeed(context.Background(), store, 7, validProfile(), nil); err != nil {
			t.Fatalf("materialize pass %d: %v", i, err)
		}
	}

	if len(store.upserts) != 6 {
		t.Fatalf("expected 6 upserts across 2 idempotent passes, got %d", len(store.upserts))
	}
}

func TestMaterializeAdapterSeedRejectsInvalidProfileBeforeWriting(t *testing.T) {
	store := &recordingStore{}
	invalid := AdapterProfile{Provider: "deepseek", Protocol: "openai", Declarations: []Declaration{
		{Key: Key("bogus.key"), SupportLevel: SupportLevelFull},
	}}

	if err := MaterializeAdapterSeed(context.Background(), store, 1, invalid, nil); err == nil {
		t.Fatal("expected error for invalid profile")
	}
	if len(store.upserts) != 0 {
		t.Fatalf("expected no upserts on invalid profile, got %d", len(store.upserts))
	}
}

func TestMaterializeAdapterSeedPropagatesStoreError(t *testing.T) {
	wantErr := errors.New("store down")
	store := &recordingStore{err: wantErr}

	err := MaterializeAdapterSeed(context.Background(), store, 1, validProfile(), nil)
	if !errors.Is(err, wantErr) {
		t.Fatalf("expected store error propagated, got %v", err)
	}
}
