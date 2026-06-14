package chatcompletions

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

// newOfficialBlackboxAdapter 在显式开启黑盒回归时构造官方 1P OpenAI adapter（即协议族 base）。
//
// 为避免普通 go test 误触发计费请求，本组用例要求：
//   - OPENAI_BLACKBOX=1 显式开启；
//   - OPENAI_BLACKBOX_API_KEY 提供可用 key。
//
// 可选覆盖（用于经 OpenAI 兼容代理实测，如 OpenRouter）：
//   - OPENAI_BLACKBOX_BASE_URL，默认官方 https://api.openai.com/v1；
//   - OPENAI_BLACKBOX_MODEL，默认 gpt-5.5。
//
// 任一必填缺失即 skip，符合「黑盒用例作为可选回归保留」的约定；key 只经 env 注入，不入代码。
func newOfficialBlackboxAdapter(t *testing.T) (*Adapter, channel.Runtime, string) {
	t.Helper()

	if os.Getenv("OPENAI_BLACKBOX") != "1" {
		t.Skip("OPENAI_BLACKBOX is not set to 1")
	}
	apiKey := os.Getenv("OPENAI_BLACKBOX_API_KEY")
	if apiKey == "" {
		t.Skip("OPENAI_BLACKBOX_API_KEY is not set")
	}

	baseURL := os.Getenv("OPENAI_BLACKBOX_BASE_URL")
	if baseURL == "" {
		baseURL = "https://api.openai.com/v1"
	}
	model := os.Getenv("OPENAI_BLACKBOX_MODEL")
	if model == "" {
		model = "gpt-5.5"
	}

	adapter := NewAdapter(&http.Client{Timeout: 120 * time.Second})
	runtime := channel.Runtime{
		BaseURL:      baseURL,
		APIKey:       apiKey,
		Timeout:      120 * time.Second,
		ProviderSlug: "openai",
	}

	return adapter, runtime, model
}

// OAI-1P-01：非流式 + 路线 C 忠实字段（max_completion_tokens、developer role、reasoning_effort 原生枚举）。
//
// 这是 N6 黑盒冻结的核心用例：去方言化后的 base 把这三个字段原样发给官方语义端点，
// 上游必须 200 接受（而不是因 max_tokens 塌缩 / developer 塌缩才能工作）。
func TestOpenAIOfficialBlackboxNonStreamFaithfulFields(t *testing.T) {
	adapter, runtime, model := newOfficialBlackboxAdapter(t)

	effort := "low"
	resp, err := adapter.ChatCompletions(context.Background(), runtime, ChatRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "developer", Content: jsonContent("You answer with exactly one word.")},
			{Role: "user", Content: jsonContent("Reply with the single word: ok")},
		},
		MaxCompletionTokens: intPtr(1024),
		ReasoningEffort:     &effort,
	})
	if err != nil {
		t.Fatalf("ChatCompletions with faithful official fields: %v", err)
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
	t.Logf("model=%s finish=%s usage=%+v reasoning_tokens=%d cached_tokens=%d",
		resp.Model, resp.FinishReason, resp.Usage, resp.Usage.ReasoningTokens, resp.Usage.CachedTokens)
}

// OAI-1P-02：流式 + 强制注入 stream_options.include_usage 的尾包 usage 形状（N6 黑盒项）。
func TestOpenAIOfficialBlackboxStream(t *testing.T) {
	adapter, runtime, model := newOfficialBlackboxAdapter(t)

	effort := "low"
	var content string
	var finalUsage *ChatStreamChunk
	outcome, err := adapter.StreamChatCompletions(context.Background(), runtime, ChatRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "developer", Content: jsonContent("You answer with exactly one word.")},
			{Role: "user", Content: jsonContent("Reply with the single word: ok")},
		},
		MaxCompletionTokens: intPtr(1024),
		ReasoningEffort:     &effort,
	}, func(chunk ChatStreamChunk) error {
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
		t.Fatal("expected a final usage chunk (injected include_usage)")
	}
	if finalUsage.Usage.PromptTokens <= 0 || finalUsage.Usage.CompletionTokens <= 0 {
		t.Fatalf("unexpected stream usage: %+v", finalUsage.Usage)
	}
	if outcome.Facts == nil || outcome.Facts.UsageSource != usage.SourceUpstreamStream {
		t.Fatalf("unexpected stream outcome facts: %+v", outcome.Facts)
	}
	t.Logf("stream usage=%+v", finalUsage.Usage)
}

// OAI-1P-03：客户同时显式传 max_tokens 与 max_completion_tokens 时忠实双发（不塌缩），上游语义裁决。
//
// N6 黑盒项「max_tokens vs max_completion_tokens 实际接受/语义」：本用例冻结上游对双字段的实际行为；
// 若上游拒绝双发（400），该事实同样需要记录（届时由 ingress 校验侧裁决，不归 adapter 塌缩）。
func TestOpenAIOfficialBlackboxBothMaxTokenFields(t *testing.T) {
	adapter, runtime, model := newOfficialBlackboxAdapter(t)

	resp, err := adapter.ChatCompletions(context.Background(), runtime, ChatRequest{
		Model: model,
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Reply with the single word: ok")},
		},
		MaxTokens:           intPtr(1024),
		MaxCompletionTokens: intPtr(1024),
	})
	if err != nil {
		// 不直接 Fatal：上游拒绝双发也是要冻结的黑盒事实，打印后失败以引起记录。
		t.Fatalf("upstream rejected faithful double max token fields (record this fact): %v", err)
	}

	if resp.Content == "" {
		t.Fatal("expected non-empty content")
	}
	t.Logf("both max token fields accepted; finish=%s usage=%+v", resp.FinishReason, resp.Usage)
}
