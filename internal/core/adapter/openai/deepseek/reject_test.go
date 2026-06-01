package deepseek

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

func TestRejectUnsupportedRequestRejectsHardUnsupportedFields(t *testing.T) {
	tests := []struct {
		name      string
		req       openai.ChatRequest
		wantParam string
	}{
		{
			name:      "json_schema response_format",
			req:       openai.ChatRequest{ResponseFormat: &openai.ChatResponseFormat{Type: "json_schema"}},
			wantParam: "response_format",
		},
		{
			name:      "custom tool",
			req:       openai.ChatRequest{Tools: []openai.ChatTool{{Type: "custom"}}},
			wantParam: "tools",
		},
		{
			name:      "n greater than one",
			req:       openai.ChatRequest{Extensions: map[string]json.RawMessage{"n": json.RawMessage("2")}},
			wantParam: "n",
		},
		{
			name:      "audio output",
			req:       openai.ChatRequest{Extensions: map[string]json.RawMessage{"audio": json.RawMessage(`{"voice":"x","format":"mp3"}`)}},
			wantParam: "audio",
		},
		{
			name:      "modalities with audio",
			req:       openai.ChatRequest{Extensions: map[string]json.RawMessage{"modalities": json.RawMessage(`["text","audio"]`)}},
			wantParam: "modalities",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := rejectUnsupportedRequest(tt.req)
			if err == nil {
				t.Fatalf("expected reject error")
			}
			if got := failure.CodeOf(err); got != failure.CodeAdapterRequestUnsupported {
				t.Fatalf("code = %q, want %q", got, failure.CodeAdapterRequestUnsupported)
			}

			param := ""
			for _, field := range failure.FieldsOf(err) {
				if field.Key == "param" {
					param, _ = field.Value.(string)
				}
			}
			if param != tt.wantParam {
				t.Fatalf("param = %q, want %q", param, tt.wantParam)
			}
		})
	}
}

func TestRejectUnsupportedRequestAcceptsSupportedRequest(t *testing.T) {
	reqs := []openai.ChatRequest{
		{Model: "deepseek-chat"},
		{ResponseFormat: &openai.ChatResponseFormat{Type: "json_object"}},
		{Tools: []openai.ChatTool{{Type: "function"}}},
		{Extensions: map[string]json.RawMessage{"n": json.RawMessage("1")}},
		{Extensions: map[string]json.RawMessage{"modalities": json.RawMessage(`["text"]`)}},
		// 以下字段 DeepSeek 黑盒 200 静默忽略，记录 No-op，透传不拒绝。
		{Extensions: map[string]json.RawMessage{"seed": json.RawMessage("42")}},
		{Extensions: map[string]json.RawMessage{"metadata": json.RawMessage(`{"k":"v"}`)}},
		{Extensions: map[string]json.RawMessage{"logit_bias": json.RawMessage(`{"1":0}`)}},
	}

	for i, req := range reqs {
		if err := rejectUnsupportedRequest(req); err != nil {
			t.Fatalf("request %d: unexpected reject: %v", i, err)
		}
	}
}

// TestAdapterChatCompletionsRejectsBeforeUpstream 验证不支持字段在调用上游前被拒绝（不依赖网络）。
func TestAdapterChatCompletionsRejectsBeforeUpstream(t *testing.T) {
	adapter := NewAdapter(nil)

	_, err := adapter.ChatCompletions(context.Background(), channel.Runtime{}, openai.ChatRequest{
		Model:          "deepseek-chat",
		ResponseFormat: &openai.ChatResponseFormat{Type: "json_schema"},
	})
	if err == nil {
		t.Fatal("expected reject before upstream")
	}
	if got := failure.CodeOf(err); got != failure.CodeAdapterRequestUnsupported {
		t.Fatalf("code = %q, want %q", got, failure.CodeAdapterRequestUnsupported)
	}
}
