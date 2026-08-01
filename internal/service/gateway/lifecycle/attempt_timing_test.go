package lifecycle

import (
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

func TestAttemptTimingObserverRecordsResponseHeaderFactsFirstWriteWins(t *testing.T) {
	start := time.Date(2026, time.July, 22, 2, 30, 0, 0, time.UTC)
	observer := newAttemptTimingObserver(false, sequenceClock(start, start.Add(275*time.Millisecond), start.Add(time.Second)))

	observer.TransportStarted()
	observer.ResponseHeadersReceived(adapter.UpstreamMetadata{StatusCode: 201, RequestID: "upstream-1"})
	observer.ResponseHeadersReceived(adapter.UpstreamMetadata{StatusCode: 500, RequestID: "ignored"})

	facts := observer.Snapshot()
	assertTimeEqual(t, facts.UpstreamResponseHeadersAt, start.Add(275*time.Millisecond))
	if got := facts.ResponseHeaderMs(); got == nil || *got != 275 {
		t.Fatalf("response header ms = %v, want 275", got)
	}
	if facts.UpstreamStatusCode != 201 || facts.UpstreamRequestID != "upstream-1" {
		t.Fatalf("response metadata = %+v", facts)
	}
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

// TestChatStreamChunkMetaWiresFirstTokenPayload 验证 meta 映射把 adapter 的权威判定同时接到
// FirstTokenEligible 和 VisibleText 上：两者同源，才不会出现「算了首字但 partial settlement
// 不计这段文本」的错位。完整协议矩阵由 adapter 包的 TestFirstTokenPayloadMatrix 覆盖。
func TestChatStreamChunkMetaWiresFirstTokenPayload(t *testing.T) {
	reasoning := "thinking"
	finish := "stop"
	tests := []struct {
		name            string
		chunk           chatcompletionsadapter.ChatStreamChunk
		wantEligible    bool
		wantVisibleText string
	}{
		{
			name:            "content delta carries both facts",
			chunk:           chatcompletionsadapter.ChatStreamChunk{Content: "hello"},
			wantEligible:    true,
			wantVisibleText: "hello",
		},
		{
			name:            "reasoning delta counts as generated output",
			chunk:           chatcompletionsadapter.ChatStreamChunk{ReasoningContent: &reasoning},
			wantEligible:    true,
			wantVisibleText: "thinking",
		},
		{
			name:  "role-only is a prelude frame",
			chunk: chatcompletionsadapter.ChatStreamChunk{Role: "assistant"},
		},
		{
			name:  "finish-only is not a first token",
			chunk: chatcompletionsadapter.ChatStreamChunk{FinishReason: &finish},
		},
		{
			name:  "usage-only is not a first token",
			chunk: chatcompletionsadapter.ChatStreamChunk{Usage: &adapter.ChatUsage{TotalTokens: 1}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			meta := chatStreamChunkMeta(tt.chunk)
			if meta.FirstTokenEligible != tt.wantEligible {
				t.Fatalf("FirstTokenEligible = %v, want %v", meta.FirstTokenEligible, tt.wantEligible)
			}
			if meta.VisibleText != tt.wantVisibleText {
				t.Fatalf("VisibleText = %q, want %q", meta.VisibleText, tt.wantVisibleText)
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
