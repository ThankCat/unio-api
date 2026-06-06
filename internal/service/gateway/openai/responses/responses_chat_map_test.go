package responses

import (
	"encoding/json"
	"testing"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
)

func decodeRequest(t *testing.T, body string) gatewayapi.ResponsesRequest {
	t.Helper()
	var req gatewayapi.ResponsesRequest
	if err := json.Unmarshal([]byte(body), &req); err != nil {
		t.Fatalf("decode request: %v", err)
	}
	return req
}

func mapBody(t *testing.T, body string) (openai.ChatRequest, requestTranslation) {
	t.Helper()
	return mapResponsesRequestToChat(decodeRequest(t, body), "deepseek-chat")
}

func TestMapInstructionsAndStringInput(t *testing.T) {
	chat, _ := mapBody(t, `{
		"model": "gpt-5-codex",
		"instructions": "be terse",
		"input": "hello there"
	}`)

	if chat.Model != "deepseek-chat" {
		t.Fatalf("expected upstream model, got %q", chat.Model)
	}
	if len(chat.Messages) != 2 {
		t.Fatalf("expected system+user, got %d messages", len(chat.Messages))
	}
	if chat.Messages[0].Role != "system" || chat.Messages[0].ContentString() != "be terse" {
		t.Errorf("expected system instructions, got role=%q content=%q", chat.Messages[0].Role, chat.Messages[0].ContentString())
	}
	if chat.Messages[1].Role != "user" || chat.Messages[1].ContentString() != "hello there" {
		t.Errorf("expected user input, got role=%q content=%q", chat.Messages[1].Role, chat.Messages[1].ContentString())
	}
}

func TestMapMessageItemsContentParts(t *testing.T) {
	chat, _ := mapBody(t, `{
		"model": "m",
		"input": [
			{"type":"message","role":"developer","content":[{"type":"input_text","text":"a"},{"type":"input_text","text":"b"}]},
			{"type":"message","role":"user","content":[{"type":"input_text","text":"hi"}]}
		]
	}`)

	if len(chat.Messages) != 2 {
		t.Fatalf("expected 2 messages, got %d", len(chat.Messages))
	}
	if chat.Messages[0].Role != "developer" || chat.Messages[0].ContentString() != "a\nb" {
		t.Errorf("expected joined developer content, got role=%q content=%q", chat.Messages[0].Role, chat.Messages[0].ContentString())
	}
	if chat.Messages[1].Role != "user" || chat.Messages[1].ContentString() != "hi" {
		t.Errorf("expected user content, got role=%q content=%q", chat.Messages[1].Role, chat.Messages[1].ContentString())
	}
}

func TestMapConsecutiveFunctionCallsMerge(t *testing.T) {
	chat, _ := mapBody(t, `{
		"model": "m",
		"input": [
			{"type":"function_call","call_id":"c1","name":"f1","arguments":"{}"},
			{"type":"function_call","call_id":"c2","name":"f2","arguments":"{\"x\":1}"},
			{"type":"function_call_output","call_id":"c1","output":"ok1"},
			{"type":"function_call_output","call_id":"c2","output":"ok2"}
		]
	}`)

	if len(chat.Messages) != 3 {
		t.Fatalf("expected 1 assistant + 2 tool messages, got %d", len(chat.Messages))
	}
	assistant := chat.Messages[0]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 2 {
		t.Fatalf("expected merged assistant with 2 tool calls, got role=%q calls=%d", assistant.Role, len(assistant.ToolCalls))
	}
	if assistant.ToolCalls[0].ID != "c1" || assistant.ToolCalls[0].Function.Name != "f1" {
		t.Errorf("unexpected first tool call: %+v", assistant.ToolCalls[0])
	}
	if assistant.ToolCalls[1].Function.Arguments != `{"x":1}` {
		t.Errorf("unexpected second tool call args: %q", assistant.ToolCalls[1].Function.Arguments)
	}
	if chat.Messages[1].Role != "tool" || derefString(chat.Messages[1].ToolCallID) != "c1" || chat.Messages[1].ContentString() != "ok1" {
		t.Errorf("unexpected first tool output: %+v", chat.Messages[1])
	}
	if chat.Messages[2].Role != "tool" || derefString(chat.Messages[2].ToolCallID) != "c2" {
		t.Errorf("unexpected second tool output: %+v", chat.Messages[2])
	}
}

