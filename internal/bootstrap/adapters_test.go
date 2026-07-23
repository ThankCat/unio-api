package bootstrap

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	chatcompletions "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

func TestNewAdapterRegistryRegistersDeepSeekDualProtocolCapabilities(t *testing.T) {
	registry, err := NewAdapterRegistry(nil, nil)
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
	if !registry.Anthropic.HasMessages("anthropic") {
		t.Fatal("expected anthropic official messages capability to be registered")
	}
	if !registry.Anthropic.HasStreamMessages("anthropic") {
		t.Fatal("expected anthropic official stream messages capability to be registered")
	}
	if !registry.Anthropic.HasMessagesInputTokenizer("anthropic") {
		t.Fatal("expected anthropic official messages input tokenizer to be registered")
	}
	if !registry.OpenAI.HasChat("openai") {
		t.Fatal("expected openai official chat capability to be registered")
	}
	if registry.Has(routing.ProtocolOpenAI, "missing", lifecycle.AdapterCapabilityNonStream) {
		t.Fatal("expected unknown openai capability to be absent")
	}
	if registry.Has("missing", "deepseek", lifecycle.AdapterCapabilityNonStream) {
		t.Fatal("expected unknown protocol capability to be absent")
	}
}

func TestNewAdapterRegistryDoesNotReplayPOSTOnRedirect(t *testing.T) {
	for _, status := range []int{http.StatusTemporaryRedirect, http.StatusPermanentRedirect} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			var requests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				if r.URL.Path != "/v1/chat/completions" {
					t.Errorf("redirected request reached %q", r.URL.Path)
					w.WriteHeader(http.StatusOK)
					return
				}
				w.Header().Set("Location", "/replayed")
				w.WriteHeader(status)
			}))
			defer server.Close()

			registry, err := NewAdapterRegistry(http.DefaultClient, nil)
			if err != nil {
				t.Fatalf("NewAdapterRegistry returned error: %v", err)
			}
			chat, ok := registry.OpenAI.Chat("openai")
			if !ok {
				t.Fatal("expected openai chat adapter")
			}
			_, err = chat.ChatCompletions(
				context.Background(),
				channel.Runtime{BaseURL: server.URL, APIKey: "test-key"},
				chatcompletions.ChatRequest{Model: "test-model"},
			)
			if err == nil {
				t.Fatal("expected redirect response to be returned as an upstream error")
			}
			if got := requests.Load(); got != 1 {
				t.Fatalf("real upstream request count = %d, want 1", got)
			}
		})
	}
}
