package lifecycle

import (
	"errors"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic"
	anthropicdeepseek "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/deepseek/messages"
	"github.com/ThankCat/unio-gateway/internal/core/adapter/openai"
	openaideepseek "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/deepseek/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestAdapterRegistryResolvesCapabilitiesByProtocolAndKey(t *testing.T) {
	registry := newTestAdapterRegistry(t)

	for _, tt := range []struct {
		name       string
		protocol   string
		adapterKey string
		capability AdapterCapability
		want       bool
	}{
		{name: "openai non-stream", protocol: routing.ProtocolOpenAI, adapterKey: "deepseek", capability: AdapterCapabilityNonStream, want: true},
		{name: "openai stream", protocol: routing.ProtocolOpenAI, adapterKey: "deepseek", capability: AdapterCapabilityStream, want: true},
		{name: "openai tokenizer", protocol: routing.ProtocolOpenAI, adapterKey: "deepseek", capability: AdapterCapabilityInputTokenizer, want: true},
		{name: "anthropic non-stream", protocol: routing.ProtocolAnthropic, adapterKey: "deepseek", capability: AdapterCapabilityNonStream, want: true},
		{name: "anthropic stream", protocol: routing.ProtocolAnthropic, adapterKey: "deepseek", capability: AdapterCapabilityStream, want: true},
		{name: "anthropic tokenizer", protocol: routing.ProtocolAnthropic, adapterKey: "deepseek", capability: AdapterCapabilityInputTokenizer, want: true},
		{name: "unknown protocol", protocol: "unknown", adapterKey: "deepseek", capability: AdapterCapabilityNonStream, want: false},
		{name: "unknown key", protocol: routing.ProtocolOpenAI, adapterKey: "unknown", capability: AdapterCapabilityNonStream, want: false},
		{name: "unknown capability", protocol: routing.ProtocolOpenAI, adapterKey: "deepseek", capability: "unknown", want: false},
	} {
		t.Run(tt.name, func(t *testing.T) {
			if got := registry.Has(tt.protocol, tt.adapterKey, tt.capability); got != tt.want {
				t.Fatalf("Has(%q, %q, %q) = %v, want %v", tt.protocol, tt.adapterKey, tt.capability, got, tt.want)
			}
		})
	}
}

func TestAdapterRegistryFilterCandidatesPreservesRoutingOrder(t *testing.T) {
	openAIDeepSeek := openaideepseek.NewAdapter(nil, nil)
	openAIRegistry, err := openai.NewRegistry(
		openai.Registration{
			Key:                "complete",
			Chat:               openAIDeepSeek,
			StreamChat:         openAIDeepSeek,
			ChatInputTokenizer: openAIDeepSeek,
		},
		openai.Registration{
			Key:                "missing_stream",
			Chat:               openAIDeepSeek,
			ChatInputTokenizer: openAIDeepSeek,
		},
		openai.Registration{
			Key:        "missing_tokenizer",
			Chat:       openAIDeepSeek,
			StreamChat: openAIDeepSeek,
		},
	)
	if err != nil {
		t.Fatalf("openai.NewRegistry returned error: %v", err)
	}
	anthropicRegistry, err := anthropic.NewRegistry()
	if err != nil {
		t.Fatalf("anthropic.NewRegistry returned error: %v", err)
	}
	registry, err := NewAdapterRegistry(openAIRegistry, anthropicRegistry)
	if err != nil {
		t.Fatalf("NewAdapterRegistry returned error: %v", err)
	}

	candidates := []routing.ChatRouteCandidate{
		{AdapterKey: "missing_stream", Channel: channelRuntime(1)},
		{AdapterKey: "complete", Channel: channelRuntime(2)},
		{AdapterKey: "unknown", Channel: channelRuntime(3)},
		{AdapterKey: "complete", Channel: channelRuntime(4)},
		{AdapterKey: "missing_tokenizer", Channel: channelRuntime(5)},
	}

	got := registry.FilterCandidates(
		routing.ProtocolOpenAI,
		candidates,
		AdapterCapabilityStream,
		AdapterCapabilityInputTokenizer,
	)
	if len(got) != 2 {
		t.Fatalf("expected 2 candidates, got %d", len(got))
	}
	if got[0].Channel.ID != 2 || got[1].Channel.ID != 4 {
		t.Fatalf("expected original routing order [2 4], got [%d %d]", got[0].Channel.ID, got[1].Channel.ID)
	}
}

func TestAdapterRegistryHasAnyKeepsProtocolBindingsIndependent(t *testing.T) {
	registry := newTestAdapterRegistry(t)

	if !registry.HasAny(routing.ProtocolOpenAI, "deepseek") {
		t.Fatal("expected openai deepseek binding")
	}
	if !registry.HasAny(routing.ProtocolAnthropic, "deepseek") {
		t.Fatal("expected anthropic deepseek binding")
	}
	if registry.HasAny("unknown", "deepseek") {
		t.Fatal("expected unknown protocol binding to be absent")
	}
}

func TestNewAdapterRegistryRejectsMissingProtocolRegistry(t *testing.T) {
	anthropicRegistry, err := anthropic.NewRegistry()
	if err != nil {
		t.Fatalf("anthropic.NewRegistry returned error: %v", err)
	}

	_, err = NewAdapterRegistry(nil, anthropicRegistry)
	if !errors.Is(err, ErrProtocolRegistryMissing) {
		t.Fatalf("expected ErrProtocolRegistryMissing, got %v", err)
	}
	if got := failure.CodeOf(err); got != failure.CodeAdapterInvalidRegistration {
		t.Fatalf("expected code %q, got %q", failure.CodeAdapterInvalidRegistration, got)
	}
}

func newTestAdapterRegistry(t *testing.T) *AdapterRegistry {
	t.Helper()

	openAIDeepSeek := openaideepseek.NewAdapter(nil, nil)
	openAIRegistry, err := openai.NewRegistry(openai.Registration{
		Key:                "deepseek",
		Chat:               openAIDeepSeek,
		StreamChat:         openAIDeepSeek,
		ChatInputTokenizer: openAIDeepSeek,
	})
	if err != nil {
		t.Fatalf("openai.NewRegistry returned error: %v", err)
	}

	anthropicDeepSeek := anthropicdeepseek.NewAdapter(nil, nil)
	anthropicRegistry, err := anthropic.NewRegistry(anthropic.Registration{
		Key:                    "deepseek",
		Messages:               anthropicDeepSeek,
		StreamMessages:         anthropicDeepSeek,
		MessagesInputTokenizer: anthropicDeepSeek,
	})
	if err != nil {
		t.Fatalf("anthropic.NewRegistry returned error: %v", err)
	}

	registry, err := NewAdapterRegistry(openAIRegistry, anthropicRegistry)
	if err != nil {
		t.Fatalf("NewAdapterRegistry returned error: %v", err)
	}
	return registry
}

func channelRuntime(id int64) channel.Runtime {
	return channel.Runtime{ID: id}
}
