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

func TestNormalizeAnthropicReasoning(t *testing.T) {
	cases := []struct {
		name         string
		outputConfig string
		thinking     string
		wantEffort   *string
		wantBudget   *int32
	}{
		// 无任何信号 → 留空，不编造 medium。
		{"empty", "", "", nil, nil},
		{"thinking-disabled", "", `{"type":"disabled"}`, nil, nil},
		// adaptive/enabled 但无 effort 且无 budget → 留空（关键：不再默认 medium）。
		{"adaptive-no-effort-no-budget", "", `{"type":"adaptive"}`, nil, nil},
		{"enabled-no-budget", "", `{"type":"enabled"}`, nil, nil},

		// output_config.effort 优先，原样归一（含 max 不塌缩）。
		{"effort-high", `{"effort":"high"}`, "", strptr("high"), nil},
		{"effort-xhigh", `{"effort":"xhigh"}`, "", strptr("xhigh"), nil},
		{"effort-max", `{"effort":"max"}`, "", strptr("max"), nil},
		{"effort-uppercase", `{"effort":"HIGH"}`, "", strptr("high"), nil},
		{"effort-invalid-falls-through", `{"effort":"turbo"}`, "", nil, nil},

		// 真实客户端形状：adaptive thinking + output_config.effort → 取 effort（这正是之前误记 medium 的场景）。
		{"adaptive-plus-effort-xhigh", `{"effort":"xhigh"}`, `{"type":"adaptive"}`, strptr("xhigh"), nil},

		// output_config 缺失 → 退回 budget 换算，并保留原始预算。
		{"budget-500-minimal", "", `{"type":"enabled","budget_tokens":500}`, strptr("minimal"), i32ptr(500)},
		{"budget-16000-high", "", `{"type":"enabled","budget_tokens":16000}`, strptr("high"), i32ptr(16000)},
		{"budget-30000-xhigh", "", `{"type":"enabled","budget_tokens":30000}`, strptr("xhigh"), i32ptr(30000)},

		// effort 与 budget 同时存在 → effort 优先做档位，budget 仍作为细分事实保留。
		{"effort-wins-budget-kept", `{"effort":"low"}`, `{"type":"enabled","budget_tokens":30000}`, strptr("low"), i32ptr(30000)},

		{"malformed-output-config", `{`, "", nil, nil},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var oc, th json.RawMessage
			if c.outputConfig != "" {
				oc = json.RawMessage(c.outputConfig)
			}
			if c.thinking != "" {
				th = json.RawMessage(c.thinking)
			}
			got := NormalizeAnthropicReasoning(oc, th)
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
