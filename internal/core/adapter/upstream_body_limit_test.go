package adapter

import (
	"strings"
	"testing"
)

func TestMaxUpstreamResponseBytesDefaultsWhenUnset(t *testing.T) {
	t.Cleanup(func() { SetMaxUpstreamResponseBytes(0) })

	SetMaxUpstreamResponseBytes(0)
	if got := MaxUpstreamResponseBytes(); got != DefaultMaxUpstreamResponseBytes {
		t.Fatalf("expected default %d, got %d", DefaultMaxUpstreamResponseBytes, got)
	}

	SetMaxUpstreamResponseBytes(-1)
	if got := MaxUpstreamResponseBytes(); got != DefaultMaxUpstreamResponseBytes {
		t.Fatalf("expected default %d for negative limit, got %d", DefaultMaxUpstreamResponseBytes, got)
	}

	SetMaxUpstreamResponseBytes(123)
	if got := MaxUpstreamResponseBytes(); got != 123 {
		t.Fatalf("expected configured 123, got %d", got)
	}
}

func TestReadUpstreamBodyLimitedUnderLimit(t *testing.T) {
	t.Cleanup(func() { SetMaxUpstreamResponseBytes(0) })
	SetMaxUpstreamResponseBytes(16)

	data, exceeded, err := ReadUpstreamBodyLimited(strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exceeded {
		t.Fatal("expected exceeded=false for body under limit")
	}
	if string(data) != "hello" {
		t.Fatalf("expected body %q, got %q", "hello", string(data))
	}
}

func TestReadUpstreamBodyLimitedAtLimitIsNotExceeded(t *testing.T) {
	t.Cleanup(func() { SetMaxUpstreamResponseBytes(0) })
	SetMaxUpstreamResponseBytes(5)

	data, exceeded, err := ReadUpstreamBodyLimited(strings.NewReader("hello"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if exceeded {
		t.Fatal("expected exceeded=false when body length equals the limit")
	}
	if string(data) != "hello" {
		t.Fatalf("expected body %q, got %q", "hello", string(data))
	}
}

func TestReadUpstreamBodyLimitedOverLimit(t *testing.T) {
	t.Cleanup(func() { SetMaxUpstreamResponseBytes(0) })
	SetMaxUpstreamResponseBytes(8)

	data, exceeded, err := ReadUpstreamBodyLimited(strings.NewReader(strings.Repeat("a", 1024)))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !exceeded {
		t.Fatal("expected exceeded=true for body over limit")
	}
	// 超限时仅截断到 limit，不把整块 body 读进内存。
	if int64(len(data)) != 8 {
		t.Fatalf("expected truncated body length 8, got %d", len(data))
	}
}
