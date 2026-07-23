package providerendpoint_test

import (
	"context"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/provider"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerendpoint"
)

// TestUpdateStatusFenceIntegration 端到端验证 status 围栏热更新：service → EndpointFencePublisher →
// Redis fence commit + provider_endpoints.status/status_revision +1。
func TestUpdateStatusFenceIntegration(t *testing.T) {
	dbURL := os.Getenv("DATABASE_URL")
	addr := os.Getenv("REDIS_ADDR")
	if dbURL == "" || addr == "" {
		t.Skip("DATABASE_URL or REDIS_ADDR not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("pg ping: %v", err)
	}
	rc := redis.NewClient(&redis.Options{Addr: addr})
	if err := rc.Ping(ctx).Err(); err != nil {
		pool.Close()
		_ = rc.Close()
		t.Skipf("redis ping: %v", err)
	}
	ns := fmt.Sprintf("unio-epstatustest:%d", time.Now().UnixNano())
	t.Cleanup(func() {
		it := rc.Scan(context.Background(), 0, ns+":*", 0).Iterator()
		for it.Next(context.Background()) {
			_ = rc.Del(context.Background(), it.Val()).Err()
		}
		_ = rc.Close()
		pool.Close()
	})

	suffix := time.Now().UnixNano()
	var providerID, endpointID int64
	if err := pool.QueryRow(ctx, `INSERT INTO providers (slug,name,status) VALUES ($1,$2,'enabled') RETURNING id`,
		fmt.Sprintf("epstatus-prov-%d", suffix), "p").Scan(&providerID); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO provider_endpoints (provider_id,name,base_url,status) VALUES ($1,$2,$3,'enabled') RETURNING id`,
		providerID, "ep", fmt.Sprintf("https://epstatus-%d.example.test", suffix)).Scan(&endpointID); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM endpoint_routing_operations WHERE endpoint_id=$1`, endpointID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_endpoints WHERE id=$1`, endpointID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM providers WHERE id=$1`, providerID)
	})

	store := breakerstore.NewStore(rc, ns)
	if _, err := store.InitEndpointControl(ctx, endpointID, 1, 1, "enabled"); err != nil {
		t.Fatalf("init endpoint control: %v", err)
	}

	fencer := providerendpoint.NewEndpointFencer(runtimecontrol.NewEndpointFencePublisher(pool), store)
	svc := providerendpoint.NewService(sqlc.New(pool), store).WithFencer(fencer)

	ep, err := svc.UpdateStatus(ctx, endpointID, "disabled")
	if err != nil {
		t.Fatalf("update status: %v", err)
	}
	if ep.Status != "disabled" || ep.StatusRevision != 2 {
		t.Fatalf("want disabled/2, got %s/%d", ep.Status, ep.StatusRevision)
	}
	if ep.RuntimeSyncPending {
		t.Fatalf("commit succeeded; RuntimeSyncPending should be false")
	}

	// 同值幂等：再置 disabled 不推进 revision。
	ep2, err := svc.UpdateStatus(ctx, endpointID, "disabled")
	if err != nil {
		t.Fatalf("idempotent update: %v", err)
	}
	if ep2.StatusRevision != 2 {
		t.Fatalf("idempotent must not bump revision, got %d", ep2.StatusRevision)
	}
}

type failFirstCombinedCommitStore struct {
	*breakerstore.Store
	fail bool
}

func (s *failFirstCombinedCommitStore) CommitEndpointRoutingChange(
	ctx context.Context,
	endpointID int64,
	token string,
	payload string,
) (breakerstore.FenceResult, error) {
	if s.fail {
		s.fail = false
		return "", breakerstore.ErrStoreUnavailable
	}
	return s.Store.CommitEndpointRoutingChange(ctx, endpointID, token, payload)
}

