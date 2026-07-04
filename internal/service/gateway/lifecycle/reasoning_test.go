package lifecycle

import (
	"encoding/json"
	"testing"
)

func TestNormalizeOpenAIEffort(t *testing.T) {
	cases := []struct {
		in   string
		want *string
	}{
		{"", nil},
		{"   ", nil},
		{"high", strptr("high")},
		{"HIGH", strptr("high")},
		{" low ", strptr("low")},
		{"minimal", strptr("minimal")},
		{"max", strptr("xhigh")},
	}
	for _, c := range cases {
		got := NormalizeOpenAIEffort(c.in)
		if !eqStrPtr(got.Effort, c.want) {
			t.Errorf("NormalizeOpenAIEffort(%q).Effort = %v, want %v", c.in, derefStr(got.Effort), derefStr(c.want))
		}
		if got.BudgetTokens != nil {
			t.Errorf("NormalizeOpenAIEffort(%q).BudgetTokens = %v, want nil", c.in, *got.BudgetTokens)
		}
	}
}

func TestNormalizeAnthropicThinking(t *testing.T) {
	cases := []struct {
		name       string
		raw        string
		wantEffort *string
		wantBudget *int32
	}{
		{"empty", "", nil, nil},
		{"disabled", `{"type":"disabled"}`, nil, nil},
		{"enabled-no-budget", `{"type":"enabled"}`, strptr("medium"), nil},
		{"budget-500-minimal", `{"type":"enabled","budget_tokens":500}`, strptr("minimal"), i32ptr(500)},
		{"budget-2000-low", `{"type":"enabled","budget_tokens":2000}`, strptr("low"), i32ptr(2000)},
		{"budget-8000-medium", `{"type":"enabled","budget_tokens":8000}`, strptr("medium"), i32ptr(8000)},
		{"budget-16000-high", `{"type":"enabled","budget_tokens":16000}`, strptr("high"), i32ptr(16000)},
		{"budget-30000-xhigh", `{"type":"enabled","budget_tokens":30000}`, strptr("xhigh"), i32ptr(30000)},
		{"budget-0-none", `{"type":"enabled","budget_tokens":0}`, strptr("none"), i32ptr(0)},
		{"malformed", `{`, nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var raw json.RawMessage
			if c.raw != "" {
				raw = json.RawMessage(c.raw)
			}
			got := NormalizeAnthropicThinking(raw)
			if !eqStrPtr(got.Effort, c.wantEffort) {
				t.Errorf("effort = %v, want %v", derefStr(got.Effort), derefStr(c.wantEffort))
			}
			if !eqI32Ptr(got.BudgetTokens, c.wantBudget) {
				t.Errorf("budget = %v, want %v", derefI32(got.BudgetTokens), derefI32(c.wantBudget))
			}
		})
	}
}

func strptr(s string) *string { return &s }
func i32ptr(v int32) *int32   { return &v }
func eqStrPtr(a, b *string) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
func eqI32Ptr(a, b *int32) bool {
	if a == nil || b == nil {
		return a == b
	}
	return *a == *b
}
func derefStr(s *string) string {
	if s == nil {
		return "<nil>"
	}
	return *s
}
func derefI32(v *int32) int32 {
	if v == nil {
		return -1
	}
	return *v
}
