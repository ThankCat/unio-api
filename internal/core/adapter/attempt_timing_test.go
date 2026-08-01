package adapter

import (
	"context"
	"errors"
	"net/http/httptrace"
	"reflect"
	"testing"
)

type recordingAttemptTimingObserver struct {
	events []string
}

func (o *recordingAttemptTimingObserver) TransportStarted() {
	o.events = append(o.events, "started")
}

func (o *recordingAttemptTimingObserver) RequestWritten(err error) {
	if err == nil {
		o.events = append(o.events, "write_completed")
		return
	}
	o.events = append(o.events, "write_uncertain")
}

func (o *recordingAttemptTimingObserver) ResponseHeadersReceived(UpstreamMetadata) {
	o.events = append(o.events, "headers")
}

func (o *recordingAttemptTimingObserver) FirstTokenEligible() {
	o.events = append(o.events, "first_token")
}

func (o *recordingAttemptTimingObserver) TransportCompleted() {
	o.events = append(o.events, "completed")
}

func TestAttemptTimingObserverContextDispatchesAllEvents(t *testing.T) {
	observer := &recordingAttemptTimingObserver{}
	ctx := WithAttemptTimingObserver(context.Background(), observer)

	MarkTransportStarted(ctx)
	traceCtx := WithAttemptTransportTrace(ctx)
	trace := httptrace.ContextClientTrace(traceCtx)
	trace.WroteRequest(httptrace.WroteRequestInfo{})
	MarkResponseHeadersReceived(traceCtx, UpstreamMetadata{StatusCode: 200, RequestID: "upstream-1"})
	MarkFirstTokenEligible(ctx)
	MarkTransportCompleted(ctx)

	want := []string{"started", "write_completed", "headers", "first_token", "completed"}
	if !reflect.DeepEqual(observer.events, want) {
		t.Fatalf("events = %v, want %v", observer.events, want)
	}
}

func TestAttemptTimingObserverContextNilInputsAreSafe(t *testing.T) {
	// Use an explicitly typed nil instead of the bare nil identifier so the
	// contract (Mark* / WithAttemptTimingObserver tolerate a missing parent) is
	// still covered without tripping SA1012's "do not pass nil Context" lint.
	var missing context.Context
	ctx := WithAttemptTimingObserver(missing, nil)
	if ctx == nil {
		t.Fatal("nil inputs must still return a usable context")
	}
	if got := attemptTimingObserverFromContext(missing); got != nil {
		t.Fatalf("nil context must yield no observer, got %#v", got)
	}

	MarkTransportStarted(missing)
	MarkResponseHeadersReceived(missing, UpstreamMetadata{})
	MarkFirstTokenEligible(missing)
	MarkTransportCompleted(missing)
}

func TestAttemptTransportTraceReportsUncertainWrite(t *testing.T) {
	observer := &recordingAttemptTimingObserver{}
	ctx := WithAttemptTransportTrace(WithAttemptTimingObserver(context.Background(), observer))
	trace := httptrace.ContextClientTrace(ctx)
	trace.WroteRequest(httptrace.WroteRequestInfo{Err: errors.New("partial write")})
	if want := []string{"write_uncertain"}; !reflect.DeepEqual(observer.events, want) {
		t.Fatalf("events = %v, want %v", observer.events, want)
	}
}
