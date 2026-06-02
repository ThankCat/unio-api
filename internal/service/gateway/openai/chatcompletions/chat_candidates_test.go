package chatcompletions

import (
	"context"
	"errors"
	"testing"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

type openAICandidateCapabilityRegistry struct {
	allowed      map[string]bool
	protocol     string
	capabilities []lifecycle.AdapterCapability
}

func (r *openAICandidateCapabilityRegistry) FilterCandidates(protocol string, candidates []routing.ChatRouteCandidate, capabilities ...lifecycle.AdapterCapability) []routing.ChatRouteCandidate {
	r.protocol = protocol
	r.capabilities = append([]lifecycle.AdapterCapability(nil), capabilities...)

	filtered := make([]routing.ChatRouteCandidate, 0, len(candidates))
	for _, candidate := range candidates {
		if r.allowed[candidate.AdapterKey] {
			filtered = append(filtered, candidate)
		}
	}
	return filtered
}

type recordingChatInputTokenizer struct {
	reqs   []openai.ChatRequest
	tokens int64
	err    error
}

func (t *recordingChatInputTokenizer) CountChatInputTokens(req openai.ChatRequest) (int64, error) {
	t.reqs = append(t.reqs, req)
	return t.tokens, t.err
}

func TestPrepareChatCandidatesUsesEachAdapterTokenizerAndMaximumEstimate(t *testing.T) {
	primary := &recordingChatInputTokenizer{tokens: 20}
	backup := &recordingChatInputTokenizer{tokens: 80}
	capabilities := &openAICandidateCapabilityRegistry{
		allowed: map[string]bool{
			"primary": true,
			"backup":  true,
		},
	}
	service := &ChatCompletionService{
		registry: &fakeAdapterRegistry{
			chatInputTokenizers: map[string]openai.ChatInputTokenizer{
				"primary": primary,
				"backup":  backup,
			},
		},
		candidates: lifecycle.NewExecutor(capabilities),
	}
	req := chatRequestWithParams()
	req.Tools = []gatewayapi.ChatCompletionTool{{
		Type: "function",
		Function: gatewayapi.ChatCompletionFunctionTool{
			Name:       "search_docs",
			Parameters: []byte(`{"type":"object"}`),
		},
	}}

	plan, err := service.prepareChatCandidates(context.Background(), req, []routing.ChatRouteCandidate{
		routeCandidate("primary", 101, "primary-model"),
		routeCandidate("filtered", 102, "filtered-model"),
		routeCandidate("backup", 103, "backup-model"),
	}, false)
	if err != nil {
		t.Fatalf("prepareChatCandidates returned error: %v", err)
	}

	if capabilities.protocol != routing.ProtocolOpenAI {
		t.Fatalf("expected protocol %q, got %q", routing.ProtocolOpenAI, capabilities.protocol)
	}
	if len(capabilities.capabilities) != 2 ||
		capabilities.capabilities[0] != lifecycle.AdapterCapabilityInputTokenizer ||
		capabilities.capabilities[1] != lifecycle.AdapterCapabilityNonStream {
		t.Fatalf("unexpected capabilities: %#v", capabilities.capabilities)
	}
	if len(plan.Candidates) != 2 {
		t.Fatalf("expected two prepared candidates, got %d", len(plan.Candidates))
	}
	if plan.Candidates[0].RouteIndex != 0 || plan.Candidates[1].RouteIndex != 2 {
		t.Fatalf("expected SQL route indexes 0 and 2, got %#v", plan.Candidates)
	}
	if plan.ConservativeInputTokens != 80 {
		t.Fatalf("expected conservative estimate %d, got %d", int64(80), plan.ConservativeInputTokens)
	}
	if len(primary.reqs) != 1 || primary.reqs[0].Model != "primary-model" {
		t.Fatalf("unexpected primary tokenizer requests: %#v", primary.reqs)
	}
	if len(backup.reqs) != 1 || backup.reqs[0].Model != "backup-model" {
		t.Fatalf("unexpected backup tokenizer requests: %#v", backup.reqs)
	}
	if len(backup.reqs[0].Messages) != 1 || backup.reqs[0].Messages[0].ContentString() != "hello" {
		t.Fatalf("unexpected mapped tokenizer messages: %#v", backup.reqs[0].Messages)
	}
	if len(backup.reqs[0].Tools) != 1 || backup.reqs[0].Tools[0].Function.Name != "search_docs" {
		t.Fatalf("unexpected mapped tokenizer tools: %#v", backup.reqs[0].Tools)
	}
}

func TestPrepareChatCandidatesWrapsTokenizerFailure(t *testing.T) {
	tokenizeErr := errors.New("tokenizer failed")
	service := &ChatCompletionService{
		registry: &fakeAdapterRegistry{
			chatInputTokenizers: map[string]openai.ChatInputTokenizer{
				"deepseek": &recordingChatInputTokenizer{err: tokenizeErr},
			},
		},
		candidates: lifecycle.NewExecutor(&openAICandidateCapabilityRegistry{
			allowed: map[string]bool{"deepseek": true},
		}),
	}

	_, err := service.prepareChatCandidates(context.Background(), chatRequest(), []routing.ChatRouteCandidate{
		routeCandidate("deepseek", 101, "deepseek-chat"),
	}, false)
	if !errors.Is(err, tokenizeErr) {
		t.Fatalf("expected wrapped tokenizer error, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeGatewayInputTokenEstimateFailed {
		t.Fatalf("expected code %q, got %q", failure.CodeGatewayInputTokenEstimateFailed, got)
	}
}
