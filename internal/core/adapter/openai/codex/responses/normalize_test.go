package codexresponses

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestNormalizeCodexRequestPassthroughCompliant(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{
			name: "minimal",
			body: `{"input":[{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"}],"model":"gpt-5.4","store":false,"stream":true}`,
		},
		{
			// codex CLI 形态：reasoning + include 齐备，必须逐字节直传。
			name: "cli shape with reasoning and include",
			body: `{"include":["reasoning.encrypted_content"],"input":[{"content":[{"text":"hi","type":"input_text"}],"role":"user","type":"message"}],"model":"gpt-5.4","reasoning":{"effort":"medium"},"store":false,"stream":true}`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := normalizeCodexRequest([]byte(tc.body), true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if string(out) != tc.body {
				t.Fatalf("compliant body must be returned unchanged:\n got %s\nwant %s", out, tc.body)
			}
		})
	}
}

func TestNormalizeCodexRequestInjectsReasoningInclude(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		wantInclude []any
	}{
		{
			name:        "reasoning without include",
			body:        `{"store":false,"stream":true,"reasoning":{"effort":"low"}}`,
			wantInclude: []any{"reasoning.encrypted_content"},
		},
		{
			name:        "appends to existing include",
			body:        `{"store":false,"stream":true,"reasoning":{"effort":"low"},"include":["message.output_text.logprobs"]}`,
			wantInclude: []any{"message.output_text.logprobs", "reasoning.encrypted_content"},
		},
		{
			name:        "include null treated as absent",
			body:        `{"store":false,"stream":true,"reasoning":{"effort":"low"},"include":null}`,
			wantInclude: []any{"reasoning.encrypted_content"},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := normalizeCodexRequest([]byte(tc.body), true)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := decodeObject(t, out)
			if !reflect.DeepEqual(got["include"], tc.wantInclude) {
				t.Fatalf("include = %#v, want %#v", got["include"], tc.wantInclude)
			}
		})
	}
}

func TestNormalizeCodexRequestNoReasoningNoIncludeInjection(t *testing.T) {
	for _, body := range []string{
		`{"store":false,"stream":true}`,
		`{"store":false,"stream":true,"reasoning":null}`,
	} {
		out, err := normalizeCodexRequest([]byte(body), true)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if _, ok := decodeObject(t, out)["include"]; ok {
			t.Fatalf("include must not be injected without reasoning, body %s", body)
		}
	}
}

func TestNormalizeCodexRequestInvalidIncludeShapeUntouched(t *testing.T) {
	// include 非数组是非法请求：不遮掩不修补，原样交上游拒绝。
	out, err := normalizeCodexRequest([]byte(`{"store":false,"stream":true,"reasoning":{"effort":"low"},"include":"reasoning.encrypted_content"}`), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got := decodeObject(t, out)["include"]; got != "reasoning.encrypted_content" {
		t.Fatalf("invalid include shape must stay verbatim, got %#v", got)
	}
}

func TestNormalizeCodexRequestForcesStoreAndStream(t *testing.T) {
	cases := []struct {
		name   string
		body   string
		stream bool
	}{
		{name: "store true overwritten", body: `{"store":true,"stream":true}`, stream: true},
		{name: "store missing filled", body: `{"stream":true}`, stream: true},
		{name: "stream false rewritten for forced outbound", body: `{"store":false,"stream":false}`, stream: true},
		{name: "stream missing filled", body: `{"store":false}`, stream: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			out, err := normalizeCodexRequest([]byte(tc.body), tc.stream)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			got := decodeObject(t, out)
			if got["store"] != false {
				t.Fatalf("store = %v, want false", got["store"])
			}
			if got["stream"] != tc.stream {
				t.Fatalf("stream = %v, want %v", got["stream"], tc.stream)
			}
		})
	}
}