func TestMapFunctionCallInterruptedByMessageDoesNotMerge(t *testing.T) {
	chat, _ := mapBody(t, `{
		"model": "m",
		"input": [
			{"type":"function_call","call_id":"c1","name":"f1","arguments":"{}"},
			{"type":"message","role":"user","content":"between"},
			{"type":"function_call","call_id":"c2","name":"f2","arguments":"{}"}
		]
	}`)

	if len(chat.Messages) != 3 {
		t.Fatalf("expected assistant, user, assistant, got %d", len(chat.Messages))
	}
	if len(chat.Messages[0].ToolCalls) != 1 || len(chat.Messages[2].ToolCalls) != 1 {
		t.Errorf("expected two separate assistant tool-call messages")
	}
}

func TestMapFunctionCallNamespaceName(t *testing.T) {
	chat, _ := mapBody(t, `{
		"model": "m",
		"input": [
			{"type":"function_call","call_id":"c1","name":"js","namespace":"mcp__node_repl__","arguments":"{}"}
		]
	}`)

	if got := chat.Messages[0].ToolCalls[0].Function.Name; got != "mcp__node_repl__js" {
		t.Errorf("expected namespace-joined name, got %q", got)
	}
}

func TestMapToolsFlattenAndDrop(t *testing.T) {
	chat, tr := mapBody(t, `{
		"model": "m",
		"input": "x",
		"tools": [
			{"type":"function","name":"exec_command","parameters":{"type":"object"},"strict":false},
			{"type":"web_search","external_web_access":false},
			{"type":"image_generation","output_format":"png"},
			{"type":"namespace","name":"mcp__node_repl__","tools":[
				{"type":"function","name":"js","parameters":{"type":"object"}},
				{"type":"function","name":"js_reset"}
			]}
		]
	}`)

	if len(chat.Tools) != 3 {
		t.Fatalf("expected exec_command + 2 flattened mcp tools, got %d", len(chat.Tools))
	}
	names := map[string]bool{}
	for _, tool := range chat.Tools {
		if tool.Type != "function" {
			t.Errorf("expected function tool, got %q", tool.Type)
		}
		names[tool.Function.Name] = true
	}
	for _, want := range []string{"exec_command", "mcp__node_repl__js", "mcp__node_repl__js_reset"} {
		if !names[want] {
			t.Errorf("missing flattened tool %q (got %v)", want, names)
		}
	}
	if !contains(tr.DroppedFields, "tools.web_search") || !contains(tr.DroppedFields, "tools.image_generation") {
		t.Errorf("expected builtin tools dropped, got %v", tr.DroppedFields)
	}
}

func TestMapToolsDefaultParametersSchema(t *testing.T) {
	chat, _ := mapBody(t, `{"model":"m","input":"x","tools":[{"type":"function","name":"f"}]}`)
	if len(chat.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(chat.Tools))
	}
	if string(chat.Tools[0].Function.Parameters) != `{"type":"object"}` {
		t.Errorf("expected default object schema, got %s", chat.Tools[0].Function.Parameters)
	}
}

