package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

type fakeFetcher struct {
	raw RawFeed
	err error
}

func (f fakeFetcher) Fetch(context.Context) (RawFeed, error) {
	if f.err != nil {
		return RawFeed{}, f.err
	}
	return f.raw, nil
}

type fakeSyncStore struct {
	existing []ExistingModel

	// manualGuard 模拟 upsert 命中 source=manual 守护（返回 applied=false）。
	manualGuard map[string]bool
	// failUpsert 让指定 canonical_id 的 upsert 返回错误，模拟落库失败。
	failUpsert string

	nextID   int64
	upserted []string
	caps     map[int64][]capability.Key
	removed  []string

	jobCreated   int
	jobRunning   int
	jobSucceeded int
	jobFailed    int
	lastStats    []byte
	lastErrText  string
}

func newFakeSyncStore(existing ...ExistingModel) *fakeSyncStore {
	return &fakeSyncStore{
		existing:    existing,
		manualGuard: map[string]bool{},
		caps:        map[int64][]capability.Key{},
	}
}

func (s *fakeSyncStore) ListCanonicalModels(context.Context) ([]ExistingModel, error) {
	return s.existing, nil
}

func (s *fakeSyncStore) UpsertSeedModel(_ context.Context, model CanonicalModel) (int64, bool, error) {
	if model.CanonicalID == s.failUpsert {
		return 0, false, errors.New("boom upsert")
	}
	if s.manualGuard[model.CanonicalID] {
		return 0, false, nil
	}
	s.nextID++
	s.upserted = append(s.upserted, model.CanonicalID)
	return s.nextID, true, nil
}

func (s *fakeSyncStore) MarkSeedModelRemoved(_ context.Context, canonicalID string) (bool, error) {
	s.removed = append(s.removed, canonicalID)
	return true, nil
}

func (s *fakeSyncStore) UpsertCoarseCapability(_ context.Context, modelID int64, decl capability.Declaration) error {
	s.caps[modelID] = append(s.caps[modelID], decl.Key)
	return nil
}

func (s *fakeSyncStore) CreateSyncJob(context.Context) (int64, error) {
	s.jobCreated++
	return 100, nil
}

func (s *fakeSyncStore) MarkSyncJobRunning(context.Context, int64) error {
	s.jobRunning++
	return nil
}

func (s *fakeSyncStore) MarkSyncJobSucceeded(_ context.Context, _ int64, stats []byte) error {
	s.jobSucceeded++
	s.lastStats = stats
	return nil
}

func (s *fakeSyncStore) MarkSyncJobFailed(_ context.Context, _ int64, errText string) error {
	s.jobFailed++
	s.lastErrText = errText
	return nil
}

func (s *fakeSyncStore) LatestSyncJob(context.Context) (LatestSyncJob, error) {
	return LatestSyncJob{}, nil
}

func TestSyncDryRunDoesNotWrite(t *testing.T) {
	store := newFakeSyncStore()
	syncer := NewSyncer(fakeFetcher{raw: RawFeed{ModelsJSON: []byte(sampleModelsJSON), APIJSON: []byte(sampleAPIJSON)}}, store)

	result, err := syncer.Sync(context.Background(), Options{DryRun: true})
	if err != nil {
		t.Fatalf("Sync dry-run: %v", err)
	}
	if !result.DryRun || result.FeedModels != 2 || result.Inserted != 2 {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}
	if store.jobCreated != 0 || len(store.upserted) != 0 || len(store.caps) != 0 {
		t.Fatalf("dry-run must not write: %+v", store)
	}
}

