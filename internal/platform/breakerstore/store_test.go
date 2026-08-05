package breakerstore

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// newTestStore 连接本地 Redis（REDIS_ADDR，缺失即 skip），并使用唯一 namespace 隔离本测试数据。
func newTestStore(t *testing.T) (*Store, *redis.Client, string) {
	t.Helper()
	addr := os.Getenv("REDIS_ADDR")
	if addr == "" {
		t.Skip("REDIS_ADDR is not set")
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ping(ctx).Err(); err != nil {
		t.Skipf("redis ping failed: %v", err)
	}
	ns := fmt.Sprintf("unio-breakertest:%d", time.Now().UnixNano())
	store := NewStore(client, ns)
	identity, err := store.readRedisServerIdentity(ctx)
	if err != nil {
		_ = client.Close()
		t.Skipf("redis server identity unavailable: %v", err)
	}
	if err := client.Set(ctx, store.keys.runtimeReconciliationProof(), identity.runID, 0).Err(); err != nil {
		_ = client.Close()
		t.Skipf("seed test reconciliation proof: %v", err)
	}
	t.Cleanup(func() {
		// 清理本 namespace 的所有 key。
		iter := client.Scan(context.Background(), 0, ns+":*", 0).Iterator()
		for iter.Next(context.Background()) {
			_ = client.Del(context.Background(), iter.Val()).Err()
		}
		_ = client.Close()
	})
	return store, client, ns
}

func testConfig() Config {
	c := DefaultConfig()
	c.MinRequests = 4
	c.OpenDurationsMs = []int64{60, 120}
	c.AttemptPermitTTLMs = 30000
	c.AttemptRenewMs = 10000
	c.AttemptTerminalTTLMs = 300000
	return c
}

func testCircuitBreakerPayload(cfg Config) string {
	openDurations := make([]string, 0, len(cfg.OpenDurationsMs))
	for _, duration := range cfg.OpenDurationsMs {
		openDurations = append(openDurations, fmt.Sprintf("%d", duration))
	}
	return fmt.Sprintf(
		`{"enabled":%t,"window_ms":%d,"min_requests":%d,"failure_ratio":%g,`+
			`"consecutive_failures":%d,"consecutive_window_ms":%d,"half_open_successes":%d,`+
			`"attempt_permit_ttl_ms":%d,"attempt_permit_renew_interval_ms":%d,"attempt_permit_terminal_ttl_ms":%d,`+
			`"origin_revision_operation_ttl_ms":86400000,"status_revision_operation_ttl_ms":86400000,`+
			`"open_durations_ms":[%s],`+
			`"provider_ambiguous_distinct_channels":%d,"provider_ambiguous_distinct_models":%d}`,
		cfg.Enabled, cfg.WindowMs, cfg.MinRequests, cfg.FailureRatio,
		cfg.ConsecutiveFailures, cfg.ConsecutiveWindowMs, cfg.HalfOpenSuccesses,
		cfg.AttemptPermitTTLMs, cfg.AttemptRenewMs, cfg.AttemptTerminalTTLMs,
		strings.Join(openDurations, ","), cfg.ProviderAmbiguousDistinctChannels, cfg.ProviderAmbiguousDistinctModels,
	)
}

const (
	testAttemptIntegrityEpoch    = "test-attempt-epoch"
	testAttemptIntegrityRevision = int64(1)
	testRouteRateRevision        = int64(1)
)

const (
	// testRoutingBalancePayload 是 objective_v1 五项评分的 canonical 控制载荷（§7/§14.6 默认值）。
	testRoutingBalancePayload = `{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,` +
		`"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":1800000,` +
		`"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,` +
		`"error_penalty_points_per_percent":2.5}`
	// testRoutingBalancePayloadCustom 使用非默认权重与惩罚参数，验证载荷被原样透出而非硬编码。
	testRoutingBalancePayloadCustom = `{"cost_weight_pct":40,"concurrency_weight_pct":10,"ttft_weight_pct":20,` +
		`"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":900000,` +
		`"ttft_penalty_unit_ms":500,"ttft_penalty_points_per_unit":1.5,"error_window_ms":600000,` +
		`"error_penalty_points_per_percent":4}`
)

func ensureTestControl(t *testing.T, s *Store, target ControlTarget, payload string) {
	ensureTestControlAtRevision(t, s, target, 1, payload)
}

func ensureTestControlAtRevision(t *testing.T, s *Store, target ControlTarget, revision int64, payload string) {
	t.Helper()
	if _, err := s.RestoreMissingControl(context.Background(), target, revision, payload); err != nil {
		t.Fatalf("restore test runtime control: %v", err)
	}
	snapshot, err := s.ReadControl(context.Background(), target, revision)
	if err != nil || snapshot.SyncState != "active" || snapshot.ActivePayload != payload {
		t.Fatalf("test runtime control mismatch: snapshot=%+v err=%v", snapshot, err)
	}
}

func seedAttemptControls(t *testing.T, s *Store, cfg Config, channelID int64, channelPayload string) {
	seedAttemptControlsWithRoutingBalance(t, s, cfg, channelID, channelPayload, testRoutingBalancePayload)
}

func seedAttemptControlsWithRoutingBalance(
	t *testing.T,
	s *Store,
	cfg Config,
	channelID int64,
	channelPayload string,
	routingBalancePayload string,
) {
	t.Helper()
	seedAttemptIntegrity(t, s)
	ensureTestControlAtRevision(t, s, s.RouteRateLimitControl(), testRouteRateRevision, `{"rpm":97,"rpd":970}`)
	ensureTestControl(t, s, s.GlobalConcurrencyControl(), `{"key_limit":0,"channel_limit":0}`)
	ensureTestControl(t, s, s.SettingControl("gateway.circuit_breaker"), testCircuitBreakerPayload(cfg))
	ensureTestControl(t, s, s.SettingControl("gateway.routing_balance"), routingBalancePayload)
	ensureTestControl(t, s, s.ChannelCapacityControl(channelID), channelPayload)
}

func seedAttemptIntegrity(t *testing.T, s *Store) {
	t.Helper()
	if _, err := s.BootstrapStateEpoch(
		context.Background(),
		testAttemptIntegrityEpoch,
		testAttemptIntegrityRevision,
		HashPayload("test-attempt-bootstrap"),
	); err != nil {
		t.Fatalf("bootstrap attempt integrity: %v", err)
	}
	snapshot, err := s.StateIntegrity(context.Background())
	if err != nil || !snapshot.Ready(testAttemptIntegrityEpoch, testAttemptIntegrityRevision) {
		t.Fatalf("attempt integrity marker is not ready: snapshot=%+v err=%v", snapshot, err)
	}
}

// seedActiveRequestAdmission 造一个 active request token 供 attempt acquire 绑定。
// 它不再冻结任何 TPM 状态：attempt 准入只要求 token active 且属于同一 integrity epoch。
func seedActiveRequestAdmission(t *testing.T, s *Store, in AcquireAttemptInput) {
	t.Helper()
	if err := s.client.HSet(
		context.Background(),
		s.keys.admissionRequest(in.RequestAdmissionID),
		"status", "active",
		"runtime_integrity_epoch", in.IntegrityEpoch,
		"runtime_integrity_revision", in.IntegrityRevision,
		"route_id", in.RouteID,
	).Err(); err != nil {
		t.Fatalf("seed reserved request admission: %v", err)
	}
}

func acquireAttempt(t *testing.T, s *Store, in AcquireAttemptInput) (AttemptAdmission, error) {
	t.Helper()
	seedActiveRequestAdmission(t, s, in)
	return s.AcquireAttempt(context.Background(), in)
}

func withAttemptControlRevisions(in AcquireAttemptInput) AcquireAttemptInput {
	in.IntegrityEpoch = testAttemptIntegrityEpoch
	in.IntegrityRevision = testAttemptIntegrityRevision
	in.GlobalConcurrencyRevision = 1
	in.CircuitBreakerRevision = 1
	in.ChannelCapacityRevision = 1
	return in
}

func acquire(t *testing.T, s *Store, cfg Config, permitID string, ch, ep int64) AttemptAdmission {
	t.Helper()
	seedAttemptControls(t, s, cfg, ch, `{"concurrency":null}`)
	adm, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
		PermitID:               permitID,
		AdmissionFingerprint:   permitID + "-fp",
		RequestAdmissionID:     "req-1",
		ProviderID:             ep,
		ChannelID:              ch,
		OriginRevision:         1,
		ProviderStatusRevision: 1,
		ChannelConfigRevision:  1,
		ModelID:                100,
		UpstreamEndpoint:       EndpointChatCompletions,
		RequestMode:            ModeNonStream,
	}))
	if err != nil {
		t.Fatalf("acquire %s: %v (cause: %v)", permitID, err, errors.Unwrap(err))
	}
	return adm
}

func finish(t *testing.T, s *Store, _ Config, permit *AttemptPermit, ep, ch Outcome) FinishResult {
	t.Helper()
	res, err := s.Finish(context.Background(), *permit, FinishOutcome{
		ProviderOutcome: ep, ChannelOutcome: ch, RequestWriteState: RequestWriteCompleted,
	})
	if err != nil {
		t.Fatalf("finish %s: %v", permit.PermitID, err)
	}
	return res
}

func dumpTestNamespace(t *testing.T, client *redis.Client, namespace string) map[string]string {
	t.Helper()
	ctx := context.Background()
	keys, err := client.Keys(ctx, namespace+":*").Result()
	if err != nil {
		t.Fatalf("list test namespace: %v", err)
	}
	dump := make(map[string]string, len(keys))
	for _, key := range keys {
		keyType, err := client.Type(ctx, key).Result()
		if err != nil {
			t.Fatalf("read key type %s: %v", key, err)
		}
		var value any
		switch keyType {
		case "hash":
			value, err = client.HGetAll(ctx, key).Result()
		case "zset":
			value, err = client.ZRangeWithScores(ctx, key, 0, -1).Result()
		case "string":
			value, err = client.Get(ctx, key).Result()
		default:
			value, err = client.Dump(ctx, key).Result()
		}
		if err != nil {
			t.Fatalf("read key %s: %v", key, err)
		}
		encoded, err := json.Marshal(value)
		if err != nil {
			t.Fatalf("encode key %s: %v", key, err)
		}
		dump[key] = keyType + "\x00" + string(encoded)
	}
	return dump
}

