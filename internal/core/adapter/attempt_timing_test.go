package adapter

import (
	"context"
	"reflect"
	"testing"
)

type recordingAttemptTimingObserver struct {
	events []string
}

func (o *recordingAttemptTimingObserver) TransportStarted() {
	o.events = append(o.events, "started")
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
	MarkFirstTokenEligible(ctx)
	MarkTransportCompleted(ctx)

	want := []string{"started", "first_token", "completed"}
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
	MarkFirstTokenEligible(nil)
	MarkTransportCompleted(nil)
}
