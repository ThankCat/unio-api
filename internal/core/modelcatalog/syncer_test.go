package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
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
	existing []ExistingCatalogEntry

	// failUpsert 让指定 canonical_id 的 upsert 返回错误，模拟落库失败。
	failUpsert string

	upserted []string
	capHints map[string]int
	removed  []string

	jobCreated   int
	jobRunning   int
	jobSucceeded int
	jobFailed    int
	lastStats    []byte
	lastErrText  string
}

func newFakeSyncStore(existing ...ExistingCatalogEntry) *fakeSyncStore {
	return &fakeSyncStore{
		existing: existing,
		capHints: map[string]int{},
	}
}

func (s *fakeSyncStore) ListCatalogEntries(context.Context) ([]ExistingCatalogEntry, error) {
	return s.existing, nil
}

func (s *fakeSyncStore) UpsertCatalogEntry(_ context.Context, model CanonicalModel) error {
	if model.CanonicalID == s.failUpsert {
		return errors.New("boom upsert")
	}
	s.upserted = append(s.upserted, model.CanonicalID)
	s.capHints[model.CanonicalID] = len(model.CoarseCapabilities)
	return nil
}

func (s *fakeSyncStore) MarkCatalogRemovedUpstream(_ context.Context, canonicalID string) (bool, error) {
	s.removed = append(s.removed, canonicalID)
	return true, nil
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
	if !result.DryRun || result.FeedModels != 2 || result.Upserted != 2 {
		t.Fatalf("unexpected dry-run result: %+v", result)
	}
	if store.jobCreated != 0 || len(store.upserted) != 0 {
		t.Fatalf("dry-run must not write: %+v", store)
	}
}

func TestSyncAppliesPlanAndWritesCatalog(t *testing.T) {
	existing := []ExistingCatalogEntry{
		{CanonicalID: "acme/acme-mini"},
		{CanonicalID: "deepseek/old"},
	}
	store := newFakeSyncStore(existing...)
	syncer := NewSyncer(fakeFetcher{raw: RawFeed{ModelsJSON: []byte(sampleModelsJSON), APIJSON: []byte(sampleAPIJSON)}}, store)

	result, err := syncer.Sync(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	// feed = [acme/acme-mini, deepseek/deepseek-v4-pro] 全量 upsert；deepseek/old 缺失 → removal。
	if result.Upserted != 2 || result.Removed != 1 {
		t.Fatalf("counts: upserted=%d removed=%d", result.Upserted, result.Removed)
	}
	if result.CapabilityHints <= 0 {
		t.Fatalf("want capability hints recorded, got %d", result.CapabilityHints)
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
	if stats.Upserted != 2 || stats.Removed != 1 {
		t.Fatalf("stats counts off: %+v", stats)
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
