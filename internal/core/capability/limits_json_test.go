package capability

import (
	"encoding/json"
	"testing"
)

func TestLimitsJSONPresent(t *testing.T) {
	t.Parallel()

	cases := []struct {
		raw  string
		want bool
	}{
		{raw: "", want: false},
		{raw: "null", want: false},
		{raw: " null ", want: false},
		{raw: `{}`, want: true},
		{raw: `{"max_effort":"high"}`, want: true},
	}

	for _, tc := range cases {
		var raw json.RawMessage
		if tc.raw != "" {
			raw = json.RawMessage(tc.raw)
		}
		if got := LimitsJSONPresent(raw); got != tc.want {
			t.Fatalf("LimitsJSONPresent(%q) = %v, want %v", tc.raw, got, tc.want)
		}
	}
}
