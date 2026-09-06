package quota

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/accountusage"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func usageSnapshotJSON(t *testing.T, primaryPercent, secondaryPercent float64, capturedAt time.Time) []byte {
	t.Helper()
	raw, err := json.Marshal(accountusage.Snapshot{
		Primary:    &accountusage.Window{UsedPercent: primaryPercent, WindowMinutes: 300, ResetAt: testNow.Unix() + 3600},
		Secondary:  &accountusage.Window{UsedPercent: secondaryPercent, WindowMinutes: 10080, ResetAt: testNow.Unix() + 86400},
		CapturedAt: capturedAt,
	})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// autoRow 构造一行自动用卡账号：默认 any 模式、5h 与 7d 阈值都是 100（两个窗口都参与）。
func autoRow(id int64, snapshot []byte, state *AutoResetState) sqlc.ListAutoResetCreditAccountsRow {
	row := sqlc.ListAutoResetCreditAccountsRow{
		ID: id, DisplayName: "acct", UsageSnapshot: snapshot, AutoResetCreditMode: AutoResetModeAny,
		AutoResetCredit5hThresholdPercent: pgtype.Int4{Int32: 100, Valid: true},
		AutoResetCredit7dThresholdPercent: pgtype.Int4{Int32: 100, Valid: true},
	}
	if state != nil {
		row.AutoResetCreditState, _ = json.Marshal(state)
	}
	return row
}

func newTestWorker(q *fakeQueries, upstream *fakeUpstream, observer *fakeObserver) *AutoResetWorker {
	worker := NewAutoResetWorker(q, newTestService(q, upstream, observer), nil, AutoResetOptions{})
	worker.now = func() time.Time { return testNow }
	return worker
}

func stateOf(t *testing.T, q *fakeQueries, id int64) *AutoResetState {
	t.Helper()
	state := ParseAutoResetState(q.states[id])
	if state == nil {
		t.Fatalf("no state persisted for account %d", id)
	}
	return state
}

// 快照新鲜且未触顶：不打上游，也不写状态。
func TestAutoResetSkipsFreshAccountsBelowThreshold(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	q.autoRows = []sqlc.ListAutoResetCreditAccountsRow{autoRow(1, usageSnapshotJSON(t, 50, 60, testNow.Add(-time.Minute)), nil)}
	upstream := &fakeUpstream{usage: fullUsage(50, 60, 2), credits: twoCredits()}

	processed, err := newTestWorker(q, upstream, &fakeObserver{}).RunOnce(context.Background())
	if err != nil || !processed {
		t.Fatalf("RunOnce = %v, %v", processed, err)
	}
	if len(upstream.consumed) != 0 || len(q.states) != 0 {
		t.Fatalf("fresh account below threshold must not touch upstream or state: consumed=%v states=%v", upstream.consumed, q.states)
	}
}

// 本地快照触顶 → 主动查用量确认 → 消费最早到期的卡 → 回读 → success。
func TestAutoResetConsumesEarliestCreditWhenThresholdReached(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	q.autoRows = []sqlc.ListAutoResetCreditAccountsRow{autoRow(1, usageSnapshotJSON(t, 100, 60, testNow.Add(-time.Minute)), nil)}
	after := fullUsage(0, 60, 1)
	upstream := &fakeUpstream{
		usage: fullUsage(100, 60, 2), credits: twoCredits(),
		consume: ConsumeResult{Code: "success", WindowsReset: 2}, afterConsume: &after,
	}
	observer := &fakeObserver{}

	if _, err := newTestWorker(q, upstream, observer).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(upstream.consumed) != 1 || upstream.consumed[0].creditID != "credit-early" {
		t.Fatalf("must consume the earliest expiring credit, got %+v", upstream.consumed)
	}
	if upstream.consumed[0].redeemID != redeemRequestID(1, shortHash("credit-early"), cycleHash(fullUsage(100, 60, 2))) {
		t.Fatalf("redeem id must be derived deterministically, got %q", upstream.consumed[0].redeemID)
	}
	state := stateOf(t, q, 1)
	if state.Status != AutoResetStatusSuccess || state.TriggerWindow != "5h" || state.AvailableCount != 1 {
		t.Fatalf("state = %+v", state)
	}
	if state.AttemptCreditHash != shortHash("credit-early") || state.AttemptCycleHash == "" {
		t.Fatalf("state must keep attempt fingerprints, got %+v", state)
	}
	// 两次观测：确认查询（100%）与消费后回读（0%）。
	if len(observer.observed) != 2 || observer.observed[1].facts.Primary.UsedPercent != 0 {
		t.Fatalf("observations = %+v", observer.observed)
	}
}