func TestAttemptLifecycleIntegrityFencesAreZeroWrite(t *testing.T) {
	type fenceCase struct {
		name       string
		mutate     func(context.Context, *Store, *redis.Client, *AttemptPermit) error
		wantError  error
		wantCode   failure.Code
		wantFinish Disposition
	}
	fences := []fenceCase{
		{
			name: "marker not ready",
			mutate: func(ctx context.Context, s *Store, client *redis.Client, _ *AttemptPermit) error {
				return client.HSet(ctx, s.keys.stateIntegrityMarker(), "state", "pending").Err()
			},
			wantError: ErrRuntimeStateLost, wantCode: failure.CodeGatewayRuntimeStateLost,
			wantFinish: DispositionRuntimeStateLost,
		},
		{
			name: "marker epoch mismatch",
			mutate: func(ctx context.Context, s *Store, client *redis.Client, _ *AttemptPermit) error {
				return client.HSet(ctx, s.keys.stateIntegrityMarker(), "epoch", "different-marker-epoch").Err()
			},
			wantError: ErrStaleIntegrityEpoch, wantCode: failure.CodeGatewayRuntimeSyncRequired,
			wantFinish: DispositionStaleIntegrity,
		},
		{
			name: "caller expected epoch mismatch",
			mutate: func(_ context.Context, _ *Store, _ *redis.Client, permit *AttemptPermit) error {
				permit.IntegrityEpoch = "different-caller-epoch"
				return nil
			},
			wantError: ErrStaleIntegrityEpoch, wantCode: failure.CodeGatewayRuntimeSyncRequired,
			wantFinish: DispositionStaleIntegrity,
		},
		{
			name: "server permit epoch mismatch",
			mutate: func(ctx context.Context, s *Store, client *redis.Client, permit *AttemptPermit) error {
				return client.HSet(ctx, s.keys.permit(permit.PermitID), "runtime_integrity_epoch", "different-permit-epoch").Err()
			},
			wantError: ErrStaleIntegrityEpoch, wantCode: failure.CodeGatewayRuntimeSyncRequired,
			wantFinish: DispositionStaleIntegrity,
		},
		{
			name: "server permit epoch missing",
			mutate: func(ctx context.Context, s *Store, client *redis.Client, permit *AttemptPermit) error {
				return client.HDel(ctx, s.keys.permit(permit.PermitID), "runtime_integrity_epoch").Err()
			},
			wantError: ErrRuntimeSyncRequired, wantCode: failure.CodeGatewayRuntimeSyncRequired,
			wantFinish: DispositionRuntimeSyncReq,
		},
		{
			name: "server permit identity conflict",
			mutate: func(ctx context.Context, s *Store, client *redis.Client, permit *AttemptPermit) error {
				return client.HSet(ctx, s.keys.permit(permit.PermitID), "provider_id", permit.ProviderID+1).Err()
			},
			wantCode: failure.CodeGatewayBreakerPermitConflict, wantFinish: DispositionTerminalConflict,
		},
		{
			name: "server permit input estimate tampered",
			mutate: func(ctx context.Context, s *Store, client *redis.Client, permit *AttemptPermit) error {
				return client.HSet(ctx, s.keys.permit(permit.PermitID), "input_estimate", "not-a-number").Err()
			},
			wantError: ErrRuntimeSyncRequired, wantCode: failure.CodeGatewayRuntimeSyncRequired,
			wantFinish: DispositionRuntimeSyncReq,
		},
	}
	operations := []string{"renew", "finish", "abort"}

	for _, fence := range fences {
		for _, operation := range operations {
			t.Run(fence.name+"/"+operation, func(t *testing.T) {
				s, client, namespace := newTestStore(t)
				admission := acquire(t, s, testConfig(), "epoch-fence", 701, 7001)
				permit := *admission.Permit
				if err := fence.mutate(context.Background(), s, client, &permit); err != nil {
					t.Fatalf("prepare fence: %v", err)
				}
				before := dumpTestNamespace(t, client, namespace)

				var err error
				switch operation {
				case "renew":
					err = s.Renew(context.Background(), permit)
				case "abort":
					err = s.Abort(context.Background(), permit)
				case "finish":
					var result FinishResult
					result, err = s.Finish(context.Background(), permit, FinishOutcome{
						ProviderOutcome:   OutcomeIgnored,
						ChannelOutcome:    OutcomeIgnored,
						RequestWriteState: RequestWriteCompleted,
					})
					if result.ProviderDisposition != fence.wantFinish || result.ChannelDisposition != fence.wantFinish {
						t.Fatalf("finish disposition = %s/%s, want %s", result.ProviderDisposition, result.ChannelDisposition, fence.wantFinish)
					}
				}

				if operation == "finish" {
					if err != nil {
						t.Fatalf("finish fence returned transport error: %v", err)
					}
				} else {
					if fence.wantError != nil && !errors.Is(err, fence.wantError) {
						t.Fatalf("lifecycle error = %v, want sentinel %v", err, fence.wantError)
					}
					if got := failure.CodeOf(err); got != fence.wantCode {
						t.Fatalf("lifecycle failure code = %q, want %q (err=%v)", got, fence.wantCode, err)
					}
				}

				after := dumpTestNamespace(t, client, namespace)
				if !reflect.DeepEqual(after, before) {
					t.Fatalf("rejected %s changed Redis namespace\nbefore=%v\nafter=%v", operation, before, after)
				}
			})
		}
	}
}

// TestAcquireFinishSuccessClosed 验证成功链路：closed 下 Acquire→Finish(success) 应用到 breaker。
func TestAcquireFinishSuccessClosed(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()

	adm := acquire(t, s, cfg, "p1", 1, 10)
	if adm.Mode != AdmissionPermit {
		t.Fatalf("want permit, got %s/%s", adm.Mode, adm.Reason)
	}
	res := finish(t, s, cfg, adm.Permit, OutcomeEligibleSuccess, OutcomeEligibleSuccess)
	if res.ChannelDisposition != DispositionApplied || res.ProviderDisposition != DispositionApplied {
		t.Fatalf("want applied/applied, got %s/%s", res.ProviderDisposition, res.ChannelDisposition)
	}

	snap, err := s.Snapshot(context.Background(), ScopeChannel, 1)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snap.State != StateClosed || snap.EligibleSuccesses != 1 {
		t.Fatalf("unexpected snapshot: %+v", snap)
	}
}

// TestFastTriggerOpensAfterThreeConsecutiveFailures 验证快速触发：连续 3 次可归因失败 → open。
func TestFastTriggerOpensAfterThreeConsecutiveFailures(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()

	for i := 0; i < 3; i++ {
		adm := acquire(t, s, cfg, fmt.Sprintf("f%d", i), 1, 10)
		if adm.Mode != AdmissionPermit {
			t.Fatalf("attempt %d: want permit, got %s/%s", i, adm.Mode, adm.Reason)
		}
		finish(t, s, cfg, adm.Permit, OutcomeIgnored, OutcomeEligibleFailure)
	}

	snap, _ := s.Snapshot(context.Background(), ScopeChannel, 1)
	if snap.State != StateOpen {
		t.Fatalf("want open after 3 consecutive failures, got %s (%+v)", snap.State, snap)
	}

	// open 期间新 Acquire 应 denied(open)。
	adm := acquire(t, s, cfg, "after-open", 1, 10)
	if adm.Mode != AdmissionDenied || adm.Reason != ReasonOpen {
		t.Fatalf("want denied/open, got %s/%s", adm.Mode, adm.Reason)
	}
}

// TestRatioTriggerOpens 验证比例触发：>=MinRequests 样本且失败率>=50% → open（且避免快速触发干扰：
// 交替 成功/失败 使连续失败计数不达 3）。
func TestRatioTriggerOpens(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig() // MinRequests=4, ratio=0.5
	// 交替 success/fail/success/fail：末事件为失败时评估比例（total=4, fail=2, ratio=0.5），连续失败最多 1，
	// 避免快速触发（需连续 3 次）先行开路。
	seq := []Outcome{OutcomeEligibleSuccess, OutcomeEligibleFailure, OutcomeEligibleSuccess, OutcomeEligibleFailure}
	var last FinishResult
	for i, oc := range seq {
		adm := acquire(t, s, cfg, fmt.Sprintf("r%d", i), 2, 20)
		if adm.Mode != AdmissionPermit {
			t.Fatalf("attempt %d denied: %s", i, adm.Reason)
		}
		last = finish(t, s, cfg, adm.Permit, OutcomeIgnored, oc)
	}
	_ = last
	snap, _ := s.Snapshot(context.Background(), ScopeChannel, 2)
	if snap.State != StateOpen {
		t.Fatalf("want open after ratio trigger, got %s (%+v)", snap.State, snap)
	}
}

// TestHalfOpenRecoversAfterTwoDistinctSuccesses 验证 half-open 双探测恢复 closed。
func TestHalfOpenRecoversAfterTwoDistinctSuccesses(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()

	// 先快速触发 open。
	for i := 0; i < 3; i++ {
		adm := acquire(t, s, cfg, fmt.Sprintf("o%d", i), 3, 30)
		finish(t, s, cfg, adm.Permit, OutcomeIgnored, OutcomeEligibleFailure)
	}
	if snap, _ := s.Snapshot(context.Background(), ScopeChannel, 3); snap.State != StateOpen {
		t.Fatalf("precondition: want open, got %s", snap.State)
	}

	// 等待 open 冷却（OpenDurationsMs[0]=60ms）。
	time.Sleep(90 * time.Millisecond)

	// 第一个 half-open 探测成功。
	adm1 := acquire(t, s, cfg, "probe1", 3, 30)
	if adm1.Mode != AdmissionPermit || !adm1.Permit.ChannelHalfOpenProbe {
		t.Fatalf("probe1: want permit with half-open probe, got %s/%s probe=%v", adm1.Mode, adm1.Reason, adm1.Permit)
	}
	finish(t, s, cfg, adm1.Permit, OutcomeIgnored, OutcomeEligibleSuccess)
	if snap, _ := s.Snapshot(context.Background(), ScopeChannel, 3); snap.State != StateHalfOpen {
		t.Fatalf("after 1 success: want half_open, got %s", snap.State)
	}

	// 第二个不同 permit 探测成功 → closed。
	adm2 := acquire(t, s, cfg, "probe2", 3, 30)
	if adm2.Mode != AdmissionPermit || !adm2.Permit.ChannelHalfOpenProbe {
		t.Fatalf("probe2: want permit with half-open probe, got %s/%s", adm2.Mode, adm2.Reason)
	}
	finish(t, s, cfg, adm2.Permit, OutcomeIgnored, OutcomeEligibleSuccess)
	if snap, _ := s.Snapshot(context.Background(), ScopeChannel, 3); snap.State != StateClosed {
		t.Fatalf("after 2 successes: want closed, got %s", snap.State)
	}
}

