package health

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/accountusage"
	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

var testNow = time.Unix(1_700_000_000, 0)

type fakeQueries struct {
	snapshots map[int64][]byte
	touched   []int64
	policy    map[int64]sqlc.GetAccountUsagePausePolicyRow
	policyErr error
	// reconcile 是 AdminListUsagePauseReconcileAccounts 的返回；lastScope 记录调用参数。
	reconcile    []sqlc.AdminListUsagePauseReconcileAccountsRow
	reconcileErr error
	lastScope    sqlc.AdminListUsagePauseReconcileAccountsParams
}

func (q *fakeQueries) UpdateAccountUsageSnapshot(_ context.Context, arg sqlc.UpdateAccountUsageSnapshotParams) error {
	if q.snapshots == nil {
		q.snapshots = map[int64][]byte{}
	}
	q.snapshots[arg.ID] = arg.UsageSnapshot
	return nil
}

func (q *fakeQueries) TouchAccountLastSuccess(_ context.Context, arg sqlc.TouchAccountLastSuccessParams) error {
	q.touched = append(q.touched, arg.ID)
	return nil
}

func (q *fakeQueries) GetAccountUsagePausePolicy(_ context.Context, id int64) (sqlc.GetAccountUsagePausePolicyRow, error) {
	if q.policyErr != nil {
		return sqlc.GetAccountUsagePausePolicyRow{}, q.policyErr
	}
	return q.policy[id], nil
}

func (q *fakeQueries) AdminListUsagePauseReconcileAccounts(_ context.Context, arg sqlc.AdminListUsagePauseReconcileAccountsParams) ([]sqlc.AdminListUsagePauseReconcileAccountsRow, error) {
	q.lastScope = arg
	if q.reconcileErr != nil {
		return nil, q.reconcileErr
	}
	return q.reconcile, nil
}

type runtimeCall struct {
	accountID  int64
	paused     bool
	durationMs int64
	window     breakerstore.AccountUsageWindow
}

type fakeRuntime struct {
	calls    []runtimeCall
	failFor  map[int64]error
	pauseErr error
}

func (r *fakeRuntime) PauseAccountUsage(_ context.Context, accountID, durationMs int64, window breakerstore.AccountUsageWindow) (int64, error) {
	if err := r.failFor[accountID]; err != nil {
		return 0, err
	}
	if r.pauseErr != nil {
		return 0, r.pauseErr
	}
	r.calls = append(r.calls, runtimeCall{accountID: accountID, paused: true, durationMs: durationMs, window: window})
	return durationMs, nil
}

func (r *fakeRuntime) ResumeAccountUsage(_ context.Context, accountID int64) error {
	if err := r.failFor[accountID]; err != nil {
		return err
	}
	r.calls = append(r.calls, runtimeCall{accountID: accountID})
	return nil
}

func int4(v int32) pgtype.Int4 { return pgtype.Int4{Int32: v, Valid: true} }

func usageFacts(primaryPercent float64, resetAfter time.Duration) *adapter.AccountUsageFacts {
	return &adapter.AccountUsageFacts{
		PlanType: "plus",
		Primary: adapter.AccountUsageWindowFacts{
			Present: true, UsedPercent: primaryPercent, WindowMinutes: 300,
			ResetAtUnix: testNow.Add(resetAfter).Unix(),
		},
	}
}

func newTestRecorder(queries *fakeQueries, runtime *fakeRuntime, global int32) *Recorder {
	recorder := NewRecorder(queries, runtime, nil, 0).
		WithThresholdProvider(func(context.Context) int32 { return global })
	recorder.now = func() time.Time { return testNow }
	return recorder
}

// 账号阈值覆写优先于全局：全局 90 下 85% 不暂停，账号设 80 后同一观测触发暂停到窗口重置。
func TestRecorderPausesByAccountThresholdOverride(t *testing.T) {
	queries := &fakeQueries{policy: map[int64]sqlc.GetAccountUsagePausePolicyRow{
		1: {AccountThresholdPercent: int4(80)},
	}}
	runtime := &fakeRuntime{}
	recorder := newTestRecorder(queries, runtime, 90)

	recorder.RecordAccountSuccess(context.Background(), 1, usageFacts(85, 2*time.Hour))

	if len(queries.touched) != 1 || queries.touched[0] != 1 {
		t.Fatalf("success must touch LRU once, got %v", queries.touched)
	}
	if len(runtime.calls) != 1 || !runtime.calls[0].paused {
		t.Fatalf("expected a pause call, got %+v", runtime.calls)
	}
	call := runtime.calls[0]
	if call.durationMs != (2*time.Hour).Milliseconds() || call.window != breakerstore.AccountUsageWindowPrimary {
		t.Fatalf("pause must last until the primary window resets, got %+v", call)
	}
}

