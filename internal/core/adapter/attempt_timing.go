package adapter

import "context"

// AttemptTimingObserver receives protocol-independent transport timing events.
// Implementations must be concurrency-safe and first-write-wins.
type AttemptTimingObserver interface {
	TransportStarted()
	FirstTokenEligible()
	TransportCompleted()
}

type attemptTimingObserverContextKey struct{}

// WithAttemptTimingObserver attaches one attempt-scoped observer without
// changing adapter interfaces or coupling adapters to gateway lifecycle.
func WithAttemptTimingObserver(ctx context.Context, observer AttemptTimingObserver) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	if observer == nil {
		return ctx
	}
	return context.WithValue(ctx, attemptTimingObserverContextKey{}, observer)
}

// MarkTransportStarted must be called immediately before http.Client.Do.
func MarkTransportStarted(ctx context.Context) {
	if observer := attemptTimingObserverFromContext(ctx); observer != nil {
		observer.TransportStarted()
	}
}

// MarkFirstTokenEligible records the first protocol-defined stream event that
// qualifies as upstream FirstToken. Customer write acknowledgement is separate.
func MarkFirstTokenEligible(ctx context.Context) {
	if observer := attemptTimingObserverFromContext(ctx); observer != nil {
		observer.FirstTokenEligible()
	}
}

// MarkTransportCompleted is called by lifecycle after the adapter returns.
func MarkTransportCompleted(ctx context.Context) {
	if observer := attemptTimingObserverFromContext(ctx); observer != nil {
		observer.TransportCompleted()
	}
}

func attemptTimingObserverFromContext(ctx context.Context) AttemptTimingObserver {
	if ctx == nil {
		return nil
	}
	observer, _ := ctx.Value(attemptTimingObserverContextKey{}).(AttemptTimingObserver)
	return observer
}