func TestSyncAppliesPlanAndSeedsCapabilities(t *testing.T) {
	existing := []ExistingModel{
		{ID: 1, CanonicalID: "acme/acme-mini", Source: modelSourceSeedModelsDev},
		{ID: 2, CanonicalID: "deepseek/old", Source: modelSourceSeedModelsDev},
	}
	store := newFakeSyncStore(existing...)
	syncer := NewSyncer(fakeFetcher{raw: RawFeed{ModelsJSON: []byte(sampleModelsJSON), APIJSON: []byte(sampleAPIJSON)}}, store)

	result, err := syncer.Sync(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// deepseek/deepseek-v4-pro 是新模型 → insert + 粗能力位；acme/acme-mini 已存在 seed → update；deepseek/old 缺失 → removal。
	if result.Inserted != 1 || result.Updated != 1 || result.Removed != 1 {
		t.Fatalf("counts: inserted=%d updated=%d removed=%d", result.Inserted, result.Updated, result.Removed)
	}
	if result.CapabilitiesSeeded != 6 {
		t.Fatalf("want 6 coarse caps for deepseek, got %d", result.CapabilitiesSeeded)
	}
	if len(store.caps) != 1 {
		t.Fatalf("only the newly inserted model should get caps, got %+v", store.caps)
	}
	if len(store.removed) != 1 || store.removed[0] != "deepseek/old" {
		t.Fatalf("want removal of deepseek/old, got %+v", store.removed)
	}

	// sync_job 生命周期：created → running → succeeded，无 failed。
	if store.jobCreated != 1 || store.jobRunning != 1 || store.jobSucceeded != 1 || store.jobFailed != 0 {
		t.Fatalf("sync job lifecycle off: %+v", store)
	}

	var stats syncStats
	if err := json.Unmarshal(store.lastStats, &stats); err != nil {
		t.Fatalf("stats json: %v", err)
	}
	if stats.License != "MIT" || stats.Attribution == "" || stats.SourceFingerprint == "" {
		t.Fatalf("stats must carry license audit: %+v", stats)
	}
	if stats.Inserted != 1 || stats.Updated != 1 || stats.CapabilitiesSeeded != 6 {
		t.Fatalf("stats counts off: %+v", stats)
	}
}

func TestSyncManualGuardRaceRecordsConflict(t *testing.T) {
	store := newFakeSyncStore()
	// deepseek 在 plan 阶段是 insert，但落库时命中 manual 守护（竞态），应记 conflict、不写能力位。
	store.manualGuard["deepseek/deepseek-v4-pro"] = true
	syncer := NewSyncer(fakeFetcher{raw: RawFeed{ModelsJSON: []byte(sampleModelsJSON)}}, store)

	result, err := syncer.Sync(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if result.Inserted != 1 {
		t.Fatalf("acme should still insert, got inserted=%d", result.Inserted)
	}
	found := false
	for _, c := range result.ManualConflicts {
		if c == "deepseek/deepseek-v4-pro" {
			found = true
		}
	}
	if !found {
		t.Fatalf("want deepseek recorded as manual conflict, got %+v", result.ManualConflicts)
	}
	if len(store.caps) != 1 {
		t.Fatalf("guarded model must not seed caps, caps=%+v", store.caps)
	}
}

func TestSyncFetchErrorReturnsBeforeJob(t *testing.T) {
	store := newFakeSyncStore()
	syncer := NewSyncer(fakeFetcher{err: errors.New("network down")}, store)

	if _, err := syncer.Sync(context.Background(), Options{}); err == nil {
		t.Fatal("want fetch error")
	}
	if store.jobCreated != 0 {
		t.Fatalf("no sync job on fetch failure, got %d", store.jobCreated)
	}
}

func TestSyncApplyErrorMarksJobFailed(t *testing.T) {
	store := newFakeSyncStore()
	store.failUpsert = "acme/acme-mini"
	syncer := NewSyncer(fakeFetcher{raw: RawFeed{ModelsJSON: []byte(sampleModelsJSON)}}, store)

	if _, err := syncer.Sync(context.Background(), Options{}); err == nil {
		t.Fatal("want apply error")
	}
	if store.jobFailed != 1 || store.jobSucceeded != 0 {
		t.Fatalf("failed job not recorded: %+v", store)
	}
	if store.lastErrText == "" {
		t.Fatal("want error text recorded on failed job")
	}
}
