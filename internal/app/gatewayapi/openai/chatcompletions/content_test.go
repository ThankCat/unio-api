package chatcompletions

import (
	"encoding/json"
	"testing"
)

func TestValidateMessageContentAcceptsStringAndTextArray(t *testing.T) {
	tests := []struct {
		name        string
		content     string
		wantContent bool
	}{
		{"string", `"hello"`, true},
		{"empty string", `""`, false},
		{"blank string", `"   "`, false},
		{"json null", `null`, false},
		{"text part array", `[{"type":"text","text":"hi"}]`, true},
		{"text+refusal array", `[{"type":"text","text":"a"},{"type":"refusal","refusal":"no"}]`, true},
		{"empty array", `[]`, false},
		{"image_url part", `[{"type":"image_url","image_url":{"url":"http://x"}}]`, true},
		{"input_audio part", `[{"type":"input_audio","input_audio":{"data":"x","format":"wav"}}]`, true},
		{"file part", `[{"type":"file","file":{"file_id":"f"}}]`, true},
		{"text+image array", `[{"type":"text","text":"a"},{"type":"image_url","image_url":{"url":"http://x"}}]`, true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			state, err := validateMessageContent(json.RawMessage(tt.content), 0)
			if err != nil {
				t.Fatalf("unexpected error: %+v", err)
			}
			if state.hasContent != tt.wantContent {
				t.Fatalf("hasContent = %v, want %v", state.hasContent, tt.wantContent)
			}
		})
	}
}

func TestValidateMessageContentRejectsUnsupportedAndMalformed(t *testing.T) {
	tests := []struct {
		name      string
		content   string
		wantParam string
	}{
		{"unknown type", `[{"type":"mystery"}]`, "messages.0.content.0.type"},
		{"missing type", `[{"text":"hi"}]`, "messages.0.content.0.type"},
		{"empty text part", `[{"type":"text","text":"  "}]`, "messages.0.content.0.text"},
		{"non object part", `["plain"]`, "messages.0.content.0"},
		{"number content", `42`, "messages.0.content"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := validateMessageContent(json.RawMessage(tt.content), 0)
			if err == nil {
				t.Fatalf("expected validation error for %s", tt.content)
			}
			if err.param != tt.wantParam {
				t.Fatalf("param = %q, want %q (message: %q)", err.param, tt.wantParam, err.message)
			}
		})
	}
}
