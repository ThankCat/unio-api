package lifecycle

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func concurrencyFullDenial() breakerstore.AttemptAdmission {
	return breakerstore.AttemptAdmission{
		Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonConcurrencyFull,
	}
}

func cooldownDenial(remainingMs int64) breakerstore.AttemptAdmission {
	return breakerstore.AttemptAdmission{
		Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonCooldown,
		CooldownRemainingMs: remainingMs,
	}
}

func capacityWaitCandidates(ids ...int64) []Candidate {
	out := make([]Candidate, 0, len(ids))
	for _, id := range ids {
		candidate := permitGuardCandidate()
		candidate.Channel.ID = id
		out = append(out, Candidate{Route: candidate})
	}
	return out
}

// TestCapacityWaitEntersOnlyWhenWholePoolIsConcurrencyFull 冻结 §9.3 的唯一进入条件。
func TestCapacityWaitEntersOnlyWhenWholePoolIsConcurrencyFull(t *testing.T) {
	tests := []struct {
		name      string
		denials   []breakerstore.AttemptAdmission
		wantWait  bool
		wantScans int
	}{
		{
			name:      "all concurrency full waits once and rescans the whole pool",
			denials:   []breakerstore.AttemptAdmission{concurrencyFullDenial(), concurrencyFullDenial()},
			wantWait:  true,
			wantScans: 4, // 两轮 x 两个候选
		},
		{
			name:      "cooldown must never masquerade as a concurrency wait",
			denials:   []breakerstore.AttemptAdmission{cooldownDenial(2_000), cooldownDenial(3_000)},
			wantWait:  false,
			wantScans: 2,
		},
		{
			name: "mixed cooldown and concurrency does not wait",
			denials: []breakerstore.AttemptAdmission{
				concurrencyFullDenial(), cooldownDenial(2_000),
			},
			wantWait:  false,
			wantScans: 2,
		},
		{
			name: "breaker open does not wait",
			denials: []breakerstore.AttemptAdmission{
				{Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonOpen},
				concurrencyFullDenial(),
			},
			wantWait:  false,
			wantScans: 2,
		},
		{
			name: "model permission paused does not wait",
			denials: []breakerstore.AttemptAdmission{
				{Mode: breakerstore.AdmissionDenied, Reason: breakerstore.ReasonModelPermissionPaused},
				concurrencyFullDenial(),
			},
			wantWait:  false,
			wantScans: 2,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
			runner, store, _, ctx := newPermitGuardRunner(log)
			// 两轮扫描共 4 次 acquire；预算设小以保持测试快速。
			store.acquireResults = append(append([]breakerstore.AttemptAdmission{}, tc.denials...), tc.denials...)
			runner.SetCapacityWaitTimeout(80 * time.Millisecond)

			start := time.Now()
			_, err := runner.RunNonStream(ctx, RunNonStreamParams{Candidates: capacityWaitCandidates(1, 2)})
			elapsed := time.Since(start)

			if err == nil {
				t.Fatal("expected a routing failure when the whole pool is denied")
			}
			if store.acquireCalls != tc.wantScans {
				t.Fatalf("acquire calls = %d, want %d (wait=%v)", store.acquireCalls, tc.wantScans, tc.wantWait)
			}
			if tc.wantWait && elapsed < 60*time.Millisecond {
				t.Fatalf("expected a bounded wait before rescanning, elapsed=%v", elapsed)
			}
			if !tc.wantWait && elapsed > 60*time.Millisecond {
				t.Fatalf("must not wait for non-concurrency denials, elapsed=%v", elapsed)
			}
		})
	}
}

// TestCapacityWaitIsSharedNotPerChannel 冻结 §9.4：等待是整请求一次，总时长不随渠道数线性增长。
func TestCapacityWaitIsSharedNotPerChannel(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
	runner, store, _, ctx := newPermitGuardRunner(log)
	const channels = 6
	ids := make([]int64, 0, channels)
	for i := 1; i <= channels; i++ {
		ids = append(ids, int64(i))
	}
	denials := make([]breakerstore.AttemptAdmission, 0, channels*2)
	for i := 0; i < channels*2; i++ {
		denials = append(denials, concurrencyFullDenial())
	}
	store.acquireResults = denials
	budget := 120 * time.Millisecond
	runner.SetCapacityWaitTimeout(budget)

	start := time.Now()
	_, err := runner.RunNonStream(ctx, RunNonStreamParams{Candidates: capacityWaitCandidates(ids...)})
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected capacity exhaustion")
	}
	if store.acquireCalls != channels*2 {
		t.Fatalf("acquire calls = %d, want exactly two full-pool scans (%d)", store.acquireCalls, channels*2)
	}
	// 若按渠道各等一次，总时长会接近 6*budget。
	if elapsed > 3*budget {
		t.Fatalf("wait must be shared across the pool: elapsed=%v budget=%v", elapsed, budget)
	}
}

