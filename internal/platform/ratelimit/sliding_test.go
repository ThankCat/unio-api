package ratelimit

import (
	"testing"
	"time"
)

func TestBucketKeysCountAndOrder(t *testing.T) {
	store := NewSlidingWindowStore(nil, "unio:test")
	store.now = func() time.Time { return time.Unix(1_000_000, 0) }

	keys, ttl := store.bucketKeys("key:7:rpm", time.Minute, time.Second)
	if len(keys) != 60 {
		t.Fatalf("expected 60 one-second buckets for a minute window, got %d", len(keys))
	}
	if keys[0] != "unio:test:rl:key:7:rpm:1000000" {
		t.Fatalf("current bucket key mismatch: %s", keys[0])
	}
	if keys[1] != "unio:test:rl:key:7:rpm:999999" {
		t.Fatalf("previous bucket key mismatch: %s", keys[1])
	}
	if ttl != time.Minute+time.Second {
		t.Fatalf("expected ttl window+bucket, got %s", ttl)
	}
}

func TestBucketKeysDayWindowUsesHourBuckets(t *testing.T) {
	store := NewSlidingWindowStore(nil, "unio:test")
	store.now = func() time.Time { return time.Unix(86_400, 0) }

	keys, _ := store.bucketKeys("chan:1:rpd", 24*time.Hour, time.Hour)
	if len(keys) != 24 {
		t.Fatalf("expected 24 hourly buckets for a day window, got %d", len(keys))
	}
}

func TestBucketKeysClampsToMax(t *testing.T) {
	store := NewSlidingWindowStore(nil, "unio:test")
	keys, _ := store.bucketKeys("x", 10000*time.Hour, time.Second)
	if len(keys) != maxSlidingBuckets {
		t.Fatalf("expected clamp to %d buckets, got %d", maxSlidingBuckets, len(keys))
	}
}

func TestParsePair(t *testing.T) {
	allowed, count, err := parsePair([]any{int64(1), int64(42)})
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if allowed != 1 || count != 42 {
		t.Fatalf("unexpected parse result allowed=%d count=%d", allowed, count)
	}

	if _, _, err := parsePair("nope"); err == nil {
		t.Fatalf("expected error for non-array result")
	}
}

func TestToInt64(t *testing.T) {
	cases := []struct {
		in   any
		want int64
	}{
		{int64(5), 5},
		{int(7), 7},
		{"9", 9},
	}
	for _, c := range cases {
		got, err := toInt64(c.in)
		if err != nil {
			t.Fatalf("toInt64(%v) err: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("toInt64(%v) = %d, want %d", c.in, got, c.want)
		}
	}
	if _, err := toInt64(1.5); err == nil {
		t.Fatalf("expected error for float input")
	}
}