// 账号级阈值：7d 阈值 70 时 71% 的 7d 水位触发，5h 未触发；trigger_window 标 7d。
func TestAutoResetHonoursPerWindowThresholds(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	row := autoRow(1, usageSnapshotJSON(t, 10, 71, testNow.Add(-time.Minute)), nil)
	row.AutoResetCredit7dThresholdPercent = pgtype.Int4{Int32: 70, Valid: true}
	q.autoRows = []sqlc.ListAutoResetCreditAccountsRow{row}
	after := fullUsage(10, 0, 1)
	upstream := &fakeUpstream{usage: fullUsage(10, 71, 2), credits: twoCredits(), consume: ConsumeResult{Code: "success", WindowsReset: 2}, afterConsume: &after}

	if _, err := newTestWorker(q, upstream, &fakeObserver{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(upstream.consumed) != 1 {
		t.Fatalf("7d threshold 70 must trigger on 71%%, got %+v", upstream.consumed)
	}
	if state := stateOf(t, q, 1); state.TriggerWindow != "7d" || state.Status != AutoResetStatusSuccess {
		t.Fatalf("state = %+v", state)
	}
}

// 本地快照说触顶，但上游现查已回落（例如刚被手动重置）：不消费，状态记 available。
func TestAutoResetDoesNotConsumeWhenFreshUsageIsBelowThreshold(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	q.autoRows = []sqlc.ListAutoResetCreditAccountsRow{autoRow(1, usageSnapshotJSON(t, 100, 60, testNow.Add(-time.Minute)), nil)}
	upstream := &fakeUpstream{usage: fullUsage(5, 60, 2), credits: twoCredits()}

	if _, err := newTestWorker(q, upstream, &fakeObserver{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(upstream.consumed) != 0 {
		t.Fatalf("must not consume when fresh usage is below threshold, got %+v", upstream.consumed)
	}
	if state := stateOf(t, q, 1); state.Status != AutoResetStatusAvailable || state.AvailableCount != 2 || state.TriggerWindow != "" {
		t.Fatalf("state = %+v", state)
	}
}

// 无卡：只记 no_credit，不消费；快照陈旧时也会主动查一次刷新水位。
func TestAutoResetRecordsNoCreditAndRefreshesStaleSnapshots(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	q.autoRows = []sqlc.ListAutoResetCreditAccountsRow{autoRow(1, usageSnapshotJSON(t, 100, 60, testNow.Add(-time.Hour)), nil)}
	upstream := &fakeUpstream{usage: fullUsage(100, 60, 0), credits: ResetCredits{}}
	observer := &fakeObserver{}

	if _, err := newTestWorker(q, upstream, observer).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(observer.observed) != 1 {
		t.Fatalf("stale snapshot must trigger a usage refresh, got %d observations", len(observer.observed))
	}
	if len(upstream.consumed) != 0 {
		t.Fatalf("no credit must not consume, got %+v", upstream.consumed)
	}
	if state := stateOf(t, q, 1); state.Status != AutoResetStatusNoCredit || state.ErrorCode != AutoResetErrorNoCredit || state.TriggerWindow != "5h" {
		t.Fatalf("state = %+v", state)
	}
}

// 同周期重试必须复用原卡与同一 redeem id；原卡消失时拒绝换卡。
func TestAutoResetRetriesSameCreditWithinCycleAndRefusesToSwitch(t *testing.T) {
	usage := fullUsage(100, 60, 2)
	cycle := cycleHash(usage)
	previous := &AutoResetState{
		Status: AutoResetStatusFailed, TriggerWindow: "5h", AvailableCount: 2,
		AttemptCycleHash: cycle, AttemptCreditHash: shortHash("credit-late"),
	}

	q := newFakeQueries(openAIAccount(1))
	q.autoRows = []sqlc.ListAutoResetCreditAccountsRow{autoRow(1, usageSnapshotJSON(t, 100, 60, testNow.Add(-time.Minute)), previous)}
	after := fullUsage(0, 60, 1)
	upstream := &fakeUpstream{usage: usage, credits: twoCredits(), consume: ConsumeResult{Code: "success", WindowsReset: 2}, afterConsume: &after}

	if _, err := newTestWorker(q, upstream, &fakeObserver{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(upstream.consumed) != 1 || upstream.consumed[0].creditID != "credit-late" {
		t.Fatalf("retry must reuse the credit attempted earlier in the cycle, got %+v", upstream.consumed)
	}
	if upstream.consumed[0].redeemID != redeemRequestID(1, shortHash("credit-late"), cycle) {
		t.Fatalf("retry must reuse the same redeem id, got %q", upstream.consumed[0].redeemID)
	}

	// 原卡已不在列表：拒绝换卡，状态 failed/ORIGINAL_CREDIT_UNAVAILABLE。
	gone := twoCredits()
	gone.Credits = gone.Credits[1:] // 只剩 credit-early
	q2 := newFakeQueries(openAIAccount(1))
	q2.autoRows = []sqlc.ListAutoResetCreditAccountsRow{autoRow(1, usageSnapshotJSON(t, 100, 60, testNow.Add(-time.Minute)), previous)}
	upstream2 := &fakeUpstream{usage: fullUsage(100, 60, 1), credits: gone}
	if _, err := newTestWorker(q2, upstream2, &fakeObserver{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(upstream2.consumed) != 0 {
		t.Fatalf("must refuse to switch credits, got %+v", upstream2.consumed)
	}
	if state := stateOf(t, q2, 1); state.Status != AutoResetStatusFailed || state.ErrorCode != AutoResetErrorOriginalCreditGone {
		t.Fatalf("state = %+v", state)
	}
}

// 消费失败：状态 failed，保留尝试指纹供下一轮复用；查询失败同样记 failed。
func TestAutoResetRecordsFailuresWithAttemptFingerprints(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	q.autoRows = []sqlc.ListAutoResetCreditAccountsRow{autoRow(1, usageSnapshotJSON(t, 100, 60, testNow.Add(-time.Minute)), nil)}
	upstream := &fakeUpstream{usage: fullUsage(100, 60, 2), credits: twoCredits(), consumeErr: errors.New("upstream 500")}

	if _, err := newTestWorker(q, upstream, &fakeObserver{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	state := stateOf(t, q, 1)
	if state.Status != AutoResetStatusFailed || state.ErrorCode != AutoResetErrorConsumeFailed {
		t.Fatalf("state = %+v", state)
	}
	if state.AttemptCreditHash != shortHash("credit-early") || state.AttemptCycleHash != cycleHash(fullUsage(100, 60, 2)) {
		t.Fatalf("failed state must keep attempt fingerprints, got %+v", state)
	}

	q2 := newFakeQueries(openAIAccount(1))
	q2.autoRows = []sqlc.ListAutoResetCreditAccountsRow{autoRow(1, usageSnapshotJSON(t, 100, 60, testNow.Add(-time.Minute)), nil)}
	upstream2 := &fakeUpstream{usageErr: errors.New("usage 502")}
	if _, err := newTestWorker(q2, upstream2, &fakeObserver{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if state := stateOf(t, q2, 1); state.Status != AutoResetStatusFailed || state.ErrorCode != AutoResetErrorQueryFailed {
		t.Fatalf("state = %+v", state)
	}
}

// 水位回落后清掉挂着的触发窗口；周期未到不重复扫描。
func TestAutoResetClearsTriggerWhenUsageDropsAndRespectsInterval(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	previous := &AutoResetState{Status: AutoResetStatusNoCredit, TriggerWindow: "5h", ErrorCode: AutoResetErrorNoCredit}
	q.autoRows = []sqlc.ListAutoResetCreditAccountsRow{autoRow(1, usageSnapshotJSON(t, 10, 20, testNow.Add(-time.Minute)), previous)}
	upstream := &fakeUpstream{}
	worker := newTestWorker(q, upstream, &fakeObserver{})

	if _, err := worker.RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if state := stateOf(t, q, 1); state.TriggerWindow != "" || state.ErrorCode != "" || state.Status != AutoResetStatusNoCredit {
		t.Fatalf("trigger must be cleared once usage drops, got %+v", state)
	}

	processed, err := worker.RunOnce(context.Background())
	if err != nil || processed {
		t.Fatalf("second run within the interval must be a no-op, got %v %v", processed, err)
	}
}

func TestResolveThresholdsTreatsNullAsNotParticipating(t *testing.T) {
	limits := resolveThresholds("", pgtype.Int4{}, pgtype.Int4{Int32: 0, Valid: true})
	if limits.fiveHour != nil || limits.sevenDay != nil || limits.mode != AutoResetModeAny {
		t.Fatalf("limits = %+v", limits)
	}
	limits = resolveThresholds(AutoResetModeAll, pgtype.Int4{Int32: 90, Valid: true}, pgtype.Int4{Int32: 80, Valid: true})
	if limits.fiveHour == nil || *limits.fiveHour != 90 || limits.sevenDay == nil || *limits.sevenDay != 80 || limits.mode != AutoResetModeAll {
		t.Fatalf("limits = %+v", limits)
	}
}

// 只填 7d 90%、5h 留空：5h 打满不触发（几小时内自己恢复，不值一张周重置卡），7d 达到才用卡。
func TestAutoResetIgnoresWindowsWithoutThreshold(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	row := autoRow(1, usageSnapshotJSON(t, 100, 60, testNow.Add(-time.Minute)), nil)
	row.AutoResetCredit5hThresholdPercent = pgtype.Int4{}
	row.AutoResetCredit7dThresholdPercent = pgtype.Int4{Int32: 90, Valid: true}
	q.autoRows = []sqlc.ListAutoResetCreditAccountsRow{row}
	upstream := &fakeUpstream{usage: fullUsage(100, 60, 2), credits: twoCredits(), consume: ConsumeResult{Code: "success", WindowsReset: 2}}

	if _, err := newTestWorker(q, upstream, &fakeObserver{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(upstream.consumed) != 0 {
		t.Fatalf("5h exhaustion must not consume when only 7d participates, got %+v", upstream.consumed)
	}
	if len(q.states) != 0 {
		t.Fatalf("fresh snapshot below the participating threshold must not touch state, got %v", q.states)
	}

	// 7d 达到 90 → 触发，trigger_window 只标 7d。
	q2 := newFakeQueries(openAIAccount(1))
	row2 := row
	row2.UsageSnapshot = usageSnapshotJSON(t, 100, 91, testNow.Add(-time.Minute))
	q2.autoRows = []sqlc.ListAutoResetCreditAccountsRow{row2}
	after := fullUsage(0, 0, 1)
	upstream2 := &fakeUpstream{usage: fullUsage(100, 91, 2), credits: twoCredits(), consume: ConsumeResult{Code: "success", WindowsReset: 2}, afterConsume: &after}
	if _, err := newTestWorker(q2, upstream2, &fakeObserver{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(upstream2.consumed) != 1 {
		t.Fatalf("7d reaching its threshold must consume, got %+v", upstream2.consumed)
	}
	if state := stateOf(t, q2, 1); state.TriggerWindow != "7d" || state.Status != AutoResetStatusSuccess {
		t.Fatalf("state = %+v", state)
	}
}

// all 模式：两个窗口都参与时必须同时达到；只有 5h 达到不消费，两个都达到才消费。
func TestAutoResetAllModeRequiresEveryParticipatingWindow(t *testing.T) {
	q := newFakeQueries(openAIAccount(1))
	row := autoRow(1, usageSnapshotJSON(t, 100, 60, testNow.Add(-time.Minute)), nil)
	row.AutoResetCreditMode = AutoResetModeAll
	row.AutoResetCredit7dThresholdPercent = pgtype.Int4{Int32: 90, Valid: true}
	q.autoRows = []sqlc.ListAutoResetCreditAccountsRow{row}
	upstream := &fakeUpstream{usage: fullUsage(100, 60, 2), credits: twoCredits(), consume: ConsumeResult{Code: "success", WindowsReset: 2}}

	if _, err := newTestWorker(q, upstream, &fakeObserver{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(upstream.consumed) != 0 {
		t.Fatalf("all mode must not consume when only 5h reached, got %+v", upstream.consumed)
	}

	q2 := newFakeQueries(openAIAccount(1))
	row2 := row
	row2.UsageSnapshot = usageSnapshotJSON(t, 100, 95, testNow.Add(-time.Minute))
	q2.autoRows = []sqlc.ListAutoResetCreditAccountsRow{row2}
	after := fullUsage(0, 0, 1)
	upstream2 := &fakeUpstream{usage: fullUsage(100, 95, 2), credits: twoCredits(), consume: ConsumeResult{Code: "success", WindowsReset: 2}, afterConsume: &after}
	if _, err := newTestWorker(q2, upstream2, &fakeObserver{}).RunOnce(context.Background()); err != nil {
		t.Fatalf("RunOnce: %v", err)
	}
	if len(upstream2.consumed) != 1 {
		t.Fatalf("all mode must consume once both windows reached, got %+v", upstream2.consumed)
	}
	if state := stateOf(t, q2, 1); state.TriggerWindow != "5h+7d" || state.Status != AutoResetStatusSuccess {
		t.Fatalf("state = %+v", state)
	}
}
