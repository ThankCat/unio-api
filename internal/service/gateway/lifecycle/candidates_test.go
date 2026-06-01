package lifecycle

import (
	"context"
	"errors"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

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