// TestHalfOpenReopensOnProbeFailure 验证 half-open 探测失败立即重新 open。
func TestHalfOpenReopensOnProbeFailure(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()

	for i := 0; i < 3; i++ {
		adm := acquire(t, s, cfg, fmt.Sprintf("ro%d", i), 4, 40)
		finish(t, s, cfg, adm.Permit, OutcomeIgnored, OutcomeEligibleFailure)
	}
	time.Sleep(90 * time.Millisecond)

	probe := acquire(t, s, cfg, "reprobe", 4, 40)
	if probe.Mode != AdmissionPermit || !probe.Permit.ChannelHalfOpenProbe {
		t.Fatalf("reprobe: want half-open probe permit, got %s/%s", probe.Mode, probe.Reason)
	}
	finish(t, s, cfg, probe.Permit, OutcomeIgnored, OutcomeEligibleFailure)
	if snap, _ := s.Snapshot(context.Background(), ScopeChannel, 4); snap.State != StateOpen {
		t.Fatalf("after probe failure: want open, got %s", snap.State)
	}
}

// TestAbortReleasesWithoutBreakerChange 验证 Abort 不计成功/失败。
func TestAbortReleasesWithoutBreakerChange(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()

	adm := acquire(t, s, cfg, "ab1", 5, 50)
	if err := s.Abort(context.Background(), *adm.Permit); err != nil {
		t.Fatalf("abort: %v", err)
	}
	snap, _ := s.Snapshot(context.Background(), ScopeChannel, 5)
	if snap.EligibleSuccesses != 0 || snap.EligibleFailures != 0 {
		t.Fatalf("abort must not change breaker counts: %+v", snap)
	}
	// 重复 Abort 幂等（terminal_conflict，不 panic）。
	if err := s.Abort(context.Background(), *adm.Permit); err != nil {
		t.Fatalf("second abort: %v", err)
	}
}

// TestFinishIdempotentFirstTerminalWins 验证重复 Finish first-terminal-wins。
func TestFinishIdempotentFirstTerminalWins(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()

	adm := acquire(t, s, cfg, "idem1", 6, 60)
	first := finish(t, s, cfg, adm.Permit, OutcomeEligibleSuccess, OutcomeEligibleSuccess)
	second := finish(t, s, cfg, adm.Permit, OutcomeEligibleFailure, OutcomeEligibleFailure)
	if first.ChannelDisposition != DispositionApplied {
		t.Fatalf("first finish want applied, got %s", first.ChannelDisposition)
	}
	// 第二次返回首次 disposition，不重复计数。
	if second.ChannelDisposition != DispositionApplied {
		t.Fatalf("second finish should return first disposition, got %s", second.ChannelDisposition)
	}
	snap, _ := s.Snapshot(context.Background(), ScopeChannel, 6)
	if snap.EligibleSuccesses != 1 {
		t.Fatalf("duplicate finish must not double count: %+v", snap)
	}
}

func TestRouteChannelRPDAttributionRequiresInteractionEvidence(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const routeID, channelID, providerID = int64(41), int64(42), int64(420)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":null}`)

	acquireForRoute := func(permitID string) *AttemptPermit {
		t.Helper()
		admission, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
			PermitID: permitID, AdmissionFingerprint: permitID + "-fp", RequestAdmissionID: "req-" + permitID,
			RouteID: routeID, ProviderID: providerID, ChannelID: channelID,
			OriginRevision: 1, ProviderStatusRevision: 1, ChannelConfigRevision: 1,
			ModelID: 100, UpstreamEndpoint: EndpointChatCompletions, RequestMode: ModeNonStream,
			InputEstimate: 10,
		}))
		if err != nil || admission.Mode != AdmissionPermit || admission.Permit == nil {
			t.Fatalf("acquire route attribution permit: admission=%+v err=%v", admission, err)
		}
		return admission.Permit
	}

	withEvidence := acquireForRoute("route-rpd-evidence")
	if _, err := s.Finish(context.Background(), *withEvidence, FinishOutcome{
		ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleSuccess,
		RequestWriteState: RequestWriteCompleted,
	}); err != nil {
		t.Fatalf("finish route attribution permit: %v", err)
	}
	if _, err := s.Finish(context.Background(), *withEvidence, FinishOutcome{
		ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleSuccess,
		RequestWriteState: RequestWriteCompleted,
	}); err != nil {
		t.Fatalf("repeat finish route attribution permit: %v", err)
	}

	withoutEvidence := acquireForRoute("route-rpd-no-evidence")
	if err := s.Abort(context.Background(), *withoutEvidence); err != nil {
		t.Fatalf("abort without interaction evidence: %v", err)
	}
	aborted := acquireForRoute("route-rpd-abort")
	if err := s.Abort(context.Background(), *aborted); err != nil {
		t.Fatalf("abort route attribution permit: %v", err)
	}

	used, err := s.RouteChannelRPDUsage(context.Background(), routeID, channelID)
	if err != nil || used != 1 {
		t.Fatalf("route-channel RPD attribution = %d, want 1, err=%v", used, err)
	}
}

// TestFinishDoesNotWriteTTFTIntoBreakerState 冻结 §12：TTFT 样本只进 30 分钟分钟桶，
// 不再写入 breaker 状态（EWMA 已删除），Finish 仍正常收口资源与 breaker 计数。
func TestFinishDoesNotWriteTTFTIntoBreakerState(t *testing.T) {
	s, client, _ := newTestStore(t)
	cfg := testConfig()
	seedAttemptControls(t, s, cfg, 7, `{"concurrency":null}`)

	adm, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "s1", AdmissionFingerprint: "s1-fp", RequestAdmissionID: "req",
		ProviderID: 70, ChannelID: 7, OriginRevision: 1, ProviderStatusRevision: 1,
		ChannelConfigRevision: 1, ModelID: 100, UpstreamEndpoint: EndpointChatCompletions,
		RequestMode: ModeStream,
	}))
	if err != nil {
		t.Fatalf("acquire stream: %v", err)
	}
	if _, err := s.Finish(context.Background(), *adm.Permit, FinishOutcome{
		ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleSuccess,
		RequestWriteState: RequestWriteCompleted, FirstTokenEligible: true,
	}); err != nil {
		t.Fatalf("finish stream: %v", err)
	}
	snap, err := s.Snapshot(context.Background(), ScopeChannel, 7)
	if err != nil {
		t.Fatalf("snapshot channel: %v", err)
	}
	if snap.EligibleSuccesses != 1 {
		t.Fatalf("breaker success counting must still work: %+v", snap)
	}
	// breaker hash 内不得再出现任何 TTFT 字段。
	fields, err := client.HGetAll(context.Background(), s.keys.channel(7)).Result()
	if err != nil {
		t.Fatalf("read channel breaker hash: %v", err)
	}
	if _, ok := fields["ttft_ewma_ms"]; ok {
		t.Fatalf("breaker state must not carry ttft_ewma_ms: %v", fields)
	}
	if _, ok := fields["ttft_samples"]; ok {
		t.Fatalf("breaker state must not carry ttft_samples: %v", fields)
	}
}

// TestConcurrencyLimitDenies 验证 Channel 并发上限（0=不限；>0 满员 denied）。
func TestConcurrencyLimitDenies(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	seedAttemptControls(t, s, cfg, 8, `{"concurrency":1}`)

	in := func(id string) AcquireAttemptInput {
		return withAttemptControlRevisions(AcquireAttemptInput{
			PermitID: id, AdmissionFingerprint: id + "-fp", RequestAdmissionID: "req",
			ProviderID: 80, ChannelID: 8, OriginRevision: 1, ProviderStatusRevision: 1,
			ChannelConfigRevision: 1, ModelID: 100, UpstreamEndpoint: EndpointChatCompletions,
			RequestMode: ModeNonStream,
		})
	}
	a1, err := acquireAttempt(t, s, in("c1"))
	if err != nil || a1.Mode != AdmissionPermit {
		t.Fatalf("c1: want permit, got %v/%s err=%v", a1.Mode, a1.Reason, err)
	}
	a2, err := acquireAttempt(t, s, in("c2"))
	if err != nil {
		t.Fatalf("c2 err: %v", err)
	}
	if a2.Mode != AdmissionDenied || a2.Reason != ReasonConcurrencyFull {
		t.Fatalf("c2: want denied/concurrency_full, got %s/%s", a2.Mode, a2.Reason)
	}
	// 释放 c1 后 c2 可入。
	if err := s.Abort(context.Background(), *a1.Permit); err != nil {
		t.Fatalf("abort c1: %v", err)
	}
	a3, err := acquireAttempt(t, s, in("c3"))
	if err != nil || a3.Mode != AdmissionPermit {
		t.Fatalf("c3 after release: want permit, got %s/%s err=%v", a3.Mode, a3.Reason, err)
	}
}

// TestResetRestoresClosed 验证 Reset 推进 generation 并恢复 closed，旧 permit Finish 变 stale。
func TestResetRestoresClosed(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()

	adm := acquire(t, s, cfg, "rs1", 9, 90)
	gen, err := s.Reset(context.Background(), ScopeChannel, 9)
	if err != nil {
		t.Fatalf("reset: %v", err)
	}
	if gen < 2 {
		t.Fatalf("reset should advance generation, got %d", gen)
	}
	// 旧 permit Finish：channel generation 已变 → stale；资源仍收口。
	res := finish(t, s, cfg, adm.Permit, OutcomeIgnored, OutcomeEligibleFailure)
	if res.ChannelDisposition != DispositionStaleGeneration {
		t.Fatalf("post-reset finish want stale_generation, got %s", res.ChannelDisposition)
	}
	snap, _ := s.Snapshot(context.Background(), ScopeChannel, 9)
	if snap.State != StateClosed || snap.EligibleFailures != 0 {
		t.Fatalf("reset must restore closed/no-sample, got %+v", snap)
	}
}

