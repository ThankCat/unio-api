package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// TestExecutorPrepareCandidatesCheapestOrdersByChannelCost 验证 cheapest 线路按命中渠道成本升序排序候选（DEC-026）。
// 售价已由 线路 × 模型 固定，cheapest 在池内挑成本最低 = 平台毛利最大。
func TestExecutorPrepareCandidatesCheapestOrdersByChannelCost(t *testing.T) {
	executor := NewExecutor(candidateCapabilityRegistry{
		allowed: map[int64]bool{1: true, 2: true, 3: true},
	})

	withCost := func(id, output int64) routing.ChatRouteCandidate {
		c := candidateRoute(id, "openai")
		c.ChannelCost = billing.ProviderCostSnapshot{
			UncachedInputCost: gatewayTestNumeric(1, 0),
			OutputCost:        gatewayTestNumeric(output, 0),
		}
		return c
	}

	plan, err := executor.PrepareCandidates(context.Background(), PrepareCandidatesParams{
		Protocol: "openai",
		// SQL priority 基序：id1(成本贵=10) → id2(中=5) → id3(便宜=2)。
		Candidates: []routing.ChatRouteCandidate{
			withCost(1, 10),
			withCost(2, 5),
			withCost(3, 2),
		},
		Capabilities: []AdapterCapability{
			AdapterCapabilityNonStream,
			AdapterCapabilityInputTokenizer,
		},
		EstimateInputTokens: func(_ context.Context, _ routing.ChatRouteCandidate) (int64, error) {
			return 1, nil
		},
		Mode: "cheapest",
	})
	if err != nil {
		t.Fatalf("PrepareCandidates returned error: %v", err)
	}

	// cheapest：按成本升序 → id3(2) → id2(5) → id1(10)。
	want := []int64{3, 2, 1}
	if len(plan.Candidates) != len(want) {
		t.Fatalf("expected %d candidates, got %d", len(want), len(plan.Candidates))
	}
	for i, c := range plan.Candidates {
		if c.Route.Channel.ID != want[i] {
			t.Fatalf("cheapest order position %d: expected channel %d, got %d", i, want[i], c.Route.Channel.ID)
		}
	}
}

// TestSortCandidatesByModeRandomShufflesKeepingSet 验证 random 策略洗牌：候选集合不变（仍保留全部 fallback），
// 且多次洗牌能产生与基序不同的顺序（20 次全部相同的概率约 (1/6)^20，可忽略）。
func TestSortCandidatesByModeRandomShufflesKeepingSet(t *testing.T) {
	in := []routing.ChatRouteCandidate{
		candidateRoute(1, "a"),
		candidateRoute(2, "b"),
		candidateRoute(3, "c"),
	}

	sawDifferentOrder := false
	for range 20 {
		out := sortCandidatesByMode(in, "random", nil)
		if len(out) != len(in) {
			t.Fatalf("random mode changed candidate count: got %d, want %d", len(out), len(in))
		}
		seen := map[int64]bool{}
		for _, c := range out {
			seen[c.Channel.ID] = true
		}
		for _, c := range in {
			if !seen[c.Channel.ID] {
				t.Fatalf("random mode dropped channel %d", c.Channel.ID)
			}
		}
		if out[0].Channel.ID != in[0].Channel.ID || out[1].Channel.ID != in[1].Channel.ID {
			sawDifferentOrder = true
		}
	}
	if !sawDifferentOrder {
		t.Fatal("random mode never produced an order different from base priority order in 20 shuffles")
	}

	// 入参不被修改（洗牌发生在副本上）。
	for i, want := range []int64{1, 2, 3} {
		if in[i].Channel.ID != want {
			t.Fatalf("input slice mutated at %d: got %d, want %d", i, in[i].Channel.ID, want)
		}
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
