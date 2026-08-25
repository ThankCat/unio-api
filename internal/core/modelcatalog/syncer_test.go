package modelcatalog

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"testing"
	"time"
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

	upsertedLabs []string
	// staleLabs 是 ListLabsNeedingLogo 的返回值，模拟「图标缺失或过期」的出品方。
	staleLabs  []string
	savedLogos map[string]string
}

func newFakeSyncStore(existing ...ExistingCatalogEntry) *fakeSyncStore {
	return &fakeSyncStore{
		existing:   existing,
		capHints:   map[string]int{},
		savedLogos: map[string]string{},
	}
}

func (s *fakeSyncStore) UpsertLab(_ context.Context, slug string) error {
	s.upsertedLabs = append(s.upsertedLabs, slug)
	return nil
}

func (s *fakeSyncStore) ListLabsNeedingLogo(context.Context, time.Time) ([]string, error) {
	return s.staleLabs, nil
}

func (s *fakeSyncStore) SaveLabLogo(_ context.Context, slug, logoSVG string) error {
	s.savedLogos[slug] = logoSVG
	return nil
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

// logoFakeFetcher 在元数据 fetcher 之上实现 LogoFetcher，可对指定 slug 制造抓取失败。
type logoFakeFetcher struct {
	fakeFetcher
	logos     map[string]string
	failSlugs map[string]bool
	requested []string
}

func (f *logoFakeFetcher) FetchLabLogo(_ context.Context, slug string) (string, error) {
	f.requested = append(f.requested, slug)
	if f.failSlugs[slug] {
		return "", errors.New("boom logo")
	}
	return f.logos[slug], nil
}

// 出品方图标随目录同步补齐：feed 里出现的 lab 都要登记，缺图标的抓一次。
func TestSyncRegistersLabsAndFetchesMissingLogos(t *testing.T) {
	store := newFakeSyncStore()
	store.staleLabs = []string{"acme", "deepseek"}
	fetcher := &logoFakeFetcher{
		fakeFetcher: fakeFetcher{raw: RawFeed{ModelsJSON: []byte(sampleModelsJSON), APIJSON: []byte(sampleAPIJSON)}},
		logos:       map[string]string{"acme": `<svg viewBox="0 0 24 24"/>`},
	}

	result, err := NewSyncer(fetcher, store).Sync(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}

	if !slices.Contains(store.upsertedLabs, "acme") || !slices.Contains(store.upsertedLabs, "deepseek") {
		t.Fatalf("feed 里的出品方都要登记，实际 %+v", store.upsertedLabs)
	}
	if got := store.savedLogos["acme"]; got != `<svg viewBox="0 0 24 24"/>` {
		t.Fatalf("acme 图标 = %q", got)
	}
	// 上游没有 deepseek 图标：也要落空串并打时间戳，否则每次同步都会重抓。
	got, ok := store.savedLogos["deepseek"]
	if !ok || got != "" {
		t.Fatalf("上游缺图标时应落空串，实际 ok=%v value=%q", ok, got)
	}
	if result.LogosSynced != 1 {
		t.Fatalf("LogosSynced = %d, want 1（只计实际抓到内容的）", result.LogosSynced)
	}
}

// 单个图标抓取失败不写库、也不让整次目录同步失败——留到下次重试。
func TestSyncLogoFetchFailureDoesNotFailSync(t *testing.T) {
	store := newFakeSyncStore()
	store.staleLabs = []string{"acme"}
	fetcher := &logoFakeFetcher{
		fakeFetcher: fakeFetcher{raw: RawFeed{ModelsJSON: []byte(sampleModelsJSON), APIJSON: []byte(sampleAPIJSON)}},
		failSlugs:   map[string]bool{"acme": true},
	}

	result, err := NewSyncer(fetcher, store).Sync(context.Background(), Options{})
	if err != nil {
		t.Fatalf("图标抓取失败不应让目录同步失败: %v", err)
	}
	if result.Upserted == 0 {
		t.Fatal("目录条目仍应正常写入")
	}
	if _, saved := store.savedLogos["acme"]; saved {
		t.Fatal("抓取失败不应写库，否则会把失败固化成「已确认没有」")
	}
	if store.jobSucceeded != 1 || store.jobFailed != 0 {
		t.Fatalf("同步任务应记为成功: succeeded=%d failed=%d", store.jobSucceeded, store.jobFailed)
	}
}

// 不实现 LogoFetcher 的 fetcher 照常工作：图标能力是可选扩展。
func TestSyncWithoutLogoFetcherStillRegistersLabs(t *testing.T) {
	store := newFakeSyncStore()
	store.staleLabs = []string{"acme"}

	result, err := NewSyncer(fakeFetcher{raw: RawFeed{ModelsJSON: []byte(sampleModelsJSON), APIJSON: []byte(sampleAPIJSON)}}, store).
		Sync(context.Background(), Options{})
	if err != nil {
		t.Fatalf("Sync: %v", err)
	}
	if len(store.upsertedLabs) == 0 {
		t.Fatal("出品方登记不依赖图标能力")
	}
	if len(store.savedLogos) != 0 || result.LogosSynced != 0 {
		t.Fatalf("没有图标能力时不应写图标: saved=%+v synced=%d", store.savedLogos, result.LogosSynced)
	}
}
