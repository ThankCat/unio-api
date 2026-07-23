package breakerstore

import (
	"context"
	"testing"
	"time"
)

func seedControl(t *testing.T, s *Store, target ControlTarget, token, payload string) {
	t.Helper()
	code, _, err := s.PrepareControl(context.Background(), target, token, 0, 1, payload)
	if err != nil {
		t.Fatalf("prepare control: %v", err)
	}
	if code != ControlPrepared {
		t.Fatalf("prepare control want prepared, got %s", code)
	}
	if _, err := s.CommitControl(context.Background(), target, token, payload); err != nil {
		t.Fatalf("commit control: %v", err)
	}
}

// TestRuntimeControlLifecycle 验证通用 control 状态机：prepare→commit 推进 revision、幂等、冲突、abort、restore。
func TestRuntimeControlLifecycle(t *testing.T) {
	s, _, _ := newTestStore(t)
	target := s.SettingControl("gateway.routing_balance")

	// 0->1 prepare+commit。
	if code, _, err := s.PrepareControl(context.Background(), target, "tok1", 0, 1, `{"ttft_weight":0.35}`); err != nil || code != ControlPrepared {
		t.Fatalf("prepare 0->1: code=%s err=%v", code, err)
	}
	// 同 token 同 payload 幂等 prepare。
	if code, _, _ := s.PrepareControl(context.Background(), target, "tok1", 0, 1, `{"ttft_weight":0.35}`); code != ControlPrepared {
		t.Fatalf("idempotent prepare want prepared, got %s", code)
	}
	// 不同 token 撞 pending → conflict。
	if code, _, _ := s.PrepareControl(context.Background(), target, "tok-other", 0, 1, `{"ttft_weight":0.35}`); code != ControlPreparePendingConflict {
		t.Fatalf("other token during pending want conflict_pending, got %s", code)
	}
	if rev, err := s.CommitControl(context.Background(), target, "tok1", `{"ttft_weight":0.35}`); err != nil || rev != 1 {
		t.Fatalf("commit 0->1 want rev=1, got %d err=%v", rev, err)
	}
	snap, _ := s.ReadControl(context.Background(), target, 1)
	if snap.ActiveRevision != 1 || snap.SyncState != "active" {
		t.Fatalf("after commit want active rev=1, got %+v", snap)
	}

	// 1->2 prepare then abort → 回到 active=1。
	if code, _, _ := s.PrepareControl(context.Background(), target, "tok2", 1, 2, `{"ttft_weight":0.5}`); code != ControlPrepared {
		t.Fatalf("prepare 1->2 want prepared, got %s", code)
	}
	snap, _ = s.ReadControl(context.Background(), target, 1)
	if snap.PendingPayload != `{"ttft_weight":0.5}` {
		t.Fatalf("pending payload must be readable for recovery, got %q", snap.PendingPayload)
	}
	if err := s.AbortControl(context.Background(), target, "tok2", `{"ttft_weight":0.5}`); err != nil {
		t.Fatalf("abort: %v", err)
	}
	snap, _ = s.ReadControl(context.Background(), target, 1)
	if snap.ActiveRevision != 1 || snap.PendingRevision != 0 {
		t.Fatalf("after abort want active=1 pending=0, got %+v", snap)
	}

	// stale expected revision 读出 stale。
	snap, _ = s.ReadControl(context.Background(), target, 2)
	if snap.SyncState != "stale" {
		t.Fatalf("read with higher expected want stale, got %s", snap.SyncState)
	}
}

