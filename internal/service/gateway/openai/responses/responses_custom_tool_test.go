package responses

import (
	"encoding/json"
	"strings"
	"testing"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/responses"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
)

// applyPatchToolJSON 是 Codex v0.147 抓包实测的 custom 工具定义（lark grammar 已截断保留首尾）。
const applyPatchToolJSON = `{
	"type": "custom",
	"name": "apply_patch",
	"description": "The ` + "`apply_patch`" + ` tool can be used to edit files.",
	"format": {
		"type": "grammar",
		"syntax": "lark",
		"definition": "start: begin_patch hunk+ end_patch\nbegin_patch: \"*** Begin Patch\" LF\nend_patch: \"*** End Patch\" LF?\n"
	}
}`

// applyPatchDocument 是抓包实测的 patch 裸文本（含换行与引号敏感字符）。
const applyPatchDocument = "*** Begin Patch\n*** Update File: calc.py\n@@\n def sub(a, b):\n     return a - b\n+\n+\n+def multiply(a, b):\n+    return a * b\n*** End Patch\n"

// TestCustomToolOutboundMapping 验证出站方向：custom 工具降级为单参数 function，
// 且 lark 语法作为软约束进入 description。
func TestCustomToolOutboundMapping(t *testing.T) {
	chat, tr := mapBody(t, `{
		"model": "gpt-5.4-mini",
		"input": "edit the file",
		"tools": [`+applyPatchToolJSON+`]
	}`)

	if len(chat.Tools) != 1 {
		t.Fatalf("expected custom tool to be mapped, got %d tools", len(chat.Tools))
	}
	tool := chat.Tools[0]
	if tool.Type != "function" {
		t.Errorf("expected function type, got %q", tool.Type)
	}
	if tool.Function.Name != "apply_patch" {
		t.Errorf("expected name preserved, got %q", tool.Function.Name)
	}
	if !strings.Contains(tool.Function.Description, "*** Begin Patch") {
		t.Errorf("expected lark definition carried into description, got %q", tool.Function.Description)
	}
	if !strings.Contains(tool.Function.Description, "not wrapped in JSON") {
		t.Errorf("expected freeform instruction in description, got %q", tool.Function.Description)
	}

	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
		Required   []string                   `json:"required"`
	}
	if err := json.Unmarshal(tool.Function.Parameters, &schema); err != nil {
		t.Fatalf("decode parameters: %v", err)
	}
	if _, ok := schema.Properties["input"]; !ok {
		t.Errorf("expected input property, got %v", schema.Properties)
	}
	if len(schema.Required) != 1 || schema.Required[0] != "input" {
		t.Errorf("expected input required, got %v", schema.Required)
	}

	// custom 工具已被承载，不应再记为契约缺失 Drop。
	for _, dropped := range tr.DroppedFields {
		if strings.HasPrefix(dropped, "tools.custom") {
			t.Errorf("custom tool should no longer be dropped, got %v", tr.DroppedFields)
		}
	}
}

// TestCustomToolCallRoundTripToChat 验证回传方向：客户端的 custom_tool_call 与
// custom_tool_call_output 还原为 Chat 的 assistant.tool_calls 与 tool 消息。
func TestCustomToolCallRoundTripToChat(t *testing.T) {
	inputJSON, err := json.Marshal(applyPatchDocument)
	if err != nil {
		t.Fatalf("marshal patch: %v", err)
	}
	chat, _ := mapBody(t, `{
		"model": "gpt-5.4-mini",
		"tools": [`+applyPatchToolJSON+`],
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "add multiply"}]},
			{"type": "custom_tool_call", "call_id": "call_abc", "name": "apply_patch", "input": `+string(inputJSON)+`},
			{"type": "custom_tool_call_output", "call_id": "call_abc", "output": "Success. Updated the following files:\nM calc.py\n"}
		]
	}`)

	if len(chat.Messages) != 3 {
		t.Fatalf("expected user+assistant+tool, got %d messages", len(chat.Messages))
	}

	assistant := chat.Messages[1]
	if assistant.Role != "assistant" || len(assistant.ToolCalls) != 1 {
		t.Fatalf("expected assistant tool_calls, got role=%q calls=%d", assistant.Role, len(assistant.ToolCalls))
	}
	call := assistant.ToolCalls[0]
	if call.ID != "call_abc" || call.Function.Name != "apply_patch" {
		t.Errorf("unexpected tool call identity: %+v", call)
	}
	// freeform 文本必须被重新包装成降级 schema 的 JSON 参数。
	roundTripped, ok := decodeCustomToolArguments(call.Function.Arguments)
	if !ok {
		t.Fatalf("arguments not decodable: %q", call.Function.Arguments)
	}
	if roundTripped != applyPatchDocument {
		t.Errorf("patch text not preserved through round trip:\nwant %q\ngot  %q", applyPatchDocument, roundTripped)
	}

	if chat.Messages[2].Role != "tool" {
		t.Errorf("expected tool message for custom_tool_call_output, got %q", chat.Messages[2].Role)
	}
}

