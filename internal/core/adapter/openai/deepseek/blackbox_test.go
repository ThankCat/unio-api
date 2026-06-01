package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter/openai"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

// newBlackboxAdapter 在显式开启黑盒回归时构造真实 DeepSeek adapter 与 channel runtime。
//
// 为避免普通 go test 误触发计费请求，本组用例同时要求：
//   - DEEPSEEK_BLACKBOX=1 显式开启；
//   - DEEPSEEK_API_KEY 提供可用 key。
//
// 任一缺失即 skip，符合"黑盒用例作为可选回归保留"的约定。
func newBlackboxAdapter(t *testing.T) (*Adapter, channel.Runtime) {
	t.Helper()

	if os.Getenv("DEEPSEEK_BLACKBOX") != "1" {
		t.Skip("DEEPSEEK_BLACKBOX is not set to 1")
	}
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Skip("DEEPSEEK_API_KEY is not set")
	}

	adapter := NewAdapter(&http.Client{Timeout: 30 * time.Second})
	runtime := channel.Runtime{
		BaseURL:      "https://api.deepseek.com",
		APIKey:       apiKey,
		Timeout:      30 * time.Second,
		ProviderSlug: "deepseek",
	}

	return adapter, runtime
}

func blackboxUserMessage(text string) openai.ChatMessage {
	content, _ := json.Marshal(text)
	return openai.ChatMessage{Role: "user", Content: content}
}

func intPtr(v int) *int { return &v }

// DS-OAI-01：基础非流式 text，校验响应内容、usage 与 ResponseFacts。
func TestDeepSeekBlackboxNonStream(t *testing.T) {
	adapter, runtime := newBlackboxAdapter(t)

	resp, err := adapter.ChatCompletions(context.Background(), runtime, openai.ChatRequest{
		Model:     "deepseek-chat",
		Messages:  []openai.ChatMessage{blackboxUserMessage("Reply with the single word: ok")},
		MaxTokens: intPtr(8),
	})
	if err != nil {
		t.Fatalf("ChatCompletions: %v", err)
	}

	if resp.Content == "" {
		t.Fatal("expected non-empty content")
	}
	if resp.Usage.PromptTokens <= 0 || resp.Usage.CompletionTokens <= 0 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	if resp.Facts.UpstreamProtocol != "openai" {
		t.Fatalf("facts upstream protocol = %q", resp.Facts.UpstreamProtocol)
	}
	if resp.Facts.UsageSource != usage.SourceUpstreamResponse {
		t.Fatalf("facts usage source = %q", resp.Facts.UsageSource)
	}
	if out, ok := resp.Facts.Usage.OutputTokensTotal.BillableValue(); !ok || out <= 0 {
		t.Fatalf("facts output tokens = (%d, %v)", out, ok)
	}
}

// DS-OAI-02：基础流式 text，校验内容增量与最终 usage chunk。
func TestDeepSeekBlackboxStream(t *testing.T) {
	adapter, runtime := newBlackboxAdapter(t)

	var content string
	var finalUsage *openai.ChatStreamChunk
	outcome, err := adapter.StreamChatCompletions(context.Background(), runtime, openai.ChatRequest{
		Model:     "deepseek-chat",
		Messages:  []openai.ChatMessage{blackboxUserMessage("Reply with the single word: ok")},
		MaxTokens: intPtr(8),
	}, func(chunk openai.ChatStreamChunk) error {
		content += chunk.Content
		if chunk.Usage != nil {
			c := chunk
			finalUsage = &c
		}
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChatCompletions: %v", err)
	}

	if content == "" {
		t.Fatal("expected non-empty streamed content")
	}
	if finalUsage == nil {
		t.Fatal("expected a final usage chunk")
	}
	if finalUsage.Usage.PromptTokens <= 0 || finalUsage.Usage.CompletionTokens <= 0 {
		t.Fatalf("unexpected stream usage: %+v", finalUsage.Usage)
	}
	if outcome.Facts == nil || outcome.Facts.UsageSource != usage.SourceUpstreamStream {
		t.Fatalf("unexpected stream outcome facts: %+v", outcome.Facts)
	}
}