// TestControlRecoveryFromMissingOp 验证 durable reconciler 可在 Redis op tombstone 丢失后，
// 分别按 PostgreSQL 未提交/已提交事实安全 Abort 或 Commit，而不覆盖冲突 control。
func TestControlRecoveryFromMissingOp(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	target := s.SettingControl("gateway.circuit_breaker")
	oldPayload := `{"enabled":true,"failure_rate_threshold":0.5}`
	newPayload := `{"enabled":true,"failure_rate_threshold":0.6}`
	seedControl(t, s, target, "seed-recovery", oldPayload)

	if code, _, err := s.PrepareControl(ctx, target, "recover-abort", 1, 2, newPayload); err != nil || code != ControlPrepared {
		t.Fatalf("prepare abort recovery: code=%s err=%v", code, err)
	}
	if err := s.client.Del(ctx, s.opKeyFor(target, "recover-abort")).Err(); err != nil {
		t.Fatalf("delete redis op: %v", err)
	}
	if err := s.RecoverAbortedControl(ctx, target, "recover-abort", 1, 2, HashPayload(newPayload), oldPayload); err != nil {
		t.Fatalf("recover abort: %v", err)
	}
	snap, err := s.ReadControl(ctx, target, 1)
	if err != nil || snap.ActiveRevision != 1 || snap.PendingRevision != 0 || snap.ActivePayload != oldPayload {
		t.Fatalf("recover abort must preserve old active and clear pending: %+v err=%v", snap, err)
	}

	if code, _, err := s.PrepareControl(ctx, target, "recover-commit", 1, 2, newPayload); err != nil || code != ControlPrepared {
		t.Fatalf("prepare commit recovery: code=%s err=%v", code, err)
	}
	if err := s.client.Del(ctx, s.opKeyFor(target, "recover-commit")).Err(); err != nil {
		t.Fatalf("delete redis op: %v", err)
	}
	if rev, err := s.RecoverCommittedControl(ctx, target, "recover-commit", 1, 2, newPayload); err != nil || rev != 2 {
		t.Fatalf("recover commit want revision 2: rev=%d err=%v", rev, err)
	}
	snap, err = s.ReadControl(ctx, target, 2)
	if err != nil || snap.ActiveRevision != 2 || snap.PendingRevision != 0 || snap.ActivePayload != newPayload {
		t.Fatalf("recover commit must activate new payload: %+v err=%v", snap, err)
	}
}

// TestControlRecoveryRestoresMissingControl 验证 control/op 同时丢失时仍能按 PostgreSQL 当前事实恢复。
func TestControlRecoveryRestoresMissingControl(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	target := s.ChannelAdmissionControl(8181)
	oldPayload := `{"rpm":10,"tpm":null,"rpd":null,"concurrency":null}`
	newPayload := `{"rpm":20,"tpm":null,"rpd":null,"concurrency":null}`

	if err := s.RecoverAbortedControl(ctx, target, "missing-abort", 7, 8, HashPayload(newPayload), oldPayload); err != nil {
		t.Fatalf("recover missing abort: %v", err)
	}
	snap, _ := s.ReadControl(ctx, target, 7)
	if snap.ActiveRevision != 7 || snap.ActivePayload != oldPayload {
		t.Fatalf("missing abort restore want old active@7, got %+v", snap)
	}

	if err := s.client.Del(ctx, target.controlKey, s.opKeyFor(target, "missing-abort")).Err(); err != nil {
		t.Fatalf("delete restored keys: %v", err)
	}
	if rev, err := s.RecoverCommittedControl(ctx, target, "missing-commit", 7, 8, newPayload); err != nil || rev != 8 {
		t.Fatalf("recover missing commit: rev=%d err=%v", rev, err)
	}
	snap, _ = s.ReadControl(ctx, target, 8)
	if snap.ActiveRevision != 8 || snap.ActivePayload != newPayload {
		t.Fatalf("missing commit restore want new active@8, got %+v", snap)
	}
}

// TestControlRestoreMissing 验证 recovery-only restore 只在缺失时安装、已存在不覆盖。
func TestControlRestoreMissing(t *testing.T) {
	s, _, _ := newTestStore(t)
	target := s.ChannelAdmissionControl(4242)
	installed, err := s.RestoreMissingControl(context.Background(), target, 7, `{"rpm":10}`)
	if err != nil || !installed {
		t.Fatalf("restore missing want installed, got %v err=%v", installed, err)
	}
	snap, _ := s.ReadControl(context.Background(), target, 7)
	if snap.ActiveRevision != 7 {
		t.Fatalf("restore want active rev=7, got %+v", snap)
	}
	// 已存在不覆盖。
	installed, _ = s.RestoreMissingControl(context.Background(), target, 9, `{"rpm":99}`)
	if installed {
		t.Fatalf("restore must not overwrite existing control")
	}
}