func TestMapToolChoice(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"string_auto", `"auto"`, `"auto"`},
		{"string_required", `"required"`, `"required"`},
		{"obj_auto", `{"type":"auto"}`, `"auto"`},
		{"obj_none", `{"type":"none"}`, `"none"`},
		{"obj_required", `{"type":"required"}`, `"required"`},
		{"obj_tool", `{"type":"tool"}`, `"required"`},
		{"obj_allowed_tools", `{"type":"allowed_tools","tools":[]}`, `"auto"`},
		{"obj_function_name", `{"type":"function","name":"f"}`, `{"type":"function","function":{"name":"f"}}`},
		{"obj_function_nested", `{"type":"function","function":{"name":"g"}}`, `{"type":"function","function":{"name":"g"}}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chat, _ := mapBody(t, `{"model":"m","input":"x","tool_choice":`+tc.in+`}`)
			if string(chat.ToolChoice) != tc.want {
				t.Errorf("tool_choice %s → %s, want %s", tc.in, chat.ToolChoice, tc.want)
			}
		})
	}
}

func TestMapReasoningEffortAndText(t *testing.T) {
	chat, _ := mapBody(t, `{
		"model":"m","input":"x",
		"reasoning":{"effort":"high","summary":"auto"},
		"text":{"verbosity":"low","format":{"type":"json_object"}}
	}`)
	if chat.ReasoningEffort == nil || *chat.ReasoningEffort != "high" {
		t.Errorf("expected reasoning_effort=high, got %v", chat.ReasoningEffort)
	}
	if chat.Verbosity == nil || *chat.Verbosity != "low" {
		t.Errorf("expected verbosity=low, got %v", chat.Verbosity)
	}
	if chat.ResponseFormat == nil || chat.ResponseFormat.Type != "json_object" {
		t.Errorf("expected response_format json_object, got %+v", chat.ResponseFormat)
	}
}

func TestMapTextFormatJSONSchemaWrap(t *testing.T) {
	chat, _ := mapBody(t, `{
		"model":"m","input":"x",
		"text":{"format":{"type":"json_schema","name":"S","schema":{"type":"object"},"strict":true}}
	}`)
	if chat.ResponseFormat == nil || chat.ResponseFormat.Type != "json_schema" {
		t.Fatalf("expected json_schema response_format, got %+v", chat.ResponseFormat)
	}
	var wrapped map[string]json.RawMessage
	if err := json.Unmarshal(chat.ResponseFormat.JSONSchema, &wrapped); err != nil {
		t.Fatalf("json_schema not an object: %v", err)
	}
	if _, ok := wrapped["type"]; ok {
		t.Errorf("type should be lifted out of json_schema object, got %s", chat.ResponseFormat.JSONSchema)
	}
	for _, k := range []string{"name", "schema", "strict"} {
		if _, ok := wrapped[k]; !ok {
			t.Errorf("expected %q in json_schema object, got %s", k, chat.ResponseFormat.JSONSchema)
		}
	}
}

func TestMapTopLevelFieldsAndDrops(t *testing.T) {
	chat, tr := mapBody(t, `{
		"model":"m","input":"x",
		"max_output_tokens":256,"temperature":0.5,"top_p":0.9,
		"parallel_tool_calls":false,"store":false,
		"previous_response_id":"resp_1","include":["x"],"truncation":"auto","background":true,
		"client_metadata":{"x-codex-installation-id":"abc"}
	}`)

	if chat.MaxCompletionTokens == nil || *chat.MaxCompletionTokens != 256 {
		t.Errorf("expected max_output_tokens→MaxCompletionTokens=256, got %v", chat.MaxCompletionTokens)
	}
	if chat.Temperature == nil || *chat.Temperature != 0.5 {
		t.Errorf("expected temperature passthrough")
	}
	if chat.ParallelToolCalls == nil || *chat.ParallelToolCalls != false {
		t.Errorf("expected parallel_tool_calls passthrough")
	}
	for _, want := range []string{"previous_response_id", "include", "truncation", "background", "client_metadata"} {
		if !contains(tr.DroppedFields, want) {
			t.Errorf("expected %q dropped, got %v", want, tr.DroppedFields)
		}
	}
}

func contains(items []string, target string) bool {
	for _, item := range items {
		if item == target {
			return true
		}
	}
	return false
}
