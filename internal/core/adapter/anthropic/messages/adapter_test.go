package messages

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

func TestAdapterStreamMessagesWithholdsMessageStopAndReturnsFacts(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/messages" {
			t.Fatalf("path = %q, want /v1/messages", r.URL.Path)
		}

		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("request-id", "req-anthropic-stream-1")
		events := []string{
			"event: message_start\n" +
				`data: {"type":"message_start","message":{"id":"msg_1","model":"deepseek-v4-flash","usage":{"input_tokens":11,"cache_creation_input_tokens":0,"cache_read_input_tokens":2,"output_tokens":0,"service_tier":"standard"}}}` + "\n\n",
			"event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n\n",
			"event: message_delta\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":3}}` + "\n\n",
			"event: message_stop\n" +
				`data: {"type":"message_stop"}` + "\n\n",
			"event: ping\n" +
				`data: {"type":"ping"}` + "\n\n",
		}
		for _, event := range events {
			if _, err := w.Write([]byte(event)); err != nil {
				t.Fatalf("write event: %v", err)
			}
		}
	}))
	defer server.Close()

	var emitted []MessageStreamEvent
	outcome, err := NewAdapter(server.Client()).StreamMessages(
		context.Background(),
		channel.Runtime{
			BaseURL: server.URL,
			APIKey:  "test-secret",
			Timeout: 30 * time.Second,
		},
		MessageRequest{
			Model:     "deepseek-v4-flash",
			MaxTokens: intPtr(16),
			Messages: []Message{
				{Role: "user", Content: []byte(`"hello"`)},
			},
		},
		func(event MessageStreamEvent) error {
			emitted = append(emitted, event)
			return nil
		},
	)
	if err != nil {
		t.Fatalf("StreamMessages returned err: %v", err)
	}

	if got := eventTypes(emitted); len(got) != 3 || got[0] != "message_start" || got[1] != "content_block_delta" || got[2] != "message_delta" {
		t.Fatalf("emitted event types = %v", got)
	}
	if emitted[2].Usage == nil || emitted[2].Usage.InputTokens != 11 || emitted[2].Usage.OutputTokens != 3 {
		t.Fatalf("merged final usage = %+v", emitted[2].Usage)
	}

	if outcome.Facts == nil {
		t.Fatal("expected stream outcome facts")
	}
	if outcome.Facts.UpstreamResponseID != "msg_1" || outcome.Facts.UpstreamModel != "deepseek-v4-flash" {
		t.Fatalf("outcome id/model = %q/%q", outcome.Facts.UpstreamResponseID, outcome.Facts.UpstreamModel)
	}
	if outcome.Facts.Finish.RawReason != "end_turn" {
		t.Fatalf("finish reason = %q, want end_turn", outcome.Facts.Finish.RawReason)
	}
	if outcome.Facts.UsageSource != usage.SourceUpstreamStream {
		t.Fatalf("usage source = %q, want %q", outcome.Facts.UsageSource, usage.SourceUpstreamStream)
	}
	if outcome.Facts.Metadata.RequestID != "req-anthropic-stream-1" {
		t.Fatalf("request id = %q", outcome.Facts.Metadata.RequestID)
	}
	if got, ok := outcome.Facts.Usage.UncachedInputTokens.BillableValue(); !ok || got != 11 {
		t.Fatalf("uncached input = %d ok=%v", got, ok)
	}
	if got, ok := outcome.Facts.Usage.CacheReadInputTokens.BillableValue(); !ok || got != 2 {
		t.Fatalf("cache read = %d ok=%v", got, ok)
	}
	if got, ok := outcome.Facts.Usage.OutputTokensTotal.BillableValue(); !ok || got != 3 {
		t.Fatalf("output = %d ok=%v", got, ok)
	}
}

func TestAdapterStreamMessagesReturnsFactsWithTailErrorBeforeMessageStop(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			"event: message_start\n" +
				`data: {"type":"message_start","message":{"id":"msg_tail","model":"deepseek-v4-flash","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n\n",
			"event: message_delta\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":2}}` + "\n\n",
		}
		for _, event := range events {
			if _, err := w.Write([]byte(event)); err != nil {
				t.Fatalf("write event: %v", err)
			}
		}
	}))
	defer server.Close()

	outcome, err := NewAdapter(server.Client()).StreamMessages(
		context.Background(),
		channel.Runtime{BaseURL: server.URL, APIKey: "test-secret"},
		MessageRequest{
			Model:     "deepseek-v4-flash",
			MaxTokens: intPtr(16),
			Messages:  []Message{{Role: "user", Content: []byte(`"hello"`)}},
		},
		func(MessageStreamEvent) error { return nil },
	)
	if err == nil {
		t.Fatal("expected missing message_stop error")
	}
	if failure.CodeOf(err) != failure.CodeAdapterReadStreamFailed {
		t.Fatalf("failure code = %q, want %q", failure.CodeOf(err), failure.CodeAdapterReadStreamFailed)
	}
	if outcome.Facts == nil {
		t.Fatal("expected reliable facts to survive tail error")
	}
	if got, ok := outcome.Facts.Usage.OutputTokensTotal.BillableValue(); !ok || got != 2 {
		t.Fatalf("output = %d ok=%v", got, ok)
	}
}

func eventTypes(events []MessageStreamEvent) []string {
	types := make([]string, 0, len(events))
	for _, event := range events {
		types = append(types, event.Type)
	}
	return types
}

func intPtr(v int) *int {
	return &v
}
