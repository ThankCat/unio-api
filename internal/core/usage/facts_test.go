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

func TestSourceValid(t *testing.T) {
	if !SourceUpstreamResponse.Valid() || !SourceUpstreamStream.Valid() {
		t.Fatal("expected registered sources to be valid")
	}
	if Source("magic").Valid() {
		t.Fatal("expected unregistered source to be invalid")
	}
}
