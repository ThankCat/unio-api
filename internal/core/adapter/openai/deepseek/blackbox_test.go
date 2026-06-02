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

	adapter := NewAdapter(&http.Client{Timeout: 30 * time.Second}, nil)
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

func intPtr(v int) *int             { return &v }
func boolPtr(v bool) *bool          { return &v }
func float64Ptr(v float64) *float64 { return &v }
func strPtr(v string) *string       { return &v }

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

// DS-OAI-14：tokenizer 与 DeepSeek 实际 usage.prompt_tokens 校准（基于 dropped wire）。
//
// authorization 用本地估算保守冻结余额，因此核心要求是「估算不低于上游实际输入」（不低估平台风险），
// 同时上界不能离谱（否则冻结过多、影响可用额度）。本用例对多种语言/规模的 prompt 取真实
// prompt_tokens，验证估算落在 [actual, actual*upper+slack] 内，并打印比值供后续调参。
func TestDeepSeekBlackboxTokenizerCalibration(t *testing.T) {
	adapter, runtime := newBlackboxAdapter(t)

	cases := []struct {
		name     string
		messages []openai.ChatMessage
	}{
		{
			name:     "short_en",
			messages: []openai.ChatMessage{blackboxUserMessage("Reply with the single word: ok")},
		},
		{
			name: "paragraph_en",
			messages: []openai.ChatMessage{
				{Role: "system", Content: jsonMarshal(t, "You are a terse assistant. Answer in one short sentence.")},
				blackboxUserMessage("Summarize why unit tests matter for a payment gateway in one sentence."),
			},
		},
		{
			name:     "chinese",
			messages: []openai.ChatMessage{blackboxUserMessage("用一句话说明为什么计费系统必须保证幂等。")},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := openai.ChatRequest{
				Model:     "deepseek-chat",
				Messages:  tc.messages,
				MaxTokens: intPtr(16),
			}

			estimate, err := adapter.CountChatInputTokens(req)
			if err != nil {
				t.Fatalf("CountChatInputTokens: %v", err)
			}

			resp, err := adapter.ChatCompletions(context.Background(), runtime, req)
			if err != nil {
				t.Fatalf("ChatCompletions: %v", err)
			}

			actual := int64(resp.Usage.PromptTokens)
			ratio := float64(estimate) / float64(actual)
			t.Logf("estimate=%d actual=%d ratio=%.2f", estimate, actual, ratio)

			// 保守性：估算必须 >= 上游实际输入 token（authorization 不能低估风险）。
			if estimate < actual {
				t.Fatalf("estimate %d below actual prompt_tokens %d (under-estimates billing risk)", estimate, actual)
			}

			// 上界 sanity：估算不应超过实际的 5 倍 + 64 固定余量，否则冻结过度需要调参。
			if estimate > actual*5+64 {
				t.Fatalf("estimate %d unreasonably above actual %d", estimate, actual)
			}
		})
	}
}

// DS-OAI-07：logprobs + top_logprobs，校验响应 choice.logprobs 被完整透传（Lesson 3 字段贯通）。
func TestDeepSeekBlackboxLogprobs(t *testing.T) {
	adapter, runtime := newBlackboxAdapter(t)

	resp, err := adapter.ChatCompletions(context.Background(), runtime, openai.ChatRequest{
		Model:       "deepseek-chat",
		Messages:    []openai.ChatMessage{blackboxUserMessage("Reply with the single word: ok")},
		MaxTokens:   intPtr(8),
		Logprobs:    boolPtr(true),
		TopLogprobs: intPtr(3),
	})
	if err != nil {
		t.Fatalf("ChatCompletions: %v", err)
	}

	if len(resp.Logprobs) == 0 || string(resp.Logprobs) == "null" {
		t.Fatalf("expected non-empty choice logprobs, got %q", string(resp.Logprobs))
	}

	var logprobs struct {
		Content []struct {
			Token       string  `json:"token"`
			Logprob     float64 `json:"logprob"`
			TopLogprobs []struct {
				Token   string  `json:"token"`
				Logprob float64 `json:"logprob"`
			} `json:"top_logprobs"`
		} `json:"content"`
	}
	if err := json.Unmarshal(resp.Logprobs, &logprobs); err != nil {
		t.Fatalf("decode logprobs: %v (raw=%s)", err, resp.Logprobs)
	}
	if len(logprobs.Content) == 0 {
		t.Fatalf("expected logprobs.content entries, got %s", resp.Logprobs)
	}
	if len(logprobs.Content[0].TopLogprobs) == 0 {
		t.Fatalf("expected top_logprobs entries, got %s", resp.Logprobs)
	}
}

