package runtimecontrol_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func newPublisherTest(t *testing.T) (*pgxpool.Pool, *breakerstore.Store, string) {
	t.Helper()
	dbURL := os.Getenv("DATABASE_URL")
	if dbURL == "" {
		t.Skip("DATABASE_URL is not set")
	}
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR is not set")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, dbURL)
	if err != nil {
		t.Fatalf("pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres ping: %v", err)
	}
	rc := redis.NewClient(&redis.Options{Addr: addr})
	if err := rc.Ping(ctx).Err(); err != nil {
		pool.Close()
		_ = rc.Close()
		t.Skipf("redis ping: %v", err)
	}
	ns := fmt.Sprintf("unio-rctltest:%d", time.Now().UnixNano())
	t.Cleanup(func() {
		iter := rc.Scan(context.Background(), 0, ns+":*", 0).Iterator()
		for iter.Next(context.Background()) {
			_ = rc.Del(context.Background(), iter.Val()).Err()
		}
		_ = rc.Close()
		pool.Close()
	})
	return pool, breakerstore.NewStore(rc, ns), ns
}

// seedSetting 插入一条测试 app_settings 行（key 必须是 runtime_control_operations CHECK 允许的四项之一），
// 返回 key；t.Cleanup 负责删除。
func seedSetting(t *testing.T, pool *pgxpool.Pool, key, value string) string {
	t.Helper()
	ctx := context.Background()
	if _, err := pool.Exec(ctx, `INSERT INTO app_settings (key, value, revision) VALUES ($1, $2::jsonb, 1)
		ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value, revision = 1`, key, value); err != nil {
		t.Fatalf("seed setting: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM runtime_control_operations WHERE setting_key = $1`, key)
		_, _ = pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = $1`, key)
	})
	return key
}

func settingBusinessCommit(key, value string, nextRev int64) func(context.Context, pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
		ct, err := tx.Exec(ctx, `UPDATE app_settings SET value = $1::jsonb, revision = $2, updated_at = now() WHERE key = $3 AND revision = $2 - 1`, value, nextRev, key)
		if err != nil {
			return err
		}
		if ct.RowsAffected() != 1 {
			return fmt.Errorf("app_settings revision CAS failed for %s", key)
		}
		return nil
	}
}

// TestPublisherCommitsControlAndBusinessRow 验证完整发布：Redis control 激活到 next revision，
// PostgreSQL 业务行与 operation 一并提交。
func TestPublisherCommitsControlAndBusinessRow(t *testing.T) {
	pool, store, _ := newPublisherTest(t)
	ctx := context.Background()
	key := seedSetting(t, pool, "gateway.circuit_breaker", `{"v":1}`)
	target := store.SettingControl(key)

	// 建立初始 revision=1 control（recovery-only restore）。
	if _, err := store.RestoreMissingControl(ctx, target, 1, `{"v":1}`); err != nil {
		t.Fatalf("restore initial control: %v", err)
	}

	pub := runtimecontrol.NewPublisher(pool, store)
	skey := key
	res, err := pub.Publish(ctx, runtimecontrol.PublishRequest{
		Kind:            runtimecontrol.KindAppSetting,
		Target:          target,
		Token:           "tok-" + key,
		Payload:         `{"v":2}`,
		CurrentRevision: 1,
		NextRevision:    2,
		SettingKey:      &skey,
		BusinessCommit:  settingBusinessCommit(key, `{"v":2}`, 2),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.State != runtimecontrol.PublishCommitted || res.ActiveRevision != 2 {
		t.Fatalf("want committed@2, got %s@%d", res.State, res.ActiveRevision)
	}

	// Redis control active=2, payload={"v":2}。
	snap, err := store.ReadControl(ctx, target, 2)
	if err != nil {
		t.Fatalf("read control: %v", err)
	}
	if snap.ActiveRevision != 2 || snap.ActivePayload != `{"v":2}` {
		t.Fatalf("control not active@2: %+v", snap)
	}

	// 业务行 revision=2；operation committed。
	var rev int64
	if err := pool.QueryRow(ctx, `SELECT revision FROM app_settings WHERE key=$1`, key).Scan(&rev); err != nil {
		t.Fatalf("read setting: %v", err)
	}
	if rev != 2 {
		t.Fatalf("app_settings revision want 2, got %d", rev)
	}
	q := sqlc.New(pool)
	op, err := q.GetRuntimeControlOperationByToken(ctx, "tok-"+key)
	if err != nil {
		t.Fatalf("get op: %v", err)
	}
	if op.State != "committed" {
		t.Fatalf("op state want committed, got %s", op.State)
	}
}

// failingCommit 包装 store，让首次 CommitControl 失败，模拟 Redis commit 响应丢失。
type failingCommit struct {
	*breakerstore.Store
	failed bool
}

func (f *failingCommit) CommitControl(ctx context.Context, target breakerstore.ControlTarget, token, payload string) (int64, error) {
	if !f.failed {
		f.failed = true
		return 0, errors.New("simulated redis commit loss")
	}
	return f.Store.CommitControl(ctx, target, token, payload)
}

// TestPublisherRuntimeSyncPendingThenReconcile 验证 Redis commit 丢失时返回 pending，业务行已提交，
// 随后 reconciler 依据当前 payload 重试 Commit 收口为 committed。
func TestPublisherRuntimeSyncPendingThenReconcile(t *testing.T) {
	pool, store, _ := newPublisherTest(t)
	ctx := context.Background()
	key := seedSetting(t, pool, "gateway.routing_balance", `{"v":1}`)
	target := store.SettingControl(key)
	if _, err := store.RestoreMissingControl(ctx, target, 1, `{"v":1}`); err != nil {
		t.Fatalf("restore initial control: %v", err)
	}

	fc := &failingCommit{Store: store}
	pub := runtimecontrol.NewPublisher(pool, fc)
	skey := key
	res, err := pub.Publish(ctx, runtimecontrol.PublishRequest{
		Kind:            runtimecontrol.KindAppSetting,
		Target:          target,
		Token:           "tok-" + key,
		Payload:         `{"v":2}`,
		CurrentRevision: 1,
		NextRevision:    2,
		SettingKey:      &skey,
		BusinessCommit:  settingBusinessCommit(key, `{"v":2}`, 2),
	})
	if err != nil {
		t.Fatalf("publish: %v", err)
	}
	if res.State != runtimecontrol.PublishRuntimeSyncPending {
		t.Fatalf("want runtime_sync_pending, got %s", res.State)
	}
	// 业务行已提交（revision=2），但 Redis control 仍 pending（active=1）。
	var rev int64
	_ = pool.QueryRow(ctx, `SELECT revision FROM app_settings WHERE key=$1`, key).Scan(&rev)
	if rev != 2 {
		t.Fatalf("business row must be committed at 2, got %d", rev)
	}

	// reconciler：db_committed → 重试 CommitControl（这次成功）→ committed。
	rec := runtimecontrol.NewReconciler(pool, store, func(op sqlc.RuntimeControlOperation) (breakerstore.ControlTarget, bool) {
		if op.SettingKey.Valid && op.SettingKey.String == key {
			return store.SettingControl(key), true
		}
		return breakerstore.ControlTarget{}, false
	})
	handled, err := rec.ReconcileWithPayload(ctx, func(ctx context.Context, op sqlc.RuntimeControlOperation) (string, bool, error) {
		// 依据业务当前事实还原 payload（此处即当前 setting 值）。
		return `{"v":2}`, true, nil
	})
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if handled < 1 {
		t.Fatalf("reconciler should handle the db_committed op, handled=%d", handled)
	}
	snap, err := store.ReadControl(ctx, target, 2)
	if err != nil {
		t.Fatalf("read control after reconcile: %v", err)
	}
	if snap.ActiveRevision != 2 {
		t.Fatalf("control should be active@2 after reconcile, got %+v", snap)
	}
	q := sqlc.New(pool)
	op, _ := q.GetRuntimeControlOperationByToken(ctx, "tok-"+key)
	if op.State != "committed" {
		t.Fatalf("op should be committed after reconcile, got %s", op.State)
	}
}

// TestPublisherRetryFromDBCommittedDoesNotRepeatBusinessCommit 验证第一次 Redis Commit 响应丢失后，
// 同 token 重试直接从 durable db_committed 续接，绝不再次执行数据库业务 CAS。
func TestPublisherRetryFromDBCommittedDoesNotRepeatBusinessCommit(t *testing.T) {
	pool, store, _ := newPublisherTest(t)
	ctx := context.Background()
	key := seedSetting(t, pool, "gateway.routing_balance", `{"v":1}`)
	target := store.SettingControl(key)
	if _, err := store.RestoreMissingControl(ctx, target, 1, `{"v":1}`); err != nil {
		t.Fatalf("restore initial control: %v", err)
	}

	skey := key
	req := runtimecontrol.PublishRequest{
		Kind:            runtimecontrol.KindAppSetting,
		Target:          target,
		Token:           "retry-db-committed-" + key,
		Payload:         `{"v":2}`,
		CurrentRevision: 1,
		NextRevision:    2,
		SettingKey:      &skey,
		BusinessCommit:  settingBusinessCommit(key, `{"v":2}`, 2),
	}
	first, err := runtimecontrol.NewPublisher(pool, &failingCommit{Store: store}).Publish(ctx, req)
	if err != nil || first.State != runtimecontrol.PublishRuntimeSyncPending {
		t.Fatalf("first publish want pending: result=%+v err=%v", first, err)
	}

	businessCalls := 0
	req.BusinessCommit = func(context.Context, pgx.Tx) error {
		businessCalls++
		return errors.New("business commit must not be called on db_committed retry")
	}
	second, err := runtimecontrol.NewPublisher(pool, store).Publish(ctx, req)
	if err != nil {
		t.Fatalf("retry publish: %v", err)
	}
	if second.State != runtimecontrol.PublishCommitted || second.ActiveRevision != 2 {
		t.Fatalf("retry want committed@2, got %+v", second)
	}
	if businessCalls != 0 {
		t.Fatalf("business commit repeated %d times", businessCalls)
	}
}

// TestPublisherRejectsRedisCommittedBeforeDurableBusinessState 覆盖跨存储分叉：Redis 已激活 next，
// 但 PostgreSQL operation/业务行仍在提交前。Publisher 必须保持隔离，不能补做业务更新来猜测结果。
func TestPublisherRejectsRedisCommittedBeforeDurableBusinessState(t *testing.T) {
	pool, store, _ := newPublisherTest(t)
	ctx := context.Background()
	key := seedSetting(t, pool, "gateway.circuit_breaker", `{"v":1}`)
	target := store.SettingControl(key)
	if _, err := store.RestoreMissingControl(ctx, target, 1, `{"v":1}`); err != nil {
		t.Fatalf("restore initial control: %v", err)
	}
	token := "split-before-db-" + key
	if code, _, err := store.PrepareControl(ctx, target, token, 1, 2, `{"v":2}`); err != nil || code != breakerstore.ControlPrepared {
		t.Fatalf("prepare redis split: code=%s err=%v", code, err)
	}
	if _, err := store.CommitControl(ctx, target, token, `{"v":2}`); err != nil {
		t.Fatalf("commit redis split: %v", err)
	}

	businessCalls := 0
	skey := key
	result, err := runtimecontrol.NewPublisher(pool, store).Publish(ctx, runtimecontrol.PublishRequest{
		Kind:            runtimecontrol.KindAppSetting,
		Target:          target,
		Token:           token,
		Payload:         `{"v":2}`,
		CurrentRevision: 1,
		NextRevision:    2,
		SettingKey:      &skey,
		BusinessCommit: func(context.Context, pgx.Tx) error {
			businessCalls++
			return nil
		},
	})
	if err == nil || result.State != runtimecontrol.PublishRuntimeSyncPending {
		t.Fatalf("split state must fail closed as pending: result=%+v err=%v", result, err)
	}
	if businessCalls != 0 {
		t.Fatalf("business commit must not run, calls=%d", businessCalls)
	}
	var revision int64
	if scanErr := pool.QueryRow(ctx, `SELECT revision FROM app_settings WHERE key=$1`, key).Scan(&revision); scanErr != nil {
		t.Fatalf("read setting revision: %v", scanErr)
	}
	if revision != 1 {
		t.Fatalf("business revision changed to %d, want 1", revision)
	}
}