func TestCombinedFenceRecoversDBCommittedOperationIntegration(t *testing.T) {
	pool, store := setupFenceIntegration(t, "combined-recovery")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	providerID, endpointIDs := seedFenceProvider(t, ctx, pool, "enabled", "enabled")
	endpointID := endpointIDs[0]
	t.Cleanup(func() { cleanupFenceProvider(pool, providerID) })
	if _, err := store.InitEndpointControl(ctx, endpointID, 1, 1, "enabled"); err != nil {
		t.Fatalf("init endpoint control: %v", err)
	}

	failing := &failFirstCombinedCommitStore{Store: store, fail: true}
	fencer := providerendpoint.NewEndpointFencer(runtimecontrol.NewEndpointFencePublisher(pool), failing)
	svc := providerendpoint.NewService(sqlc.New(pool), store).WithFencer(fencer).WithTransactionalDB(pool)
	nextBaseURL := fmt.Sprintf("https://combined-next-%d.example.test", time.Now().UnixNano())
	updated, err := svc.UpdateRouting(ctx, endpointID, nextBaseURL, "disabled")
	if err != nil {
		t.Fatalf("combined update: %v", err)
	}
	if !updated.RuntimeSyncPending || updated.BaseURL != nextBaseURL || updated.BaseURLRevision != 2 ||
		updated.Status != "disabled" || updated.StatusRevision != 2 {
		t.Fatalf("unexpected db_committed result: %+v", updated)
	}

	var token, state string
	if err := pool.QueryRow(ctx, `SELECT token, state FROM endpoint_routing_operations
		WHERE endpoint_id=$1 ORDER BY id DESC LIMIT 1`, endpointID).Scan(&token, &state); err != nil {
		t.Fatalf("read combined operation: %v", err)
	}
	if token == "" || state != "db_committed" {
		t.Fatalf("operation state=%q token=%q, want db_committed", state, token)
	}
	pending, err := store.Snapshot(ctx, breakerstore.ScopeEndpoint, endpointID)
	if err != nil {
		t.Fatalf("read pending endpoint: %v", err)
	}
	if pending.PendingBaseURLRevision != 2 || pending.PendingStatusRevision != 2 {
		t.Fatalf("combined pending fence was lost: %+v", pending)
	}

	if handled, err := runtimecontrol.NewEndpointRoutingReconciler(pool, store).Reconcile(ctx); err != nil || handled != 1 {
		t.Fatalf("reconcile combined operation: handled=%d err=%v", handled, err)
	}
	active, err := store.Snapshot(ctx, breakerstore.ScopeEndpoint, endpointID)
	if err != nil {
		t.Fatalf("read recovered endpoint: %v", err)
	}
	if active.BaseURLRevision != 2 || active.StatusRevision != 2 || active.EffectiveStatus != "disabled" ||
		active.PendingBaseURLRevision != 0 || active.PendingStatusRevision != 0 {
		t.Fatalf("combined operation was not recovered: %+v", active)
	}
	if err := pool.QueryRow(ctx, `SELECT state FROM endpoint_routing_operations WHERE token=$1`, token).Scan(&state); err != nil {
		t.Fatalf("read recovered operation: %v", err)
	}
	if state != "committed" {
		t.Fatalf("recovered operation state=%q, want committed", state)
	}
}

func TestProviderStatusBatchFenceIntegration(t *testing.T) {
	pool, store := setupFenceIntegration(t, "provider-batch")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	providerID, endpointIDs := seedFenceProvider(t, ctx, pool, "enabled", "enabled", "enabled")
	t.Cleanup(func() { cleanupFenceProvider(pool, providerID) })
	for _, endpointID := range endpointIDs {
		if _, err := store.InitEndpointControl(ctx, endpointID, 1, 1, "enabled"); err != nil {
			t.Fatalf("init endpoint %d: %v", endpointID, err)
		}
	}

	queries := sqlc.New(pool)
	svc := provider.NewService(queries).WithStatusFencer(
		provider.NewStatusFencer(runtimecontrol.NewEndpointFencePublisher(pool), store),
		func(context.Context) int { return 16 },
	)
	updated, err := svc.Update(ctx, provider.UpdateInput{ID: providerID, Name: "provider-renamed", Status: "disabled"})
	if err != nil {
		t.Fatalf("disable provider: %v", err)
	}
	if updated.Status != "disabled" || updated.Name != "provider-renamed" || updated.RuntimeSyncPending {
		t.Fatalf("unexpected provider result: %+v", updated)
	}
	for _, endpointID := range endpointIDs {
		var status string
		var revision int64
		if err := pool.QueryRow(ctx, `SELECT status, status_revision FROM provider_endpoints WHERE id=$1`, endpointID).
			Scan(&status, &revision); err != nil {
			t.Fatalf("read endpoint %d: %v", endpointID, err)
		}
		if status != "enabled" || revision != 2 {
			t.Fatalf("endpoint %d business fact=%s/%d, want enabled/2", endpointID, status, revision)
		}
		snapshot, err := store.Snapshot(ctx, breakerstore.ScopeEndpoint, endpointID)
		if err != nil {
			t.Fatalf("snapshot endpoint %d: %v", endpointID, err)
		}
		if snapshot.StatusRevision != 2 || snapshot.EffectiveStatus != "disabled" || snapshot.PendingStatusRevision != 0 {
			t.Fatalf("endpoint %d runtime was not batch committed: %+v", endpointID, snapshot)
		}
	}
	var state string
	if err := pool.QueryRow(ctx, `SELECT state FROM endpoint_routing_operations
		WHERE provider_id=$1 AND kind='provider_status_batch' ORDER BY id DESC LIMIT 1`, providerID).Scan(&state); err != nil {
		t.Fatalf("read provider batch operation: %v", err)
	}
	if state != "committed" {
		t.Fatalf("provider batch state=%q, want committed", state)
	}
}

