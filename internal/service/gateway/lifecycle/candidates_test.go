package lifecycle

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/requestadmission"
)

func TestExecutorPrepareCandidatesWithoutRuntimeUsesNeutralSQLOrder(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true, 2: true, 3: true},
	})

	plan, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol: "openai",
		Candidates: []routing.ChatRouteCandidate{
			candidateRoute(1, "openai"),
			candidateRoute(2, "openai"),
			candidateRoute(3, "openai"),
		},
		Capabilities: []AdapterCapability{
			AdapterCapabilityNonStream,
			AdapterCapabilityInputTokenizer,
		},
		EstimateInputTokens: func(_ context.Context, _ routing.ChatRouteCandidate) (int64, error) {
			return 1, nil
		},
		Mode: "balanced",
	})
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}

	// 没有 HTTP-owned request session 就没有 Redis SnapshotMany 权威事实；直接调用保持 SQL 顺序，
	// 不得回退到已退役的本机容量/健康评分。
	want := []int64{1, 2, 3}
	if len(plan.Candidates) != len(want) {
		t.Fatalf("expected %d candidates, got %d", len(want), len(plan.Candidates))
	}
	for i, c := range plan.Candidates {
		if c.Route.Channel.ID != want[i] {
			t.Fatalf("balanced order position %d: expected channel %d, got %d", i, want[i], c.Route.Channel.ID)
		}
	}
	for _, candidate := range plan.Candidates {
		if candidate.Balance.Weight != 100 || candidate.Balance.AlgorithmVersion != "objective_v1" || !candidate.Balance.CapacityUnknown {
			t.Fatalf("expected neutral unknown runtime score, got %+v", candidate.Balance)
		}
	}
}

func TestBalancedCostFactorPrefersCheaperCandidateAtEqualHealth(t *testing.T) {
	expensive := candidateRoute(1, "openai")
	expensive.CostRatio = 1
	cheap := candidateRoute(2, "openai")
	cheap.CostRatio = 0.2
	capacity := func(context.Context, routing.ChatRouteCandidate) (ChannelCapacity, error) {
		return ChannelCapacity{
			Concurrency: CapacitySignal{Limit: 0, Known: true},
			TPM:         CapacitySignal{Limit: 0, Known: true},
		}, nil
	}
	out, scores, _ := orderBalancedCandidates(
		context.Background(), []routing.ChatRouteCandidate{expensive, cheap}, "balanced",
		capacity,
		BalanceConfig{},
	)
	if math.Abs(scores[1].EconomicScore-0) > 1e-12 || math.Abs(scores[2].EconomicScore-80) > 1e-12 {
		t.Fatalf("unexpected cost factors: expensive=%+v cheap=%+v", scores[1], scores[2])
	}
	if scores[2].Weight <= scores[1].Weight || out[0].Channel.ID != cheap.Channel.ID {
		t.Fatalf("cheaper equal-health candidate should have more weight: order=%v scores=%+v", []int64{out[0].Channel.ID, out[1].Channel.ID}, scores)
	}
}

func TestBalancedConfiguredEconomicsCanDominateHealth(t *testing.T) {
	cheapUnhealthy := candidateRoute(1, "openai")
	cheapUnhealthy.CostRatio = 0
	expensiveHealthy := candidateRoute(2, "openai")
	expensiveHealthy.CostRatio = 1

	out, scores, _ := orderBalancedCandidates(
		context.Background(), []routing.ChatRouteCandidate{cheapUnhealthy, expensiveHealthy}, "balanced",
		func(_ context.Context, candidate routing.ChatRouteCandidate) (ChannelCapacity, error) {
			errorRate := 0.0
			if candidate.Channel.ID == cheapUnhealthy.Channel.ID {
				errorRate = 0.8
			}
			return ChannelCapacity{
				Concurrency:  CapacitySignal{Limit: 0, Known: true},
				TPM:          CapacitySignal{Limit: 0, Known: true},
				ErrorRate:    errorRate,
				ErrorSamples: 10,
			}, nil
		},
		BalanceConfig{},
	)
	if scores[1].Weight <= scores[2].Weight || out[0].Channel.ID != cheapUnhealthy.Channel.ID {
		t.Fatalf("45%% economic weight should win this configured tradeoff: order=%v scores=%+v", []int64{out[0].Channel.ID, out[1].Channel.ID}, scores)
	}
}

