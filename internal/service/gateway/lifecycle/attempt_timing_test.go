package lifecycle

import (
	"encoding/json"
	"sync"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
)

func TestAttemptTimingObserverStreamFactsAreFirstWriteWins(t *testing.T) {
	start := time.Date(2026, time.July, 22, 1, 2, 3, 0, time.UTC)
	clock := sequenceClock(start, start.Add(1250*time.Millisecond), start.Add(9*time.Second), start.Add(10*time.Second))
	observer := newAttemptTimingObserver(true, clock)

	observer.TransportStarted()
	observer.FirstTokenEligible()
	observer.FirstTokenEligible()
	observer.TransportCompleted()
	observer.TransportCompleted()

	facts := observer.Snapshot()
	assertTimeEqual(t, facts.UpstreamStartedAt, start)
	assertTimeEqual(t, facts.UpstreamFirstTokenAt, start.Add(1250*time.Millisecond))
	assertTimeEqual(t, facts.UpstreamCompletedAt, start.Add(9*time.Second))
	if got := facts.FirstTokenMs(); got == nil || *got != 1250 {
		t.Fatalf("first token ms = %v, want 1250", got)
	}
}

func TestAttemptTimingObserverNonStreamNeverRecordsFirstToken(t *testing.T) {
	start := time.Date(2026, time.July, 22, 2, 0, 0, 0, time.UTC)
	observer := newAttemptTimingObserver(false, sequenceClock(start, start.Add(time.Second), start.Add(3*time.Second)))

	observer.TransportStarted()
	observer.FirstTokenEligible()
	observer.TransportCompleted()

	facts := observer.Snapshot()
	assertTimeEqual(t, facts.UpstreamStartedAt, start)
	if facts.UpstreamFirstTokenAt != nil || facts.FirstTokenMs() != nil {
		t.Fatalf("non-stream first token must stay nil: %+v", facts)
	}
	assertTimeEqual(t, facts.UpstreamCompletedAt, start.Add(time.Second))
}

func TestAttemptTimingObserverPreTransportFailureStaysEmpty(t *testing.T) {
	observer := newAttemptTimingObserver(true, sequenceClock(time.Now()))
	observer.FirstTokenEligible()
	observer.TransportCompleted()
	if got := observer.Snapshot(); got != (AttemptTimingFacts{}) {
		t.Fatalf("pre-transport facts must be empty: %+v", got)
	}
}

func TestAttemptTimingObserverIgnoresLateFirstTokenAndClampsClockRollback(t *testing.T) {
	start := time.Date(2026, time.July, 22, 3, 0, 0, 0, time.UTC)
	observer := newAttemptTimingObserver(true, sequenceClock(start, start.Add(-time.Second), start.Add(-2*time.Second)))

	observer.TransportStarted()
	observer.TransportCompleted()
	observer.FirstTokenEligible()

	facts := observer.Snapshot()
	assertTimeEqual(t, facts.UpstreamStartedAt, start)
	assertTimeEqual(t, facts.UpstreamCompletedAt, start)
	if facts.UpstreamFirstTokenAt != nil {
		t.Fatalf("late first token must be ignored: %+v", facts)
	}
}

func TestAttemptTimingObserverConcurrentCallsPreserveInvariants(t *testing.T) {
	start := time.Date(2026, time.July, 22, 4, 0, 0, 0, time.UTC)
	observer := newAttemptTimingObserver(true, func() time.Time { return start })
	observer.TransportStarted()

	var wg sync.WaitGroup
	for i := 0; i < 32; i++ {
		wg.Add(2)
		go func() {
			defer wg.Done()
			observer.FirstTokenEligible()
		}()
		go func() {
			defer wg.Done()
			observer.TransportCompleted()
		}()
	}
	wg.Wait()

	facts := observer.Snapshot()
	if facts.UpstreamStartedAt == nil || facts.UpstreamCompletedAt == nil {
		t.Fatalf("start/completed facts missing: %+v", facts)
	}
	if facts.UpstreamFirstTokenAt != nil && facts.UpstreamFirstTokenAt.After(*facts.UpstreamCompletedAt) {
		t.Fatalf("first token after completion: %+v", facts)
	}
}

func TestChatChunkFirstTokenEligibleUsesProtocolOutputMetadata(t *testing.T) {
	reasoning := "thinking"
	refusal := "cannot comply"
	finish := "stop"
	tests := []struct {
		name  string
		chunk chatcompletionsadapter.ChatStreamChunk
		want  bool
	}{
		{name: "assistant role", chunk: chatcompletionsadapter.ChatStreamChunk{Role: "assistant"}, want: true},
		{name: "content delta", chunk: chatcompletionsadapter.ChatStreamChunk{Content: "hello"}, want: true},
		{name: "reasoning delta", chunk: chatcompletionsadapter.ChatStreamChunk{ReasoningContent: &reasoning}, want: true},
		{name: "tool call delta", chunk: chatcompletionsadapter.ChatStreamChunk{ToolCalls: json.RawMessage(`[{"index":0}]`)}, want: true},
		{name: "refusal delta", chunk: chatcompletionsadapter.ChatStreamChunk{Refusal: &refusal}, want: true},
		{name: "function call delta", chunk: chatcompletionsadapter.ChatStreamChunk{FunctionCall: json.RawMessage(`{"name":"lookup"}`)}, want: true},
		{name: "empty chunk", chunk: chatcompletionsadapter.ChatStreamChunk{}, want: false},
		{name: "finish only", chunk: chatcompletionsadapter.ChatStreamChunk{FinishReason: &finish}, want: false},
		{name: "usage only", chunk: chatcompletionsadapter.ChatStreamChunk{Usage: &adapter.ChatUsage{TotalTokens: 1}}, want: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := chatStreamChunkMeta(tt.chunk)
			if meta.FirstTokenEligible != tt.want {
				t.Fatalf("FirstTokenEligible = %v, want %v", meta.FirstTokenEligible, tt.want)
			}
		})
	}
}

func sequenceClock(values ...time.Time) func() time.Time {
	var mu sync.Mutex
	index := 0
	return func() time.Time {
		mu.Lock()
		defer mu.Unlock()
		if index >= len(values) {
			return values[len(values)-1]
		}
		value := values[index]
		index++
		return value
	}
}

func assertTimeEqual(t *testing.T, got *time.Time, want time.Time) {
	t.Helper()
	if got == nil || !got.Equal(want) {
		t.Fatalf("time = %v, want %v", got, want)
	}
}
