package tpmobserver

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

type recordedBatch struct {
	operationID string
	entries     []breakerstore.TPMObservationEntry
}

type storeStub struct {
	mu sync.Mutex

	batches          []recordedBatch
	recordErrs       []error
	corrections      []string
	correctionResult breakerstore.TPMCorrectionResult
}

func (s *storeStub) RecordTPMObservations(_ context.Context, operationID string, entries []breakerstore.TPMObservationEntry) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.batches = append(s.batches, recordedBatch{operationID: operationID, entries: entries})
	if len(s.recordErrs) > 0 {
		err := s.recordErrs[0]
		s.recordErrs = s.recordErrs[1:]
		if err != nil {
			return false, err
		}
	}
	return true, nil
}

func (s *storeStub) CorrectTPMObservations(_ context.Context, scope string, _ []breakerstore.TPMObservationEntry) (breakerstore.TPMCorrectionResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.corrections = append(s.corrections, scope)
	return s.correctionResult, nil
}

func (s *storeStub) snapshot() ([]recordedBatch, []string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]recordedBatch(nil), s.batches...), append([]string(nil), s.corrections...)
}

func newTestObserver(t *testing.T, store Store) *Observer {
	t.Helper()
	observer := New(store, Options{FlushInterval: time.Hour})
	if observer == nil {
		t.Fatal("observer must be created for a non-nil store")
	}
	return observer
}

func minuteOf(sec int) time.Time {
	return time.Unix(int64(sec)*60, 0).UTC()
}

func TestObserverAggregatesOneBatchPerScopeAndMinute(t *testing.T) {
	store := &storeStub{}
	observer := newTestObserver(t, store)
	route := Scope{Kind: breakerstore.TPMScopeRoute, ID: 5, Key: "req-1"}

	observer.Input(route, minuteOf(10), 100)
	observer.Output(route, minuteOf(10), 20)
	observer.Output(route, minuteOf(10), 30)
	observer.Output(route, minuteOf(11), 40)
	observer.flushOnce(context.Background())

	batches, _ := store.snapshot()
	if len(batches) != 1 {
		t.Fatalf("flush must send exactly one batch, got %d", len(batches))
	}
	if len(batches[0].entries) != 2 {
		t.Fatalf("entries = %d, want one per minute", len(batches[0].entries))
	}
	for _, entry := range batches[0].entries {
		switch entry.Scope.Minute {
		case 10:
			if entry.Delta.InputTokens != 100 || entry.Delta.OutputTokens != 50 ||
				entry.Delta.ProvisionalTokens != 150 || entry.Delta.ObservedAttempts != 1 {
				t.Fatalf("minute 10 delta = %+v", entry.Delta)
			}
		case 11:
			if entry.Delta.OutputTokens != 40 || entry.Delta.ObservedAttempts != 0 {
				t.Fatalf("minute 11 delta = %+v", entry.Delta)
			}
		default:
			t.Fatalf("unexpected minute %d", entry.Scope.Minute)
		}
	}
}

// 输入幂等保证 fallback 不会把 Route 输入记第二遍。
func TestObserverRecordsInputOnlyOncePerScope(t *testing.T) {
	store := &storeStub{}
	observer := newTestObserver(t, store)
	route := Scope{Kind: breakerstore.TPMScopeRoute, ID: 5, Key: "req-2"}

	observer.Input(route, minuteOf(10), 100)
	observer.Input(route, minuteOf(12), 250)
	observer.flushOnce(context.Background())

	batches, _ := store.snapshot()
	if len(batches[0].entries) != 1 || batches[0].entries[0].Delta.InputTokens != 100 {
		t.Fatalf("repeated input must be ignored, got %+v", batches[0].entries)
	}
}

// flush 结果未知时必须用同一个 operation id 重试，否则「写成功但响应丢失」会被重复累加。
func TestObserverRetriesFailedBatchWithStableOperationID(t *testing.T) {
	store := &storeStub{recordErrs: []error{errors.New("redis timeout")}}
	observer := newTestObserver(t, store)
	route := Scope{Kind: breakerstore.TPMScopeRoute, ID: 5, Key: "req-3"}

	observer.Input(route, minuteOf(10), 100)
	observer.flushOnce(context.Background())
	observer.flushOnce(context.Background())

	batches, _ := store.snapshot()
	if len(batches) != 2 {
		t.Fatalf("failed batch must be retried once, got %d attempts", len(batches))
	}
	if batches[0].operationID != batches[1].operationID {
		t.Fatalf("retry used a new operation id: %q vs %q", batches[0].operationID, batches[1].operationID)
	}
}