// TestCapacityWaitStillFullReturnsServiceUnavailable 冻结 §9.5：等待后仍满 → 503 容量耗尽。
func TestCapacityWaitStillFullReturnsServiceUnavailable(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
	runner, store, _, ctx := newPermitGuardRunner(log)
	store.acquireResults = []breakerstore.AttemptAdmission{
		concurrencyFullDenial(), concurrencyFullDenial(),
	}
	runner.SetCapacityWaitTimeout(40 * time.Millisecond)

	result, err := runner.RunNonStream(ctx, RunNonStreamParams{Candidates: capacityWaitCandidates(1)})
	if failure.CodeOf(err) != failure.CodeRoutingChannelCapacityExhausted {
		t.Fatalf("code = %q, want %q (err=%v)",
			failure.CodeOf(err), failure.CodeRoutingChannelCapacityExhausted, err)
	}
	// Retry-After 必须是可证明的 1 秒（§9.5）。
	if got := ProvableRetryAfter(err); got != time.Second {
		t.Fatalf("provable retry after = %v, want 1s", got)
	}
	if result.CapacityWaitResult != string(capacityWaitCapacityExhausted) {
		t.Fatalf("capacity wait result = %q, want %q", result.CapacityWaitResult, capacityWaitCapacityExhausted)
	}
}

// TestCapacityWaitResultReflectsSuccessfulRescan 冻结 trace 口径：等待预算结束只是触发重扫，
// capacity_wait_result 必须记录重扫取得 permit，而不是提前固定成 capacity_exhausted。
func TestCapacityWaitResultReflectsSuccessfulRescan(t *testing.T) {
	for _, stream := range []bool{false, true} {
		name := "non_stream"
		if stream {
			name = "stream"
		}
		t.Run(name, func(t *testing.T) {
			log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
			runner, store, _, ctx := newPermitGuardRunner(log)
			store.acquireResults = []breakerstore.AttemptAdmission{
				concurrencyFullDenial(), store.acquireResult,
			}
			runner.SetCapacityWaitTimeout(30 * time.Millisecond)
			stopErr := errors.New("stop after acquired rescan")

			var result RunResult
			if stream {
				result, _ = RunStreamGeneric(ctx, runner, RunStreamParamsGeneric[struct{}]{
					Candidates: capacityWaitCandidates(1),
					Stream: func(context.Context, routing.ChatRouteCandidate, func(struct{}) error) (*adapter.ResponseFacts, error) {
						return nil, stopErr
					},
					ChunkMeta: func(struct{}) StreamChunkMeta { return StreamChunkMeta{} },
				})
			} else {
				result, _ = runner.RunNonStream(ctx, RunNonStreamParams{
					Candidates: capacityWaitCandidates(1),
					Invoke: func(context.Context, routing.ChatRouteCandidate) (AttemptSuccess, error) {
						return AttemptSuccess{}, stopErr
					},
				})
			}

			if result.CapacityWaitResult != string(capacityWaitAcquired) {
				t.Fatalf("capacity wait result = %q, want %q", result.CapacityWaitResult, capacityWaitAcquired)
			}
			if result.CapacityWaitMs == nil {
				t.Fatal("capacity wait duration must be recorded after a rescan")
			}
		})
	}
}

// TestCapacityWaitResultReflectsCooldownRescan 冻结 §9.5：等待后全池变成 429 时，
// trace 的等待结果与最终 429 必须一致。
func TestCapacityWaitResultReflectsCooldownRescan(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
	runner, store, _, ctx := newPermitGuardRunner(log)
	store.acquireResults = []breakerstore.AttemptAdmission{
		concurrencyFullDenial(), cooldownDenial(1_500),
	}
	runner.SetCapacityWaitTimeout(30 * time.Millisecond)

	result, err := runner.RunNonStream(ctx, RunNonStreamParams{Candidates: capacityWaitCandidates(1)})
	if failure.CodeOf(err) != failure.CodeGatewayChannelRateLimited {
		t.Fatalf("code = %q, want %q", failure.CodeOf(err), failure.CodeGatewayChannelRateLimited)
	}
	if result.CapacityWaitResult != string(capacityWaitRateLimited) {
		t.Fatalf("capacity wait result = %q, want %q", result.CapacityWaitResult, capacityWaitRateLimited)
	}
}