// 渠道阈值介于账号与全局之间：账号 NULL 时按渠道 95 判定，90% 观测不暂停并显式恢复。
func TestRecorderResumesWhenChannelThresholdRaised(t *testing.T) {
	queries := &fakeQueries{policy: map[int64]sqlc.GetAccountUsagePausePolicyRow{
		1: {ChannelThresholdPercent: int4(95)},
	}}
	runtime := &fakeRuntime{}
	recorder := newTestRecorder(queries, runtime, 90)

	recorder.RecordAccountUsageObservation(context.Background(), 1, usageFacts(90, time.Hour))

	if len(queries.touched) != 0 {
		t.Fatalf("failure observation must not touch LRU, got %v", queries.touched)
	}
	if len(runtime.calls) != 1 || runtime.calls[0].paused {
		t.Fatalf("expected an explicit resume, got %+v", runtime.calls)
	}
}

// 两层覆写读不到时退回全局阈值继续评估，而不是放弃暂停。
func TestRecorderFallsBackToGlobalWhenPolicyUnavailable(t *testing.T) {
	queries := &fakeQueries{policyErr: errors.New("postgres down")}
	runtime := &fakeRuntime{}
	recorder := newTestRecorder(queries, runtime, 90)

	recorder.RecordAccountUsageObservation(context.Background(), 1, usageFacts(90, time.Hour))

	if len(runtime.calls) != 1 || !runtime.calls[0].paused {
		t.Fatalf("global threshold must still pause a 90%% account, got %+v", runtime.calls)
	}
}

// 快照落库时把相对重置秒数换算成绝对时刻：调度侧按快照实时判定，缺绝对时刻会把高水位误判为已重置。
func TestRecorderPersistsAbsoluteResetAt(t *testing.T) {
	queries := &fakeQueries{}
	recorder := newTestRecorder(queries, &fakeRuntime{}, 90)
	facts := &adapter.AccountUsageFacts{
		Primary: adapter.AccountUsageWindowFacts{Present: true, UsedPercent: 50, ResetAfterSeconds: 600},
	}

	recorder.RecordAccountUsageObservation(context.Background(), 1, facts)

	snapshot, ok := accountusage.ParseSnapshot(queries.snapshots[1])
	if !ok || snapshot.Primary == nil {
		t.Fatalf("snapshot must be persisted, got %s", queries.snapshots[1])
	}
	if snapshot.Primary.ResetAt != testNow.Unix()+600 {
		t.Fatalf("reset_at = %d, want %d", snapshot.Primary.ResetAt, testNow.Unix()+600)
	}
	if snapshot.Secondary != nil {
		t.Fatalf("missing secondary window must stay absent, got %+v", snapshot.Secondary)
	}
	var raw map[string]any
	if err := json.Unmarshal(queries.snapshots[1], &raw); err != nil {
		t.Fatalf("snapshot must be JSON: %v", err)
	}
	if _, has := raw["captured_at"]; !has {
		t.Fatalf("snapshot must carry captured_at, got %v", raw)
	}
}

