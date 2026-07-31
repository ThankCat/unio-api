package adapter

import (
	"context"
	"errors"
	"testing"
	"time"
)

func startedStreamTimeoutContext(parent context.Context, config StreamTimeoutConfig) (context.Context, StreamTimeoutHandles) {
	ctx, handles := StreamTimeoutContext(parent, config)
	MarkTransportStarted(ctx)
	return ctx, handles
}

func TestStreamTimeoutDoesNotStartBeforeTransport(t *testing.T) {
	ctx, h := StreamTimeoutContext(context.Background(), StreamTimeoutConfig{
		ResponseHeader: 20 * time.Millisecond,
		FirstToken:     20 * time.Millisecond,
	})
	defer h.Cancel()

	time.Sleep(60 * time.Millisecond)
	if err := ctx.Err(); err != nil {
		t.Fatalf("timeout started before transport: %v", context.Cause(ctx))
	}
	MarkTransportStarted(ctx)
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("timeout did not start with transport")
	}
}

func TestStreamIdleTimeoutDefaultsWhenUnset(t *testing.T) {
	t.Cleanup(func() { SetStreamIdleTimeout(0) })

	SetStreamIdleTimeout(0)
	if got := StreamIdleTimeout(); got != DefaultStreamIdleTimeout {
		t.Fatalf("expected default %v, got %v", DefaultStreamIdleTimeout, got)
	}

	SetStreamIdleTimeout(-time.Second)
	if got := StreamIdleTimeout(); got != DefaultStreamIdleTimeout {
		t.Fatalf("expected default %v for a negative timeout, got %v", DefaultStreamIdleTimeout, got)
	}

	SetStreamIdleTimeout(42 * time.Second)
	if got := StreamIdleTimeout(); got != 42*time.Second {
		t.Fatalf("expected the configured 42s, got %v", got)
	}
}

// TestResponseHeaderTimeoutFiresWithPhase 覆盖 §19.4「流式响应头慢 → response_header 超时」。
func TestResponseHeaderTimeoutFiresWithPhase(t *testing.T) {
	ctx, h := startedStreamTimeoutContext(context.Background(), StreamTimeoutConfig{
		ResponseHeader: 20 * time.Millisecond,
		FirstToken:     5 * time.Second,
	})
	defer h.Cancel()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("response header timeout did not fire")
	}
	if !errors.Is(context.Cause(ctx), context.DeadlineExceeded) {
		t.Fatalf("expected cause DeadlineExceeded, got %v", context.Cause(ctx))
	}
	if got := h.State.TimeoutPhase(); got != TimeoutPhaseResponseHeader {
		t.Fatalf("timeout phase = %q, want %q", got, TimeoutPhaseResponseHeader)
	}
}

// TestFirstTokenTimeoutFiresAfterFastHeaders 覆盖 §19.4「响应头快但无有效事件 → first_token 超时」。
func TestFirstTokenTimeoutFiresAfterFastHeaders(t *testing.T) {
	ctx, h := startedStreamTimeoutContext(context.Background(), StreamTimeoutConfig{
		ResponseHeader: 5 * time.Second,
		FirstToken:     40 * time.Millisecond,
		Idle:           5 * time.Second,
	})
	defer h.Cancel()

	h.HeadersReceived()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("first token timeout did not fire")
	}
	if !errors.Is(context.Cause(ctx), ErrFirstTokenTimeout) {
		t.Fatalf("expected cause ErrFirstTokenTimeout, got %v", context.Cause(ctx))
	}
	if got := h.State.TimeoutPhase(); got != TimeoutPhaseFirstToken {
		t.Fatalf("timeout phase = %q, want %q", got, TimeoutPhaseFirstToken)
	}
}

// TestBothStreamTimersShareTheSameStart 冻结 §11.2：首字预算不是「响应头之后再给一份」。
// 响应头几乎立刻到达时，首字截止时间仍然是「发起调用 + FirstToken」。
func TestBothStreamTimersShareTheSameStart(t *testing.T) {
	const firstToken = 120 * time.Millisecond
	start := time.Now()
	ctx, h := startedStreamTimeoutContext(context.Background(), StreamTimeoutConfig{
		ResponseHeader: 60 * time.Millisecond,
		FirstToken:     firstToken,
	})
	defer h.Cancel()

	// 在响应头预算内拿到响应头。
	time.Sleep(30 * time.Millisecond)
	h.HeadersReceived()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("first token timeout did not fire")
	}
	elapsed := time.Since(start)
	if !errors.Is(context.Cause(ctx), ErrFirstTokenTimeout) {
		t.Fatalf("expected first token timeout, got %v", context.Cause(ctx))
	}
	// 若首字预算在 HeadersReceived 时重新起算，总时长会接近 30ms+120ms=150ms 以上。
	if elapsed > firstToken+60*time.Millisecond {
		t.Fatalf("first token budget appears to restart after headers: elapsed=%v budget=%v", elapsed, firstToken)
	}
}

// TestHeartbeatsDoNotStopFirstTokenTiming 冻结 §11.2/§19.4：SSE 空行、注释和纯心跳
// 只会重置 idle，绝不能停止首字计时。ResetIdle 在首个有效生成 Token 之前是无效操作。
func TestHeartbeatsDoNotStopFirstTokenTiming(t *testing.T) {
	ctx, h := startedStreamTimeoutContext(context.Background(), StreamTimeoutConfig{
		ResponseHeader: 5 * time.Second,
		FirstToken:     100 * time.Millisecond,
		Idle:           20 * time.Millisecond,
	})
	defer h.Cancel()

	h.HeadersReceived()
	// 模拟上游持续心跳（每 10ms 一次）却始终不产生有效事件。
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 30; i++ {
			h.ResetIdle()
			time.Sleep(10 * time.Millisecond)
		}
	}()

	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("heartbeats prevented the first token timeout from firing")
	}
	<-done
	if got := h.State.TimeoutPhase(); got != TimeoutPhaseFirstToken {
		t.Fatalf("timeout phase = %q, want %q (heartbeats must not convert this into stream_idle)",
			got, TimeoutPhaseFirstToken)
	}
}