// TestCustomToolNonStreamRestore 验证非流式响应方向：声明为 custom 的工具还原为
// custom_tool_call item，未声明的仍是 function_call。
func TestCustomToolNonStreamRestore(t *testing.T) {
	req := decodeRequest(t, `{
		"model": "gpt-5.4-mini",
		"input": "edit",
		"tools": [`+applyPatchToolJSON+`, {"type":"function","name":"exec_command","parameters":{"type":"object"}}]
	}`)

	resp := mapChatResponseToResponses(req, chatcompletionsadapter.ChatResponse{
		ToolCalls: []chatcompletionsadapter.ChatToolCall{
			{ID: "call_1", Function: chatcompletionsadapter.ChatToolCallFunction{
				Name: "apply_patch", Arguments: encodeCustomToolArguments(applyPatchDocument),
			}},
			{ID: "call_2", Function: chatcompletionsadapter.ChatToolCallFunction{
				Name: "exec_command", Arguments: `{"cmd":"ls"}`,
			}},
		},
	})

	if len(resp.Output) != 2 {
		t.Fatalf("expected two output items, got %d", len(resp.Output))
	}
	custom := resp.Output[0]
	if custom.Type != "custom_tool_call" {
		t.Errorf("expected custom_tool_call, got %q", custom.Type)
	}
	if custom.Input != applyPatchDocument {
		t.Errorf("patch not restored:\nwant %q\ngot  %q", applyPatchDocument, custom.Input)
	}
	if custom.Status != "completed" {
		t.Errorf("expected completed status, got %q", custom.Status)
	}
	if !strings.HasPrefix(custom.ID, "ctc_") {
		t.Errorf("expected ctc_ id prefix, got %q", custom.ID)
	}
	if resp.Output[1].Type != "function_call" {
		t.Errorf("non-custom tool should stay function_call, got %q", resp.Output[1].Type)
	}
}

// TestCustomToolRestoreFallback 验证兜底：上游违反降级 schema 时原样透出并标记
// incomplete，绝不静默产出空 input（那会让客户端误以为编辑成功）。
func TestCustomToolRestoreFallback(t *testing.T) {
	req := decodeRequest(t, `{
		"model": "gpt-5.4-mini",
		"input": "edit",
		"tools": [`+applyPatchToolJSON+`]
	}`)

	cases := []struct {
		name      string
		arguments string
	}{
		{"bare payload not wrapped in json", applyPatchDocument},
		{"json object without input key", `{"patch":"*** Begin Patch"}`},
		{"input is not a string", `{"input":{"nested":true}}`},
		{"empty arguments", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp := mapChatResponseToResponses(req, chatcompletionsadapter.ChatResponse{
				ToolCalls: []chatcompletionsadapter.ChatToolCall{
					{ID: "call_1", Function: chatcompletionsadapter.ChatToolCallFunction{
						Name: "apply_patch", Arguments: tc.arguments,
					}},
				},
			})
			if len(resp.Output) != 1 {
				t.Fatalf("expected one item, got %d", len(resp.Output))
			}
			item := resp.Output[0]
			if item.Type != "custom_tool_call" {
				t.Errorf("expected custom_tool_call, got %q", item.Type)
			}
			if item.Status != "incomplete" {
				t.Errorf("contract violation must be surfaced as incomplete, got %q", item.Status)
			}
			if item.Input != tc.arguments {
				t.Errorf("upstream text must be preserved verbatim:\nwant %q\ngot  %q", tc.arguments, item.Input)
			}
		})
	}
}