func TestProviderStatusBatchConflictHasNoPartialMutationIntegration(t *testing.T) {
	pool, store := setupFenceIntegration(t, "provider-batch-conflict")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	providerID, endpointIDs := seedFenceProvider(t, ctx, pool, "enabled", "enabled", "enabled")
	t.Cleanup(func() { cleanupFenceProvider(pool, providerID) })
	if _, err := store.InitEndpointControl(ctx, endpointIDs[0], 1, 1, "enabled"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.InitEndpointControl(ctx, endpointIDs[1], 1, 2, "enabled"); err != nil {
		t.Fatal(err)
	}

	svc := provider.NewService(sqlc.New(pool)).WithStatusFencer(
		provider.NewStatusFencer(runtimecontrol.NewEndpointFencePublisher(pool), store),
		func(context.Context) int { return 16 },
	)
	if _, err := svc.Update(ctx, provider.UpdateInput{ID: providerID, Name: "unchanged", Status: "disabled"}); err == nil {
		t.Fatal("expected stale batch to fail")
	}
	var providerStatus string
	if err := pool.QueryRow(ctx, `SELECT status FROM providers WHERE id=$1`, providerID).Scan(&providerStatus); err != nil {
		t.Fatal(err)
	}
	if providerStatus != "enabled" {
		t.Fatalf("provider partially changed to %q", providerStatus)
	}
	for i, endpointID := range endpointIDs {
		var revision int64
		if err := pool.QueryRow(ctx, `SELECT status_revision FROM provider_endpoints WHERE id=$1`, endpointID).Scan(&revision); err != nil {
			t.Fatal(err)
		}
		if revision != 1 {
			t.Fatalf("endpoint %d database revision changed to %d", endpointID, revision)
		}
		snapshot, err := store.Snapshot(ctx, breakerstore.ScopeEndpoint, endpointID)
		if err != nil {
			t.Fatal(err)
		}
		wantRevision := int64(1)
		if i == 1 {
			wantRevision = 2
		}
		if snapshot.StatusRevision != wantRevision || snapshot.PendingStatusRevision != 0 || snapshot.StatusRevisionState != "active" {
			t.Fatalf("endpoint %d runtime partially changed: %+v", endpointID, snapshot)
		}
	}
}

func setupFenceIntegration(
	t *testing.T,
	label string,
) (*pgxpool.Pool, *breakerstore.Store) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	redisAddr := os.Getenv("REDIS_ADDR")
	if databaseURL == "" || redisAddr == "" {
		t.Skip("DATABASE_URL and REDIS_ADDR are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres unavailable: %v", err)
	}
	rc := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := rc.Ping(ctx).Err(); err != nil {
		pool.Close()
		_ = rc.Close()
		t.Skipf("redis unavailable: %v", err)
	}
	namespace := fmt.Sprintf("unio-%s-test:%d", label, time.Now().UnixNano())
	store := breakerstore.NewStore(rc, namespace)
	t.Cleanup(func() {
		iter := rc.Scan(context.Background(), 0, namespace+":*", 0).Iterator()
		for iter.Next(context.Background()) {
			_ = rc.Del(context.Background(), iter.Val()).Err()
		}
		_ = rc.Close()
		pool.Close()
	})
	return pool, store
}

func seedFenceProvider(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
	providerStatus string,
	endpointStatuses ...string,
) (int64, []int64) {
	t.Helper()
	suffix := time.Now().UnixNano()
	var providerID int64
	if err := pool.QueryRow(ctx, `INSERT INTO providers (slug,name,status) VALUES ($1,$2,$3) RETURNING id`,
		fmt.Sprintf("fence-provider-%d", suffix), "unchanged", providerStatus).Scan(&providerID); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	endpointIDs := make([]int64, 0, len(endpointStatuses))
	for i, status := range endpointStatuses {
		var endpointID int64
		if err := pool.QueryRow(ctx, `INSERT INTO provider_endpoints (provider_id,name,base_url,status)
			VALUES ($1,$2,$3,$4) RETURNING id`, providerID, fmt.Sprintf("endpoint-%d", i),
			fmt.Sprintf("https://fence-%d-%d.example.test", suffix, i), status).Scan(&endpointID); err != nil {
			t.Fatalf("seed endpoint %d: %v", i, err)
		}
		endpointIDs = append(endpointIDs, endpointID)
	}
	return providerID, endpointIDs
}

func cleanupFenceProvider(pool *pgxpool.Pool, providerID int64) {
	ctx := context.Background()
	_, _ = pool.Exec(ctx, `DELETE FROM endpoint_routing_operations WHERE provider_id=$1`, providerID)
	_, _ = pool.Exec(ctx, `DELETE FROM provider_endpoints WHERE provider_id=$1`, providerID)
	_, _ = pool.Exec(ctx, `DELETE FROM providers WHERE id=$1`, providerID)
}