// TestChannel429CooldownDeniesAcquire 验证 429 冷却优先于 breaker：冷却期内 Acquire denied(rate_limited)，
// 且不增加 breaker eligible 计数；到期后恢复放行。Reset breaker 不清冷却。
func TestChannel429CooldownDeniesAcquire(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()

	until, err := s.SetChannel429Cooldown(context.Background(), 12, 60, 60)
	if err != nil {
		t.Fatalf("set cooldown: %v", err)
	}
	if until <= 0 {
		t.Fatalf("expected positive cooldown until, got %d", until)
	}
	rem, err := s.Channel429CooldownRemainingMs(context.Background(), 12)
	if err != nil || rem <= 0 {
		t.Fatalf("cooldown remaining want >0, got %d err=%v", rem, err)
	}

	adm := acquire(t, s, cfg, "cd1", 12, 120)
	// 429 冷却拒绝必须报 cooldown（与并发满严格区分），并携带准确剩余毫秒供 Retry-After（§9.5）。
	if adm.Mode != AdmissionDenied || adm.Reason != ReasonCooldown {
		t.Fatalf("want denied/cooldown during cooldown, got %s/%s", adm.Mode, adm.Reason)
	}
	if adm.CooldownRemainingMs <= 0 {
		t.Fatalf("cooldown denial must report remaining ms, got %d", adm.CooldownRemainingMs)
	}
	// 冷却拒绝不产生 breaker 样本。
	if snap, _ := s.Snapshot(context.Background(), ScopeChannel, 12); snap.SampleCount != 0 {
		t.Fatalf("cooldown deny must not create breaker samples: %+v", snap)
	}

	// 到期后恢复放行。
	time.Sleep(90 * time.Millisecond)
	adm2 := acquire(t, s, cfg, "cd2", 12, 120)
	if adm2.Mode != AdmissionPermit {
		t.Fatalf("after cooldown want permit, got %s/%s", adm2.Mode, adm2.Reason)
	}
}

// TestChannelModelPermissionPause 验证 403 权限暂停：revision 匹配时 denied(model_permission_paused)，
// 清除后放行；config revision 前进后旧暂停 stale、不再命中。
func TestChannelModelPermissionPause(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	seedAttemptControls(t, s, cfg, 13, `{"concurrency":null}`)

	// 暂停 (channel=13, model=130) at config_rev=1, base_url_rev=1, status_rev=1。
	if err := s.PauseChannelModelPermission(context.Background(), 13, 130, 1, 1, 1); err != nil {
		t.Fatalf("pause permission: %v", err)
	}

	acq := func(id string, model, cfgRev int64) AttemptAdmission {
		adm, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
			PermitID: id, AdmissionFingerprint: id + "-fp", RequestAdmissionID: "req",
			ProviderID: 130, ChannelID: 13, OriginRevision: 1, ProviderStatusRevision: 1,
			ChannelConfigRevision: cfgRev, ModelID: model, UpstreamEndpoint: EndpointChatCompletions,
			RequestMode: ModeNonStream,
		}))
		if err != nil {
			t.Fatalf("acquire %s: %v", id, err)
		}
		return adm
	}

	// 匹配 revision 的绑定被拒。
	if adm := acq("perm1", 130, 1); adm.Mode != AdmissionDenied || adm.Reason != ReasonModelPermissionPaused {
		t.Fatalf("want denied/model_permission_paused, got %s/%s", adm.Mode, adm.Reason)
	}
	// 同 Channel 其它模型不受影响。
	if adm := acq("perm2", 999, 1); adm.Mode != AdmissionPermit {
		t.Fatalf("other model must not be paused, got %s/%s", adm.Mode, adm.Reason)
	}
	// config revision 前进（模拟配置真变化）→ 旧暂停 stale，不再命中。
	if adm := acq("perm3", 130, 2); adm.Mode != AdmissionPermit {
		t.Fatalf("stale permission (new config rev) must not pause, got %s/%s", adm.Mode, adm.Reason)
	}

	// 用错误 expected revision 清除 → stale，不改状态。
	cleared, err := s.ClearChannelModelPermission(context.Background(), 13, 130, 2, 1, 1)
	if err != nil {
		t.Fatalf("clear (stale): %v", err)
	}
	if cleared {
		t.Fatalf("clear with mismatched revision must not clear")
	}
	if adm := acq("perm4", 130, 1); adm.Mode != AdmissionDenied {
		t.Fatalf("still paused after stale clear, got %s/%s", adm.Mode, adm.Reason)
	}
	// 正确 revision 清除 → 放行。
	cleared, err = s.ClearChannelModelPermission(context.Background(), 13, 130, 1, 1, 1)
	if err != nil || !cleared {
		t.Fatalf("clear (match) want cleared, got %v err=%v", cleared, err)
	}
	if adm := acq("perm5-stale", 130, 1); adm.Mode != AdmissionDenied || adm.Reason != ReasonStaleConfigRevision {
		t.Fatalf("clearing permission must not revive stale config revision, got %s/%s", adm.Mode, adm.Reason)
	}
	if adm := acq("perm5", 130, 2); adm.Mode != AdmissionPermit {
		t.Fatalf("after clear current revision want permit, got %s/%s", adm.Mode, adm.Reason)
	}
}

// TestResetClearsTTFT 验证管理员 Reset 恢复 closed/no-sample，并物理删除 TTFT EWMA/sample 字段。
func TestResetClearsTTFT(t *testing.T) {
	s, client, _ := newTestStore(t)
	cfg := testConfig()
	seedAttemptControls(t, s, cfg, 201, `{"concurrency":null}`)
	adm, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "reset-ttft", AdmissionFingerprint: "reset-ttft-fp", RequestAdmissionID: "req",
		ProviderID: 2010, ChannelID: 201, OriginRevision: 1, ProviderStatusRevision: 1,
		ChannelConfigRevision: 1, ModelID: 100, UpstreamEndpoint: EndpointChatCompletions,
		RequestMode: ModeStream,
	}))
	if err != nil || adm.Mode != AdmissionPermit {
		t.Fatalf("acquire stream: mode=%s reason=%s err=%v", adm.Mode, adm.Reason, err)
	}
	if _, err := s.Finish(context.Background(), *adm.Permit, FinishOutcome{
		ProviderOutcome:    OutcomeIgnored,
		ChannelOutcome:     OutcomeEligibleSuccess,
		RequestWriteState:  RequestWriteCompleted,
		FirstTokenEligible: true,
	}); err != nil {
		t.Fatalf("finish stream: %v", err)
	}
	before, err := s.Snapshot(context.Background(), ScopeChannel, 201)
	if err != nil || before.EligibleSuccesses != 1 {
		t.Fatalf("precondition breaker sample missing: %+v err=%v", before, err)
	}

	if _, err := s.Reset(context.Background(), ScopeChannel, 201); err != nil {
		t.Fatalf("reset: %v", err)
	}
	after, err := s.Snapshot(context.Background(), ScopeChannel, 201)
	if err != nil {
		t.Fatalf("snapshot after reset: %v", err)
	}
	if after.State != StateClosed || after.SampleCount != 0 {
		t.Fatalf("reset must restore closed/no-sample: %+v", after)
	}
	fields, err := client.HMGet(context.Background(), s.keys.channel(201), "ttft_ewma_ms", "ttft_samples").Result()
	if err != nil {
		t.Fatalf("read reset fields: %v", err)
	}
	if fields[0] != nil || fields[1] != nil {
		t.Fatalf("reset must delete TTFT fields, got %v", fields)
	}
}

func TestParseSnapshotKeepsOriginPendingRevisions(t *testing.T) {
	snapshot, err := parseSnapshotRow(ScopeProvider, 88, []interface{}{
		"present", int64(1_000), int64(0), []interface{}{
			"state", "closed",
			"control_present", "1",
			"effective_status", "enabled",
			"origin_revision", "3",
			"pending_origin_revision", "4",
			"origin_revision_state", "pending",
			"status_revision", "5",
			"pending_status_revision", "6",
			"status_revision_state", "pending",
		},
	})
	if err != nil {
		t.Fatalf("parse origin snapshot: %v", err)
	}
	if snapshot.OriginRevision != 3 || snapshot.PendingOriginRevision != 4 ||
		snapshot.StatusRevision != 5 || snapshot.PendingStatusRevision != 6 {
		t.Fatalf("pending revisions were lost: %+v", snapshot)
	}
}