func TestNormalizeCodexRequestStripsUnsupportedFields(t *testing.T) {
	body := []byte(`{
		"model":"gpt-5.4",
		"store":false,
		"stream":true,
		"temperature":0.7,
		"top_p":0.9,
		"frequency_penalty":0.1,
		"presence_penalty":0.2,
		"max_output_tokens":256,
		"max_completion_tokens":128,
		"stream_options":{"include_usage":true},
		"service_tier":"priority"
	}`)
	out, err := normalizeCodexRequest(body, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeObject(t, out)
	for _, key := range codexUnsupportedFields {
		if _, ok := got[key]; ok {
			t.Fatalf("unsupported field %q must be stripped", key)
		}
	}
	if got["service_tier"] != "priority" {
		t.Fatalf("service_tier must be preserved, got %v", got["service_tier"])
	}
}

func TestNormalizeCodexRequestStringInputToStructured(t *testing.T) {
	out, err := normalizeCodexRequest([]byte(`{"model":"gpt-5.4","store":false,"stream":true,"input":"hello"}`), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeObject(t, out)
	items, ok := got["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("input = %#v, want 1 structured item", got["input"])
	}
	message := items[0].(map[string]any)
	if message["type"] != "message" || message["role"] != "user" {
		t.Fatalf("item = %#v, want type=message role=user", message)
	}
	content, ok := message["content"].([]any)
	if !ok || len(content) != 1 {
		t.Fatalf("content = %#v, want 1 input_text part", message["content"])
	}
	part := content[0].(map[string]any)
	if part["type"] != "input_text" || part["text"] != "hello" {
		t.Fatalf("part = %#v, want input_text/hello", part)
	}
}

func TestNormalizeCodexRequestBlankStringInputKeptVerbatim(t *testing.T) {
	// 只修形状不改语义：空白字符串照样包装成 user message，可否服务由上游裁决。
	out, err := normalizeCodexRequest([]byte(`{"input":"   ","store":false,"stream":true}`), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeObject(t, out)
	items, ok := got["input"].([]any)
	if !ok || len(items) != 1 {
		t.Fatalf("blank string input must still wrap into one message, got %#v", got["input"])
	}
	part := items[0].(map[string]any)["content"].([]any)[0].(map[string]any)
	if part["text"] != "   " {
		t.Fatalf("text = %q, want verbatim blank", part["text"])
	}
}

func TestNormalizeCodexRequestPromotesSystemIntoInstructions(t *testing.T) {
	body := []byte(`{
		"store":false,
		"stream":true,
		"instructions":"keep existing",
		"input":[
			{"type":"message","role":"system","content":[{"type":"input_text","text":"be terse"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
		]
	}`)
	out, err := normalizeCodexRequest(body, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeObject(t, out)
	if got["instructions"] != "keep existing\n\nbe terse" {
		t.Fatalf("instructions = %q, want concatenated", got["instructions"])
	}
	items := got["input"].([]any)
	first := items[0].(map[string]any)
	if first["role"] != "developer" {
		t.Fatalf("system message role = %q, want developer", first["role"])
	}
	second := items[1].(map[string]any)
	if second["role"] != "user" {
		t.Fatalf("user message must stay, got %q", second["role"])
	}
}

func TestNormalizeCodexRequestPromotesStringSystemContent(t *testing.T) {
	out, err := normalizeCodexRequest([]byte(`{
		"store":false,
		"stream":true,
		"input":[{"type":"message","role":"system","content":"you are a router"}]
	}`), true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeObject(t, out)
	if got["instructions"] != "you are a router" {
		t.Fatalf("instructions = %q, want system text", got["instructions"])
	}
}

func TestNormalizeCodexRequestMalformedJSONPassthrough(t *testing.T) {
	body := []byte(`not-json`)
	out, err := normalizeCodexRequest(body, true)
	if err != nil {
		t.Fatalf("malformed JSON must not fail here: %v", err)
	}
	if string(out) != string(body) {
		t.Fatalf("malformed JSON must pass through, got %s", out)
	}
}

func TestNormalizeCodexRequestPreservesUnrelatedFields(t *testing.T) {
	body := []byte(`{"model":"gpt-5.4","store":false,"stream":true,"text":{"format":{"type":"json_object"}},"tools":[{"type":"function","name":"fn"}]}`)
	out, err := normalizeCodexRequest(body, true)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	got := decodeObject(t, out)
	want := decodeObject(t, body)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("unrelated fields mutated:\n got %#v\nwant %#v", got, want)
	}
}

func decodeObject(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var got map[string]any
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatalf("decode %s: %v", raw, err)
	}
	return got
}
