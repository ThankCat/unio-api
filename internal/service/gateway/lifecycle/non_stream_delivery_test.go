package lifecycle

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestDeliveryFinalizerFirstTerminalWins(t *testing.T) {
	var completed atomic.Int64
	var interrupted atomic.Int64
	finalizer := NewDeliveryFinalizer(
		func() { completed.Add(1) },
		func() { interrupted.Add(1) },
	)

	var wg sync.WaitGroup
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			if i%2 == 0 {
				finalizer.Complete()
				return
			}
			finalizer.Interrupt()
		}(i)
	}
	wg.Wait()

	if got := completed.Load() + interrupted.Load(); got != 1 {
		t.Fatalf("terminal callbacks = %d, want exactly 1", got)
	}
}

func TestNonStreamResultFinalizeDelivery(t *testing.T) {
	t.Run("successful write completes", func(t *testing.T) {
		completed, interrupted := 0, 0
		result := NewNonStreamResult("response", NewDeliveryFinalizer(
			func() { completed++ },
			func() { interrupted++ },
		))

		if err := result.FinalizeDelivery(func(got string) error {
			if got != "response" {
				t.Fatalf("response = %q, want response", got)
			}
			return nil
		}); err != nil {
			t.Fatalf("finalize delivery: %v", err)
		}
		if completed != 1 || interrupted != 0 {
			t.Fatalf("completed=%d interrupted=%d, want 1/0", completed, interrupted)
		}
	})

	t.Run("write error interrupts", func(t *testing.T) {
		completed, interrupted := 0, 0
		writeErr := errors.New("client disconnected")
		result := NewNonStreamResult("response", NewDeliveryFinalizer(
			func() { completed++ },
			func() { interrupted++ },
		))

		if err := result.FinalizeDelivery(func(string) error { return writeErr }); !errors.Is(err, writeErr) {
			t.Fatalf("error = %v, want %v", err, writeErr)
		}
		if completed != 0 || interrupted != 1 {
			t.Fatalf("completed=%d interrupted=%d, want 0/1", completed, interrupted)
		}
	})

	t.Run("write panic interrupts and repanics", func(t *testing.T) {
		completed, interrupted := 0, 0
		result := NewNonStreamResult("response", NewDeliveryFinalizer(
			func() { completed++ },
			func() { interrupted++ },
		))

		const panicValue = "response writer panic"
		func() {
			defer func() {
				if recovered := recover(); recovered != panicValue {
					t.Fatalf("recovered = %#v, want %q", recovered, panicValue)
				}
			}()
			_ = result.FinalizeDelivery(func(string) error { panic(panicValue) })
		}()
		if completed != 0 || interrupted != 1 {
			t.Fatalf("completed=%d interrupted=%d, want 0/1", completed, interrupted)
		}
	})
}
