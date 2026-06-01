package bootstrap

import (
	"testing"

	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/service/gateway/lifecycle"
)

func TestNewAdapterRegistryRegistersDeepSeekDualProtocolCapabilities(t *testing.T) {
	registry, err := NewAdapterRegistry(nil)
	if err != nil {
		t.Fatalf("NewAdapterRegistry returned error: %v", err)
	}

	if !registry.OpenAI.HasChat("deepseek") {
		t.Fatal("expected deepseek openai chat capability to be registered")
	}
	if !registry.OpenAI.HasStreamChat("deepseek") {
		t.Fatal("expected deepseek openai stream chat capability to be registered")
	}
	if !registry.OpenAI.HasChatInputTokenizer("deepseek") {
		t.Fatal("expected deepseek openai chat input tokenizer to be registered")
	}
	if !registry.Anthropic.HasMessages("deepseek") {
		t.Fatal("expected deepseek anthropic messages capability to be registered")
	}
	if !registry.Anthropic.HasStreamMessages("deepseek") {
		t.Fatal("expected deepseek anthropic stream messages capability to be registered")
	}
	if !registry.Anthropic.HasMessagesInputTokenizer("deepseek") {
		t.Fatal("expected deepseek anthropic messages input tokenizer to be registered")
	}
	if registry.Has(routing.ProtocolOpenAI, "missing", lifecycle.AdapterCapabilityNonStream) {
		t.Fatal("expected unknown openai capability to be absent")
	}
	if registry.Has("missing", "deepseek", lifecycle.AdapterCapabilityNonStream) {
		t.Fatal("expected unknown protocol capability to be absent")
	}
}