func TestBalancedZeroEconomicWeightUsesOtherDimensions(t *testing.T) {
	first := candidateRoute(1, "openai")
	first.CostRatio = 1
	second := candidateRoute(2, "openai")
	second.CostRatio = 0
	capacity := func(context.Context, routing.ChatRouteCandidate) (ChannelCapacity, error) {
		return ChannelCapacity{
			Concurrency: CapacitySignal{Used: 2, Limit: 10, Known: true},
			TPM:         CapacitySignal{Used: 25, Limit: 100, Known: true},
			ErrorRate:   0.2, TTFTEWMAMs: 2000, TTFTSamples: 8,
		}, nil
	}

	out, scores, _ := orderBalancedCandidates(
		context.Background(), []routing.ChatRouteCandidate{first, second}, "balanced",
		capacity,
		BalanceConfig{TTFTTargetMs: 2000, TTFTWeight: 0.35, EconomicWeightPct: 0, HealthWeightPct: 50, CapacityWeightPct: 40, PriorityWeightPct: 10},
	)
	if out[0].Channel.ID != first.Channel.ID || out[1].Channel.ID != second.Channel.ID {
		t.Fatalf("equal non-economic scores must use channel ID order: %v", []int64{out[0].Channel.ID, out[1].Channel.ID})
	}
	for _, channelID := range []int64{first.Channel.ID, second.Channel.ID} {
		if math.Abs(scores[channelID].Weight-73) > 1e-12 {
			t.Fatalf("unexpected zero-economic score for channel %d: %+v", channelID, scores[channelID])
		}
	}
}

func TestFixedModeRecordsNeutralCostWithoutChangingOrder(t *testing.T) {
	first := candidateRoute(1, "openai")
	first.CostRatio = 1
	second := candidateRoute(2, "openai")
	second.CostRatio = 0
	out, scores, _ := orderBalancedCandidates(
		context.Background(), []routing.ChatRouteCandidate{first, second}, "fixed", nil,
		BalanceConfig{},
	)
	if out[0].Channel.ID != first.Channel.ID || out[1].Channel.ID != second.Channel.ID {
		t.Fatalf("fixed order changed: %v", []int64{out[0].Channel.ID, out[1].Channel.ID})
	}
	if scores[first.Channel.ID].EconomicScore != 0 || scores[first.Channel.ID].FinalScore != 55 {
		t.Fatalf("fixed mode must preserve order while exposing objective facts: %+v", scores[first.Channel.ID])
	}
}

func TestCostAwareOrderStillAllowsStickyPostOrderPin(t *testing.T) {
	expensive := candidateRoute(1, "openai")
	expensive.CostRatio = 1
	cheap := candidateRoute(2, "openai")
	cheap.CostRatio = 0
	ordered, scores, _ := orderBalancedCandidates(
		context.Background(), []routing.ChatRouteCandidate{expensive, cheap}, "balanced", nil,
		BalanceConfig{},
	)
	if ordered[0].Channel.ID != cheap.Channel.ID {
		t.Fatalf("test setup expected cheap candidate first, got %d", ordered[0].Channel.ID)
	}
	candidates := []Candidate{
		{Route: ordered[0], Balance: scores[ordered[0].Channel.ID]},
		{Route: ordered[1], Balance: scores[ordered[1].Channel.ID]},
	}
	pinned, found, reordered := pinStickyCandidate(candidates, expensive.Channel.ID)
	if !found || !reordered || pinned[0].Route.Channel.ID != expensive.Channel.ID {
		t.Fatalf("sticky must remain the final ordering step: found=%v reordered=%v order=%v", found, reordered, []int64{pinned[0].Route.Channel.ID, pinned[1].Route.Channel.ID})
	}
}

func TestBalancedAllUnknownKeepsCostFactor(t *testing.T) {
	expensive := candidateRoute(1, "openai")
	expensive.CostRatio = 1
	cheap := candidateRoute(2, "openai")
	cheap.CostRatio = 0
	_, scores, _ := orderBalancedCandidates(
		context.Background(), []routing.ChatRouteCandidate{expensive, cheap}, "balanced", nil,
		BalanceConfig{},
	)
	if scores[expensive.Channel.ID].Weight != 55 || scores[cheap.Channel.ID].Weight != 100 {
		t.Fatalf("all-unknown neutral capacity overwrote cost factors: %+v", scores)
	}
}