// TestStateIntegrityBootstrapAndRotate 验证完整性 marker bootstrap 与 epoch 轮换。
func TestStateIntegrityBootstrapAndRotate(t *testing.T) {
	s, _, _ := newTestStore(t)
	ok, err := s.BootstrapStateEpoch(context.Background(), "epoch-A", 1, HashPayload("bootstrap"))
	if err != nil || !ok {
		t.Fatalf("bootstrap want ok, got %v err=%v", ok, err)
	}
	snap, _ := s.StateIntegrity(context.Background())
	if !snap.Ready("epoch-A", 1) {
		t.Fatalf("want ready epoch-A rev1, got %+v", snap)
	}
	// 再次 bootstrap（marker 已存在）→ no-op false。
	if ok, _ := s.BootstrapStateEpoch(context.Background(), "epoch-B", 2, HashPayload("x")); ok {
		t.Fatalf("bootstrap on existing marker must be no-op")
	}
	// 轮换 A(1) -> B(2)。
	code, err := s.PrepareStateEpoch(context.Background(), "epoch-A", 1, "epoch-B", 2, HashPayload("rotate"))
	if err != nil || code != "prepared" {
		t.Fatalf("prepare rotate want prepared, got %s err=%v", code, err)
	}
	committed, err := s.CommitStateEpoch(context.Background(), "epoch-B", 2)
	if err != nil || !committed {
		t.Fatalf("commit rotate want committed, got %v err=%v", committed, err)
	}
	snap, _ = s.StateIntegrity(context.Background())
	if !snap.Ready("epoch-B", 2) {
		t.Fatalf("after rotate want ready epoch-B rev2, got %+v", snap)
	}
}

func seedAdmissionEnv(t *testing.T, s *Store) (epoch string, epochRev int64) {
	return seedAdmissionEnvWithControls(
		t, s,
		`{"rpm":0,"tpm":0,"rpd":0}`,
		`{"key_limit":0,"channel_limit":0}`,
		testConfig(),
	)
}

func seedAdmissionEnvWithControls(t *testing.T, s *Store, ratePayload, concurrencyPayload string, cfg Config) (epoch string, epochRev int64) {
	t.Helper()
	if ok, err := s.BootstrapStateEpoch(context.Background(), "epoch-ra", 1, HashPayload("bootstrap")); err != nil || !ok {
		t.Fatalf("bootstrap epoch: %v", err)
	}
	seedControl(t, s, s.RouteRateLimitControl(), "seed-route-rate", ratePayload)
	ensureTestControlAtRevision(t, s, s.ChannelRateLimitControl(), testChannelRateRevision, `{"rpm":701,"tpm":702,"rpd":703}`)
	seedControl(t, s, s.GlobalConcurrencyControl(), "seed-conc", concurrencyPayload)
	seedControl(t, s, s.SettingControl("gateway.circuit_breaker"), "seed-breaker", testCircuitBreakerPayload(cfg))
	return "epoch-ra", 1
}

func raInput(id string, route, user int64, epoch string, epochRev int64) RequestAdmissionInput {
	return RequestAdmissionInput{
		RequestAdmissionID: id, Fingerprint: id + "-fp", RouteID: route, UserID: user,
		IntegrityEpoch: epoch, IntegrityRevision: epochRev,
		RouteRateRevision: testRouteRateRevision, GlobalConcurrencyRevision: 1,
	}
}

