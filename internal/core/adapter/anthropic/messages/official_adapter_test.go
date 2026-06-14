package messages

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/channel"
)

func TestOfficialAdapterForwardsWhitelistedBetaHeader(t *testing.T) {
	var capturedBeta string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBeta = r.Header.Get("anthropic-beta")
		_, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1",
			"type": "message",
			"role": "assistant",
			"model": "claude-sonnet-4-20250514",
			"content": [{"type":"text","text":"ok"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 3, "output_tokens": 1}
		}`))
	}))
	defer server.Close()

	official := NewOfficialAdapter(server.Client(), nil)
	maxTokens := 16
	_, err := official.Messages(context.Background(), channel.Runtime{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	}, MessageRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []Message{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
		MaxTokens: &maxTokens,
		AnthropicBeta: []string{
			"made-up-beta",
			"prompt-caching-2024-07-31",
		},
	})
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}

	if capturedBeta != "prompt-caching-2024-07-31" {
		t.Fatalf("anthropic-beta = %q, want prompt-caching-2024-07-31", capturedBeta)
	}
}

func TestOfficialAdapterDropsUnsupportedBetaHeader(t *testing.T) {
	var capturedBeta string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedBeta = r.Header.Get("anthropic-beta")
		_, _ = io.ReadAll(r.Body)

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "msg_1",
			"type": "message",
			"role": "assistant",
			"model": "claude-sonnet-4-20250514",
			"content": [{"type":"text","text":"ok"}],
			"stop_reason": "end_turn",
			"usage": {"input_tokens": 3, "output_tokens": 1}
		}`))
	}))
	defer server.Close()

	official := NewOfficialAdapter(server.Client(), nil)
	maxTokens := 16
	_, err := official.Messages(context.Background(), channel.Runtime{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	}, MessageRequest{
		Model: "claude-sonnet-4-20250514",
		Messages: []Message{
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
		MaxTokens:     &maxTokens,
		AnthropicBeta: []string{"made-up-beta"},
	})
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}

	if capturedBeta != "" {
		t.Fatalf("expected anthropic-beta omitted, got %q", capturedBeta)
	}
}
