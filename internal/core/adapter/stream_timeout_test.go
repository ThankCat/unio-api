package adapter

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestStreamIdleTimeoutDefaultsWhenUnset(t *testing.T) {
	t.Cleanup(func() { SetStreamIdleTimeout(0) })

	SetStreamIdleTimeout(0)
	if got := StreamIdleTimeout(); got != DefaultStreamIdleTimeout {
		t.Fatalf("expected default %v, got %v", DefaultStreamIdleTimeout, got)
	}

	SetStreamIdleTimeout(-time.Second)
	if got := StreamIdleTimeout(); got != DefaultStreamIdleTimeout {
		t.Fatalf("expected default %v for negative timeout, got %v", DefaultStreamIdleTimeout, got)
	}

	SetStreamIdleTimeout(42 * time.Second)
	if got := StreamIdleTimeout(); got != 42*time.Second {
		t.Fatalf("expected configured 42s, got %v", got)
	}
}

func TestStreamTimeoutContextIdleFiresAfterHeaders(t *testing.T) {
	ctx, headersReceived, _, cancel := StreamTimeoutContext(context.Background(), 0, 20*time.Millisecond)
	defer cancel()

	headersReceived()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("idle watchdog did not fire")
	}
	if !errors.Is(context.Cause(ctx), ErrStreamIdleTimeout) {
		t.Fatalf("expected cause ErrStreamIdleTimeout, got %v", context.Cause(ctx))
	}
}

func TestStreamTimeoutContextResetKeepsStreamAlive(t *testing.T) {
	ctx, headersReceived, resetIdle, cancel := StreamTimeoutContext(context.Background(), 0, 80*time.Millisecond)
	defer cancel()

	headersReceived()

	// 持续活动（每 20ms 复位一次）应让 80ms idle 窗口始终不触发。
	for i := 0; i < 6; i++ {
		time.Sleep(20 * time.Millisecond)
		resetIdle()
		if err := ctx.Err(); err != nil {
			t.Fatalf("ctx canceled while active: %v", context.Cause(ctx))
		}
	}
}

func TestStreamTimeoutContextIdleDoesNotFireBeforeHeaders(t *testing.T) {
	ctx, _, _, cancel := StreamTimeoutContext(context.Background(), 0, 20*time.Millisecond)
	defer cancel()

	// 未调用 headersReceived（仍在等响应头阶段）时 idle 看门狗不应启动。
	time.Sleep(120 * time.Millisecond)
	if err := ctx.Err(); err != nil {
		t.Fatalf("idle watchdog fired before headers received: %v", context.Cause(ctx))
	}
}

func TestStreamTimeoutContextHeaderTimeoutFires(t *testing.T) {
	ctx, _, _, cancel := StreamTimeoutContext(context.Background(), 20*time.Millisecond, 0)
	defer cancel()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("header timeout did not fire")
	}
	if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		t.Fatalf("expected cause DeadlineExceeded, got %v", context.Cause(ctx))
	}
}
