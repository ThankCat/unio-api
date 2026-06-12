package capability_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	core "github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	capadmin "github.com/ThankCat/unio-api/internal/service/admin/capability"
)

// fakeStore 实现完整 core/capability.Store，覆盖 admin Store / SyncJobStore / SeedService 三处依赖。
type fakeStore struct {
	lookupErr error

	upsertModelParams   []core.UpsertModelCapabilityParams
	upsertModelRow      core.ModelCapability
	upsertChannelParams []core.UpsertChannelOverrideParams
	upsertChannelRow    core.ChannelOverride

	deleteModelCalls   int
	deleteChannelCalls int

	syncJobs      []core.SyncJob
	syncJobsLimit int32
}

func (s *fakeStore) LookupModelByID(context.Context, int64) (core.Model, error) {
	if s.lookupErr != nil {
		return core.Model{}, s.lookupErr
	}
	return core.Model{ID: 1, ModelID: "deepseek-chat"}, nil
}

func (s *fakeStore) LookupModelByModelID(context.Context, string) (core.Model, error) {
	return core.Model{}, nil
}

func (s *fakeStore) ListModelCapabilities(context.Context, int64) ([]core.ModelCapability, error) {
	return nil, nil
}

func (s *fakeStore) ListModelsByCapability(context.Context, core.Key) ([]core.ModelCapability, error) {
	return nil, nil
}

func (s *fakeStore) UpsertModelCapability(_ context.Context, params core.UpsertModelCapabilityParams) (core.ModelCapability, error) {
	s.upsertModelParams = append(s.upsertModelParams, params)
	return s.upsertModelRow, nil
}

func (s *fakeStore) DeleteModelCapability(context.Context, int64, core.Key) error {
	s.deleteModelCalls++
	return nil
}

func (s *fakeStore) ListChannelOverrides(context.Context, int64) ([]core.ChannelOverride, error) {
	return nil, nil
}

func (s *fakeStore) UpsertChannelOverride(_ context.Context, params core.UpsertChannelOverrideParams) (core.ChannelOverride, error) {
	s.upsertChannelParams = append(s.upsertChannelParams, params)
	return s.upsertChannelRow, nil
}

func (s *fakeStore) DeleteChannelOverride(context.Context, int64, core.Key) error {
	s.deleteChannelCalls++
	return nil
}

func (s *fakeStore) CreateSyncJob(context.Context, core.Source) (core.SyncJob, error) {
	return core.SyncJob{}, nil
}

func (s *fakeStore) MarkSyncJobRunning(context.Context, int64) (core.SyncJob, error) {
	return core.SyncJob{}, nil
}

func (s *fakeStore) MarkSyncJobSucceeded(context.Context, int64, json.RawMessage) (core.SyncJob, error) {
	return core.SyncJob{}, nil
}

func (s *fakeStore) MarkSyncJobFailed(context.Context, int64, string) (core.SyncJob, error) {
	return core.SyncJob{}, nil
}

func (s *fakeStore) GetLatestSyncJob(context.Context, core.Source) (core.SyncJob, error) {
	return core.SyncJob{}, nil
}

func (s *fakeStore) ListSyncJobs(_ context.Context, limit int32) ([]core.SyncJob, error) {
	s.syncJobsLimit = limit
	return s.syncJobs, nil
}

func TestSetModelCapabilityFixesSourceManual(t *testing.T) {
	store := &fakeStore{}
	_, err := capadmin.NewCapabilityService(store).SetModelCapability(context.Background(), capadmin.SetModelCapabilityInput{
		ModelID:      1,
		Key:          string(core.KeyToolsFunction),
		SupportLevel: string(core.SupportLevelFull),
		Actor:        "admin",
	})
	if err != nil {
		t.Fatalf("set model capability: %v", err)
	}
	if len(store.upsertModelParams) != 1 {
		t.Fatalf("expected one upsert, got %d", len(store.upsertModelParams))
	}
	got := store.upsertModelParams[0]
	if got.Source != core.SourceManual {
		t.Fatalf("expected source=manual, got %q", got.Source)
	}
	if got.UpdatedBy == nil || *got.UpdatedBy != "admin" {
		t.Fatalf("expected updated_by=admin, got %v", got.UpdatedBy)
	}
}