func TestObserverDropsObservationsWhenQueueIsFull(t *testing.T) {
	store := &storeStub{}
	observer := New(store, Options{FlushInterval: time.Hour, QueueCapacity: 1})
	route := Scope{Kind: breakerstore.TPMScopeRoute, ID: 5, Key: "req-4"}

	// 队列容量为 1：第二条起必须被丢弃而不是阻塞客户响应。
	observer.Input(route, minuteOf(10), 10)
	for i := 0; i < 50; i++ {
		observer.Output(route, minuteOf(10), 1)
	}
	observer.flushOnce(context.Background())

	batches, _ := store.snapshot()
	if len(batches) != 1 || len(batches[0].entries) != 1 {
		t.Fatalf("unexpected batches: %+v", batches)
	}
	if got := batches[0].entries[0].Delta.InputTokens; got != 10 {
		t.Fatalf("first observation must survive, got input=%d", got)
	}
}

func TestFinalizeWithoutReliableUsageOnlyCountsMissing(t *testing.T) {
	store := &storeStub{}
	observer := newTestObserver(t, store)
	channel := Scope{Kind: breakerstore.TPMScopeChannel, ID: 8, Key: "attempt-1"}

	observer.Input(channel, minuteOf(10), 60)
	observer.Finalize(channel, minuteOf(11), usage.Facts{}, false)
	observer.flushOnce(context.Background())

	batches, corrections := store.snapshot()
	if len(corrections) != 0 {
		t.Fatalf("unreliable usage must not correct any bucket, got %+v", corrections)
	}
	var missing int64
	var provisional int64
	for _, entry := range batches[0].entries {
		missing += entry.Delta.MissingUsageCount
		provisional += entry.Delta.ProvisionalTokens
	}
	if missing != 1 {
		t.Fatalf("missing usage count = %d, want 1", missing)
	}
	if provisional != 60 {
		t.Fatalf("provisional estimate must be retained, got %d", provisional)
	}
}

func TestFinalizeWithoutTrackedWeightsDoesNotCorrect(t *testing.T) {
	store := &storeStub{}
	observer := newTestObserver(t, store)
	// recovery worker 在另一个进程重放：本进程没有写过任何 provisional，不能凭空补桶。
	channel := Scope{Kind: breakerstore.TPMScopeChannel, ID: 8, Key: "attempt-replayed"}
	observer.Finalize(channel, minuteOf(11), reliableFacts(30, 70), true)

	if _, corrections := store.snapshot(); len(corrections) != 0 {
		t.Fatalf("untracked scope must not produce a correction: %+v", corrections)
	}
}

func TestFinalizeAppliesCorrectionOncePerScope(t *testing.T) {
	store := &storeStub{}
	observer := newTestObserver(t, store)
	channel := Scope{Kind: breakerstore.TPMScopeChannel, ID: 8, Key: "attempt-2"}

	observer.Input(channel, minuteOf(10), 30)
	observer.Output(channel, minuteOf(10), 50)
	observer.Finalize(channel, minuteOf(10), reliableFacts(30, 70), true)
	// 第二次 Finalize 已经没有权重可用，不会再排一次修正。
	observer.Finalize(channel, minuteOf(10), reliableFacts(30, 70), true)

	job := <-observer.corrections
	observer.applyCorrection(context.Background(), job)

	_, corrections := store.snapshot()
	if len(corrections) != 1 || corrections[0] != "channel:attempt-2" {
		t.Fatalf("corrections = %+v, want exactly one channel:attempt-2", corrections)
	}
	if len(observer.corrections) != 0 {
		t.Fatalf("duplicate finalize queued another correction")
	}
}

func TestNilObserverIsSafeNoOp(t *testing.T) {
	var observer *Observer
	scope := Scope{Kind: breakerstore.TPMScopeRoute, ID: 1, Key: "req"}
	observer.Input(scope, minuteOf(1), 10)
	observer.Output(scope, minuteOf(1), 10)
	observer.Finalize(scope, minuteOf(1), usage.Facts{}, true)
	observer.Run(context.Background())
	observer.Wait(context.Background())
}

func reliableFacts(input, output int64) usage.Facts {
	return usage.Facts{
		UncachedInputTokens:      usage.KnownTokens(input),
		CacheReadInputTokens:     usage.KnownTokens(0),
		CacheWrite5mInputTokens:  usage.NotApplicableTokens(),
		CacheWrite30mInputTokens: usage.NotApplicableTokens(),
		CacheWrite1hInputTokens:  usage.NotApplicableTokens(),
		OutputTokensTotal:        usage.KnownTokens(output),
		ReasoningOutputTokens:    usage.NotApplicableTokens(),
	}
}