// TestRequestAdmissionAcquireAndRPMLimit 验证入口准入 allowed 与 RPM 超限 429。
func TestRequestAdmissionAcquireAndRPMLimit(t *testing.T) {
	s, _, _ := newTestStore(t)
	epoch, rev := seedAdmissionEnvWithControls(
		t, s, `{"rpm":2,"tpm":0,"rpd":0}`, `{"key_limit":0,"channel_limit":0}`, testConfig(),
	)

	// Redis active route-default RPM=2，前两次 allowed，第三次 limited(rpm)。
	for i := 0; i < 2; i++ {
		r, err := s.AcquireRequestAdmission(context.Background(), raInput(
			"ra"+string(rune('a'+i)), 1, 1, epoch, rev))
		if err != nil || r.Outcome != RequestAllowed {
			t.Fatalf("acquire %d want allowed, got %s/%s err=%v", i, r.Outcome, r.LimitedDimension, err)
		}
	}
	r, err := s.AcquireRequestAdmission(context.Background(), raInput("ra-third", 1, 1, epoch, rev))
	if err != nil {
		t.Fatalf("acquire third err: %v", err)
	}
	if r.Outcome != RequestLimited || r.LimitedDimension != "rpm" {
		t.Fatalf("third want limited/rpm, got %s/%s", r.Outcome, r.LimitedDimension)
	}
}

// TestRequestAdmissionStaleEpochFailClosed 验证 epoch 不匹配 fail-closed。
func TestRequestAdmissionStaleEpochFailClosed(t *testing.T) {
	s, _, _ := newTestStore(t)
	_, rev := seedAdmissionEnv(t, s)
	r, err := s.AcquireRequestAdmission(context.Background(), raInput("ra-stale", 2, 2, "wrong-epoch", rev))
	if err != nil {
		t.Fatalf("acquire err: %v", err)
	}
	if r.Outcome != RequestStaleEpoch {
		t.Fatalf("want stale_integrity_epoch, got %s", r.Outcome)
	}
}

// TestRequestAdmissionConcurrencyAndFinish 验证 route-user 并发上限与 Finish 释放。
func TestRequestAdmissionConcurrencyAndFinish(t *testing.T) {
	s, _, _ := newTestStore(t)
	epoch, rev := seedAdmissionEnvWithControls(
		t, s, `{"rpm":0,"tpm":0,"rpd":0}`, `{"key_limit":1,"channel_limit":0}`, testConfig(),
	)

	r1, err := s.AcquireRequestAdmission(context.Background(), raInput("rc1", 3, 3, epoch, rev))
	if err != nil || r1.Outcome != RequestAllowed {
		t.Fatalf("rc1 want allowed, got %s err=%v", r1.Outcome, err)
	}
	r2, _ := s.AcquireRequestAdmission(context.Background(), raInput("rc2", 3, 3, epoch, rev))
	if r2.Outcome != RequestLimited || r2.LimitedDimension != "concurrency" {
		t.Fatalf("rc2 want limited/concurrency, got %s/%s", r2.Outcome, r2.LimitedDimension)
	}
	// Finish rc1 释放并发。
	if outcome, err := s.FinishRequestAdmission(context.Background(), "rc1", 3, 3, -1, epoch, rev); err != nil || outcome != RequestLifecycleFinished {
		t.Fatalf("finish rc1: outcome=%s err=%v", outcome, err)
	}
	r3, _ := s.AcquireRequestAdmission(context.Background(), raInput("rc3", 3, 3, epoch, rev))
	if r3.Outcome != RequestAllowed {
		t.Fatalf("rc3 after finish want allowed, got %s/%s", r3.Outcome, r3.LimitedDimension)
	}
}