func snapshotJSON(t *testing.T, usedPercent float64, resetAt time.Time) []byte {
	t.Helper()
	raw, err := json.Marshal(accountusage.Snapshot{
		Primary:    &accountusage.Window{UsedPercent: usedPercent, WindowMinutes: 300, ResetAt: resetAt.Unix()},
		CapturedAt: testNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func newTestReconciler(queries *fakeQueries, runtime *fakeRuntime, global int32) *Reconciler {
	reconciler := NewReconciler(queries, runtime, func(context.Context) int32 { return global }, nil)
	reconciler.now = func() time.Time { return testNow }
	return reconciler
}

// 全局阈值从 90 提到 100：90% 的账号恢复、100% 的账号维持暂停、无快照的账号跳过。
func TestReconcilerRewritesMarkersForNewThreshold(t *testing.T) {
	queries := &fakeQueries{reconcile: []sqlc.AdminListUsagePauseReconcileAccountsRow{
		{ID: 1, ChannelID: 7, UsageSnapshot: snapshotJSON(t, 90, testNow.Add(time.Hour))},
		{ID: 2, ChannelID: 7, UsageSnapshot: snapshotJSON(t, 100, testNow.Add(time.Hour))},
		{ID: 3, ChannelID: 8},
	}}
	runtime := &fakeRuntime{}

	result, err := newTestReconciler(queries, runtime, 100).ReconcileAll(context.Background())
	if err != nil {
		t.Fatalf("ReconcileAll: %v", err)
	}
	want := ReconcileResult{Scanned: 3, Paused: 1, Resumed: 1, Skipped: 1}
	if result != want {
		t.Fatalf("result = %+v, want %+v", result, want)
	}
	if queries.lastScope.ChannelID.Valid || queries.lastScope.AccountID.Valid {
		t.Fatalf("global reconcile must not narrow the scope, got %+v", queries.lastScope)
	}
	if len(runtime.calls) != 2 {
		t.Fatalf("expected one resume and one pause, got %+v", runtime.calls)
	}
	if runtime.calls[0].accountID != 1 || runtime.calls[0].paused {
		t.Fatalf("account 1 must be resumed, got %+v", runtime.calls[0])
	}
	if runtime.calls[1].accountID != 2 || !runtime.calls[1].paused || runtime.calls[1].durationMs != time.Hour.Milliseconds() {
		t.Fatalf("account 2 must stay paused until reset, got %+v", runtime.calls[1])
	}
}

// 账号/渠道范围通过查询参数收窄；账号自身的覆写在重算时同样生效。
func TestReconcilerScopesAndHonoursOverrides(t *testing.T) {
	queries := &fakeQueries{reconcile: []sqlc.AdminListUsagePauseReconcileAccountsRow{
		{
			ID: 5, ChannelID: 7,
			UsageSnapshot:              snapshotJSON(t, 85, testNow.Add(time.Hour)),
			UsagePauseThresholdPercent: int4(80),
		},
	}}
	runtime := &fakeRuntime{}
	reconciler := newTestReconciler(queries, runtime, 90)

	result, err := reconciler.ReconcileAccount(context.Background(), 5)
	if err != nil {
		t.Fatalf("ReconcileAccount: %v", err)
	}
	if !queries.lastScope.AccountID.Valid || queries.lastScope.AccountID.Int64 != 5 || queries.lastScope.ChannelID.Valid {
		t.Fatalf("account reconcile must scope by account only, got %+v", queries.lastScope)
	}
	if result.Paused != 1 || len(runtime.calls) != 1 || !runtime.calls[0].paused {
		t.Fatalf("account threshold 80 must pause an 85%% account, got %+v / %+v", result, runtime.calls)
	}

	if _, err := reconciler.ReconcileChannel(context.Background(), 7); err != nil {
		t.Fatalf("ReconcileChannel: %v", err)
	}
	if !queries.lastScope.ChannelID.Valid || queries.lastScope.ChannelID.Int64 != 7 || queries.lastScope.AccountID.Valid {
		t.Fatalf("channel reconcile must scope by channel only, got %+v", queries.lastScope)
	}
}

// 单个账号的 Redis 写失败只计入 Failed，不中断其余账号；列表读失败才整体返回错误。
func TestReconcilerCountsFailuresWithoutAborting(t *testing.T) {
	queries := &fakeQueries{reconcile: []sqlc.AdminListUsagePauseReconcileAccountsRow{
		{ID: 1, ChannelID: 7, UsageSnapshot: snapshotJSON(t, 95, testNow.Add(time.Hour))},
		{ID: 2, ChannelID: 7, UsageSnapshot: snapshotJSON(t, 95, testNow.Add(time.Hour))},
	}}
	runtime := &fakeRuntime{failFor: map[int64]error{1: errors.New("redis down")}}

	result, err := newTestReconciler(queries, runtime, 90).ReconcileAll(context.Background())
	if err != nil {
		t.Fatalf("ReconcileAll: %v", err)
	}
	if result.Failed != 1 || result.Paused != 1 || result.Scanned != 2 {
		t.Fatalf("result = %+v, want failed=1 paused=1 scanned=2", result)
	}

	queries.reconcileErr = errors.New("postgres down")
	if _, err := newTestReconciler(queries, runtime, 90).ReconcileAll(context.Background()); err == nil {
		t.Fatal("list failure must surface as error")
	}
}
