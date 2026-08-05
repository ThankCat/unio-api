package usage

import "testing"

func TestTokenCountConstructorsSetState(t *testing.T) {
	if got := KnownTokens(42); got.State != CountKnown || got.Value != 42 {
		t.Fatalf("KnownTokens: got %+v", got)
	}
	if got := NotApplicableTokens(); got.State != CountNotApplicable || got.Value != 0 {
		t.Fatalf("NotApplicableTokens: got %+v", got)
	}
	if got := UnknownTokens(); got.State != CountUnknown {
		t.Fatalf("UnknownTokens: got %+v", got)
	}
}

func TestTokenCountBillableValueNeverTreatsUnknownAsZero(t *testing.T) {
	tests := []struct {
		name         string
		count        TokenCount
		wantValue    int64
		wantBillable bool
	}{
		{"known", KnownTokens(7), 7, true},
		{"known zero", KnownTokens(0), 0, true},
		{"not applicable", NotApplicableTokens(), 0, true},
		{"unknown", UnknownTokens(), 0, false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			value, billable := tt.count.BillableValue()
			if value != tt.wantValue || billable != tt.wantBillable {
				t.Fatalf("BillableValue() = (%d, %v), want (%d, %v)", value, billable, tt.wantValue, tt.wantBillable)
			}
		})
	}
}

func TestTokenCountIsKnown(t *testing.T) {
	if !KnownTokens(1).IsKnown() {
		t.Fatal("expected known token to report IsKnown true")
	}
	if NotApplicableTokens().IsKnown() {
		t.Fatal("expected not_applicable token to report IsKnown false")
	}
	if UnknownTokens().IsKnown() {
		t.Fatal("expected unknown token to report IsKnown false")
	}
}

func TestActualTotalTokensRequiresCompleteLuaExactUsage(t *testing.T) {
	complete := Facts{
		UncachedInputTokens:      KnownTokens(11),
		CacheReadInputTokens:     KnownTokens(12),
		CacheWrite5mInputTokens:  KnownTokens(13),
		CacheWrite30mInputTokens: KnownTokens(14),
		CacheWrite1hInputTokens:  KnownTokens(15),
		OutputTokensTotal:        KnownTokens(16),
		ReasoningOutputTokens:    KnownTokens(7),
	}
	if got, reliable := complete.ActualTotalTokens(); !reliable || got != 81 {
		t.Fatalf("ActualTotalTokens() = (%d, %v), want (81, true)", got, reliable)
	}

	unknown := complete
	unknown.CacheReadInputTokens = UnknownTokens()
	if got, reliable := unknown.ActualTotalTokens(); reliable || got != 0 {
		t.Fatalf("unknown ActualTotalTokens() = (%d, %v), want (0, false)", got, reliable)
	}

	overflow := complete
	overflow.UncachedInputTokens = KnownTokens(maxLuaExactInteger)
	if got, reliable := overflow.ActualTotalTokens(); reliable || got != 0 {
		t.Fatalf("Lua-inexact ActualTotalTokens() = (%d, %v), want (0, false)", got, reliable)
	}
}

func TestSourceValid(t *testing.T) {
	if !SourceUpstreamResponse.Valid() || !SourceUpstreamStream.Valid() {
		t.Fatal("expected registered sources to be valid")
	}
	if Source("magic").Valid() {
		t.Fatal("expected unregistered source to be invalid")
	}
}

func TestObservedInputAndOutputSplitEveryCategoryOnce(t *testing.T) {
	facts := Facts{
		UncachedInputTokens:      KnownTokens(1_000),
		CacheReadInputTokens:     KnownTokens(80_000),
		CacheWrite5mInputTokens:  KnownTokens(200),
		CacheWrite30mInputTokens: KnownTokens(400),
		CacheWrite1hInputTokens:  KnownTokens(300),
		OutputTokensTotal:        KnownTokens(500),
		ReasoningOutputTokens:    KnownTokens(100),
	}

	input, inputOK := facts.ObservedInputTokens()
	if want := int64(81_900); !inputOK || input != want {
		t.Fatalf("ObservedInputTokens = (%d, %v), want (%d, true)", input, inputOK, want)
	}
	output, outputOK := facts.ObservedOutputTokens()
	if !outputOK || output != 500 {
		t.Fatalf("ObservedOutputTokens = (%d, %v), want (500, true)", output, outputOK)
	}
	// 观测拆分必须与账单合计严格一致，否则 TPM 与账单会各说各话。
	total, totalOK := facts.ActualTotalTokens()
	if !totalOK || total != input+output {
		t.Fatalf("ActualTotalTokens = (%d, %v), want %d", total, totalOK, input+output)
	}
}

func TestObservedInputRejectsUnknownCategory(t *testing.T) {
	facts := Facts{
		UncachedInputTokens:      KnownTokens(0),
		CacheReadInputTokens:     KnownTokens(90_000),
		CacheWrite5mInputTokens:  NotApplicableTokens(),
		CacheWrite30mInputTokens: UnknownTokens(),
		CacheWrite1hInputTokens:  NotApplicableTokens(),
		OutputTokensTotal:        KnownTokens(42),
		ReasoningOutputTokens:    NotApplicableTokens(),
	}
	if got, ok := facts.ObservedInputTokens(); ok || got != 0 {
		t.Fatalf("ObservedInputTokens = (%d, %v), want (0, false)", got, ok)
	}
}