func TestRequestAdmissionLifecycleEpochMismatchLeavesResourcesUntouched(t *testing.T) {
	s, _, _ := newTestStore(t)
	epoch, revision := seedAdmissionEnvWithControls(
		t, s, `{"rpm":0,"tpm":100,"rpd":0}`, `{"key_limit":0,"channel_limit":0}`, testConfig(),
	)
	const requestID = "request-epoch-fence"
	const routeID int64 = 88
	const userID int64 = 89
	if result, err := s.AcquireRequestAdmission(
		context.Background(),
		raInput(requestID, routeID, userID, epoch, revision),
	); err != nil || result.Outcome != RequestAllowed {
		t.Fatalf("acquire request token: result=%+v err=%v", result, err)
	}
	if result, err := s.ReserveRequestTokens(context.Background(), requestID, routeID, userID, 10,
		epoch, revision); err != nil || result != ReserveReserved {
		t.Fatalf("reserve request tokens: result=%s err=%v", result, err)
	}

	tokenKey := s.keys.admissionRequest(requestID)
	concKey := s.keys.requestConcurrency(routeID, userID)
	tpmKey, err := s.client.HGet(context.Background(), tokenKey, "reserved_tpm_bucket").Result()
	if err != nil {
		t.Fatalf("read reserved TPM bucket: %v", err)
	}
	leaseBefore, _ := s.client.HGet(context.Background(), tokenKey, "lease_until_ms").Result()
	concBefore, _ := s.client.ZScore(context.Background(), concKey, requestID).Result()
	tpmBefore, _ := s.client.Get(context.Background(), tpmKey).Result()

	if outcome, renewErr := s.RenewRequestAdmission(
		context.Background(), requestID, routeID, userID, "wrong-epoch", revision,
	); renewErr != nil || outcome != RequestLifecycleStaleEpoch {
		t.Fatalf("stale renew: outcome=%s err=%v", outcome, renewErr)
	}
	if outcome, finishErr := s.FinishRequestAdmission(
		context.Background(), requestID, routeID, userID, 7, "wrong-epoch", revision,
	); finishErr != nil || outcome != RequestLifecycleStaleEpoch {
		t.Fatalf("stale finish: outcome=%s err=%v", outcome, finishErr)
	}

	assertUnchanged := func() {
		t.Helper()
		leaseAfter, _ := s.client.HGet(context.Background(), tokenKey, "lease_until_ms").Result()
		status, _ := s.client.HGet(context.Background(), tokenKey, "status").Result()
		concAfter, _ := s.client.ZScore(context.Background(), concKey, requestID).Result()
		tpmAfter, _ := s.client.Get(context.Background(), tpmKey).Result()
		if leaseAfter != leaseBefore || status != "active" || concAfter != concBefore || tpmAfter != tpmBefore {
			t.Fatalf("epoch rejection mutated resources: lease=%s/%s status=%s conc=%v/%v tpm=%s/%s",
				leaseBefore, leaseAfter, status, concBefore, concAfter, tpmBefore, tpmAfter)
		}
	}
	assertUnchanged()

	// Even with marker and PostgreSQL expected values aligned, a token from another epoch cannot mutate.
	if err := s.client.HSet(context.Background(), tokenKey, "runtime_integrity_epoch", "token-wrong-epoch").Err(); err != nil {
		t.Fatalf("corrupt token epoch: %v", err)
	}
	if outcome, finishErr := s.FinishRequestAdmission(
		context.Background(), requestID, routeID, userID, 7, epoch, revision,
	); finishErr != nil || outcome != RequestLifecycleStaleEpoch {
		t.Fatalf("token epoch mismatch finish: outcome=%s err=%v", outcome, finishErr)
	}
	assertUnchanged()

	if err := s.client.HSet(context.Background(), tokenKey, "runtime_integrity_epoch", epoch).Err(); err != nil {
		t.Fatalf("restore token epoch: %v", err)
	}
	if outcome, finishErr := s.FinishRequestAdmission(
		context.Background(), requestID, routeID, userID, 7, epoch, revision,
	); finishErr != nil || outcome != RequestLifecycleFinished {
		t.Fatalf("valid finish: outcome=%s err=%v", outcome, finishErr)
	}
}