// TestSnapshotManyReadsCandidateIdentityAndPreservesOrder 验证单 Lua 批量读取、输入顺序和 Channel 绑定输出。
func TestSnapshotManyReadsCandidateIdentityAndPreservesOrder(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()

	seed := func(permitID string, providerID, channelID, baseRev, statusRev, configRev int64, mode RequestMode, streamFirstToken bool) {
		t.Helper()
		seedAttemptControls(t, s, cfg, channelID, `{"concurrency":null}`)
		created, err := s.InitProviderControl(context.Background(), providerID, baseRev, statusRev, "enabled")
		if err != nil || !created {
			t.Fatalf("init origin %d: created=%v err=%v", providerID, created, err)
		}
		adm, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
			PermitID: permitID, AdmissionFingerprint: permitID + "-fp", RequestAdmissionID: "req",
			ProviderID: providerID, ChannelID: channelID,
			OriginRevision: baseRev, ProviderStatusRevision: statusRev, ChannelConfigRevision: configRev,
			ModelID: 100, UpstreamEndpoint: EndpointResponses, RequestMode: mode,
			EnforceProviderControl: true,
		}))
		if err != nil || adm.Mode != AdmissionPermit {
			t.Fatalf("acquire %s: mode=%s reason=%s err=%v", permitID, adm.Mode, adm.Reason, err)
		}
		if _, err := s.Finish(context.Background(), *adm.Permit, FinishOutcome{
			ProviderOutcome:    OutcomeEligibleSuccess,
			ChannelOutcome:     OutcomeEligibleSuccess,
			RequestWriteState:  RequestWriteCompleted,
			FirstTokenEligible: streamFirstToken,
		}); err != nil {
			t.Fatalf("finish %s: %v", permitID, err)
		}
	}

	seed("batch-a", 3010, 301, 4, 5, 6, ModeStream, true)
	seed("batch-b", 3020, 302, 7, 8, 9, ModeNonStream, false)

	candidates := []SnapshotCandidateInput{
		{ProviderID: 3020, ChannelID: 302, OriginRevision: 7, ProviderStatusRevision: 8, ChannelConfigRevision: 9, ChannelCapacityRevision: 1},
		{ProviderID: 3010, ChannelID: 301, OriginRevision: 4, ProviderStatusRevision: 5, ChannelConfigRevision: 6, ChannelCapacityRevision: 1},
	}
	result, err := s.SnapshotMany(context.Background(), SnapshotManyInput{
		IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
		GlobalConcurrencyRevision: 1, CircuitBreakerRevision: 1, RoutingBalanceRevision: 1,
		ModelID: 100, Candidates: candidates,
	})
	if err != nil {
		t.Fatalf("snapshot many: %v", err)
	}
	snapshots := result.Candidates
	if len(snapshots) != 2 || snapshots[0].Candidate.ChannelID != 302 || snapshots[1].Candidate.ChannelID != 301 {
		t.Fatalf("snapshot order changed: %+v", snapshots)
	}
	for i, snapshot := range snapshots {
		candidate := candidates[i]
		if snapshot.Status != CandidateSnapshotCurrent {
			t.Fatalf("candidate %d want current, got %s", candidate.ChannelID, snapshot.Status)
		}
		if !snapshot.Provider.ControlPresent || snapshot.Provider.OriginRevision != candidate.OriginRevision ||
			snapshot.Provider.StatusRevision != candidate.ProviderStatusRevision {
			t.Fatalf("candidate %d origin identity mismatch: %+v", candidate.ChannelID, snapshot.Provider)
		}
		if snapshot.Channel.ProviderID != candidate.ProviderID ||
			snapshot.Channel.OriginRevision != candidate.OriginRevision ||
			snapshot.Channel.StatusRevision != candidate.ProviderStatusRevision ||
			snapshot.Channel.ChannelConfigRevision != candidate.ChannelConfigRevision {
			t.Fatalf("candidate %d channel binding mismatch: %+v", candidate.ChannelID, snapshot.Channel)
		}
	}
}

// TestSnapshotManyFailsWholeBatchOnWrongType 验证任一畸形 key 都让整批作为基础设施错误失败。
func TestSnapshotManyFailsWholeBatchOnWrongType(t *testing.T) {
	s, client, _ := newTestStore(t)
	cfg := testConfig()
	seedAttemptControls(t, s, cfg, 401, `{"concurrency":null}`)
	ensureTestControl(t, s, s.ChannelCapacityControl(402), `{"concurrency":null}`)
	if _, err := s.InitProviderControl(context.Background(), 4010, 1, 1, "enabled"); err != nil {
		t.Fatalf("init origin: %v", err)
	}
	if err := client.Set(context.Background(), s.keys.channel(402), "not-a-hash", 0).Err(); err != nil {
		t.Fatalf("seed wrong type: %v", err)
	}

	got, err := s.SnapshotMany(context.Background(), SnapshotManyInput{
		IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
		GlobalConcurrencyRevision: 1, CircuitBreakerRevision: 1, RoutingBalanceRevision: 1,
		ModelID: 100,
		Candidates: []SnapshotCandidateInput{
			{ProviderID: 4010, ChannelID: 401, OriginRevision: 1, ProviderStatusRevision: 1, ChannelConfigRevision: 1, ChannelCapacityRevision: 1},
			{ProviderID: 4010, ChannelID: 402, OriginRevision: 1, ProviderStatusRevision: 1, ChannelConfigRevision: 1, ChannelCapacityRevision: 1},
		},
	})
	if err == nil || !errors.Is(err, ErrStoreUnavailable) {
		t.Fatalf("wrong type want store unavailable, got snapshots=%v err=%v", got, err)
	}
	if got.Candidates != nil {
		t.Fatalf("whole batch must fail without partial results, got %+v", got)
	}
}

func TestSnapshotManyReturnsAuthoritativeRoutingFacts(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID, providerID, modelID = int64(451), int64(4510), int64(100)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":2}`)
	if created, err := s.InitProviderControl(context.Background(), providerID, 3, 4, "enabled"); err != nil || !created {
		t.Fatalf("init origin: created=%v err=%v", created, err)
	}
	input := withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "snapshot-facts", AdmissionFingerprint: "snapshot-facts-fp", RequestAdmissionID: "req",
		ProviderID: providerID, ChannelID: channelID, OriginRevision: 3, ProviderStatusRevision: 4,
		ChannelConfigRevision: 5, ModelID: modelID, UpstreamEndpoint: EndpointResponses, RequestMode: ModeStream,
		EnforceProviderControl: true, InputEstimate: 25,
	})
	if admission, err := acquireAttempt(t, s, input); err != nil || admission.Mode != AdmissionPermit {
		t.Fatalf("acquire active capacity: admission=%+v err=%v", admission, err)
	}
	if _, err := s.SetChannel429Cooldown(context.Background(), channelID, 5_000, 5_000); err != nil {
		t.Fatalf("set cooldown: %v", err)
	}
	if err := s.PauseChannelModelPermission(context.Background(), channelID, modelID, 5, 3, 4); err != nil {
		t.Fatalf("pause permission: %v", err)
	}

	result, err := s.SnapshotMany(context.Background(), SnapshotManyInput{
		IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
		GlobalConcurrencyRevision: 1, CircuitBreakerRevision: 1, RoutingBalanceRevision: 1,
		ModelID: modelID,
		Candidates: []SnapshotCandidateInput{{
			ProviderID: providerID, ChannelID: channelID, OriginRevision: 3, ProviderStatusRevision: 4,
			ChannelConfigRevision: 5, ChannelCapacityRevision: 1,
		}},
	})
	if err != nil {
		t.Fatalf("snapshot many: %v", err)
	}
	// objective_v1 五项权重 + TTFT/错误率窗口惩罚参数必须原样透出（§7/§14.6）。
	if result.RoutingBalance.Revision != 1 ||
		result.RoutingBalance.CostWeightPct != 25 || result.RoutingBalance.ConcurrencyWeightPct != 20 ||
		result.RoutingBalance.TTFTWeightPct != 25 || result.RoutingBalance.ErrorRateWeightPct != 20 ||
		result.RoutingBalance.PriorityWeightPct != 10 ||
		result.RoutingBalance.TTFTWindowMs != 1_800_000 || result.RoutingBalance.TTFTPenaltyUnitMs != 1000 ||
		result.RoutingBalance.TTFTPenaltyPointsPerUnit != 2.5 ||
		result.RoutingBalance.ErrorWindowMs != 1_800_000 ||
		result.RoutingBalance.ErrorPenaltyPointsPerPercent != 2.5 {
		t.Fatalf("routing balance payload mismatch: %+v", result.RoutingBalance)
	}
	snapshot := result.Candidates[0]
	if snapshot.Status != CandidateSnapshotRateLimited || snapshot.CooldownRemainingMs <= 0 ||
		!snapshot.ModelPermissionPaused || snapshot.ModelPermissionRecheckState != "queued" {
		t.Fatalf("cooldown/permission facts mismatch: %+v", snapshot)
	}
	// 并发是唯一被准入占用的渠道级资源；RPM/RPD/TPM 观测走独立的 30 分钟样本桶。
	if snapshot.Concurrency.Used != 1 || snapshot.Concurrency.Limit != 2 {
		t.Fatalf("concurrency facts mismatch: %+v", snapshot.Concurrency)
	}
}

func TestSnapshotManyReturnsRoutingBalanceFromCurrentPayload(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const channelID, providerID = int64(452), int64(4520)
	seedAttemptControlsWithRoutingBalance(
		t, s, cfg, channelID,
		`{"concurrency":null}`,
		testRoutingBalancePayloadCustom,
	)
	if created, err := s.InitProviderControl(context.Background(), providerID, 1, 1, "enabled"); err != nil || !created {
		t.Fatalf("init origin: created=%v err=%v", created, err)
	}

	result, err := s.SnapshotMany(context.Background(), SnapshotManyInput{
		IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
		GlobalConcurrencyRevision: 1,
		CircuitBreakerRevision:    1, RoutingBalanceRevision: 1, ModelID: 100,
		Candidates: []SnapshotCandidateInput{{
			ProviderID: providerID, ChannelID: channelID,
			OriginRevision: 1, ProviderStatusRevision: 1,
			ChannelConfigRevision: 1, ChannelCapacityRevision: 1,
		}},
	})
	if err != nil {
		t.Fatalf("snapshot current routing balance payload: %v", err)
	}
	if result.RoutingBalance.CostWeightPct != 40 || result.RoutingBalance.ConcurrencyWeightPct != 10 ||
		result.RoutingBalance.TTFTWeightPct != 20 || result.RoutingBalance.ErrorRateWeightPct != 20 ||
		result.RoutingBalance.PriorityWeightPct != 10 ||
		result.RoutingBalance.TTFTWindowMs != 900_000 || result.RoutingBalance.TTFTPenaltyUnitMs != 500 ||
		result.RoutingBalance.TTFTPenaltyPointsPerUnit != 1.5 ||
		result.RoutingBalance.ErrorWindowMs != 600_000 ||
		result.RoutingBalance.ErrorPenaltyPointsPerPercent != 4 {
		t.Fatalf("snapshot must transport the committed routing balance verbatim: %+v", result.RoutingBalance)
	}
}

