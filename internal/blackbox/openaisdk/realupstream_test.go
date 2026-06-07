//go:build blackbox

package openaisdk_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ThankCat/unio-api/internal/blackbox/sdkfixture"
)

// OAI-SDK-Real-01：openai-go SDK 通过 unio gateway 对真实 DeepSeek 上游发起非流式请求。
//
// gate：DEEPSEEK_BLACKBOX=1 + DEEPSEEK_API_KEY；任一缺失 t.Skip。
//
// 这是 10.13「客户改 base_url + api_key 即用」的端到端硬证据。
// 同时也是 DS-OAI（adapter 层）之外、从 SDK 视角的回归。
func TestOAISDKRealNonStream(t *testing.T) {
	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:          sdkfixture.UpstreamReal,
		ModelID:       "deepseek-v4-flash",
		UpstreamModel: "deepseek-v4-flash",
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
		option.WithMaxRetries(0),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	resp, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Reply with the single word: ok"),
		},
		MaxTokens: openai.Int(8),
	})
	if err != nil {
		t.Fatalf("openai-go real-upstream non-stream call failed: %v", err)
	}
	if len(resp.Choices) == 0 || resp.Choices[0].Message.Content == "" {
		t.Fatalf("expected non-empty content, got %+v", resp.Choices)
	}
	if resp.Usage.PromptTokens <= 0 || resp.Usage.CompletionTokens <= 0 || resp.Usage.TotalTokens <= 0 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}

	// 顺手验 DB 终态：同步 settlement 应已推进。
	time.Sleep(200 * time.Millisecond)
	dbCtx, dbCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer dbCancel()
	var rrStatus string
	if err := f.Pool.QueryRow(dbCtx, `
		SELECT status FROM request_records WHERE user_id = $1 ORDER BY id DESC LIMIT 1
	`, f.UserID).Scan(&rrStatus); err != nil {
		t.Fatalf("query final status: %v", err)
	}
	if rrStatus != "succeeded" {
		t.Errorf("expected request_records.status=succeeded, got %q", rrStatus)
	}
}

// OAI-SDK-Real-02：openai-go SDK 流式 + stream_options.include_usage 真实上游。
//
// 验证 stream parser、tail usage、settlement 在真实 DeepSeek 上端到端可用。
func TestOAISDKRealStream(t *testing.T) {
	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:          sdkfixture.UpstreamReal,
		ModelID:       "deepseek-v4-flash",
		UpstreamModel: "deepseek-v4-flash",
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
		option.WithMaxRetries(0),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stream := client.Chat.Completions.NewStreaming(ctx, openai.ChatCompletionNewParams{
		Model: openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{
			openai.UserMessage("Reply with the single word: ok"),
		},
		MaxTokens: openai.Int(8),
		StreamOptions: openai.ChatCompletionStreamOptionsParam{
			IncludeUsage: openai.Bool(true),
		},
	})

	var (
		gotContent strings.Builder
		gotUsage   bool
		frames     int
	)
	for stream.Next() {
		chunk := stream.Current()
		frames++
		if len(chunk.Choices) > 0 {
			gotContent.WriteString(chunk.Choices[0].Delta.Content)
		}
		if chunk.Usage.TotalTokens > 0 {
			gotUsage = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("real-upstream stream error: %v", err)
	}
	if frames < 2 {
		t.Fatalf("expected >=2 stream frames, got %d", frames)
	}
	if gotContent.Len() == 0 {
		t.Fatalf("expected non-empty streamed content")
	}
	if !gotUsage {
		t.Fatal("expected tail usage chunk when include_usage=true")
	}
}