// DS-OAI-03：thinking 非流式 reasoning/content 分离（deepseek-reasoner）。
func TestDeepSeekBlackboxThinkingNonStream(t *testing.T) {
	adapter, runtime := newBlackboxAdapter(t)

	resp, err := adapter.ChatCompletions(context.Background(), runtime, openai.ChatRequest{
		Model:     "deepseek-reasoner",
		Messages:  []openai.ChatMessage{blackboxUserMessage("What is 2+2? Reply with just the number.")},
		MaxTokens: intPtr(512),
	})
	if err != nil {
		t.Fatalf("ChatCompletions: %v", err)
	}

	if resp.ReasoningContent == nil || *resp.ReasoningContent == "" {
		t.Fatalf("expected reasoning_content from reasoner, got %#v", resp.ReasoningContent)
	}
	if resp.Content == "" {
		t.Fatal("expected non-empty content separate from reasoning")
	}
	if out, ok := resp.Facts.Usage.ReasoningOutputTokens.BillableValue(); !ok || out <= 0 {
		t.Fatalf("expected reasoning output tokens in facts, got (%d, %v)", out, ok)
	}
}

// DS-OAI-04：thinking 流式 reasoning/content 分离（deepseek-reasoner）。
func TestDeepSeekBlackboxThinkingStream(t *testing.T) {
	adapter, runtime := newBlackboxAdapter(t)

	var content, reasoning string
	var finalUsage *openai.ChatStreamChunk
	outcome, err := adapter.StreamChatCompletions(context.Background(), runtime, openai.ChatRequest{
		Model:     "deepseek-reasoner",
		Messages:  []openai.ChatMessage{blackboxUserMessage("What is 2+2? Reply with just the number.")},
		MaxTokens: intPtr(512),
	}, func(chunk openai.ChatStreamChunk) error {
		content += chunk.Content
		if chunk.ReasoningContent != nil {
			reasoning += *chunk.ReasoningContent
		}
		if chunk.Usage != nil {
			c := chunk
			finalUsage = &c
		}
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChatCompletions: %v", err)
	}

	if reasoning == "" {
		t.Fatal("expected streamed reasoning_content from reasoner")
	}
	if content == "" {
		t.Fatal("expected streamed content separate from reasoning")
	}
	if finalUsage == nil {
		t.Fatal("expected a final usage chunk")
	}
	if outcome.Facts == nil || outcome.Facts.UsageSource != usage.SourceUpstreamStream {
		t.Fatalf("unexpected stream outcome facts: %+v", outcome.Facts)
	}
	if out, ok := outcome.Facts.Usage.ReasoningOutputTokens.BillableValue(); !ok || out <= 0 {
		t.Fatalf("expected reasoning output tokens in facts, got (%d, %v)", out, ok)
	}
}

// DS-OAI-09：客户传 DeepSeek 不支持字段时走 ingress 200 路径。
//
// 这是 DEC-012「协议为先 + provider Drop」对真实上游的核心验证：一个塞满 DeepSeek 不支持参数
// （presence/frequency penalty、logit_bias、n、modalities、store、safety_identifier、parallel_tool_calls）
// 的请求，经 adapter Drop 后仍应在上游成功返回，而不是被 DeepSeek 400 拒绝。
func TestDeepSeekBlackboxDropUnsupportedStill200(t *testing.T) {
	adapter, runtime := newBlackboxAdapter(t)

	resp, err := adapter.ChatCompletions(context.Background(), runtime, openai.ChatRequest{
		Model:             "deepseek-chat",
		Messages:          []openai.ChatMessage{blackboxUserMessage("Reply with the single word: ok")},
		MaxTokens:         intPtr(8),
		PresencePenalty:   float64Ptr(0.5),
		FrequencyPenalty:  float64Ptr(0.5),
		N:                 intPtr(2),
		LogitBias:         json.RawMessage(`{"100":-50}`),
		Modalities:        []string{"text"},
		Store:             boolPtr(true),
		SafetyIdentifier:  strPtr("blackbox-sid"),
		ParallelToolCalls: boolPtr(true),
	})
	if err != nil {
		t.Fatalf("ChatCompletions with dropped fields should succeed upstream: %v", err)
	}
	if resp.Content == "" {
		t.Fatal("expected non-empty content after dropping unsupported fields")
	}
	if resp.Usage.PromptTokens <= 0 {
		t.Fatalf("expected positive prompt tokens, got %+v", resp.Usage)
	}
}

func jsonMarshal(t *testing.T, v any) json.RawMessage {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	return b
}
