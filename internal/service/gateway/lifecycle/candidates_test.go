package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestExecutorPrepareCandidatesBalancedUsesCapacityWithoutReplacement(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true, 2: true, 3: true},
	})
	executor.SetRandomSource(func() float64 { return 0.5 })

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
		ChannelCapacitySnapshot: func(_ context.Context, candidate routing.ChatRouteCandidate) (ChannelCapacity, error) {
			remaining := map[int64]int64{1: 1, 2: 6, 3: 0}[candidate.Channel.ID]
			return ChannelCapacity{
				Concurrency: CapacitySignal{Used: 10 - remaining, Limit: 10, Known: true},
				TPM:         CapacitySignal{Used: 0, Limit: 100, Known: true},
			}, nil
		},
	})
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}

	// 权重为 0.1/0.6/0；固定 draw=0.5 先命中 2，再命中 1，零容量 3 保留在 fallback 尾部。
	want := []int64{2, 1, 3}
	if len(plan.Candidates) != len(want) {
		t.Fatalf("expected %d candidates, got %d", len(want), len(plan.Candidates))
	}
	for i, c := range plan.Candidates {
		if c.Route.Channel.ID != want[i] {
			t.Fatalf("balanced order position %d: expected channel %d, got %d", i, want[i], c.Route.Channel.ID)
		}
	}
	if plan.Candidates[2].Balance.Weight != 0 {
		t.Fatalf("expected zero-capacity fallback weight 0, got %v", plan.Candidates[2].Balance.Weight)
	}
}

func TestBalancedScoreIgnoresChannelCost(t *testing.T) {
	expensive := candidateRoute(1, "openai")
	expensive.ChannelCost = billing.ProviderCostSnapshot{OutputCost: gatewayTestNumeric(100, 0)}
	cheap := candidateRoute(2, "openai")
	cheap.ChannelCost = billing.ProviderCostSnapshot{OutputCost: gatewayTestNumeric(1, 0)}

	order := func(in []routing.ChatRouteCandidate) []routing.ChatRouteCandidate {
		out, _, _, _ := orderBalancedCandidates(context.Background(), in, "balanced", nil, nil, func() float64 { return 0 }, BalanceConfig{Enabled: true, WeightByRemaining: true})
		return out
	}
	first := order([]routing.ChatRouteCandidate{expensive, cheap})
	second := order([]routing.ChatRouteCandidate{cheap, expensive})
	if first[0].Channel.ID != expensive.Channel.ID || second[0].Channel.ID != cheap.Channel.ID {
		t.Fatalf("equal-load balanced order must not use cost: first=%d second=%d", first[0].Channel.ID, second[0].Channel.ID)
	}
}

func TestBalancedAllZeroChoosesLeastPressureThenKeepsFallback(t *testing.T) {
	in := []routing.ChatRouteCandidate{candidateRoute(1, "openai"), candidateRoute(2, "openai")}
	out, _, _, allZero := orderBalancedCandidates(context.Background(), in, "balanced", func(_ context.Context, c routing.ChatRouteCandidate) (ChannelCapacity, error) {
		if c.Channel.ID == 1 {
			return ChannelCapacity{Concurrency: CapacitySignal{Used: 10, Limit: 10, Known: true}, TPM: CapacitySignal{Used: 90, Limit: 100, Known: true}}, nil
		}
		return ChannelCapacity{Concurrency: CapacitySignal{Used: 10, Limit: 10, Known: true}, TPM: CapacitySignal{Used: 20, Limit: 100, Known: true}}, nil
	}, nil, func() float64 { return 0 }, BalanceConfig{Enabled: true, WeightByRemaining: true})
	if !allZero || len(out) != 2 || out[0].Channel.ID != 2 {
		t.Fatalf("expected least-pressure channel 2 first with full fallback, allZero=%v order=%v", allZero, []int64{out[0].Channel.ID, out[1].Channel.ID})
	}
}

// TestPrepareCandidatesDemotesFailureCooledChannels 验证失败软冷却 demote（DEC-029）：
// 软冷却中的候选被稳定移到末尾（不剔除）；全部软冷却时顺序不变；唯一候选不受影响。
func TestPrepareCandidatesDemotesFailureCooledChannels(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true, 2: true, 3: true},
	})

	prepare := func(cooled map[int64]bool, in []routing.ChatRouteCandidate) []int64 {
		plan, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
			Protocol:   "openai",
			Candidates: in,
			Capabilities: []AdapterCapability{
				AdapterCapabilityNonStream,
				AdapterCapabilityInputTokenizer,
			},
			FailurePreferred: func(c routing.ChatRouteCandidate) bool {
				return !cooled[c.Channel.ID]
			},
			EstimateInputTokens: func(_ context.Context, _ routing.ChatRouteCandidate) (int64, error) {
				return 1, nil
			},
		})
		if err != nil {
			t.Fatalf("PrepareCandidates returned error: %v", err)
		}
		ids := make([]int64, 0, len(plan.Candidates))
		for _, c := range plan.Candidates {
			ids = append(ids, c.Route.Channel.ID)
		}
		return ids
	}

	three := []routing.ChatRouteCandidate{
		candidateRoute(1, "openai"),
		candidateRoute(2, "openai"),
		candidateRoute(3, "openai"),
	}

	// 渠道 1 软冷却：demote 到末尾，2/3 保持相对顺序。
	if got := prepare(map[int64]bool{1: true}, three); got[0] != 2 || got[1] != 3 || got[2] != 1 {
		t.Fatalf("expected order [2 3 1], got %v", got)
	}

	// 全部软冷却：顺序不变（不因软冷却清空/重排池子）。
	if got := prepare(map[int64]bool{1: true, 2: true, 3: true}, three); got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("expected original order [1 2 3], got %v", got)
	}

	// 唯一候选 + 软冷却：仍然可用（唯一渠道保护）。
	single := []routing.ChatRouteCandidate{candidateRoute(1, "openai")}
	if got := prepare(map[int64]bool{1: true}, single); len(got) != 1 || got[0] != 1 {
		t.Fatalf("sole candidate must survive failure cooldown, got %v", got)
	}
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
		allowed: map[int64]bool{1: true, 2: true, 3: true},
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
		Available: func(candidate routing.ChatRouteCandidate) bool {
			return candidate.Channel.ID != 2
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
		allowed: map[int64]bool{1: true},
	})

	_, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol:   "openai",
		Candidates: []routing.ChatRouteCandidate{candidateRoute(1, "first")},
		Available: func(routing.ChatRouteCandidate) bool {
			return false
		},
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
