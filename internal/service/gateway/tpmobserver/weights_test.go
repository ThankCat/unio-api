package tpmobserver

import (
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

func deltaAt(t *testing.T, entries []breakerstore.TPMObservationEntry, minute int64) breakerstore.TPMObservationDelta {
	t.Helper()
	for _, entry := range entries {
		if entry.Scope.Minute == minute {
			return entry.Delta
		}
	}
	t.Fatalf("no correction entry for minute %d in %+v", minute, entries)
	return breakerstore.TPMObservationDelta{}
}

// PLAN §8.4 的算例：provisional 权重 300/700，可靠输出 1100 时应分配为 330/770，
// 合计严格等于实际输出，最后一个分钟承担整数除法余数。
func TestCorrectionsDistributeOutputByObservedWeight(t *testing.T) {
	w := newScopeWeights()
	w.addInput(100, 500)
	w.addOutput(100, 300)
	w.addOutput(101, 700)

	entries, expired := w.corrections(breakerstore.TPMScopeRoute, 7, 480, 1100, 0)
	if expired != 0 {
		t.Fatalf("expired = %d, want 0", expired)
	}

	first := deltaAt(t, entries, 100)
	if first.InputTokens != -20 || first.ProvisionalTokens != -800 || first.OutputTokens != 30 {
		t.Fatalf("minute 100 delta = %+v", first)
	}
	second := deltaAt(t, entries, 101)
	if second.OutputTokens != 70 || second.ProvisionalTokens != -700 {
		t.Fatalf("minute 101 delta = %+v", second)
	}

	// 分钟合计必须精确等于实际输出：330 + 770。
	total := (300 + first.OutputTokens) + (700 + second.OutputTokens)
	if total != 1100 {
		t.Fatalf("corrected output total = %d, want 1100", total)
	}
}

func TestCorrectionsGiveRemainderToLastMinute(t *testing.T) {
	w := newScopeWeights()
	w.addOutput(10, 1)
	w.addOutput(11, 1)
	w.addOutput(12, 1)

	entries, _ := w.corrections(breakerstore.TPMScopeChannel, 3, 0, 100, 0)
	allocated := int64(0)
	for _, entry := range entries {
		allocated += entry.Delta.OutputTokens + 1
	}
	if allocated != 100 {
		t.Fatalf("allocated output = %d, want 100 (no token lost to integer division)", allocated)
	}
	if last := deltaAt(t, entries, 12); last.OutputTokens+1 != 34 {
		t.Fatalf("last minute must absorb the remainder, got %d", last.OutputTokens+1)
	}
}

func TestCorrectionsDropMinutesOutsideLookback(t *testing.T) {
	w := newScopeWeights()
	w.addInput(50, 100)
	w.addOutput(50, 40)
	w.addOutput(120, 60)

	entries, expired := w.corrections(breakerstore.TPMScopeRoute, 9, 100, 100, 100)
	if expired != 1 {
		t.Fatalf("expired = %d, want 1 (minute 50 is outside the lookback window)", expired)
	}
	if len(entries) != 1 || entries[0].Scope.Minute != 120 {
		t.Fatalf("only in-window minutes may be corrected, got %+v", entries)
	}
}

// 上游 usage 含无法从流里观察到的输出（隐藏 reasoning）时，全部归入响应完成分钟。
func TestCorrectionsFallBackToCompletionMinuteWithoutObservedOutput(t *testing.T) {
	w := newScopeWeights()
	w.addInput(200, 80)
	w.noteCompletion(201)

	entries, expired := w.corrections(breakerstore.TPMScopeChannel, 4, 80, 250, 0)
	if expired != 0 {
		t.Fatalf("expired = %d, want 0", expired)
	}
	if got := deltaAt(t, entries, 201); got.OutputTokens != 250 {
		t.Fatalf("completion minute delta = %+v, want 250 output", got)
	}
	if got := deltaAt(t, entries, 200); got.InputTokens != 0 || got.ProvisionalTokens != -80 {
		t.Fatalf("input minute delta = %+v, want exact input and released provisional", got)
	}
}

func TestAddInputIsFirstWriteWins(t *testing.T) {
	w := newScopeWeights()
	w.addInput(10, 100)
	w.addInput(11, 999)
	if w.inputMinute != 10 || w.inputTokens != 100 {
		t.Fatalf("input must be recorded once, got minute=%d tokens=%d", w.inputMinute, w.inputTokens)
	}
}
