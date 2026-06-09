package workers

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
)

type fakeCatalogSyncer struct {
	calls int
	err   error
}

func (f *fakeCatalogSyncer) Sync(context.Context, modelcatalog.Options) (modelcatalog.Result, error) {
	f.calls++
	if f.err != nil {
		return modelcatalog.Result{}, f.err
	}
	return modelcatalog.Result{}, nil
}

type fakeCatalogStore struct {
	latest modelcatalog.LatestSyncJob
	err    error
	calls  int
}

func (s *fakeCatalogStore) ListCanonicalModels(context.Context) ([]modelcatalog.ExistingModel, error) {
	return nil, nil
}
func (s *fakeCatalogStore) UpsertSeedModel(context.Context, modelcatalog.CanonicalModel) (int64, bool, error) {
	return 0, false, nil
}
func (s *fakeCatalogStore) MarkSeedModelRemoved(context.Context, string) (bool, error) {
	return false, nil
}
func (s *fakeCatalogStore) UpsertCoarseCapability(context.Context, int64, capability.Declaration) error {
	return nil
}
func (s *fakeCatalogStore) CreateSyncJob(context.Context) (int64, error)              { return 0, nil }
func (s *fakeCatalogStore) MarkSyncJobRunning(context.Context, int64) error           { return nil }
func (s *fakeCatalogStore) MarkSyncJobSucceeded(context.Context, int64, []byte) error { return nil }
func (s *fakeCatalogStore) MarkSyncJobFailed(context.Context, int64, string) error    { return nil }
func (s *fakeCatalogStore) LatestSyncJob(context.Context) (modelcatalog.LatestSyncJob, error) {
	s.calls++
	return s.latest, s.err
}

func ptrTime(t time.Time) *time.Time { return &t }

func newTestCatalogWorker(syncer ModelCatalogSyncer, store modelcatalog.SyncStore, logger *slog.Logger) (*ModelCatalogSyncWorker, *time.Time) {
	worker := NewModelCatalogSyncWorker(syncer, store, logger, 24*time.Hour)
	clock := new(time.Time)
	*clock = time.Date(2026, 6, 9, 0, 0, 0, 0, time.UTC)
	worker.now = func() time.Time { return *clock }
	return worker, clock
}

func TestModelCatalogSyncWorkerRunsWhenNoJob(t *testing.T) {
	syncer := &fakeCatalogSyncer{}
	store := &fakeCatalogStore{latest: modelcatalog.LatestSyncJob{Found: false}}
	worker, _ := newTestCatalogWorker(syncer, store, slog.Default())

	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("want worked,nil; got %v,%v", worked, err)
	}
	if syncer.calls != 1 {
		t.Fatalf("want 1 sync call, got %d", syncer.calls)
	}
}

func TestModelCatalogSyncWorkerSkipsWhenRecentSuccess(t *testing.T) {
	syncer := &fakeCatalogSyncer{}
	store := &fakeCatalogStore{}
	worker, clock := newTestCatalogWorker(syncer, store, slog.Default())
	store.latest = modelcatalog.LatestSyncJob{
		Found:      true,
		Status:     capability.SyncJobStatusSucceeded,
		FinishedAt: ptrTime(clock.Add(-time.Hour)),
	}

	worked, err := worker.RunOnce(context.Background())
	if err != nil || worked {
		t.Fatalf("want false,nil; got %v,%v", worked, err)
	}
	if syncer.calls != 0 {
		t.Fatalf("recent success must skip sync, got %d calls", syncer.calls)
	}
}

func TestModelCatalogSyncWorkerRunsWhenStale(t *testing.T) {
	syncer := &fakeCatalogSyncer{}
	store := &fakeCatalogStore{}
	worker, clock := newTestCatalogWorker(syncer, store, slog.Default())
	store.latest = modelcatalog.LatestSyncJob{
		Found:      true,
		Status:     capability.SyncJobStatusSucceeded,
		FinishedAt: ptrTime(clock.Add(-25 * time.Hour)),
	}

	worked, err := worker.RunOnce(context.Background())
	if err != nil || !worked {
		t.Fatalf("want worked,nil; got %v,%v", worked, err)
	}
	if syncer.calls != 1 {
		t.Fatalf("stale success must sync, got %d", syncer.calls)
	}
}

func TestModelCatalogSyncWorkerSkipsWhenRunning(t *testing.T) {
	syncer := &fakeCatalogSyncer{}
	store := &fakeCatalogStore{latest: modelcatalog.LatestSyncJob{Found: true, Status: capability.SyncJobStatusRunning}}
	worker, _ := newTestCatalogWorker(syncer, store, slog.Default())

	worked, _ := worker.RunOnce(context.Background())
	if worked || syncer.calls != 0 {
		t.Fatalf("running job must skip; worked=%v calls=%d", worked, syncer.calls)
	}
}

func TestModelCatalogSyncWorkerPollThrottle(t *testing.T) {
	syncer := &fakeCatalogSyncer{}
	store := &fakeCatalogStore{latest: modelcatalog.LatestSyncJob{Found: false}}
	worker, _ := newTestCatalogWorker(syncer, store, slog.Default())

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("first run: %v", err)
	}
	// 时钟未推进：第二次应被 poll 节流，不再查库。
	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("second run: %v", err)
	}
	if store.calls != 1 {
		t.Fatalf("poll throttle should query store once, got %d", store.calls)
	}
}

func TestModelCatalogSyncWorkerConsecutiveFailureAlert(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelWarn}))

	syncer := &fakeCatalogSyncer{err: errors.New("upstream down")}
	store := &fakeCatalogStore{latest: modelcatalog.LatestSyncJob{Found: false}}
	worker, clock := newTestCatalogWorker(syncer, store, logger)

	// 三次到期失败：第 1、2 次警告，第 3 次告警。
	worker.RunOnce(context.Background())
	*clock = clock.Add(6 * time.Minute)
	worker.RunOnce(context.Background())
	*clock = clock.Add(20 * time.Minute)
	worker.RunOnce(context.Background())

	if syncer.calls != 3 {
		t.Fatalf("want 3 sync attempts, got %d", syncer.calls)
	}
	if worker.consecutiveFailures != 3 {
		t.Fatalf("want 3 consecutive failures, got %d", worker.consecutiveFailures)
	}
	if !strings.Contains(buf.String(), "model_catalog_sync_consecutive_failures") {
		t.Fatalf("want consecutive-failure alert in logs, got:\n%s", buf.String())
	}

	// 一次成功后归零并清退避。
	syncer.err = nil
	*clock = clock.Add(time.Hour)
	worker.RunOnce(context.Background())
	if worker.consecutiveFailures != 0 {
		t.Fatalf("success must reset failures, got %d", worker.consecutiveFailures)
	}
}