func TestSnapshotManyRejectsNonExactRoutingBalanceShapes(t *testing.T) {
	// 单一 canonical：缺字段、未知字段、权重和≠100、非正窗口/惩罚参数以及任何旧 schema 都必须 fail-closed。
	cases := map[string]string{
		"missing field": `{"concurrency_weight_pct":20,"ttft_weight_pct":25,"error_rate_weight_pct":20,` +
			`"priority_weight_pct":10,"ttft_window_ms":1800000,"ttft_penalty_unit_ms":1000,` +
			`"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,"error_penalty_points_per_percent":2.5}`,
		"unknown field": `{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,` +
			`"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":1800000,` +
			`"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,` +
			`"error_penalty_points_per_percent":2.5,"bogus":1}`,
		"weights do not sum to 100": `{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,` +
			`"error_rate_weight_pct":20,"priority_weight_pct":5,"ttft_window_ms":1800000,` +
			`"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,` +
			`"error_penalty_points_per_percent":2.5}`,
		"negative weight": `{"cost_weight_pct":-5,"concurrency_weight_pct":25,"ttft_weight_pct":25,` +
			`"error_rate_weight_pct":30,"priority_weight_pct":25,"ttft_window_ms":1800000,` +
			`"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,` +
			`"error_penalty_points_per_percent":2.5}`,
		"zero ttft window": `{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,` +
			`"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":0,` +
			`"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,` +
			`"error_penalty_points_per_percent":2.5}`,
		"zero penalty unit": `{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,` +
			`"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":1800000,` +
			`"ttft_penalty_unit_ms":0,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,` +
			`"error_penalty_points_per_percent":2.5}`,
		"zero ttft penalty points": `{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,` +
			`"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":1800000,` +
			`"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":0,"error_window_ms":1800000,` +
			`"error_penalty_points_per_percent":2.5}`,
		"NaN ttft penalty points": `{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,` +
			`"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":1800000,` +
			`"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":NaN,"error_window_ms":1800000,` +
			`"error_penalty_points_per_percent":2.5}`,
		"NaN error penalty points": `{"cost_weight_pct":25,"concurrency_weight_pct":20,"ttft_weight_pct":25,` +
			`"error_rate_weight_pct":20,"priority_weight_pct":10,"ttft_window_ms":1800000,` +
			`"ttft_penalty_unit_ms":1000,"ttft_penalty_points_per_unit":2.5,"error_window_ms":1800000,` +
			`"error_penalty_points_per_percent":NaN}`,
		"legacy objective schema": `{"economic_weight_pct":45,"health_weight_pct":25,"capacity_weight_pct":20,` +
			`"priority_weight_pct":10,"ttft_target_ms":2000,"ttft_weight":0.35,"ttft_ewma_alpha":0.2}`,
		"legacy cost schema": `{"ttft_target_ms":2000,"ttft_weight":0.35,"cost_weight":0.5,` +
			`"minimum_routing_factor":0.05,"ttft_ewma_alpha":0.2}`,
	}
	for name, payload := range cases {
		t.Run(name, func(t *testing.T) {
			s, _, _ := newTestStore(t)
			cfg := testConfig()
			const channelID, providerID = int64(453), int64(4530)
			seedAttemptControlsWithRoutingBalance(
				t, s, cfg, channelID,
				`{"concurrency":null}`,
				payload,
			)
			if created, err := s.InitProviderControl(context.Background(), providerID, 1, 1, "enabled"); err != nil || !created {
				t.Fatalf("init origin: created=%v err=%v", created, err)
			}
			_, err := s.SnapshotMany(context.Background(), SnapshotManyInput{
				IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
				GlobalConcurrencyRevision: 1,
				CircuitBreakerRevision:    1, RoutingBalanceRevision: 1, ModelID: 100,
				Candidates: []SnapshotCandidateInput{{
					ProviderID: providerID, ChannelID: channelID,
					OriginRevision: 1, ProviderStatusRevision: 1,
					ChannelConfigRevision: 1, ChannelCapacityRevision: 1,
				}},
			})
			if failure.CodeOf(err) != failure.CodeGatewayRuntimeSyncRequired {
				t.Fatalf("malformed routing balance must fail closed: code=%q err=%v", failure.CodeOf(err), err)
			}
		})
	}
}

func TestSnapshotManyTreatsExpiredClosedWindowAsNoSampleWithoutMutation(t *testing.T) {
	s, client, _ := newTestStore(t)
	cfg := testConfig()
	const channelID, providerID = int64(454), int64(4540)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":null}`)
	if created, err := s.InitProviderControl(context.Background(), providerID, 1, 1, "enabled"); err != nil || !created {
		t.Fatalf("init origin: created=%v err=%v", created, err)
	}
	in := withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "expired-window", AdmissionFingerprint: "expired-window-fp", RequestAdmissionID: "req-expired-window",
		ProviderID: providerID, ChannelID: channelID,
		OriginRevision: 1, ProviderStatusRevision: 1, ChannelConfigRevision: 1,
		ModelID: 100, UpstreamEndpoint: EndpointResponses, RequestMode: ModeStream,
		EnforceProviderControl: true,
	})
	admission, err := acquireAttempt(t, s, in)
	if err != nil || admission.Mode != AdmissionPermit {
		t.Fatalf("acquire failure sample permit: admission=%+v err=%v", admission, err)
	}
	finish(t, s, cfg, admission.Permit, OutcomeEligibleFailure, OutcomeEligibleFailure)

	redisNow, err := client.Time(context.Background()).Result()
	if err != nil {
		t.Fatalf("redis time: %v", err)
	}
	expiredWindowStart := redisNow.Add(-time.Duration(cfg.WindowMs+1_000) * time.Millisecond).UnixMilli()
	if err := client.HSet(context.Background(), s.keys.provider(providerID), "window_started_at_ms", expiredWindowStart).Err(); err != nil {
		t.Fatalf("age origin window: %v", err)
	}
	if err := client.HSet(context.Background(), s.keys.channel(channelID),
		"window_started_at_ms", expiredWindowStart,
		"ttft_ewma_ms", 777,
		"ttft_samples", 5,
	).Err(); err != nil {
		t.Fatalf("age channel window: %v", err)
	}
	beforeOrigin, err := client.HGetAll(context.Background(), s.keys.provider(providerID)).Result()
	if err != nil {
		t.Fatalf("read origin before snapshot: %v", err)
	}
	beforeChannel, err := client.HGetAll(context.Background(), s.keys.channel(channelID)).Result()
	if err != nil {
		t.Fatalf("read channel before snapshot: %v", err)
	}

	result, err := s.SnapshotMany(context.Background(), SnapshotManyInput{
		IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
		GlobalConcurrencyRevision: 1,
		CircuitBreakerRevision:    1, RoutingBalanceRevision: 1, ModelID: 100,
		Candidates: []SnapshotCandidateInput{{
			ProviderID: providerID, ChannelID: channelID,
			OriginRevision: 1, ProviderStatusRevision: 1,
			ChannelConfigRevision: 1, ChannelCapacityRevision: 1,
		}},
	})
	if err != nil {
		t.Fatalf("snapshot expired closed window: %v", err)
	}
	snapshot := result.Candidates[0]
	if snapshot.Status != CandidateSnapshotCurrent {
		t.Fatalf("expired closed window must stay eligible, status=%s", snapshot.Status)
	}
	for _, scope := range []ScopeSnapshot{snapshot.Provider, snapshot.Channel} {
		if scope.SampleCount != 0 || scope.EligibleSuccesses != 0 || scope.EligibleFailures != 0 || scope.ErrorRate != 0 {
			t.Fatalf("expired closed scope must score as no-sample: %+v", scope)
		}
	}
	afterOrigin, _ := client.HGetAll(context.Background(), s.keys.provider(providerID)).Result()
	afterChannel, _ := client.HGetAll(context.Background(), s.keys.channel(channelID)).Result()
	if !reflect.DeepEqual(afterOrigin, beforeOrigin) || !reflect.DeepEqual(afterChannel, beforeChannel) {
		t.Fatalf("SnapshotMany must not mutate expired breaker state: origin before=%v after=%v channel before=%v after=%v",
			beforeOrigin, afterOrigin, beforeChannel, afterChannel)
	}
}

func TestSnapshotManyFailsClosedOnMarkerOrPendingControl(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	seedAttemptControls(t, s, cfg, 461, `{"concurrency":null}`)
	if _, err := s.InitProviderControl(context.Background(), 4610, 1, 1, "enabled"); err != nil {
		t.Fatal(err)
	}
	input := SnapshotManyInput{
		IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
		GlobalConcurrencyRevision: 1, CircuitBreakerRevision: 1, RoutingBalanceRevision: 1,
		ModelID: 100,
		Candidates: []SnapshotCandidateInput{{
			ProviderID: 4610, ChannelID: 461, OriginRevision: 1, ProviderStatusRevision: 1,
			ChannelConfigRevision: 1, ChannelCapacityRevision: 1,
		}},
	}
	staleEpoch := input
	staleEpoch.IntegrityEpoch = "different-epoch"
	if _, err := s.SnapshotMany(context.Background(), staleEpoch); failure.CodeOf(err) != failure.CodeGatewayRuntimeStateLost {
		t.Fatalf("stale marker must fail closed, code=%q err=%v", failure.CodeOf(err), err)
	}
	balanceKey := s.keys.runtimeControlSetting("gateway.routing_balance")
	if err := s.client.HSet(context.Background(), balanceKey, "active_payload_hash", strings.Repeat("a", 64)).Err(); err != nil {
		t.Fatal(err)
	}
	if _, err := s.SnapshotMany(context.Background(), input); failure.CodeOf(err) != failure.CodeGatewayRuntimeSyncRequired {
		t.Fatalf("payload hash mismatch must fail whole batch, code=%q err=%v", failure.CodeOf(err), err)
	}
	if err := s.client.HSet(context.Background(), balanceKey, "active_payload_hash", HashPayload(testRoutingBalancePayload)).Err(); err != nil {
		t.Fatal(err)
	}
	if code, _, err := s.PrepareControl(context.Background(), s.SettingControl("gateway.routing_balance"),
		"snapshot-pending", 1, 2, testRoutingBalancePayload); err != nil || code != ControlPrepared {
		t.Fatalf("prepare pending balance: code=%s err=%v", code, err)
	}
	if _, err := s.SnapshotMany(context.Background(), input); failure.CodeOf(err) != failure.CodeGatewayRuntimeSyncRequired {
		t.Fatalf("pending control must fail whole batch, code=%q err=%v", failure.CodeOf(err), err)
	}
}

