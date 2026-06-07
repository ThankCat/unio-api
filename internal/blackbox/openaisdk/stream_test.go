//go:build blackbox

package openaisdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ThankCat/unio-api/internal/blackbox/sdkfixture"
)

// OAI-SDK-Mock-02：openai-go SDK 流式 chat completion。
//
// 验证：
//   - SDK NewStreaming 不报错；
//   - 聚合所有 chunk 后内容完整；
//   - 最后一帧带 usage（OpenAI 流式 usage 在 stream_options.include_usage=true 时附在尾包）；
//   - 上游收到 stream=true。
func TestOAISDKMockStreamSucceeds(t *testing.T) {
	var capturedBody []byte
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		// 模拟 deepseek 流：3 段 content delta，最后一帧带 usage。
		writeMockStreamChunks(w, "chatcmpl-stream-1",
			[]map[string]any{
				{"role": "assistant", "content": "hel"},
				{"content": "lo"},
				{"content": " world"},
			},
			map[string]any{
				"prompt_tokens":     5,
				"completion_tokens": 3,
				"total_tokens":      8,
			},
		)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("say hello world"),
		},
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	})

	var (
		acc          openai.ChatCompletionAccumulator
		gotUsage     bool
		streamFrames int
	)
	for stream.Next() {
		chunk := stream.Current()
		acc.AddChunk(chunk)
		streamFrames++
		if chunk.Usage.TotalTokens > 0 {
			gotUsage = true
			if chunk.Usage.PromptTokens != 5 || chunk.Usage.CompletionTokens != 3 {
				t.Fatalf("unexpected stream usage: %+v", chunk.Usage)
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if streamFrames < 3 {
		t.Fatalf("expected at least 3 stream frames, got %d", streamFrames)
	}
	if !gotUsage {
		t.Fatal("expected a usage chunk in stream tail")
	}

	if content := acc.ChatCompletion.Choices[0].Message.Content; content != "hello world" {
		t.Fatalf("expected accumulated content 'hello world', got %q", content)
	}

	// 上游应收到 stream=true。
	var upstream map[string]any
	if err := json.Unmarshal(capturedBody, &upstream); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if stream, _ := upstream["stream"].(bool); !stream {
		t.Fatalf("expected upstream stream=true, got %v (raw: %s)", upstream["stream"], string(capturedBody))
	}
}

// OAI-SDK-Mock-04：reasoning 流式。
//
// DeepSeek reasoner 流式 delta 同时带 reasoning_content 与 content 两个字段；
// SDK 没有对 reasoning_content 强类型，但应能通过 chunk.Choices[].Delta.JSON.ExtraFields 透传。
func TestOAISDKMockStreamReasoning(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeMockStreamChunks(w, "chatcmpl-stream-reason-1",
			[]map[string]any{
				{"role": "assistant", "reasoning_content": "think"},
				{"reasoning_content": "ing..."},
				{"content": "answer"},
			},
			map[string]any{
				"prompt_tokens":     10,
				"completion_tokens": 4,
				"total_tokens":      14,
				"completion_tokens_details": map[string]any{
					"reasoning_tokens": 2,
				},
			},
		)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
		ModelID:         "deepseek-v4-pro",
		UpstreamModel:   "deepseek-v4-pro",
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("why is the sky blue?"),
		},
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	})

	var (
		gotContent   strings.Builder
		gotReasoning strings.Builder
	)
	for stream.Next() {
		chunk := stream.Current()
		if len(chunk.Choices) > 0 {
			delta := chunk.Choices[0].Delta
			gotContent.WriteString(delta.Content)
			// reasoning_content 不在 SDK typed delta 上，从 RawJSON extra 字段拿。
			if raw := delta.JSON.ExtraFields["reasoning_content"].Raw(); raw != "" && raw != "null" {
				var s string
				if err := json.Unmarshal([]byte(raw), &s); err == nil {
					gotReasoning.WriteString(s)
				}
			}
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream error: %v", err)
	}
	if gotContent.String() != "answer" {
		t.Fatalf("expected content 'answer', got %q", gotContent.String())
	}
	if gotReasoning.String() != "thinking..." {
		t.Fatalf("expected reasoning 'thinking...', got %q", gotReasoning.String())
	}
}
