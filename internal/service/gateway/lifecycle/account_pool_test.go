package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
	"github.com/jackc/pgx/v5/pgtype"
)

type fakeAccountPoolStore struct {
	rows map[int64][]sqlc.ListSchedulableAccountsByChannelRow
	err  error
}

func (s fakeAccountPoolStore) ListSchedulableAccountsByChannel(
	_ context.Context, channelID int64,
) ([]sqlc.ListSchedulableAccountsByChannelRow, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.rows[channelID], nil
}

type fakeAccountRuntimeStore struct {
	runtimes map[int64]breakerstore.AccountRuntime
	err      error
}

func (s fakeAccountRuntimeStore) AccountRuntimeMany(
	_ context.Context, accountIDs []int64,
) ([]breakerstore.AccountRuntime, error) {
	if s.err != nil {
		return nil, s.err
	}
	out := make([]breakerstore.AccountRuntime, 0, len(accountIDs))
	for _, id := range accountIDs {
		runtime := s.runtimes[id]
		runtime.AccountID = id
		out = append(out, runtime)
	}
	return out, nil
}

func accountRow(id int64, limit int32) sqlc.ListSchedulableAccountsByChannelRow {
	return sqlc.ListSchedulableAccountsByChannelRow{
		ID:               id,
		Priority:         50,
		ConcurrencyLimit: pgtype.Int4{Int32: limit, Valid: true},
		ConfigRevision:   1,
	}
}

func poolRoute(channelID int64) routing.ChatRouteCandidate {
	return routing.ChatRouteCandidate{
		AdapterKey: "codex",
		Channel:    channel.Runtime{ID: channelID},
		SupplyForm: channel.SupplyFormPool,
	}
}

// 池型候选可用时，并发容量分的输入换成全池空闲槽位聚合；五项评分公式本身不变。
func TestPrepareCandidatesUsesPoolCapacityForPoolChannel(t *testing.T) {
	executor := NewExecutor(
		candidateCapabilityRegistry{allowed: map[int64]bool{7: true}},
		WithAccountPool(
			fakeAccountPoolStore{rows: map[int64][]sqlc.ListSchedulableAccountsByChannelRow{
				7: {accountRow(1, 4), accountRow(2, 4)},
			}},
			fakeAccountRuntimeStore{runtimes: map[int64]breakerstore.AccountRuntime{
				1: {InFlight: 3},
				2: {InFlight: 1},
			}},
		),
	)
	ctx := requestadmission.ContextWithRequestSession(context.Background(), &candidateSnapshotSession{
		result: breakerstore.SnapshotManyResult{Candidates: []breakerstore.CandidateSnapshot{
			{Status: breakerstore.CandidateSnapshotCurrent},
		}},
	})

	plan, err := executor.PrepareCandidates(ctx, PrepareCandidatesParams{
		Protocol:            "openai",
		Candidates:          []routing.ChatRouteCandidate{poolRoute(7)},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) { return 1, nil },
	})
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}
	if len(plan.Candidates) != 1 {
		t.Fatalf("expected the pool candidate to stay available, got %d", len(plan.Candidates))
	}
	// 全池 4/8 在途 → 剩余 50% → 并发分 50。用渠道自身并发（快照里是 0/0 不限）会得 100 分。
	if remaining := plan.Candidates[0].Balance.ConcurrencyRemaining; remaining == nil || *remaining != 0.5 {
		t.Fatalf("pool capacity must drive the concurrency score, got %+v", plan.Candidates[0].Balance)
	}
	if plan.Candidates[0].AccountPool == nil {
		t.Fatal("pool candidate must carry its frozen account snapshot for the attempt loop")
	}
}