// TestIdleWatchdogStartsOnlyAfterFirstEvent 覆盖 §19.4「首事件后停止活动 → stream_idle 超时」。
func TestIdleWatchdogStartsOnlyAfterFirstEvent(t *testing.T) {
	ctx, h := startedStreamTimeoutContext(context.Background(), StreamTimeoutConfig{
		ResponseHeader: 5 * time.Second,
		FirstToken:     5 * time.Second,
		Idle:           30 * time.Millisecond,
	})
	defer h.Cancel()

	h.HeadersReceived()
	// 首个有效生成 Token 之前 idle 不启动：否则一个「秒回 200 然后静默」的上游会被误判为 idle 而非首字超时。
	time.Sleep(90 * time.Millisecond)
	if err := ctx.Err(); err != nil {
		t.Fatalf("idle watchdog fired before the first valid event: %v", context.Cause(ctx))
	}

	h.FirstToken()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("idle watchdog did not fire after the first event")
	}
	if !errors.Is(context.Cause(ctx), ErrStreamIdleTimeout) {
		t.Fatalf("expected cause ErrStreamIdleTimeout, got %v", context.Cause(ctx))
	}
	if got := h.State.TimeoutPhase(); got != TimeoutPhaseStreamIdle {
		t.Fatalf("timeout phase = %q, want %q", got, TimeoutPhaseStreamIdle)
	}
}

// TestPostFirstEventActivityKeepsStreamAlive 覆盖 §19.4「首事件后的合法心跳会重置 idle」。
func TestPostFirstEventActivityKeepsStreamAlive(t *testing.T) {
	ctx, h := startedStreamTimeoutContext(context.Background(), StreamTimeoutConfig{
		ResponseHeader: 5 * time.Second,
		FirstToken:     5 * time.Second,
		Idle:           80 * time.Millisecond,
	})
	defer h.Cancel()

	h.HeadersReceived()
	h.FirstToken()
	for i := 0; i < 6; i++ {
		time.Sleep(20 * time.Millisecond)
		h.ResetIdle()
		if err := ctx.Err(); err != nil {
			t.Fatalf("ctx canceled while the stream was active: %v", context.Cause(ctx))
		}
	}
	if got := h.State.TimeoutPhase(); got != "" {
		t.Fatalf("an active stream must not record a timeout phase, got %q", got)
	}
}

// TestFirstTokenStopsFirstTokenTimer 验证首个有效生成 Token 之后首字预算不再能取消流。
func TestFirstTokenStopsFirstTokenTimer(t *testing.T) {
	ctx, h := startedStreamTimeoutContext(context.Background(), StreamTimeoutConfig{
		ResponseHeader: 5 * time.Second,
		FirstToken:     30 * time.Millisecond,
		Idle:           5 * time.Second,
	})
	defer h.Cancel()

	h.HeadersReceived()
	h.FirstToken()
	time.Sleep(120 * time.Millisecond)
	if err := ctx.Err(); err != nil {
		t.Fatalf("first token timer fired after the first valid event: %v", context.Cause(ctx))
	}
}

// TestNonStreamTimeoutPhaseOf 覆盖 §19.4 的非流式两段：响应头 vs 响应体。
func TestNonStreamTimeoutPhaseOf(t *testing.T) {
	if got := NonStreamTimeoutPhaseOf(false); got != TimeoutPhaseResponseHeader {
		t.Fatalf("before headers phase = %q, want %q", got, TimeoutPhaseResponseHeader)
	}
	if got := NonStreamTimeoutPhaseOf(true); got != TimeoutPhaseResponseBody {
		t.Fatalf("after headers phase = %q, want %q", got, TimeoutPhaseResponseBody)
	}
}

// TestFirstTimeoutPhaseWins 验证只记录第一个触发的阶段：后续取消都是它的连带结果。
func TestFirstTimeoutPhaseWins(t *testing.T) {
	ctx, h := startedStreamTimeoutContext(context.Background(), StreamTimeoutConfig{
		ResponseHeader: 20 * time.Millisecond,
		FirstToken:     25 * time.Millisecond,
	})
	defer h.Cancel()

	<-ctx.Done()
	time.Sleep(60 * time.Millisecond)
	if got := h.State.TimeoutPhase(); got != TimeoutPhaseResponseHeader {
		t.Fatalf("timeout phase = %q, want the first trigger %q", got, TimeoutPhaseResponseHeader)
	}
}

// TestIdleArmsAtHeadersWhenFirstTokenBudgetIsAbsent 保证「没有首字预算」时流仍受看门狗保护：
// 否则一个「响应头已到、永不产生有效事件」的连接会一直挂到客户端断开。
func TestIdleArmsAtHeadersWhenFirstTokenBudgetIsAbsent(t *testing.T) {
	ctx, h := startedStreamTimeoutContext(context.Background(), StreamTimeoutConfig{
		Idle: 20 * time.Millisecond,
	})
	defer h.Cancel()

	h.HeadersReceived()
	select {
	case <-ctx.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("a stream without a first-token budget was left unprotected")
	}
	if !errors.Is(context.Cause(ctx), ErrStreamIdleTimeout) {
		t.Fatalf("expected cause ErrStreamIdleTimeout, got %v", context.Cause(ctx))
	}
}