// TestChannelAdmissionEnforced 验证 AcquireAttempt 接入 Channel 四维限额：
// RPM 超限拒绝零 permit；Abort 归还 RPM；Finish 保留 RPM。
func TestChannelAdmissionEnforced(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	// 建立 channel admission control（revision=1）。
	seedAttemptControls(t, s, cfg, 70, `{"rpm":2,"rpd":0,"tpm":0,"concurrency":0}`)

	acq := func(id string) AttemptAdmission {
		adm, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
			PermitID: id, AdmissionFingerprint: id + "-fp", RequestAdmissionID: "req",
			EndpointID: 700, ChannelID: 70, EndpointBaseURLRevision: 1, EndpointStatusRevision: 1,
			ChannelConfigRevision: 1, ModelID: 100, UpstreamOperation: OpChatCompletions, RequestMode: ModeNonStream,
			ChannelAdmissionRevision: 1,
			EstimatedInputTokens:     10,
		}))
		if err != nil {
			t.Fatalf("acquire %s: %v", id, err)
		}
		return adm
	}

	a1 := acq("ca1")
	if a1.Mode != AdmissionPermit {
		t.Fatalf("ca1 want permit, got %s/%s", a1.Mode, a1.Reason)
	}
	a2 := acq("ca2")
	if a2.Mode != AdmissionPermit {
		t.Fatalf("ca2 want permit, got %s/%s", a2.Mode, a2.Reason)
	}
	// 第三次 RPM 超限。
	a3 := acq("ca3")
	if a3.Mode != AdmissionDenied || a3.Reason != ReasonRateLimited {
		t.Fatalf("ca3 want denied/rate_limited, got %s/%s", a3.Mode, a3.Reason)
	}

	// Abort ca1 归还 RPM → 再 acquire 可入（used 回到 1，limit 2）。
	if err := s.Abort(context.Background(), *a1.Permit); err != nil {
		t.Fatalf("abort ca1: %v", err)
	}
	a4 := acq("ca4")
	if a4.Mode != AdmissionPermit {
		t.Fatalf("ca4 after abort want permit, got %s/%s", a4.Mode, a4.Reason)
	}

	// Finish ca2（真实 transport）保留 RPM → 现在 used=2（ca4+ca2 保留），再 acquire 超限。
	if _, err := s.Finish(context.Background(), *a2.Permit, FinishOutcome{
		EndpointOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleSuccess,
	}); err != nil {
		t.Fatalf("finish ca2: %v", err)
	}
	a5 := acq("ca5")
	if a5.Mode != AdmissionDenied || a5.Reason != ReasonRateLimited {
		t.Fatalf("ca5 want denied/rate_limited (RPM retained after finish), got %s/%s", a5.Mode, a5.Reason)
	}
}

// TestChannelAdmissionStaleRevision 验证 admission control revision 落后时 fail-closed。
func TestChannelAdmissionStaleRevision(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	seedAttemptControls(t, s, cfg, 71, `{"rpm":0,"rpd":0,"tpm":0,"concurrency":0}`)
	in := withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "cs1", AdmissionFingerprint: "cs1-fp", RequestAdmissionID: "req",
		EndpointID: 710, ChannelID: 71, EndpointBaseURLRevision: 1, EndpointStatusRevision: 1,
		ChannelConfigRevision: 1, ModelID: 100, UpstreamOperation: OpChatCompletions, RequestMode: ModeNonStream,
	})
	in.ChannelAdmissionRevision = 2 // 期望 2，实际 active=1
	adm, err := acquireAttempt(t, s, in)
	if err != nil {
		t.Fatalf("acquire: %v", err)
	}
	if adm.Mode != AdmissionDenied || adm.Reason != ReasonStaleConfigRevision {
		t.Fatalf("want denied/stale_config_revision, got %s/%s", adm.Mode, adm.Reason)
	}
}