// 池内账号全部处于运行态封锁时，候选按「到点自愈」处理：整池不可用即渠道级 429，
// Retry-After 取最早恢复时刻。它与 breaker open 是两个事实，不能混成一个。
func TestPrepareCandidatesTreatsFullyCooledPoolAsRateLimited(t *testing.T) {
	executor := NewExecutor(
		candidateCapabilityRegistry{allowed: map[int64]bool{7: true}},
		WithAccountPool(
			fakeAccountPoolStore{rows: map[int64][]sqlc.ListSchedulableAccountsByChannelRow{
				7: {accountRow(1, 4), accountRow(2, 4)},
			}},
			fakeAccountRuntimeStore{runtimes: map[int64]breakerstore.AccountRuntime{
				1: {CooldownRemainingMs: 9_000},
				2: {UsagePauseRemainingMs: 2_500},
			}},
		),
	)
	ctx := requestadmission.ContextWithRequestSession(context.Background(), &candidateSnapshotSession{
		result: breakerstore.SnapshotManyResult{Candidates: []breakerstore.CandidateSnapshot{
			{Status: breakerstore.CandidateSnapshotCurrent},
		}},
	})

	plan, err := executor.PrepareCandidates(ctx, PrepareCandidatesParams{
		Protocol:            "openai",
		Candidates:          []routing.ChatRouteCandidate{poolRoute(7)},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) { return 1, nil },
	})
	if failure.CodeOf(err) != failure.CodeGatewayChannelRateLimited {
		t.Fatalf("fully cooled pool must surface as channel rate limit, got %v", err)
	}
	if got := failureInt64Field(err, "retry_after_ms"); got != 2_500 {
		t.Fatalf("retry_after_ms = %d, want 2500 (earliest recovery in the pool)", got)
	}
	if len(plan.Excluded) != 1 || plan.Excluded[0].Reason != accountPoolReasonCooldown {
		t.Fatalf("exclusion must name the account pool cooldown, got %+v", plan.Excluded)
	}
}

// 账号事实读不出来时 fail-closed：宁可少一个候选，也不拿一个不知道能不能用的号出站。
func TestPrepareCandidatesExcludesPoolWhenAccountFactsUnavailable(t *testing.T) {
	executor := NewExecutor(
		candidateCapabilityRegistry{allowed: map[int64]bool{7: true, 8: true}},
		WithAccountPool(
			fakeAccountPoolStore{err: errors.New("postgres down")},
			fakeAccountRuntimeStore{},
		),
	)
	ctx := requestadmission.ContextWithRequestSession(context.Background(), &candidateSnapshotSession{
		result: breakerstore.SnapshotManyResult{Candidates: []breakerstore.CandidateSnapshot{
			{Status: breakerstore.CandidateSnapshotCurrent},
			{Status: breakerstore.CandidateSnapshotCurrent},
		}},
	})

	plan, err := executor.PrepareCandidates(ctx, PrepareCandidatesParams{
		Protocol:            "openai",
		Candidates:          []routing.ChatRouteCandidate{poolRoute(7), candidateRoute(8, "openai")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) { return 1, nil },
	})
	if err != nil {
		t.Fatalf("credential candidate must still be usable: %v", err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Route.Channel.ID != 8 {
		t.Fatalf("only the credential candidate may survive, got %+v", plan.Candidates)
	}
	if len(plan.Excluded) != 1 || plan.Excluded[0].Reason != accountPoolReasonUnavailable {
		t.Fatalf("pool candidate must be excluded as unavailable, got %+v", plan.Excluded)
	}
}

// 未注入账号读取器的装配（既有单测与维护调用）绝不能悄悄放行池型候选：
// 没有账号就没有凭据，放行只会在出站时炸开。credential 型候选完全不受影响。
func TestPrepareCandidatesExcludesPoolWithoutAccountReaders(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{allowed: map[int64]bool{7: true, 8: true}})

	plan, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol:            "openai",
		Candidates:          []routing.ChatRouteCandidate{poolRoute(7), candidateRoute(8, "openai")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) { return 1, nil },
	})
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}
	if len(plan.Candidates) != 1 || plan.Candidates[0].Route.Channel.ID != 8 {
		t.Fatalf("pool candidate must not be prepared without account readers, got %+v", plan.Candidates)
	}
}
