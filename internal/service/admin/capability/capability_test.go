package capability_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/jackc/pgx/v5"

	core "github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	capadmin "github.com/ThankCat/unio-api/internal/service/admin/capability"
)

// fakeStore 实现完整 core/capability.Store，覆盖 admin Store / SyncJobStore / SeedService 三处依赖。
type fakeStore struct {
	lookupErr error

	upsertModelParams []core.UpsertModelCapabilityParams
	upsertModelRow    core.ModelCapability

	deleteModelCalls int

	unknownKeys map[core.Key]struct{}

	syncJobs       []core.SyncJob
	syncJobsParams sqlc.ListSyncJobsParams
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

func (s *fakeStore) ListCapabilityKeys(context.Context) ([]core.CapabilityKey, error) {
	return nil, nil
}

func (s *fakeStore) GetCapabilityKey(context.Context, core.Key) (core.CapabilityKey, error) {
	return core.CapabilityKey{}, nil
}

func (s *fakeStore) CreateCapabilityKey(context.Context, core.CreateCapabilityKeyParams) (core.CapabilityKey, error) {
	return core.CapabilityKey{}, nil
}

func (s *fakeStore) UpdateCapabilityKey(context.Context, core.UpdateCapabilityKeyParams) (core.CapabilityKey, error) {
	return core.CapabilityKey{}, nil
}

func (s *fakeStore) DeleteCapabilityKey(context.Context, core.Key) error {
	return nil
}

func (s *fakeStore) CapabilityKeyExists(_ context.Context, key core.Key) (bool, error) {
	if s.unknownKeys != nil {
		if _, bad := s.unknownKeys[key]; bad {
			return false, nil
		}
	}
	return true, nil
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

func (s *fakeStore) ListSyncJobs(_ context.Context, arg sqlc.ListSyncJobsParams) ([]core.SyncJob, error) {
	s.syncJobsParams = arg
	return s.syncJobs, nil
}

func (s *fakeStore) CountSyncJobs(context.Context) (int64, error) {
	return int64(len(s.syncJobs)), nil
}

func TestSetModelCapabilityPropagatesActor(t *testing.T) {
	store := &fakeStore{}
	_, err := capadmin.NewCapabilityService(store, nil, nil).SetModelCapability(context.Background(), capadmin.SetModelCapabilityInput{
		ModelID:      1,
		Key:          string(core.Key("tools.function")),
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
	if got.UpdatedBy == nil || *got.UpdatedBy != "admin" {
		t.Fatalf("expected updated_by=admin, got %v", got.UpdatedBy)
	}
}

func TestSetModelCapabilityRejectsKeyNotInDictionary(t *testing.T) {
	store := &fakeStore{unknownKeys: map[core.Key]struct{}{"tools.unknown": {}}}
	_, err := capadmin.NewCapabilityService(store, nil, nil).SetModelCapability(context.Background(), capadmin.SetModelCapabilityInput{
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
	_, err := capadmin.NewCapabilityService(store, nil, nil).SetModelCapability(context.Background(), capadmin.SetModelCapabilityInput{
		ModelID:      1,
		Key:          string(core.Key("reasoning.effort")),
		SupportLevel: string(core.SupportLevelFull),
		Limits:       json.RawMessage(`{"max_effort":"high"}`),
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminInvalidArgument {
		t.Fatalf("expected %q, got %q", failure.CodeAdminInvalidArgument, got)
	}
}

func TestSetModelCapabilityAcceptsNullLimitsAtFull(t *testing.T) {
	store := &fakeStore{}
	_, err := capadmin.NewCapabilityService(store, nil, nil).SetModelCapability(context.Background(), capadmin.SetModelCapabilityInput{
		ModelID:      1,
		Key:          string(core.Key("text.input")),
		SupportLevel: string(core.SupportLevelFull),
		Limits:       json.RawMessage("null"),
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(store.upsertModelParams) != 1 {
		t.Fatalf("expected upsert, got %d calls", len(store.upsertModelParams))
	}
}

func TestSetModelCapabilityModelNotFound(t *testing.T) {
	store := &fakeStore{lookupErr: pgx.ErrNoRows}
	_, err := capadmin.NewCapabilityService(store, nil, nil).SetModelCapability(context.Background(), capadmin.SetModelCapabilityInput{
		ModelID:      99,
		Key:          string(core.Key("tools.function")),
		SupportLevel: string(core.SupportLevelFull),
	})
	if got := failure.CodeOf(err); got != failure.CodeAdminNotFound {
		t.Fatalf("expected %q, got %q", failure.CodeAdminNotFound, got)
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
	return modelcatalog.Result{DryRun: opts.DryRun, Upserted: 3}, nil
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
	if !result.DryRun || result.Upserted != 3 {
		t.Fatalf("unexpected result: %+v", result)
	}
}

func TestSyncListJobsForwardsPagination(t *testing.T) {
	store := &fakeStore{}
	svc := capadmin.NewSyncService(&fakeSyncer{}, store)

	jobs, total, err := svc.ListJobs(context.Background(), capadmin.ListJobsParams{
		SortField: "created_at",
		SortDesc:  true,
		Limit:     15,
		Offset:    30,
	})
	if err != nil {
		t.Fatalf("list jobs: %v", err)
	}
	if total != 0 || len(jobs) != 0 {
		t.Fatalf("unexpected jobs=%d total=%d", len(jobs), total)
	}
	if store.syncJobsParams.PageLimit != 15 || store.syncJobsParams.PageOffset != 30 {
		t.Fatalf("pagination not forwarded: %+v", store.syncJobsParams)
	}
	if !store.syncJobsParams.SortField.Valid || store.syncJobsParams.SortField.String != "created_at" {
		t.Fatalf("sort field not forwarded: %+v", store.syncJobsParams.SortField)
	}
	if !store.syncJobsParams.SortDesc.Valid || !store.syncJobsParams.SortDesc.Bool {
		t.Fatalf("sort desc not forwarded: %+v", store.syncJobsParams.SortDesc)
	}
}

// deepSeekProfile 构造一个最小 adapter 画像用于物化测试。
func deepSeekProfile() core.AdapterProfile {
	return core.AdapterProfile{
		Provider: "deepseek",
		Protocol: "openai",
		Declarations: []core.Declaration{
			{Key: core.Key("text.input"), SupportLevel: core.SupportLevelFull},
			{Key: core.Key("tools.builtin.web_search"), SupportLevel: core.SupportLevelUnsupported},
		},
	}
}