// TestAcquireRotatesChannelRevisionState 验证新 revision 原子清旧样本，旧 revision 与同 revision 换 Origin 被拒绝。
func TestAcquireRotatesChannelRevisionState(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	seedAttemptControls(t, s, cfg, 501, `{"concurrency":null}`)
	first, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "rotate-v1", AdmissionFingerprint: "rotate-v1-fp", RequestAdmissionID: "req",
		ProviderID: 5010, ChannelID: 501, OriginRevision: 1, ProviderStatusRevision: 1,
		ChannelConfigRevision: 1, ModelID: 100, UpstreamEndpoint: EndpointMessages, RequestMode: ModeStream,
	}))
	if err != nil || first.Mode != AdmissionPermit {
		t.Fatalf("acquire v1: mode=%s reason=%s err=%v", first.Mode, first.Reason, err)
	}
	if _, err := s.Finish(context.Background(), *first.Permit, FinishOutcome{
		ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleFailure,
		RequestWriteState: RequestWriteCompleted, FirstTokenEligible: true,
	}); err != nil {
		t.Fatalf("finish v1: %v", err)
	}

	v2Input := withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "rotate-v2", AdmissionFingerprint: "rotate-v2-fp", RequestAdmissionID: "req",
		ProviderID: 5010, ChannelID: 501, OriginRevision: 1, ProviderStatusRevision: 1,
		ChannelConfigRevision: 2, ModelID: 100, UpstreamEndpoint: EndpointMessages, RequestMode: ModeNonStream,
	})
	v2, err := acquireAttempt(t, s, v2Input)
	if err != nil || v2.Mode != AdmissionPermit {
		t.Fatalf("acquire v2: mode=%s reason=%s err=%v", v2.Mode, v2.Reason, err)
	}
	snapshot, err := s.Snapshot(context.Background(), ScopeChannel, 501)
	if err != nil {
		t.Fatalf("snapshot v2: %v", err)
	}
	if snapshot.ChannelConfigRevision != 2 || snapshot.SampleCount != 0 {
		t.Fatalf("v2 must rotate to closed/no-sample: %+v", snapshot)
	}
	if err := s.Abort(context.Background(), *v2.Permit); err != nil {
		t.Fatalf("abort v2: %v", err)
	}

	stale := v2Input
	stale.PermitID = "rotate-stale"
	stale.AdmissionFingerprint = "rotate-stale-fp"
	stale.ChannelConfigRevision = 1
	if adm, err := acquireAttempt(t, s, stale); err != nil || adm.Mode != AdmissionDenied || adm.Reason != ReasonStaleConfigRevision {
		t.Fatalf("old config want stale_config_revision, got %s/%s err=%v", adm.Mode, adm.Reason, err)
	}

	wrongOrigin := v2Input
	wrongOrigin.PermitID = "rotate-wrong-origin"
	wrongOrigin.AdmissionFingerprint = "rotate-wrong-origin-fp"
	wrongOrigin.ProviderID = 5099
	if adm, err := acquireAttempt(t, s, wrongOrigin); err != nil || adm.Mode != AdmissionDenied || adm.Reason != ReasonStaleConfigRevision {
		t.Fatalf("same config with different origin want stale_config_revision, got %s/%s err=%v", adm.Mode, adm.Reason, err)
	}
}

// TestAcquireAndFinishRejectInvalidInputBeforeRedisWrite 验证非法负数和 enum 在脚本执行前被拒绝。
func TestAcquireAndFinishRejectInvalidInputBeforeRedisWrite(t *testing.T) {
	s, client, ns := newTestStore(t)
	cfg := testConfig()
	base := AcquireAttemptInput{
		PermitID: "invalid-acquire", AdmissionFingerprint: "invalid-acquire-fp", RequestAdmissionID: "req",
		IntegrityEpoch: testAttemptIntegrityEpoch, IntegrityRevision: testAttemptIntegrityRevision,
		ProviderID: 6010, ChannelID: 601, OriginRevision: 1, ProviderStatusRevision: 1,
		ChannelConfigRevision: 1, ModelID: 100, UpstreamEndpoint: EndpointChatCompletions,
		RequestMode:               ModeNonStream,
		GlobalConcurrencyRevision: 1,
		CircuitBreakerRevision:    1, ChannelCapacityRevision: 1,
	}
	invalidAcquireCases := []struct {
		name   string
		mutate func(*AcquireAttemptInput)
	}{
		{name: "operation enum", mutate: func(in *AcquireAttemptInput) { in.UpstreamEndpoint = UpstreamEndpoint("invalid") }},
		{name: "request mode enum", mutate: func(in *AcquireAttemptInput) { in.RequestMode = RequestMode("invalid") }},
		{name: "negative token estimate", mutate: func(in *AcquireAttemptInput) { in.InputEstimate = -1 }},
		{name: "missing control revision", mutate: func(in *AcquireAttemptInput) { in.CircuitBreakerRevision = 0 }},
	}
	for _, tc := range invalidAcquireCases {
		invalid := base
		tc.mutate(&invalid)
		if _, err := s.AcquireAttempt(context.Background(), invalid); failure.CodeOf(err) != failure.CodeConfigInvalid {
			t.Fatalf("invalid acquire %s want config_invalid, got %v", tc.name, err)
		}
	}
	keys, err := client.Keys(context.Background(), ns+":*").Result()
	if err != nil {
		t.Fatalf("list namespace keys: %v", err)
	}
	if len(keys) != 1 || keys[0] != s.keys.runtimeReconciliationProof() {
		t.Fatalf("invalid acquire must not write Redis, got keys %v", keys)
	}

	valid := base
	valid.PermitID = "valid-before-invalid-finish"
	valid.AdmissionFingerprint = "valid-before-invalid-finish-fp"
	seedAttemptControls(t, s, cfg, 601, `{"concurrency":null}`)
	adm, err := acquireAttempt(t, s, valid)
	if err != nil || adm.Mode != AdmissionPermit {
		t.Fatalf("valid acquire: mode=%s reason=%s err=%v", adm.Mode, adm.Reason, err)
	}
	finishCases := []struct {
		name    string
		outcome FinishOutcome
	}{
		{name: "outcome enum", outcome: FinishOutcome{ProviderOutcome: Outcome("invalid"), ChannelOutcome: OutcomeEligibleFailure}},
		{name: "evidence enum", outcome: FinishOutcome{ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleFailure, ProviderEvidence: ProviderEvidenceCategory("invalid")}},
		{name: "evidence requires channel failure", outcome: FinishOutcome{ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeIgnored, ProviderEvidence: ProviderEvidenceHTTP500}},
	}
	for _, tc := range finishCases {
		if _, err := s.Finish(context.Background(), *adm.Permit, tc.outcome); failure.CodeOf(err) != failure.CodeConfigInvalid {
			t.Fatalf("invalid finish %s want config_invalid, got %v", tc.name, err)
		}
	}
	status, err := client.HGet(context.Background(), s.keys.permit(adm.Permit.PermitID), "status").Result()
	if err != nil || status != "active" {
		t.Fatalf("invalid finish must leave permit active, status=%q err=%v", status, err)
	}
	snapshot, err := s.Snapshot(context.Background(), ScopeChannel, 601)
	if err != nil || snapshot.SampleCount != 0 {
		t.Fatalf("invalid finish must not add breaker samples: %+v err=%v", snapshot, err)
	}
	if err := s.Abort(context.Background(), *adm.Permit); err != nil {
		t.Fatalf("abort after invalid finish: %v", err)
	}
}

// TestProviderAmbiguousEvidenceRequiresDistinctChannelsAndModels 验证条件故障只有在同一类别、同一短窗内
// 同时满足 distinct Channel 与 model 门槛后，才把当前 Finish 计入 Origin。
func TestProviderAmbiguousEvidenceRequiresDistinctChannelsAndModels(t *testing.T) {
	s, client, _ := newTestStore(t)
	cfg := testConfig()
	const providerID int64 = 6110

	acquireEvidence := func(permitID string, channelID, modelID int64) *AttemptPermit {
		seedAttemptControls(t, s, cfg, channelID, `{"concurrency":null}`)
		adm, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
			PermitID: permitID, AdmissionFingerprint: permitID + "-fp", RequestAdmissionID: "req-" + permitID,
			ProviderID: providerID, ChannelID: channelID,
			OriginRevision: 1, ProviderStatusRevision: 1, ChannelConfigRevision: 1,
			ModelID: modelID, UpstreamEndpoint: EndpointChatCompletions, RequestMode: ModeNonStream,
		}))
		if err != nil || adm.Mode != AdmissionPermit || adm.Permit == nil {
			t.Fatalf("acquire %s: mode=%s reason=%s err=%v", permitID, adm.Mode, adm.Reason, err)
		}
		return adm.Permit
	}
	finishEvidence := func(permit *AttemptPermit, category ProviderEvidenceCategory) FinishResult {
		res, err := s.Finish(context.Background(), *permit, FinishOutcome{
			ProviderOutcome:   OutcomeIgnored,
			ChannelOutcome:    OutcomeEligibleFailure,
			ProviderEvidence:  category,
			RequestWriteState: RequestWriteCompleted,
		})
		if err != nil {
			t.Fatalf("finish %s: %v", permit.PermitID, err)
		}
		return res
	}

	first := finishEvidence(acquireEvidence("evidence-1", 611, 1001), ProviderEvidenceHTTP500)
	second := finishEvidence(acquireEvidence("evidence-2", 611, 1002), ProviderEvidenceHTTP500)
	if first.ProviderDisposition != DispositionNotApplicable || second.ProviderDisposition != DispositionNotApplicable {
		t.Fatalf("single distinct channel must not count origin: first=%s second=%s", first.ProviderDisposition, second.ProviderDisposition)
	}
	before, err := s.Snapshot(context.Background(), ScopeProvider, providerID)
	if err != nil || before.EligibleFailures != 0 {
		t.Fatalf("origin gained failure before both thresholds: %+v err=%v", before, err)
	}

	third := finishEvidence(acquireEvidence("evidence-3", 612, 1002), ProviderEvidenceHTTP500)
	if third.ProviderDisposition != DispositionApplied {
		t.Fatalf("threshold-crossing finish disposition=%s, want applied", third.ProviderDisposition)
	}
	after, err := s.Snapshot(context.Background(), ScopeProvider, providerID)
	if err != nil || after.EligibleFailures != 1 {
		t.Fatalf("threshold-crossing finish must add one origin failure: %+v err=%v", after, err)
	}

	channelEvidence := s.keys.providerEvidenceChannels(providerID, string(ProviderEvidenceHTTP500))
	modelEvidence := s.keys.providerEvidenceModels(providerID, string(ProviderEvidenceHTTP500))
	if got := client.SCard(context.Background(), channelEvidence).Val(); got != 2 {
		t.Fatalf("bounded distinct channel evidence=%d, want 2", got)
	}
	if got := client.SCard(context.Background(), modelEvidence).Val(); got != 2 {
		t.Fatalf("bounded distinct model evidence=%d, want 2", got)
	}

	if _, err := s.Reset(context.Background(), ScopeProvider, providerID); err != nil {
		t.Fatalf("reset origin: %v", err)
	}
	if got := client.Exists(context.Background(), channelEvidence, modelEvidence).Val(); got != 0 {
		t.Fatalf("origin reset must clear ambiguous evidence, existing keys=%d", got)
	}
}

