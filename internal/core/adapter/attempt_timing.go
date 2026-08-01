package adapter

import (
	"context"
	"net/http/httptrace"
)

// RequestWriteState 表示 Go HTTP 客户端对请求写出结果的观测。
type RequestWriteState string

const (
	RequestWriteNotStarted RequestWriteState = "not_started"
	RequestWriteCompleted  RequestWriteState = "completed"
	RequestWriteUncertain  RequestWriteState = "uncertain"
)

// AttemptTimingObserver receives protocol-independent transport timing events.
// Implementations must be concurrency-safe and first-write-wins.
type AttemptTimingObserver interface {
	TransportStarted()
	RequestWritten(error)
	ResponseHeadersReceived(UpstreamMetadata)
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
	startStreamTimeout(ctx)
}

// MarkRequestWritten is primarily useful for custom transports and deterministic tests.
// Production HTTP adapters receive the same fact from httptrace automatically.
func MarkRequestWritten(ctx context.Context, err error) {
	if observer := attemptTimingObserverFromContext(ctx); observer != nil {
		observer.RequestWritten(err)
	}
}

// WithAttemptTransportTrace records whether net/http completed writing the request.
// It observes lifecycle events only and never reads or stores the request body.
func WithAttemptTransportTrace(ctx context.Context) context.Context {
	if ctx == nil {
		ctx = context.Background()
	}
	observer := attemptTimingObserverFromContext(ctx)
	if observer == nil {
		return ctx
	}
	return httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		WroteRequest: func(info httptrace.WroteRequestInfo) {
			observer.RequestWritten(info.Err)
		},
	})
}

// MarkResponseHeadersReceived records that http.Client.Do returned an HTTP response.
// Callers pass only already-sanitized response metadata; response bodies and headers are never retained.
func MarkResponseHeadersReceived(ctx context.Context, metadata UpstreamMetadata) {
	if observer := attemptTimingObserverFromContext(ctx); observer != nil {
		observer.ResponseHeadersReceived(metadata)
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