func TestApplyObjectiveFactorsClampsCostRatio(t *testing.T) {
	clamped := ApplyObjectiveFactors(BalanceScore{CapacityScore: 100, HealthScore: 100}, 2, 0, BalanceConfig{})
	if clamped.CostRatio != 2 || clamped.EconomicScore != 0 || math.Abs(clamped.FinalScore-55) > 1e-12 {
		t.Fatalf("unexpected clamped objective score: %+v", clamped)
	}
}

func TestBalancedScoreUsesCapacityErrorRateAndStreamTTFT(t *testing.T) {
	score := ScoreBalanceCandidateWithConfig(ChannelCapacity{
		Concurrency:  CapacitySignal{Used: 2, Limit: 10, Known: true},
		TPM:          CapacitySignal{Used: 25, Limit: 100, Known: true},
		ErrorRate:    0.2,
		TTFTEWMAMs:   2000,
		TTFTSamples:  8,
		RuntimeKnown: true,
	}, BalanceConfig{
		Revision: 9, TTFTTargetMs: 2000, TTFTWeight: 0.35,
	})
	// capacity=min(0.8,0.75)=75; latency=0.5; health=0.8*(1-0.35*0.5)*100=66.
	if math.Abs(score.CapacityScore-75) > 1e-9 || math.Abs(score.ErrorRate-0.2) > 1e-9 ||
		math.Abs(score.LatencyPenalty-0.5) > 1e-9 || math.Abs(score.RoutingFactor-0.66) > 1e-9 ||
		math.Abs(score.HealthScore-66) > 1e-9 || score.Weight != 0 || score.RoutingBalanceRevision != 9 ||
		score.TTFTSampleSource != "stream_only" {
		t.Fatalf("unexpected three-factor score: %+v", score)
	}

	noTTFT := ScoreBalanceCandidateWithConfig(ChannelCapacity{
		Concurrency: CapacitySignal{Limit: 0, Known: true}, TPM: CapacitySignal{Limit: 0, Known: true},
		RuntimeKnown: true,
	}, BalanceConfig{TTFTTargetMs: 2000, TTFTWeight: 0.35})
	if noTTFT.LatencyPenalty != 0 || noTTFT.HealthScore != 100 || noTTFT.CapacityScore != 100 {
		t.Fatalf("no stream samples must keep latency neutral: %+v", noTTFT)
	}
}

func TestBalancedHalfOpenStaysBehindObjectiveCandidates(t *testing.T) {
	in := []routing.ChatRouteCandidate{candidateRoute(1, "openai"), candidateRoute(2, "openai"), candidateRoute(3, "openai")}
	out, scores, _ := orderBalancedCandidates(context.Background(), in, "balanced",
		func(_ context.Context, candidate routing.ChatRouteCandidate) (ChannelCapacity, error) {
			return ChannelCapacity{
				Concurrency: CapacitySignal{Limit: 0, Known: true}, TPM: CapacitySignal{Limit: 0, Known: true},
				HalfOpen: candidate.Channel.ID == 1, RuntimeKnown: true,
			}, nil
		}, BalanceConfig{
			TTFTTargetMs: 2000, TTFTWeight: 0.35,
		})
	if len(out) != 3 || out[2].Channel.ID != 1 || scores[1].Weight != 0 || scores[1].RoutingFactor != 0 {
		t.Fatalf("half-open candidate must stay behind objective candidates: order=%v score=%+v",
			[]int64{out[0].Channel.ID, out[1].Channel.ID, out[2].Channel.ID}, scores[1])
	}
}

func TestBalancedAllZeroStillUsesDeterministicScoreTieBreak(t *testing.T) {
	in := []routing.ChatRouteCandidate{candidateRoute(1, "openai"), candidateRoute(2, "openai")}
	out, _, allZero := orderBalancedCandidates(context.Background(), in, "balanced", func(_ context.Context, c routing.ChatRouteCandidate) (ChannelCapacity, error) {
		if c.Channel.ID == 1 {
			return ChannelCapacity{Concurrency: CapacitySignal{Used: 10, Limit: 10, Known: true}, TPM: CapacitySignal{Used: 90, Limit: 100, Known: true}}, nil
		}
		return ChannelCapacity{Concurrency: CapacitySignal{Used: 10, Limit: 10, Known: true}, TPM: CapacitySignal{Used: 20, Limit: 100, Known: true}}, nil
	}, BalanceConfig{})
	if !allZero || len(out) != 2 || out[0].Channel.ID != 1 {
		t.Fatalf("expected deterministic channel-ID tie break with full fallback, allZero=%v order=%v", allZero, []int64{out[0].Channel.ID, out[1].Channel.ID})
	}
}