// TestCustomToolStreamEvents 验证流式方向：不逐片转发 function_call_arguments，
// 收尾时补发成对的 custom_tool_call_input delta/done。
func TestCustomToolStreamEvents(t *testing.T) {
	req := decodeRequest(t, `{
		"model": "gpt-5.4-mini",
		"input": "edit",
		"tools": [`+applyPatchToolJSON+`]
	}`)

	var events []gatewayapi.ResponsesStreamEvent
	enc := newStreamEncoder(req, "resp_test", 1700000000, func(ev gatewayapi.ResponsesStreamEvent) error {
		events = append(events, ev)
		return nil
	})

	// 上游按降级 function 分片吐 JSON arguments。
	full := encodeCustomToolArguments(applyPatchDocument)
	mid := len(full) / 2
	deltas := []string{
		`[{"index":0,"id":"call_abc","function":{"name":"apply_patch","arguments":` + mustJSONString(t, full[:mid]) + `}}]`,
		`[{"index":0,"function":{"arguments":` + mustJSONString(t, full[mid:]) + `}}]`,
	}
	for _, d := range deltas {
		if err := enc.handleToolCallDeltas(json.RawMessage(d)); err != nil {
			t.Fatalf("handle deltas: %v", err)
		}
	}
	if _, err := enc.closeItems(); err != nil {
		t.Fatalf("close items: %v", err)
	}

	var sawItemAdded, sawInputDelta, sawInputDone, sawItemDone bool
	for _, ev := range events {
		switch ev.Type {
		case gatewayapi.EventOutputItemAdded:
			if ev.Item != nil && ev.Item.Type == "custom_tool_call" {
				sawItemAdded = true
			}
		case gatewayapi.EventFunctionCallArgsDelta:
			t.Errorf("custom tool must not emit function_call_arguments.delta")
		case gatewayapi.EventCustomToolCallInputDelta:
			sawInputDelta = true
			if ev.Delta != applyPatchDocument {
				t.Errorf("delta should carry restored patch text, got %q", ev.Delta)
			}
		case gatewayapi.EventCustomToolCallInputDone:
			sawInputDone = true
			if ev.Input != applyPatchDocument {
				t.Errorf("done should carry restored patch text, got %q", ev.Input)
			}
		case gatewayapi.EventOutputItemDone:
			if ev.Item != nil && ev.Item.Type == "custom_tool_call" {
				sawItemDone = true
				if ev.Item.Input != applyPatchDocument {
					t.Errorf("final item input mismatch: %q", ev.Item.Input)
				}
				if !strings.HasPrefix(ev.Item.ID, "ctc_") {
					t.Errorf("expected ctc_ id prefix, got %q", ev.Item.ID)
				}
			}
		}
	}
	if !sawItemAdded || !sawInputDelta || !sawInputDone || !sawItemDone {
		t.Errorf("missing events: added=%v delta=%v done=%v itemDone=%v", sawItemAdded, sawInputDelta, sawInputDone, sawItemDone)
	}
}

// TestReasoningSurvivesCommentaryBeforeToolCall 锁住一个端到端实测暴露的回归：
// Codex v0.147 的回传序列是 reasoning → assistant commentary → function_call，
// 中间那条 assistant 消息一旦清空暂存思维链，该轮 tool_call 就会缺 reasoning_content，
// DeepSeek 以 400 "reasoning_content must be passed back" 拒绝整个请求。
func TestReasoningSurvivesCommentaryBeforeToolCall(t *testing.T) {
	carrier := encodeReasoningCarrier("the user wants a multiply helper")
	chat, _ := mapBody(t, `{
		"model": "gpt-5.4-mini",
		"input": [
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "add multiply"}]},
			{"type": "reasoning", "encrypted_content": `+mustJSONString(t, carrier)+`},
			{"type": "message", "role": "assistant", "content": [{"type": "output_text", "text": "I'll check calc.py first."}]},
			{"type": "function_call", "call_id": "call_1", "name": "exec_command", "arguments": "{\"cmd\":\"cat calc.py\"}"},
			{"type": "function_call_output", "call_id": "call_1", "output": "def add(a, b): ..."}
		]
	}`)

	var toolCallMsg *chatcompletionsadapter.ChatMessage
	for i := range chat.Messages {
		if len(chat.Messages[i].ToolCalls) > 0 {
			toolCallMsg = &chat.Messages[i]
			break
		}
	}
	if toolCallMsg == nil {
		t.Fatal("expected an assistant message carrying tool_calls")
	}
	if toolCallMsg.ReasoningContent == nil {
		t.Fatal("tool_call turn lost reasoning_content; upstream would reject with 400")
	}
	if *toolCallMsg.ReasoningContent != "the user wants a multiply helper" {
		t.Errorf("unexpected reasoning content: %q", *toolCallMsg.ReasoningContent)
	}
}

// TestReasoningDroppedOnNewUserTurn 验证换轮语义未被上一条修复破坏：
// 新的 user 输入代表新一轮，此前暂存的思维链必须丢弃。
func TestReasoningDroppedOnNewUserTurn(t *testing.T) {
	carrier := encodeReasoningCarrier("stale reasoning from a previous turn")
	chat, _ := mapBody(t, `{
		"model": "gpt-5.4-mini",
		"input": [
			{"type": "reasoning", "encrypted_content": `+mustJSONString(t, carrier)+`},
			{"type": "message", "role": "user", "content": [{"type": "input_text", "text": "actually do something else"}]},
			{"type": "function_call", "call_id": "call_2", "name": "exec_command", "arguments": "{\"cmd\":\"ls\"}"}
		]
	}`)

	for _, m := range chat.Messages {
		if len(m.ToolCalls) > 0 && m.ReasoningContent != nil {
			t.Errorf("reasoning from a previous turn must not leak across a new user input, got %q", *m.ReasoningContent)
		}
	}
}

func mustJSONString(t *testing.T, s string) string {
	t.Helper()
	encoded, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("marshal string: %v", err)
	}
	return string(encoded)
}
