package chatcompletions

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
)

// TestAdapterUpstreamWireCollapsesOfficialFields 在 HTTP 边界断言路线 C 下沉后的最终出站 wire：
// 经 deepseek.Adapter 全链路（dropUnsupported → base wire 编码），max_completion_tokens 不出现在
// upstream body（已塌缩进 max_tokens）、developer role 已塌缩为 system。这是改造前 base 方言的
// 行为基线，下沉后必须逐字节等价，防止 drop 单测与 wire 编码之间出现组合回归。
func TestAdapterUpstreamWireCollapsesOfficialFields(t *testing.T) {
	var captured []byte
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Errorf("read upstream body: %v", err)
		}
		captured = body

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{
			"id": "chatcmpl-1",
			"object": "chat.completion",
			"created": 1,
			"model": "deepseek-v4-flash",
			"choices": [{"index": 0, "message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
			"usage": {"prompt_tokens": 3, "completion_tokens": 1, "total_tokens": 4}
		}`))
	}))
	defer server.Close()

	adapter := NewAdapter(server.Client(), nil)
	maxTokens := 10
	maxCompletionTokens := 20
	_, err := adapter.ChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL,
		APIKey:  "test-key",
		Timeout: 5 * time.Second,
	}, chatcompletionsadapter.ChatRequest{
		Model: "deepseek-v4-flash",
		Messages: []chatcompletionsadapter.ChatMessage{
			{Role: "developer", Content: json.RawMessage(`"rules"`)},
			{Role: "user", Content: json.RawMessage(`"hi"`)},
		},
		MaxTokens:           &maxTokens,
		MaxCompletionTokens: &maxCompletionTokens,
	})
	if err != nil {
		t.Fatalf("ChatCompletions: %v", err)
	}

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(captured, &wire); err != nil {
		t.Fatalf("unmarshal captured wire: %v", err)
	}

	if _, ok := wire["max_completion_tokens"]; ok {
		t.Fatalf("max_completion_tokens must not reach deepseek upstream wire: %s", captured)
	}
	var wireMaxTokens int
	if err := json.Unmarshal(wire["max_tokens"], &wireMaxTokens); err != nil || wireMaxTokens != 20 {
		t.Fatalf("wire max_tokens = %s, want 20 (collapsed, completion tokens win)", wire["max_tokens"])
	}

	var messages []struct {
		Role string `json:"role"`
	}
	if err := json.Unmarshal(wire["messages"], &messages); err != nil {
		t.Fatalf("unmarshal wire messages: %v", err)
	}
	if len(messages) != 2 || messages[0].Role != "system" || messages[1].Role != "user" {
		t.Fatalf("wire roles = %+v, want [system user] (developer collapsed)", messages)
	}
}