func TestBalancedOrderUsesDescendingObjectiveScore(t *testing.T) {
	in := []routing.ChatRouteCandidate{candidateRoute(3, "openai"), candidateRoute(1, "openai"), candidateRoute(2, "openai")}
	capacity := func(_ context.Context, candidate routing.ChatRouteCandidate) (ChannelCapacity, error) {
		return ChannelCapacity{
			Concurrency:  CapacitySignal{Used: candidate.Channel.ID, Limit: 10, Known: true},
			TPM:          CapacitySignal{Limit: 0, Known: true},
			RuntimeKnown: true,
		}, nil
	}
	ordered, _, _ := orderBalancedCandidates(context.Background(), in, "balanced", capacity, BalanceConfig{})
	want := []int64{1, 2, 3}
	for index, channelID := range want {
		if ordered[index].Channel.ID != channelID {
			t.Fatalf("unexpected objective order: got=%v want=%v", candidateIDs(ordered), want)
		}
	}
}

func TestBalancedTieBreakUsesPriorityThenChannelID(t *testing.T) {
	highPriorityLargeID := candidateRoute(9, "openai")
	highPriorityLargeID.Priority = 10
	lowPrioritySmallID := candidateRoute(1, "openai")
	lowPrioritySmallID.Priority = 20
	highPrioritySmallID := candidateRoute(2, "openai")
	highPrioritySmallID.Priority = 10

	out, _, _ := orderBalancedCandidates(
		context.Background(),
		[]routing.ChatRouteCandidate{highPriorityLargeID, lowPrioritySmallID, highPrioritySmallID},
		"balanced",
		nil,
		BalanceConfig{EconomicWeightPct: 45, HealthWeightPct: 25, CapacityWeightPct: 30},
	)
	want := []int64{2, 9, 1}
	for index, channelID := range want {
		if out[index].Channel.ID != channelID {
			t.Fatalf("unexpected deterministic tie break: got=%v want=%v", candidateIDs(out), want)
		}
	}
}

func candidateIDs(candidates []routing.ChatRouteCandidate) []int64 {
	ids := make([]int64, len(candidates))
	for index, candidate := range candidates {
		ids[index] = candidate.Channel.ID
	}
	return ids
}

type candidateCapabilityRegistry struct {
	allowed map[int64]bool
}

func (r candidateCapabilityRegistry) FilterCandidates(_ string, candidates []routing.ChatRouteCandidate, _ ...AdapterCapability) []routing.ChatRouteCandidate {
	filtered := make([]routing.ChatRouteCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if r.allowed[candidate.Channel.ID] {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

func TestExecutorPrepareCandidatesFiltersAndUsesConservativeEstimate(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true, 3: true},
	})
	var estimated []int64

	plan, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol: "openai",
		Candidates: []routing.ChatRouteCandidate{
			candidateRoute(1, "first"),
			candidateRoute(2, "second"),
			candidateRoute(3, "third"),
			candidateRoute(4, "filtered-by-capability"),
		},
		Capabilities: []AdapterCapability{
			AdapterCapabilityNonStream,
			AdapterCapabilityInputTokenizer,
		},
		EstimateInputTokens: func(_ context.Context, candidate routing.ChatRouteCandidate) (int64, error) {
			estimated = append(estimated, candidate.Channel.ID)
			switch candidate.Channel.ID {
			case 1:
				return 20, nil
			case 3:
				return 80, nil
			default:
				return 0, nil
			}
		},
	})
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}

	if len(plan.Candidates) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(plan.Candidates))
	}
	if plan.Candidates[0].Route.Channel.ID != 1 || plan.Candidates[0].RouteIndex != 0 {
		t.Fatalf("unexpected first candidate: %#v", plan.Candidates[0])
	}
	if plan.Candidates[1].Route.Channel.ID != 3 || plan.Candidates[1].RouteIndex != 2 {
		t.Fatalf("unexpected second candidate: %#v", plan.Candidates[1])
	}
	if plan.ConservativeInputTokens != 80 {
		t.Fatalf("expected conservative estimate %d, got %d", int64(80), plan.ConservativeInputTokens)
	}
	if len(estimated) != 2 || estimated[0] != 1 || estimated[1] != 3 {
		t.Fatalf("unexpected estimated candidates: %#v", estimated)
	}
}