// TestProviderAmbiguousEvidenceCategoriesDoNotMix 验证 HTTP 500 与 timeout 各自维护独立短窗。
func TestProviderAmbiguousEvidenceCategoriesDoNotMix(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()
	const providerID int64 = 6210

	finishOne := func(permitID string, channelID, modelID int64, category ProviderEvidenceCategory) {
		seedAttemptControls(t, s, cfg, channelID, `{"concurrency":null}`)
		adm, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
			PermitID: permitID, AdmissionFingerprint: permitID + "-fp", RequestAdmissionID: "req-" + permitID,
			ProviderID: providerID, ChannelID: channelID,
			OriginRevision: 1, ProviderStatusRevision: 1, ChannelConfigRevision: 1,
			ModelID: modelID, UpstreamEndpoint: EndpointResponses, RequestMode: ModeStream,
		}))
		if err != nil || adm.Permit == nil {
			t.Fatalf("acquire %s: %+v err=%v", permitID, adm, err)
		}
		res, err := s.Finish(context.Background(), *adm.Permit, FinishOutcome{
			ProviderOutcome: OutcomeIgnored, ChannelOutcome: OutcomeEligibleFailure, ProviderEvidence: category,
			RequestWriteState: RequestWriteCompleted,
		})
		if err != nil || res.ProviderDisposition != DispositionNotApplicable {
			t.Fatalf("finish %s: result=%+v err=%v", permitID, res, err)
		}
	}

	finishOne("mixed-500", 621, 2001, ProviderEvidenceHTTP500)
	finishOne("mixed-timeout", 622, 2002, ProviderEvidenceFirstTokenTimeout)
	snapshot, err := s.Snapshot(context.Background(), ScopeProvider, providerID)
	if err != nil || snapshot.EligibleFailures != 0 {
		t.Fatalf("different evidence categories must not combine: %+v err=%v", snapshot, err)
	}
}

func TestOriginFenceMakesExistingPermitBreakerResultStale(t *testing.T) {
	s, client, _ := newTestStore(t)
	cfg := testConfig()
	seedAttemptControls(t, s, cfg, 631, `{"concurrency":1}`)
	if created, err := s.InitProviderControl(context.Background(), 6310, 1, 1, "enabled"); err != nil || !created {
		t.Fatalf("init origin control: created=%v err=%v", created, err)
	}
	adm, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "status-fenced-finish", AdmissionFingerprint: "status-fenced-finish-fp",
		RequestAdmissionID: "req-status-fenced-finish",
		ProviderID:         6310, ChannelID: 631,
		OriginRevision: 1, ProviderStatusRevision: 1, ChannelConfigRevision: 1,
		ModelID: 3001, UpstreamEndpoint: EndpointMessages, RequestMode: ModeStream,
		EnforceProviderControl: true,
	}))
	if err != nil || adm.Permit == nil {
		t.Fatalf("acquire permit: %+v err=%v", adm, err)
	}
	concurrencyKey := s.keys.channel(631) + ":conc"
	leaseBeforeFence, err := client.ZScore(context.Background(), concurrencyKey, adm.Permit.PermitID).Result()
	if err != nil {
		t.Fatalf("read initial concurrency lease: %v", err)
	}
	payload := `{"provider_id":6310,"current_status_revision":1,"next_status_revision":2}`
	if result, err := s.PrepareProviderStatusRevision(context.Background(), 6310, 1, 2, "disabled", "status-fence", payload); err != nil || result != FenceResult("prepared") {
		t.Fatalf("prepare status fence: result=%s err=%v", result, err)
	}
	time.Sleep(5 * time.Millisecond)
	if err := s.Renew(context.Background(), *adm.Permit); err != nil {
		t.Fatalf("renew pre-fence permit: %v", err)
	}
	leaseAfterFence, err := client.ZScore(context.Background(), concurrencyKey, adm.Permit.PermitID).Result()
	if err != nil {
		t.Fatalf("read renewed concurrency lease: %v", err)
	}
	if leaseAfterFence <= leaseBeforeFence {
		t.Fatalf("fenced permit concurrency lease was not renewed: before=%v after=%v", leaseBeforeFence, leaseAfterFence)
	}

	denied, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "status-fenced-new", AdmissionFingerprint: "status-fenced-new-fp",
		RequestAdmissionID: "req-status-fenced-new",
		ProviderID:         6310, ChannelID: 631,
		OriginRevision: 1, ProviderStatusRevision: 1, ChannelConfigRevision: 1,
		ModelID: 3001, UpstreamEndpoint: EndpointMessages, RequestMode: ModeStream,
		EnforceProviderControl: true,
	}))
	if err != nil || denied.Mode != AdmissionDenied || denied.Reason != ReasonRuntimeSyncRequired {
		t.Fatalf("new permit during status fence: admission=%+v err=%v", denied, err)
	}
	result, err := s.Finish(context.Background(), *adm.Permit, FinishOutcome{
		ProviderOutcome:    OutcomeEligibleSuccess,
		ChannelOutcome:     OutcomeEligibleSuccess,
		RequestWriteState:  RequestWriteCompleted,
		FirstTokenEligible: true,
	})
	if err != nil {
		t.Fatalf("finish old permit: %v", err)
	}
	if result.ProviderDisposition != DispositionStaleStatusRev || result.ChannelDisposition != DispositionStaleStatusRev {
		t.Fatalf("old permit dispositions=%s/%s, want stale status for both scopes", result.ProviderDisposition, result.ChannelDisposition)
	}
	origin, _ := s.Snapshot(context.Background(), ScopeProvider, 6310)
	channel, _ := s.Snapshot(context.Background(), ScopeChannel, 631)
	if origin.SampleCount != 0 || channel.SampleCount != 0 {
		t.Fatalf("fenced finish changed current runtime: origin=%+v channel=%+v", origin, channel)
	}
	if used := client.ZCard(context.Background(), concurrencyKey).Val(); used != 0 {
		t.Fatalf("fenced finish leaked channel concurrency: used=%d", used)
	}
}

func TestOriginCommitClearsAllAmbiguousEvidence(t *testing.T) {
	s, client, _ := newTestStore(t)
	const providerID int64 = 6410
	if created, err := s.InitProviderControl(context.Background(), providerID, 1, 1, "enabled"); err != nil || !created {
		t.Fatalf("init origin control: created=%v err=%v", created, err)
	}
	for _, category := range []ProviderEvidenceCategory{
		ProviderEvidenceHTTP500,
		ProviderEvidenceFirstTokenTimeout,
		ProviderEvidenceBodyReadTimeout,
	} {
		keys := s.providerEvidenceKeys(providerID, category)
		if err := client.SAdd(context.Background(), keys[0], 1).Err(); err != nil {
			t.Fatalf("seed channel evidence: %v", err)
		}
		if err := client.SAdd(context.Background(), keys[1], 2).Err(); err != nil {
			t.Fatalf("seed model evidence: %v", err)
		}
	}
	payload := `{"provider_id":6410,"current_origin_revision":1,"next_origin_revision":2}`
	if result, err := s.PrepareOriginRevision(context.Background(), providerID, 1, 2, "base-fence", payload); err != nil || result != FenceResult("prepared") {
		t.Fatalf("prepare base url fence: result=%s err=%v", result, err)
	}
	if result, err := s.CommitOriginRevision(context.Background(), providerID, "base-fence", payload); err != nil || result != FenceResult("committed") {
		t.Fatalf("commit base url fence: result=%s err=%v", result, err)
	}
	if existing := client.Exists(context.Background(), s.allProviderEvidenceKeys(providerID)...).Val(); existing != 0 {
		t.Fatalf("base url commit left %d evidence keys", existing)
	}
}

// TestOriginBreakerIndependent 验证 Origin 作用域独立于 Channel。
func TestOriginBreakerIndependent(t *testing.T) {
	s, _, _ := newTestStore(t)
	cfg := testConfig()

	// 对 origin 记 3 次可归因失败（channel ignored）→ origin open, channel closed。
	for i := 0; i < 3; i++ {
		adm := acquire(t, s, cfg, fmt.Sprintf("e%d", i), 11, 110)
		finish(t, s, cfg, adm.Permit, OutcomeEligibleFailure, OutcomeIgnored)
	}
	epSnap, _ := s.Snapshot(context.Background(), ScopeProvider, 110)
	chSnap, _ := s.Snapshot(context.Background(), ScopeChannel, 11)
	if epSnap.State != StateOpen {
		t.Fatalf("origin want open, got %s", epSnap.State)
	}
	if chSnap.State != StateClosed {
		t.Fatalf("channel want closed (origin failures ignored for channel), got %s", chSnap.State)
	}
	// origin open → 后续 Acquire denied(open)。
	adm := acquire(t, s, cfg, "e-after", 11, 110)
	if adm.Mode != AdmissionDenied || adm.Reason != ReasonOpen {
		t.Fatalf("want denied/open due to origin, got %s/%s", adm.Mode, adm.Reason)
	}
}
