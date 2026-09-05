package responses

import (
	"encoding/json"
	"testing"
)

// TestStreamProgressMatrix 冻结「上游进展」判定（对齐 Sub2API client output started）：
// 协议前导与失败终态不算；reasoning item 空壳不算、带 encrypted_content 或非空 summary 才算；
// 各类 .done / .delta / 未知事件算。它与 FirstTokenPayload 正交：reasoning item 是进展但不是可见内容。
func TestStreamProgressMatrix(t *testing.T) {
	cases := []struct {
		name         string
		eventType    string
		data         string
		wantProgress bool
		wantVisible  bool
	}{
		{"response.created is preamble", "response.created", `{"response":{"id":"r"}}`, false, false},
		{"response.in_progress is preamble", "response.in_progress", `{"response":{"id":"r"}}`, false, false},
		{"response.queued is preamble", "response.queued", `{"response":{"id":"r"}}`, false, false},
		{"response.failed is not progress", "response.failed", `{"response":{"error":{"code":"server_error"}}}`, false, false},
		{"error event is not progress", "error", `{"type":"error","code":"server_is_overloaded"}`, false, false},
		{"reasoning item shell is not progress", "response.output_item.added", `{"item":{"type":"reasoning","summary":[]}}`, false, false},
		{"reasoning item with encrypted content is progress", "response.output_item.added", `{"item":{"type":"reasoning","encrypted_content":"gAAAA","summary":[]}}`, true, false},
		{"reasoning item with summary text is progress", "response.output_item.added", `{"item":{"type":"reasoning","summary":[{"type":"summary_text","text":"thinking"}]}}`, true, false},
		{"reasoning item done is progress", "response.output_item.done", `{"item":{"type":"reasoning","encrypted_content":"gAAAA"}}`, true, false},
		{"empty message item is not progress", "response.output_item.added", `{"item":{"type":"message","role":"assistant","content":[]}}`, false, false},
		{"message item with text is progress", "response.output_item.added", `{"item":{"type":"message","content":[{"type":"output_text","text":"hi"}]}}`, true, false},
		{"function call without arguments is not progress", "response.output_item.added", `{"item":{"type":"function_call","name":"exec","arguments":""}}`, false, true},
		{"function call with arguments is progress and visible", "response.output_item.added", `{"item":{"type":"function_call","name":"exec","arguments":"{}"}}`, true, true},
		{"custom tool call with input is progress (name makes it visible per ADR-0017)", "response.output_item.added", `{"item":{"type":"custom_tool_call","name":"apply_patch","input":"*** Begin"}}`, true, true},
		{"compaction item with encrypted content is progress", "response.output_item.added", `{"item":{"type":"compaction","encrypted_content":"gAAAA"}}`, true, false},
		{"unknown item type is progress", "response.output_item.added", `{"item":{"type":"image_generation_call"}}`, true, false},
		{"empty content part is not progress", "response.content_part.added", `{"part":{"type":"output_text","text":""}}`, false, false},
		{"summary part with text is progress", "response.reasoning_summary_part.added", `{"part":{"type":"summary_text","text":"plan"}}`, true, false},
		{"output text delta is progress and visible", "response.output_text.delta", `{"delta":"h"}`, true, true},
		{"reasoning summary delta is progress and visible", "response.reasoning_summary_text.delta", `{"delta":"th"}`, true, true},
		{"unknown event is progress", "response.something_new", `{"x":1}`, true, false},
		{"completed is progress at adapter level", "response.completed", `{"response":{"id":"r","status":"completed"}}`, true, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			chunk := StreamChunk{EventType: tc.eventType, Data: json.RawMessage(tc.data)}
			if got := StreamProgress(chunk); got != tc.wantProgress {
				t.Fatalf("StreamProgress = %v, want %v", got, tc.wantProgress)
			}
			if got := FirstTokenPayload(chunk) != ""; got != tc.wantVisible {
				t.Fatalf("visible = %v, want %v", got, tc.wantVisible)
			}
		})
	}
}

// TestFunctionCallNameOnlyStaysVisibleButNotProgress 记录一个刻意保留的差异：ADR-0017 把「携带真实工具名称」的
// function_call item 视为可见首字（工具名本身对客户有意义），而 Sub2API 的进展判定要求 arguments 非空。
// 两者不冲突：可见即算首字，也必然解除首字预算。
func TestFunctionCallNameOnlyStaysVisibleButNotProgress(t *testing.T) {
	chunk := StreamChunk{EventType: "response.output_item.added", Data: json.RawMessage(`{"item":{"type":"function_call","name":"exec"}}`)}
	if FirstTokenPayload(chunk) == "" {
		t.Fatal("function_call with a name must remain a visible first token (ADR-0017)")
	}
	if StreamProgress(chunk) {
		t.Fatal("Sub2API progress rule requires non-empty arguments for function_call")
	}
}
