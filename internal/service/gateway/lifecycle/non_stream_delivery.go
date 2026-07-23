package lifecycle

import (
	"context"
	"sync"

	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
)

// DeliveryFinalizer binds an HTTP delivery terminal state to one internal request.
// It deliberately exposes neither the request ID nor the request log implementation.
type DeliveryFinalizer interface {
	Complete()
	Interrupt()
}

type deliveryFinalizer struct {
	once      sync.Once
	complete  func()
	interrupt func()
}

// NewDeliveryFinalizer builds a first-terminal-wins delivery finalizer.
// It is exported so handler tests can use the same production wrapper with recording callbacks.
func NewDeliveryFinalizer(complete, interrupt func()) DeliveryFinalizer {
	if complete == nil || interrupt == nil {
		panic("lifecycle: delivery finalizer callbacks are required")
	}
	return &deliveryFinalizer{complete: complete, interrupt: interrupt}
}

func (f *deliveryFinalizer) Complete() {
	f.once.Do(f.complete)
}

func (f *deliveryFinalizer) Interrupt() {
	f.once.Do(f.interrupt)
}

// NonStreamResult keeps the public response DTO separate from the internal delivery finalizer.
// Only Response is handed to the JSON encoder; no request or attempt ID enters the public DTO.
type NonStreamResult[T any] struct {
	Response  T
	finalizer DeliveryFinalizer
}

// NewNonStreamResult requires a bound delivery finalizer for every successful non-stream response.
func NewNonStreamResult[T any](response T, finalizer DeliveryFinalizer) *NonStreamResult[T] {
	if finalizer == nil {
		panic("lifecycle: non-stream result requires delivery finalizer")
	}
	return &NonStreamResult[T]{Response: response, finalizer: finalizer}
}

// FinalizeDelivery runs the final HTTP write and advances delivery exactly once.
// A write error or panic records interrupted; panics are rethrown for net/http recovery.
func (r *NonStreamResult[T]) FinalizeDelivery(write func(T) error) (err error) {
	if r == nil || r.finalizer == nil {
		panic("lifecycle: non-stream result is not initialized")
	}
	if write == nil {
		panic("lifecycle: non-stream delivery write callback is required")
	}

	defer func() {
		if recovered := recover(); recovered != nil {
			r.finalizer.Interrupt()
			panic(recovered)
		}
	}()

	if err = write(r.Response); err != nil {
		r.finalizer.Interrupt()
		return err
	}

	r.finalizer.Complete()
	return nil
}

// NewNonStreamDeliveryFinalizer binds one settled request to the detached audit writer.
func (l *RequestLifecycle) NewNonStreamDeliveryFinalizer(ctx context.Context, requestRecord requestlog.RequestRecord) DeliveryFinalizer {
	if l == nil || l.requestLog == nil || requestRecord.ID == 0 {
		panic("lifecycle: non-stream delivery requires a persisted request")
	}
	return NewDeliveryFinalizer(
		func() { l.MarkDeliveryCompleted(ctx, requestRecord) },
		func() { l.MarkDeliveryInterrupted(ctx, requestRecord) },
	)
}