// TestCapacityWaitAllCooldownReturnsRateLimitedWithAccurateRetryAfter 冻结 §6.3/§9.5：
// 全池 429 冷却直接返回限流错误，并带上最短剩余冷却作为 Retry-After 依据。
func TestCapacityWaitAllCooldownReturnsRateLimitedWithAccurateRetryAfter(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
	runner, store, _, ctx := newPermitGuardRunner(log)
	store.acquireResults = []breakerstore.AttemptAdmission{
		cooldownDenial(5_000), cooldownDenial(1_500), cooldownDenial(9_000),
	}

	_, err := runner.RunNonStream(ctx, RunNonStreamParams{Candidates: capacityWaitCandidates(1, 2, 3)})
	if failure.CodeOf(err) != failure.CodeGatewayChannelRateLimited {
		t.Fatalf("code = %q, want %q (err=%v)",
			failure.CodeOf(err), failure.CodeGatewayChannelRateLimited, err)
	}
	var retryAfter any
	for _, field := range failure.FieldsOf(err) {
		if field.Key == "retry_after_ms" {
			retryAfter = field.Value
		}
	}
	if retryAfter != int64(1_500) {
		t.Fatalf("retry_after_ms = %v, want the shortest remaining cooldown 1500", retryAfter)
	}
}

// TestCapacityWaitDisabledByZeroBudget 验证 gateway.capacity_wait_timeout_ms=0 关闭短等：
// 并发满立即 failover/返回，不重扫。
func TestCapacityWaitDisabledByZeroBudget(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
	runner, store, _, ctx := newPermitGuardRunner(log)
	store.acquireResults = []breakerstore.AttemptAdmission{
		concurrencyFullDenial(), concurrencyFullDenial(),
	}
	runner.SetCapacityWaitTimeout(0)

	start := time.Now()
	if _, err := runner.RunNonStream(ctx, RunNonStreamParams{Candidates: capacityWaitCandidates(1)}); err == nil {
		t.Fatal("expected capacity exhaustion")
	}
	if store.acquireCalls != 1 {
		t.Fatalf("acquire calls = %d, want a single scan when the wait is disabled", store.acquireCalls)
	}
	if elapsed := time.Since(start); elapsed > 40*time.Millisecond {
		t.Fatalf("zero budget must not sleep, elapsed=%v", elapsed)
	}
}

// TestCapacityWaitStopsOnClientCancel 冻结 §9.4：等待期间客户取消立即返回，不再重扫。
func TestCapacityWaitStopsOnClientCancel(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
	runner, store, _, ctx := newPermitGuardRunner(log)
	store.acquireResults = []breakerstore.AttemptAdmission{
		concurrencyFullDenial(), concurrencyFullDenial(),
	}
	runner.SetCapacityWaitTimeout(2 * time.Second)

	cancelCtx, cancel := context.WithCancel(ctx)
	go func() {
		time.Sleep(30 * time.Millisecond)
		cancel()
	}()

	start := time.Now()
	if _, err := runner.RunNonStream(cancelCtx, RunNonStreamParams{Candidates: capacityWaitCandidates(1)}); err == nil {
		t.Fatal("expected a routing failure")
	}
	if elapsed := time.Since(start); elapsed > time.Second {
		t.Fatalf("cancel must abandon the wait promptly, elapsed=%v", elapsed)
	}
	if store.acquireCalls != 1 {
		t.Fatalf("acquire calls = %d, want no rescan after cancel", store.acquireCalls)
	}
}