func TestSetModelCapabilityRejectsUnregisteredKey(t *testing.T) {
	store := &fakeStore{}
	_, err := capadmin.NewCapabilityService(store).SetModelCapability(context.Background(), capadmin.SetModelCapabilityInput{
		ModelID:      1,
		Key:          "tools.unknown",
		SupportLevel: string(core.SupportLevelFull),
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
	if len(store.upsertModelParams) != 0 {
		t.Fatalf("store should not be called on invalid key")
	}
}

func TestSetModelCapabilityRejectsLimitsAtNonLimited(t *testing.T) {
	store := &fakeStore{}
	_, err := capadmin.NewCapabilityService(store).SetModelCapability(context.Background(), capadmin.SetModelCapabilityInput{
		ModelID:      1,
		Key:          string(core.KeyReasoningEffort),
		SupportLevel: string(core.SupportLevelFull),
		Limits:       json.RawMessage(`{"max_effort":"high"}`),
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
}

func TestSetModelCapabilityModelNotFound(t *testing.T) {
	store := &fakeStore{lookupErr: pgx.ErrNoRows}
	_, err := capadmin.NewCapabilityService(store).SetModelCapability(context.Background(), capadmin.SetModelCapabilityInput{
		ModelID:      99,
		Key:          string(core.KeyToolsFunction),
		SupportLevel: string(core.SupportLevelFull),
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
	}
}

func TestSetChannelOverrideRejectsFull(t *testing.T) {
	store := &fakeStore{}
	_, err := capadmin.NewCapabilityService(store).SetChannelOverride(context.Background(), capadmin.SetChannelOverrideInput{
		ChannelID:    5,
		Key:          string(core.KeyToolsFunction),
		SupportLevel: string(core.SupportLevelFull),
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
	if len(store.upsertChannelParams) != 0 {
		t.Fatalf("store should not be called when override level is full")
	}
}

func TestSetChannelOverrideAcceptsSubtract(t *testing.T) {
	store := &fakeStore{}
	_, err := capadmin.NewCapabilityService(store).SetChannelOverride(context.Background(), capadmin.SetChannelOverrideInput{
		ChannelID:    5,
		Key:          string(core.KeyToolsBuiltinWebSearch),
		SupportLevel: string(core.SupportLevelUnsupported),
		Reason:       "  upstream lacks web_search  ",
		Actor:        "admin",
	})
	if err != nil {
		t.Fatalf("set channel override: %v", err)
	}
	if len(store.upsertChannelParams) != 1 {
		t.Fatalf("expected one upsert, got %d", len(store.upsertChannelParams))
	}
	got := store.upsertChannelParams[0]
	if got.Reason == nil || *got.Reason != "upstream lacks web_search" {
		t.Fatalf("expected trimmed reason, got %v", got.Reason)
	}
}

func TestSeedMaterializeProfileNotFound(t *testing.T) {
	store := &fakeStore{}
	svc := capadmin.NewSeedService(store, []core.AdapterProfile{deepSeekProfile()})
	_, err := svc.Materialize(context.Background(), 1, "unknown:protocol", "admin")
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
}

func TestSeedMaterializeWritesAdapterSeed(t *testing.T) {
	store := &fakeStore{}
	profile := deepSeekProfile()
	svc := capadmin.NewSeedService(store, []core.AdapterProfile{profile})

	result, err := svc.Materialize(context.Background(), 1, "deepseek:openai", "admin")
	if err != nil {
		t.Fatalf("materialize: %v", err)
	}
	if result.Materialized != len(profile.Declarations) {
		t.Fatalf("expected %d materialized, got %d", len(profile.Declarations), result.Materialized)
	}
	if len(store.upsertModelParams) != len(profile.Declarations) {
		t.Fatalf("expected %d upserts, got %d", len(profile.Declarations), len(store.upsertModelParams))
	}
	for _, p := range store.upsertModelParams {
		if p.Source != core.SourceAdapterSeed {
			t.Fatalf("expected source=adapter_seed, got %q", p.Source)
		}
	}
}

func TestSeedProfilesSortedWithKey(t *testing.T) {
	store := &fakeStore{}
	svc := capadmin.NewSeedService(store, []core.AdapterProfile{
		{Provider: "deepseek", Protocol: "openai"},
		{Provider: "deepseek", Protocol: "anthropic"},
	})
	profiles := svc.Profiles()
	if len(profiles) != 2 {
		t.Fatalf("expected 2 profiles, got %d", len(profiles))
	}
	if profiles[0].Key != "deepseek:anthropic" || profiles[1].Key != "deepseek:openai" {
		t.Fatalf("expected sorted keys, got %q,%q", profiles[0].Key, profiles[1].Key)
	}
}

type fakeSyncer struct {
	gotDryRun bool
	called    bool
}

func (f *fakeSyncer) Sync(_ context.Context, opts modelcatalog.Options) (modelcatalog.Result, error) {
	f.called = true
	f.gotDryRun = opts.DryRun
	return modelcatalog.Result{DryRun: opts.DryRun, Inserted: 3}, nil
}

func TestSyncTriggerPassesDryRun(t *testing.T) {
	syncer := &fakeSyncer{}
	store := &fakeStore{}
	result, err := capadmin.NewSyncService(syncer, store).Trigger(context.Background(), true)
	if err != nil {
		t.Fatalf("trigger: %v", err)
	}
	if !syncer.called || !syncer.gotDryRun {
		t.Fatalf("expected dry-run trigger, got called=%v dryRun=%v", syncer.called, syncer.gotDryRun)
	}
	if !result.DryRun || result.Inserted != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSyncListJobsClampsLimit(t *testing.T) {
	store := &fakeStore{}
	svc := capadmin.NewSyncService(&fakeSyncer{}, store)

	if _, err := svc.ListJobs(context.Background(), 0); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if store.syncJobsLimit != 20 {
		t.Fatalf("expected default limit 20, got %d", store.syncJobsLimit)
	}

	if _, err := svc.ListJobs(context.Background(), 999); err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if store.syncJobsLimit != 50 {
		t.Fatalf("expected clamped limit 50, got %d", store.syncJobsLimit)
	}
}

// deepSeekProfile 构造一个最小 adapter 画像用于物化测试。
func deepSeekProfile() core.AdapterProfile {
	return core.AdapterProfile{
		Provider: "deepseek",
		Protocol: "openai",
		Declarations: []core.Declaration{
			{Key: core.KeyTextInput, SupportLevel: core.SupportLevelFull},
			{Key: core.KeyToolsBuiltinWebSearch, SupportLevel: core.SupportLevelUnsupported},
		},
	}
}