func TestExecutorPrepareCandidatesReturnsNoAvailableChannelAfterFiltering(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{},
	})

	_, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return 10, nil
		},
	})
	if !errors.Is(err, routing.ErrNoAvailableChannel) {
		t.Fatalf("expected ErrNoAvailableChannel, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeRoutingNoAvailableChannel {
		t.Fatalf("expected code %q, got %q", failure.CodeRoutingNoAvailableChannel, got)
	}
}

func TestExecutorPrepareCandidatesAggregatesCooldownOnlyAsRateLimit(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{allowed: map[int64]bool{1: true, 2: true}})
	ctx := requestadmission.ContextWithUsageSession(context.Background(), &candidateSnapshotSession{
		result: breakerstore.SnapshotManyResult{Candidates: []breakerstore.CandidateSnapshot{
			{Status: breakerstore.CandidateSnapshotRateLimited, CooldownRemainingMs: 2_001},
			{Status: breakerstore.CandidateSnapshotRateLimited, CooldownRemainingMs: 1_001},
		}},
	})

	_, err := executor.PrepareCandidates(ctx, PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first"), candidateRoute(2, "second")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return 10, nil
		},
	})
	if failure.CodeOf(err) != failure.CodeGatewayChannelRateLimited {
		t.Fatalf("expected all-cooldown rate limit, got %v", err)
	}
	if got := failureInt64Field(err, "retry_after_ms"); got != 1_001 {
		t.Fatalf("expected earliest provable cooldown 1001ms, got %d", got)
	}
}

func TestExecutorPrepareCandidatesKeepsMixedExclusionsUnavailable(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{allowed: map[int64]bool{1: true, 2: true}})
	ctx := requestadmission.ContextWithUsageSession(context.Background(), &candidateSnapshotSession{
		result: breakerstore.SnapshotManyResult{Candidates: []breakerstore.CandidateSnapshot{
			{Status: breakerstore.CandidateSnapshotRateLimited, CooldownRemainingMs: 1_000},
			{Status: breakerstore.CandidateSnapshotOpen},
		}},
	})

	_, err := executor.PrepareCandidates(ctx, PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first"), candidateRoute(2, "second")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return 10, nil
		},
	})
	if failure.CodeOf(err) != failure.CodeRoutingNoAvailableChannel {
		t.Fatalf("mixed cooldown/breaker reasons must map to unavailable, got %v", err)
	}
}

type candidateSnapshotSession struct {
	result breakerstore.SnapshotManyResult
}

func (*candidateSnapshotSession) Reserve(context.Context, int64) error { return nil }
func (*candidateSnapshotSession) PublishAuthoritativeUsage(int64) bool { return true }
func (s *candidateSnapshotSession) SnapshotMany(context.Context, int64, []breakerstore.SnapshotCandidateInput) (breakerstore.SnapshotManyResult, error) {
	return s.result, nil
}

func failureInt64Field(err error, key string) int64 {
	for _, field := range failure.FieldsOf(err) {
		if field.Key == key {
			value, _ := field.Value.(int64)
			return value
		}
	}
	return 0
}

func TestExecutorPrepareCandidatesWrapsEstimatorError(t *testing.T) {
	estimateErr := errors.New("tokenizer unavailable")
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true},
	})

	_, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return 0, estimateErr
		},
	})
	if !errors.Is(err, estimateErr) {
		t.Fatalf("expected estimator error cause, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeGatewayInputTokenEstimateFailed {
		t.Fatalf("expected code %q, got %q", failure.CodeGatewayInputTokenEstimateFailed, got)
	}
}

func TestExecutorPrepareCandidatesRejectsNegativeEstimate(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true},
	})

	_, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first")},
		EstimateInputTokens: func(context.Context, routing.ChatRouteCandidate) (int64, error) {
			return -1, nil
		},
	})
	if !errors.Is(err, ErrCandidateInputTokenEstimateInvalid) {
		t.Fatalf("expected ErrCandidateInputTokenEstimateInvalid, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeGatewayInputTokenEstimateFailed {
		t.Fatalf("expected code %q, got %q", failure.CodeGatewayInputTokenEstimateFailed, got)
	}
}

func candidateRoute(channelID int64, adapterKey string) routing.ChatRouteCandidate {
	return routing.ChatRouteCandidate{
		AdapterKey: adapterKey,
		Channel:    channel.Runtime{ID: channelID},
	}
}
