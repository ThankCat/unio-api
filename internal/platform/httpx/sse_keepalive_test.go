package httpx

import (
	"context"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// TestSSEKeepaliveWritesCommentWhenIdle 冻结下游保活：空闲超过间隔即写 SSE 注释帧（首字之前也发），
// 注释提交了 SSE 响应头（Started 为 true），停止后不再写；业务事件写出会重置空闲计时。
func TestSSEKeepaliveWritesCommentWhenIdle(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := NewSSEWriter(context.Background(), rec, SSEWriterConfig{})
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	stop := sw.RunKeepalive(40 * time.Millisecond)

	time.Sleep(150 * time.Millisecond)
	stop()
	stop() // 幂等

	body := rec.Body.String()
	if !strings.Contains(body, ": \n\n") {
		t.Fatalf("expected keepalive comment frames, got %q", body)
	}
	if !sw.Started() {
		t.Fatal("keepalive commits the SSE response, Started must report true")
	}
	if rec.Header().Get("Content-Type") != ContentTypeSSE {
		t.Fatalf("content type = %q, want %q", rec.Header().Get("Content-Type"), ContentTypeSSE)
	}

	before := len(rec.Body.String())
	time.Sleep(100 * time.Millisecond)
	if after := len(rec.Body.String()); after != before {
		t.Fatalf("keepalive kept writing after stop: %d -> %d bytes", before, after)
	}
}

// TestSSEKeepaliveDisabledWhenIntervalZero 冻结 0 = 关闭。
func TestSSEKeepaliveDisabledWhenIntervalZero(t *testing.T) {
	rec := httptest.NewRecorder()
	sw, err := NewSSEWriter(context.Background(), rec, SSEWriterConfig{})
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	stop := sw.RunKeepalive(0)
	defer stop()
	time.Sleep(60 * time.Millisecond)
	if rec.Body.Len() != 0 || sw.Started() {
		t.Fatalf("interval 0 must not write anything, got %q", rec.Body.String())
	}
}

// TestSSEKeepaliveStopsOnContextDone 冻结客户端断开后保活 goroutine 退出。
func TestSSEKeepaliveStopsOnContextDone(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	rec := httptest.NewRecorder()
	sw, err := NewSSEWriter(ctx, rec, SSEWriterConfig{})
	if err != nil {
		t.Fatalf("NewSSEWriter: %v", err)
	}
	stop := sw.RunKeepalive(20 * time.Millisecond)
	defer stop()
	cancel()
	time.Sleep(60 * time.Millisecond)
	if rec.Body.Len() != 0 {
		t.Fatalf("no frames must be written after the client context is done, got %q", rec.Body.String())
	}
}

func TestSSEKeepaliveIntervalSetting(t *testing.T) {
	t.Cleanup(func() { SetSSEKeepaliveInterval(0) })
	SetSSEKeepaliveInterval(3 * time.Second)
	if got := SSEKeepaliveInterval(); got != 3*time.Second {
		t.Fatalf("interval = %v, want 3s", got)
	}
	SetSSEKeepaliveInterval(-time.Second)
	if got := SSEKeepaliveInterval(); got != 0 {
		t.Fatalf("negative must clamp to 0 (off), got %v", got)
	}
}
