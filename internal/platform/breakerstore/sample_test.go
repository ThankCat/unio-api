package breakerstore

import (
	"context"
	"testing"
	"time"
)

func sampleInt(v int64) *int64 { return &v }

// sampleBaseTime 是一个远离 UTC 日边界的固定时刻，保证 now-10min 仍在同一 UTC 日。
func sampleBaseTime() time.Time {
	return time.Date(2026, 3, 15, 12, 30, 0, 0, time.UTC)
}

func TestRecordChannelSampleAggregatesArithmeticMean(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()
	now := sampleBaseTime()
	const ch = int64(90001)

	mustRecord := func(in ChannelSampleInput, at time.Time) {
		t.Helper()
		if err := store.recordChannelSampleAt(ctx, in, at); err != nil {
			t.Fatalf("record sample: %v", err)
		}
	}
	// 三个 attempt：当前分钟 / 5 分钟前 / 10 分钟前，TTFT 1000/3000/2000（均值 2000）。
	mustRecord(ChannelSampleInput{ChannelID: ch, AttemptID: 1, TTFTMs: sampleInt(1000), ErrorEligible: true, ObservedRequest: true}, now)
	mustRecord(ChannelSampleInput{ChannelID: ch, AttemptID: 2, TTFTMs: sampleInt(3000), ErrorEligible: true, ObservedRequest: true}, now.Add(-5*time.Minute))
	mustRecord(ChannelSampleInput{ChannelID: ch, AttemptID: 3, TTFTMs: sampleInt(2000), ErrorEligible: true, IsError: true, ObservedRequest: true}, now.Add(-10*time.Minute))

	windows, err := store.aggregateChannelSamplesAt(ctx, []int64{ch}, now)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	w := windows[ch]
	if w.TTFTCount != 3 || w.TTFTSumMs != 6000 {
		t.Fatalf("ttft sum/count = %d/%d, want 6000/3", w.TTFTSumMs, w.TTFTCount)
	}
	if w.ErrorAttemptCount != 3 || w.ErrorCount != 1 {
		t.Fatalf("error denom/num = %d/%d, want 3/1", w.ErrorAttemptCount, w.ErrorCount)
	}
	if w.RPM != 1 {
		t.Fatalf("RPM (current minute) = %d, want 1", w.RPM)
	}
	if w.RPD != 3 {
		t.Fatalf("RPD (utc day) = %d, want 3", w.RPD)
	}
}

func TestRecordChannelSampleIdempotentPerAttempt(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()
	now := sampleBaseTime()
	const ch = int64(90002)

	in := ChannelSampleInput{ChannelID: ch, AttemptID: 7, TTFTMs: sampleInt(500), ErrorEligible: true, ObservedRequest: true}
	for i := 0; i < 3; i++ {
		if err := store.recordChannelSampleAt(ctx, in, now); err != nil {
			t.Fatalf("record sample %d: %v", i, err)
		}
	}
	windows, err := store.aggregateChannelSamplesAt(ctx, []int64{ch}, now)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	w := windows[ch]
	if w.TTFTCount != 1 || w.RPM != 1 || w.RPD != 1 || w.ErrorAttemptCount != 1 {
		t.Fatalf("idempotency failed: %+v", w)
	}
}

func TestAggregateChannelSamplesExcludesOutsideWindow(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()
	now := sampleBaseTime()
	const ch = int64(90003)

	// 31 分钟前的 attempt 落在 30 分钟窗口外，不进 TTFT/错误率聚合。
	if err := store.recordChannelSampleAt(ctx, ChannelSampleInput{ChannelID: ch, AttemptID: 11, TTFTMs: sampleInt(9000), ErrorEligible: true, IsError: true, ObservedRequest: true}, now.Add(-31*time.Minute)); err != nil {
		t.Fatalf("record old sample: %v", err)
	}
	windows, err := store.aggregateChannelSamplesAt(ctx, []int64{ch}, now)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	w := windows[ch]
	if w.TTFTCount != 0 || w.ErrorAttemptCount != 0 {
		t.Fatalf("outside-window sample must be excluded from 30m aggregate: %+v", w)
	}
	// RPD 是当日计数，31 分钟前仍计入当天 RPD。
	if w.RPD != 1 {
		t.Fatalf("RPD = %d, want 1", w.RPD)
	}
}

func TestAggregateChannelSamplesNoSampleIsZero(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()
	now := sampleBaseTime()
	const ch = int64(90004)

	windows, err := store.aggregateChannelSamplesAt(ctx, []int64{ch}, now)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	w := windows[ch]
	if w.TTFTCount != 0 || w.ErrorAttemptCount != 0 || w.RPM != 0 || w.RPD != 0 {
		t.Fatalf("no-sample window must be zero: %+v", w)
	}
}

func TestRecordChannelSampleNonStreamNoTTFT(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()
	now := sampleBaseTime()
	const ch = int64(90005)

	// 非流式：调用方传 TTFTMs=nil；桶不应产生 ttft 计数。
	if err := store.recordChannelSampleAt(ctx, ChannelSampleInput{ChannelID: ch, AttemptID: 21, TTFTMs: nil, ErrorEligible: true, ObservedRequest: true}, now); err != nil {
		t.Fatalf("record: %v", err)
	}
	windows, err := store.aggregateChannelSamplesAt(ctx, []int64{ch}, now)
	if err != nil {
		t.Fatalf("aggregate: %v", err)
	}
	if w := windows[ch]; w.TTFTCount != 0 || w.ErrorAttemptCount != 1 {
		t.Fatalf("non-stream TTFT must be absent: %+v", w)
	}
}