// TestRescanNeverRetriesAnAlreadyAttemptedChannel 冻结 §3.5：禁止 A -> B -> A。
// 第一个候选真实发起过上游调用后，即使它在重扫中重新排到前面也不得再被调用。
func TestRescanNeverRetriesAnAlreadyAttemptedChannel(t *testing.T) {
	log := &permitGuardPanicLog{attemptAuditLog: &attemptAuditLog{}}
	runner, store, _, ctx := newPermitGuardRunner(log)
	permit := store.acquireResult
	store.acquireResults = []breakerstore.AttemptAdmission{permit, permit}
	runner.SetCapacityWaitTimeout(50 * time.Millisecond)

	transportErr := errors.New("upstream failed")
	var invoked []int64
	candidates := capacityWaitCandidates(7, 7) // 同一 Channel 出现两次（例如不同 route index）
	_, err := runner.RunNonStream(ctx, RunNonStreamParams{
		Candidates: candidates,
		Invoke: func(_ context.Context, candidate routing.ChatRouteCandidate) (AttemptSuccess, error) {
			invoked = append(invoked, candidate.Channel.ID)
			return AttemptSuccess{}, transportErr
		},
	})
	if err == nil {
		t.Fatal("expected the transport error to surface")
	}
	if len(invoked) != 1 || invoked[0] != 7 {
		t.Fatalf("channel 7 must be invoked exactly once, got %v", invoked)
	}
}

// TestCapacityWaitPollIntervalStaysInJitterWindow 冻结 §9.4 的 75±25ms 轮询窗口。
func TestCapacityWaitPollIntervalStaysInJitterWindow(t *testing.T) {
	for i := 0; i < 200; i++ {
		d := capacityWaitPollInterval()
		if d < 50*time.Millisecond || d > 100*time.Millisecond {
			t.Fatalf("poll interval %v outside [50ms,100ms]", d)
		}
	}
}

// TestCapacityWaitTimeoutFallsBackToDefault 验证未配置时使用冻结默认 1000ms，负值视为关闭。
func TestCapacityWaitTimeoutFallsBackToDefault(t *testing.T) {
	var policy capacityWaitPolicy
	if got := policy.Timeout(); got != defaultCapacityWaitTimeout {
		t.Fatalf("unset timeout = %v, want default %v", got, defaultCapacityWaitTimeout)
	}
	policy.SetCapacityWaitTimeout(0)
	if got := policy.Timeout(); got != 0 {
		t.Fatalf("zero timeout = %v, want 0 (wait disabled)", got)
	}
	policy.SetCapacityWaitTimeout(-5 * time.Second)
	if got := policy.Timeout(); got != 0 {
		t.Fatalf("negative timeout = %v, want 0 (wait disabled)", got)
	}
	policy.SetCapacityWaitTimeout(250 * time.Millisecond)
	if got := policy.Timeout(); got != 250*time.Millisecond {
		t.Fatalf("timeout = %v, want 250ms", got)
	}
}

// TestDenialSummaryClassification 单独冻结分类语义，避免未来把 cooldown 混进并发等待。
func TestDenialSummaryClassification(t *testing.T) {
	var empty attemptDenialSummary
	if empty.AllConcurrencyFull() || empty.AllCooldown() {
		t.Fatalf("empty summary must not claim any全池 verdict: %+v", empty)
	}

	var concurrency attemptDenialSummary
	concurrency.Record(concurrencyFullDenial())
	concurrency.Record(concurrencyFullDenial())
	if !concurrency.AllConcurrencyFull() || concurrency.AllCooldown() {
		t.Fatalf("all-concurrency summary misclassified: %+v", concurrency)
	}

	var cooldown attemptDenialSummary
	cooldown.Record(cooldownDenial(900))
	if cooldown.AllConcurrencyFull() || !cooldown.AllCooldown() {
		t.Fatalf("all-cooldown summary misclassified: %+v", cooldown)
	}
	if cooldown.minCooldownRemainingMs != 900 {
		t.Fatalf("min cooldown = %d, want 900", cooldown.minCooldownRemainingMs)
	}

	cooldown.Reset()
	if cooldown.seen || cooldown.cooldown != 0 || cooldown.minCooldownRemainingMs != 0 {
		t.Fatalf("reset must clear the previous scan: %+v", cooldown)
	}
}

// TestConcurrencyFullAndCooldownSkipReasonsAreDistinct 冻结指标/审计口径可区分（§19.2）。
func TestConcurrencyFullAndCooldownSkipReasonsAreDistinct(t *testing.T) {
	full := attemptDeniedSkipReason(breakerstore.ReasonConcurrencyFull)
	cool := attemptDeniedSkipReason(breakerstore.ReasonCooldown)
	if full != "channel_concurrency" || cool != "channel_cooldown" || full == cool {
		t.Fatalf("skip reasons must stay distinct: concurrency=%q cooldown=%q", full, cool)
	}
}

var _ = requestlog.EndpointChatCompletions
