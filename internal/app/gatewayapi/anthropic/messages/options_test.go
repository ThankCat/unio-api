package messages

import (
	"encoding/json"
	"testing"
)

func TestValidateSystem(t *testing.T) {
	ok := []string{
		`"you are helpful"`,
		`[{"type":"text","text":"a"},{"type":"text","text":"b"}]`,
	}
	for _, c := range ok {
		if verr := validateSystem(json.RawMessage(c)); verr != nil {
			t.Fatalf("system %s unexpected error: %+v", c, verr)
		}
	}

	bad := []struct {
		content   string
		wantParam string
	}{
		{`42`, "system"},
		{`[{"type":"image"}]`, "system.0.type"},
	}
	for _, c := range bad {
		verr := validateSystem(json.RawMessage(c.content))
		if verr == nil || verr.param != c.wantParam {
			t.Fatalf("system %s: got %+v want param %q", c.content, verr, c.wantParam)
		}
	}
}

func TestValidateThinking(t *testing.T) {
	if verr := validateThinking(json.RawMessage(`{"type":"enabled","budget_tokens":1024}`)); verr != nil {
		t.Fatalf("unexpected error: %+v", verr)
	}
	verr := validateThinking(json.RawMessage(`{"type":"turbo"}`))
	if verr == nil || verr.param != "thinking.type" {
		t.Fatalf("got %+v", verr)
	}
}

func TestValidateToolChoice(t *testing.T) {
	ok := []string{`{"type":"auto"}`, `{"type":"any"}`, `{"type":"none"}`, `{"type":"tool","name":"get_weather"}`}
	for _, c := range ok {
		if verr := validateToolChoice(json.RawMessage(c)); verr != nil {
			t.Fatalf("tool_choice %s unexpected error: %+v", c, verr)
		}
	}

	bad := []struct {
		content   string
		wantParam string
	}{
		{`{"type":"required"}`, "tool_choice.type"},
		{`{"type":"tool"}`, "tool_choice.name"},
	}
	for _, c := range bad {
		verr := validateToolChoice(json.RawMessage(c.content))
		if verr == nil || verr.param != c.wantParam {
			t.Fatalf("tool_choice %s: got %+v want param %q", c.content, verr, c.wantParam)
		}
	}
}

func TestValidateTools(t *testing.T) {
	ok := []string{
		`[{"name":"get_weather","input_schema":{"type":"object"}}]`,
		`[{"type":"custom","name":"x","input_schema":{}}]`,
		`[{"type":"web_search_20250305","name":"web_search"}]`,
	}
	for _, c := range ok {
		if verr := validateTools(json.RawMessage(c)); verr != nil {
			t.Fatalf("tools %s unexpected error: %+v", c, verr)
		}
	}

	bad := []struct {
		content   string
		wantParam string
	}{
		{`{"name":"x"}`, "tools"},
		{`[{"name":"x"}]`, "tools.0.input_schema"},
		{`[{"type":"frobnicate_20990101","name":"x"}]`, "tools.0.type"},
	}
	for _, c := range bad {
		verr := validateTools(json.RawMessage(c.content))
		if verr == nil || verr.param != c.wantParam {
			t.Fatalf("tools %s: got %+v want param %q", c.content, verr, c.wantParam)
		}
	}
}
