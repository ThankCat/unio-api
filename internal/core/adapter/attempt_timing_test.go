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

func (o *recordingAttemptTimingObserver) ResponseHeadersReceived() {
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
	MarkResponseHeadersReceived(traceCtx)
	MarkFirstTokenEligible(ctx)
	MarkTransportCompleted(ctx)

	want := []string{"started", "write_completed", "headers", "first_token", "completed"}
	if !reflect.DeepEqual(observer.events, want) {
		t.Fatalf("events = %v, want %v", observer.events, want)
	}
}

func TestAttemptTimingObserverContextNilInputsAreSafe(t *testing.T) {
	ctx := WithAttemptTimingObserver(nil, nil)
	if ctx == nil {
		t.Fatal("nil inputs must still return a usable context")
	}

	MarkTransportStarted(nil)
	MarkResponseHeadersReceived(nil)
	MarkFirstTokenEligible(nil)
	MarkTransportCompleted(nil)
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
