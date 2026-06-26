package responses

import (
	"encoding/json"
	"testing"
)

// TestResponsesRequestUnmarshalItemsInput 验证 input 为 item 数组时各 item 类型解码进 typed 字段。
func TestResponsesRequestUnmarshalItemsInput(t *testing.T) {
	raw := `{
		"model": "deepseek-v4-flash",
		"instructions": "You are a coding agent.",
		"input": [
			{"type": "message", "role": "developer", "content": [{"type": "input_text", "text": "rules"}]},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "hi"}]},
			{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": "{\"cmd\":\"ls\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "files"},
			{"type": "reasoning", "id": "rs_1", "summary": [{"type": "summary_text", "text": "s"}]}
		],
		"stream": true,
		"store": false,
		"parallel_tool_calls": false,
		"reasoning": null
	}`

	var req ResponsesRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if req.Model != "deepseek-v4-flash" {
		t.Fatalf("model = %q", req.Model)
	}
	if req.Instructions == nil || *req.Instructions != "You are a coding agent." {
		t.Fatalf("instructions = %#v", req.Instructions)
	}
	if !req.StreamEnabled() {
		t.Fatal("expected stream enabled")
	}
	if req.Store == nil || *req.Store {
		t.Fatalf("store = %#v", req.Store)
	}
	if req.Reasoning != nil {
		t.Fatalf("expected reasoning:null → nil, got %#v", req.Reasoning)
	}
	if req.Input.Text != nil {
		t.Fatalf("expected items input, got text %#v", req.Input.Text)
	}
	if len(req.Input.Items) != 5 {
		t.Fatalf("expected 5 items, got %d", len(req.Input.Items))
	}

	fc := req.Input.Items[2]
	if fc.Type != "function_call" || fc.CallID == nil || *fc.CallID != "call_1" || fc.Name == nil || *fc.Name != "exec_command" {
		t.Fatalf("function_call item = %#v", fc)
	}
	fco := req.Input.Items[3]
	if fco.Type != "function_call_output" || fco.CallID == nil || *fco.CallID != "call_1" || len(fco.Output) == 0 {
		t.Fatalf("function_call_output item = %#v", fco)
	}
	rs := req.Input.Items[4]
	if rs.Type != "reasoning" || rs.ID == nil || *rs.ID != "rs_1" || len(rs.Summary) == 0 {
		t.Fatalf("reasoning item = %#v", rs)
	}
}

// TestResponsesRequestUnmarshalStringInput 验证 input 为单条字符串时解码进 Text。
func TestResponsesRequestUnmarshalStringInput(t *testing.T) {
	raw := `{"model": "deepseek-v4-flash", "input": "hello world"}`

	var req ResponsesRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.Input.Text == nil || *req.Input.Text != "hello world" {
		t.Fatalf("expected string input, got %#v", req.Input)
	}
	if len(req.Input.Items) != 0 {
		t.Fatalf("expected no items, got %d", len(req.Input.Items))
	}
}

// TestResponsesRequestUnmarshalTools 验证 function（扁平）与 namespace（嵌套）工具解码。
func TestResponsesRequestUnmarshalTools(t *testing.T) {
	raw := `{
		"model": "deepseek-v4-flash",
		"input": "hi",
		"tools": [
			{"type": "function", "name": "exec_command", "description": "run", "parameters": {"type": "object"}, "strict": false},
			{"type": "namespace", "name": "mcp__node_repl__", "tools": [
				{"type": "function", "name": "js", "parameters": {"type": "object"}}
			]}
		]
	}`

	var req ResponsesRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("expected 2 tools, got %d", len(req.Tools))
	}
	if !req.Tools[0].IsFunction() || req.Tools[0].Name != "exec_command" {
		t.Fatalf("function tool = %#v", req.Tools[0])
	}
	ns := req.Tools[1]
	if !ns.IsNamespace() || ns.Name != "mcp__node_repl__" || len(ns.Tools) != 1 || ns.Tools[0].Name != "js" {
		t.Fatalf("namespace tool = %#v", ns)
	}
}

// TestResponsesRequestUnmarshalPreservesClientMetadataExtension 验证 Codex 专属 client_metadata
// 保留进 Extensions（DEC-012 decode 不丢字段），已建模字段不泄漏。
func TestResponsesRequestUnmarshalPreservesClientMetadataExtension(t *testing.T) {
	raw := `{
		"model": "deepseek-v4-flash",
		"input": "hi",
		"include": [],
		"client_metadata": {"x-codex-installation-id": "abc"}
	}`

	var req ResponsesRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if !req.HasExtension("client_metadata") {
		t.Fatalf("expected client_metadata preserved in extensions, got %#v", req.Extensions)
	}
	if req.HasExtension("model") || req.HasExtension("input") || req.HasExtension("include") {
		t.Fatalf("known fields must not leak into extensions, got %#v", req.Extensions)
	}
}

// TestResponsesRequestUnmarshalTextAndReasoning 验证 text / reasoning 对象解码进 typed 字段。
func TestResponsesRequestUnmarshalTextAndReasoning(t *testing.T) {
	raw := `{
		"model": "deepseek-v4-pro",
		"input": "hi",
		"reasoning": {"effort": "high", "summary": "auto"},
		"text": {"verbosity": "low", "format": {"type": "json_object"}},
		"max_output_tokens": 256
	}`

	var req ResponsesRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.Reasoning == nil || req.Reasoning.Effort == nil || *req.Reasoning.Effort != "high" {
		t.Fatalf("reasoning = %#v", req.Reasoning)
	}
	if req.Text == nil || req.Text.Verbosity == nil || *req.Text.Verbosity != "low" || len(req.Text.Format) == 0 {
		t.Fatalf("text = %#v", req.Text)
	}
	if req.MaxOutputTokens == nil || req.MaxOutputTokens.Int() != 256 {
		t.Fatalf("max_output_tokens = %#v", req.MaxOutputTokens)
	}
}

func TestResponsesRequestUnmarshalMaxOutputTokensAcceptsIntegralNumber(t *testing.T) {
	var req ResponsesRequest
	if err := json.Unmarshal([]byte(`{"model": "m", "input": "hi", "max_output_tokens": 256.0}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}
	if req.MaxOutputTokens == nil || req.MaxOutputTokens.Int() != 256 {
		t.Fatalf("max_output_tokens = %#v", req.MaxOutputTokens)
	}
}
