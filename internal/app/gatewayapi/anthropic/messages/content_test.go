package messages

import (
	"encoding/json"
	"testing"
)

func TestValidateMessageContentAccepts(t *testing.T) {
	cases := []struct {
		name    string
		content string
	}{
		{"string shorthand", `"hello"`},
		{"text block array", `[{"type":"text","text":"hi"}]`},
		{"tool_use block", `[{"type":"tool_use","id":"tu_1","name":"get_weather","input":{}}]`},
		{"tool_result block", `[{"type":"tool_result","tool_use_id":"tu_1","content":"ok"}]`},
		{"image block typed accepted at ingress", `[{"type":"image","source":{"type":"base64","data":"x"}}]`},
		{"mixed blocks", `[{"type":"text","text":"a"},{"type":"thinking","thinking":"b"}]`},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if verr := validateMessageContent(0, json.RawMessage(tc.content)); verr != nil {
				t.Fatalf("unexpected error: %+v", verr)
			}
		})
	}
}

func TestValidateMessageContentRejects(t *testing.T) {
	cases := []struct {
		name      string
		content   string
		wantParam string
	}{
		{"empty string", `""`, "messages.0.content"},
		{"empty array", `[]`, "messages.0.content"},
		{"non string non array", `42`, "messages.0.content"},
		{"block missing type", `[{"text":"hi"}]`, "messages.0.content.0.type"},
		{"unknown block type", `[{"type":"video","url":"x"}]`, "messages.0.content.0.type"},
		{"text block missing text", `[{"type":"text"}]`, "messages.0.content.0.text"},
		{"tool_use missing name", `[{"type":"tool_use","id":"tu_1"}]`, "messages.0.content.0.name"},
		{"tool_result missing tool_use_id", `[{"type":"tool_result","content":"x"}]`, "messages.0.content.0.tool_use_id"},
		{"block not object", `["plain"]`, "messages.0.content.0"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			verr := validateMessageContent(0, json.RawMessage(tc.content))
			if verr == nil {
				t.Fatalf("expected error for %s", tc.content)
			}
			if verr.param != tc.wantParam {
				t.Fatalf("param = %q, want %q (msg=%q)", verr.param, tc.wantParam, verr.message)
			}
		})
	}
}
