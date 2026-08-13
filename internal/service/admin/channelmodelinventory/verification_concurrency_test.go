package channelmodelinventory

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestVerificationConcurrencyIsFive(t *testing.T) {
	if verificationConcurrency != 5 {
		t.Fatalf("verificationConcurrency = %d, want 5", verificationConcurrency)
	}
}

func TestRunVerificationWorkLimitsConcurrency(t *testing.T) {
	const itemCount = 12
	var active atomic.Int32
	var maximum atomic.Int32
	release := make(chan struct{})
	started := make(chan struct{}, itemCount)

	done := make(chan struct{})
	go func() {
		runVerificationWork(itemCount, func(_ int) verificationExecutionResult {
			current := active.Add(1)
			for {
				previous := maximum.Load()
				if current <= previous || maximum.CompareAndSwap(previous, current) {
					break
				}
			}
			started <- struct{}{}
			<-release
			active.Add(-1)
			return verificationExecutionResult{}
		})
		close(done)
	}()

	for range verificationConcurrency {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("timed out waiting for initial verification workers")
		}
	}
	select {
	case <-started:
		t.Fatal("started more than five verification workers before one completed")
	case <-time.After(50 * time.Millisecond):
	}

	close(release)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for verification work")
	}
	if got := maximum.Load(); got != verificationConcurrency {
		t.Fatalf("maximum concurrency = %d, want %d", got, verificationConcurrency)
	}
}

func TestRunVerificationWorkStopsSchedulingAfterTerminalError(t *testing.T) {
	const itemCount = 12
	var started atomic.Int32
	release := make(chan struct{})

	done := make(chan verificationExecutionResult, 1)
	go func() {
		done <- runVerificationWork(itemCount, func(index int) verificationExecutionResult {
			started.Add(1)
			if index == 0 {
				return verificationExecutionResult{errorCode: VerificationErrorCredentialInvalid}
			}
			<-release
			return verificationExecutionResult{}
		})
	}()

	deadline := time.After(time.Second)
	for started.Load() < verificationConcurrency {
		select {
		case <-deadline:
			t.Fatal("timed out waiting for initial verification workers")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	close(release)

	select {
	case result := <-done:
		if result.errorCode != VerificationErrorCredentialInvalid {
			t.Fatalf("error code = %q, want %q", result.errorCode, VerificationErrorCredentialInvalid)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for verification work")
	}
	if got := started.Load(); got != verificationConcurrency {
		t.Fatalf("started %d items, want only the initial %d", got, verificationConcurrency)
	}
}
