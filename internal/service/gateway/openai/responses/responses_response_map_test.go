package responses

import (
	"strings"
	"testing"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/responses"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
)

func strptr(s string) *string { return &s }

func TestMapChatResponseToResponses_OutputOrderAndShape(t *testing.T) {
	req := gatewayapi.ResponsesRequest{Model: "unio-deepseek"}
	chatResp := openai.ChatResponse{
		ID:               "chatcmpl-abc",
		Content:          "hello world",
		ReasoningContent: strptr("let me think"),
		FinishReason:     "tool_calls",
		Created:          1700000000,
		ToolCalls: []openai.ChatToolCall{
			{
				ID:   "call_1",
				Type: "function",
				Function: openai.ChatToolCallFunction{
					Name:      "exec_command",
					Arguments: `{"cmd":"ls"}`,
				},
			},
		},
		Usage: adapter.ChatUsage{
			PromptTokens:     100,
			CompletionTokens: 40,
			TotalTokens:      140,
			CachedTokens:     20,
			ReasoningTokens:  12,
		},
	}

	got := mapChatResponseToResponses(req, chatResp)

	if got.Object != "response" || !strings.HasPrefix(got.ID, "resp_") {
		t.Fatalf("unexpected response envelope: object=%q id=%q", got.Object, got.ID)
	}
	if got.Model != "unio-deepseek" {
		t.Fatalf("expected client model echo, got %q", got.Model)
	}
	if got.CreatedAt != 1700000000 {
		t.Fatalf("expected upstream created passthrough, got %d", got.CreatedAt)
	}
	if got.Status != "completed" {
		t.Fatalf("expected completed status for tool_calls, got %q", got.Status)
	}

	if len(got.Output) != 3 {
		t.Fatalf("expected reasoning+message+function_call (3), got %d", len(got.Output))
	}
	if got.Output[0].Type != "reasoning" || got.Output[1].Type != "message" || got.Output[2].Type != "function_call" {
		t.Fatalf("unexpected output ordering: %q,%q,%q", got.Output[0].Type, got.Output[1].Type, got.Output[2].Type)
	}

	reasoning := got.Output[0]
	if !strings.HasPrefix(reasoning.ID, "rs_") || len(reasoning.Content) != 1 ||
		reasoning.Content[0].Type != "reasoning_text" || reasoning.Content[0].Text != "let me think" {
		t.Fatalf("unexpected reasoning item: %+v", reasoning)
	}

	message := got.Output[1]
	if message.Role != "assistant" || message.Status != "completed" ||
		len(message.Content) != 1 || message.Content[0].Type != "output_text" || message.Content[0].Text != "hello world" {
		t.Fatalf("unexpected message item: %+v", message)
	}

	call := got.Output[2]
	if call.CallID != "call_1" || call.Name != "exec_command" || call.Arguments != `{"cmd":"ls"}` ||
		call.Namespace != "" || call.Status != "completed" {
		t.Fatalf("unexpected function_call item: %+v", call)
	}

	if got.Usage == nil || got.Usage.InputTokens != 100 || got.Usage.OutputTokens != 40 || got.Usage.TotalTokens != 140 {
		t.Fatalf("unexpected usage totals: %+v", got.Usage)
	}
	if got.Usage.InputTokensDetails == nil || got.Usage.InputTokensDetails.CachedTokens != 20 {
		t.Fatalf("expected cached tokens detail, got %+v", got.Usage.InputTokensDetails)
	}
	if got.Usage.OutputTokensDetails == nil || got.Usage.OutputTokensDetails.ReasoningTokens != 12 {
		t.Fatalf("expected reasoning tokens detail, got %+v", got.Usage.OutputTokensDetails)
	}
}

func TestMapChatResponseToResponses_LengthBecomesIncomplete(t *testing.T) {
	got := mapChatResponseToResponses(gatewayapi.ResponsesRequest{Model: "m"}, openai.ChatResponse{
		Content:      "partial",
		FinishReason: "length",
	})
	if got.Status != "incomplete" {
		t.Fatalf("expected incomplete, got %q", got.Status)
	}
	if got.IncompleteDetails == nil || got.IncompleteDetails.Reason != "max_output_tokens" {
		t.Fatalf("expected max_output_tokens reason, got %+v", got.IncompleteDetails)
	}
}

func TestMapChatResponseToResponses_RefusalPart(t *testing.T) {
	got := mapChatResponseToResponses(gatewayapi.ResponsesRequest{Model: "m"}, openai.ChatResponse{
		Refusal:      strptr("I cannot help with that"),
		FinishReason: "stop",
	})
	if len(got.Output) != 1 || got.Output[0].Type != "message" {
		t.Fatalf("expected single message item, got %+v", got.Output)
	}
	content := got.Output[0].Content
	if len(content) != 1 || content[0].Type != "refusal" || content[0].Refusal != "I cannot help with that" {
		t.Fatalf("unexpected refusal content: %+v", content)
	}
}

func TestMapChatResponseToResponses_CreatedFallback(t *testing.T) {
	got := mapChatResponseToResponses(gatewayapi.ResponsesRequest{Model: "m"}, openai.ChatResponse{
		Content:      "x",
		FinishReason: "stop",
		Created:      0,
	})
	if got.CreatedAt <= 0 {
		t.Fatalf("expected created_at fallback to local time, got %d", got.CreatedAt)
	}
}

func TestSplitNamespaceToolName(t *testing.T) {
	cases := []struct {
		flattened     string
		wantNamespace string
		wantName      string
	}{
		{"mcp__node_repl__js", "mcp__node_repl__", "js"},
		{"mcp__openaiDeveloperDocs__search", "mcp__openaiDeveloperDocs__", "search"},
		{"exec_command", "", "exec_command"},
		{"plain__name", "", "plain__name"},
		{"mcp__only", "", "mcp__only"},
	}
	for _, c := range cases {
		ns, name := splitNamespaceToolName(c.flattened)
		if ns != c.wantNamespace || name != c.wantName {
			t.Fatalf("split(%q) = (%q,%q), want (%q,%q)", c.flattened, ns, name, c.wantNamespace, c.wantName)
		}
	}
}