// TestReserveRequestTokensIdempotent 验证 TPM 预占幂等与超限。
func TestReserveRequestTokensIdempotent(t *testing.T) {
	s, _, _ := newTestStore(t)
	epoch, rev := seedAdmissionEnvWithControls(
		t, s, `{"rpm":0,"tpm":100,"rpd":0}`, `{"key_limit":0,"channel_limit":0}`, testConfig(),
	)

	if r, err := s.AcquireRequestAdmission(context.Background(), raInput("rt1", 4, 4, epoch, rev)); err != nil || r.Outcome != RequestAllowed {
		t.Fatalf("acquire rt1: %s err=%v", r.Outcome, err)
	}
	// Redis active route-default TPM=100，estimate=40 → reserved；同 estimate 重试幂等 reserved。
	if res, err := s.ReserveRequestTokens(context.Background(), "rt1", 4, 4, 40,
		epoch, rev); err != nil || res != ReserveReserved {
		t.Fatalf("reserve want reserved, got %s err=%v", res, err)
	}
	if res, _ := s.ReserveRequestTokens(context.Background(), "rt1", 4, 4, 40,
		epoch, rev); res != ReserveReserved {
		t.Fatalf("idempotent reserve want reserved, got %s", res)
	}
	// 异 estimate → conflict。
	if res, _ := s.ReserveRequestTokens(context.Background(), "rt1", 4, 4, 41,
		epoch, rev); res != ReserveConflict {
		t.Fatalf("different estimate want conflict, got %s", res)
	}

	// 另一请求 estimate 超 TPM → limited。
	if r, _ := s.AcquireRequestAdmission(context.Background(), raInput("rt2", 4, 4, epoch, rev)); r.Outcome != RequestAllowed {
		t.Fatalf("acquire rt2: %s", r.Outcome)
	}
	if res, _ := s.ReserveRequestTokens(context.Background(), "rt2", 4, 4, 1000,
		epoch, rev); res != ReserveLimited {
		t.Fatalf("over-tpm reserve want limited, got %s", res)
	}
}

func TestReserveRequestTokensEpochFenceLeavesTPMUnchanged(t *testing.T) {
	s, _, _ := newTestStore(t)
	epoch, revision := seedAdmissionEnvWithControls(
		t, s, `{"rpm":0,"tpm":100,"rpd":0}`, `{"key_limit":0,"channel_limit":0}`, testConfig(),
	)
	const requestID = "reserve-epoch-fence"
	const routeID int64 = 41
	const userID int64 = 42
	if result, err := s.AcquireRequestAdmission(context.Background(), raInput(requestID, routeID, userID, epoch, revision)); err != nil || result.Outcome != RequestAllowed {
		t.Fatalf("acquire: result=%+v err=%v", result, err)
	}
	tokenKey := s.keys.admissionRequest(requestID)
	tpmKey := s.keys.requestTPMBucket(routeID, userID, minuteBucket(time.Now()))

	if result, err := s.ReserveRequestTokens(context.Background(), requestID, routeID, userID, 10, "wrong-epoch", revision); err != nil || result != ReserveStaleEpoch {
		t.Fatalf("marker mismatch reserve: result=%s err=%v", result, err)
	}
	if exists, _ := s.client.Exists(context.Background(), tpmKey).Result(); exists != 0 {
		t.Fatal("marker mismatch must not create a TPM bucket")
	}
	if state, _ := s.client.HGet(context.Background(), tokenKey, "reserve_state").Result(); state != "none" {
		t.Fatalf("marker mismatch mutated reserve state to %q", state)
	}

	if err := s.client.HSet(context.Background(), tokenKey, "runtime_integrity_epoch", "token-wrong-epoch").Err(); err != nil {
		t.Fatal(err)
	}
	if result, err := s.ReserveRequestTokens(context.Background(), requestID, routeID, userID, 10, epoch, revision); err != nil || result != ReserveStaleEpoch {
		t.Fatalf("token mismatch reserve: result=%s err=%v", result, err)
	}
	if exists, _ := s.client.Exists(context.Background(), tpmKey).Result(); exists != 0 {
		t.Fatal("token mismatch must not create a TPM bucket")
	}
}
