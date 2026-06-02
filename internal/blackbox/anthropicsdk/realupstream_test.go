//go:build blackbox

package anthropicsdk_test

import (
	"context"
	"strings"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/ThankCat/unio-api/internal/blackbox/sdkfixture"
)

// ANT-SDK-Real-01：anthropic-sdk-go SDK 通过 unio gateway 对真实 DeepSeek Anthropic
// endpoint 发起非流式请求。
//
// gate：DEEPSEEK_BLACKBOX=1 + DEEPSEEK_API_KEY（任一缺失 t.Skip）。
//
// 这是 10.14「Anthropic SDK 客户改 base_url + api_key 即用」的端到端硬证据，
// 与 DS-ANT（adapter 层）形成纵深。
func TestANTSDKRealNonStream(t *testing.T) {
	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:       sdkfixture.UpstreamReal,
		Protocol:   "anthropic",
		AdapterKey: "deepseek",
	})

	client := anthropic.NewClient(
		option.WithBaseURL(f.AnthropicBaseURL),
		option.WithAPIKey(f.APIKey),
		option.WithMaxRetries(0),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	// DeepSeek Anthropic endpoint 默认会同时输出 thinking + text，MaxTokens 必须留够
	// 让 final text 能出来；过低会出现 stop_reason=max_tokens 且 content 只有 thinking。
	msg, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 256,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Reply with the single word: ok")),
		},
	})
	if err != nil {
		t.Fatalf("anthropic-sdk-go real-upstream non-stream call failed: %v", err)
	}
	if len(msg.Content) == 0 {
		t.Fatal("expected at least one content block, got 0")
	}
	// 真实场景：DeepSeek 可能输出 thinking 或 text 或两者，我们只断「至少有非空文本」
	// 与 usage 合理；不强制 text block 一定存在。
	var combined strings.Builder
	for _, block := range msg.Content {
		switch block.Type {
		case "text":
			combined.WriteString(block.Text)
		case "thinking":
			combined.WriteString(block.AsThinking().Thinking)
		}
	}
	if combined.Len() == 0 {
		t.Fatalf("expected non-empty text/thinking content, got %+v", msg.Content)
	}
	if msg.Usage.InputTokens <= 0 || msg.Usage.OutputTokens <= 0 {
		t.Fatalf("unexpected usage: %+v", msg.Usage)
	}

	// 顺手验 DB 终态：同步 settlement 应已推进 succeeded。
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

// ANT-SDK-Real-02：anthropic-sdk-go SDK 流式真实 DeepSeek Anthropic endpoint。
//
// 验证 named-event SSE parser、accumulator、settlement 在真实 DeepSeek 上端到端可用。
func TestANTSDKRealStream(t *testing.T) {
	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:       sdkfixture.UpstreamReal,
		Protocol:   "anthropic",
		AdapterKey: "deepseek",
	})

	client := anthropic.NewClient(
		option.WithBaseURL(f.AnthropicBaseURL),
		option.WithAPIKey(f.APIKey),
		option.WithMaxRetries(0),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()

	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 256,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("Reply with the single word: ok")),
		},
	})

	var (
		acc       anthropic.Message
		frames    int
		sawStop   bool
		eventList []string
	)
	for stream.Next() {
		event := stream.Current()
		frames++
		eventList = append(eventList, event.Type)
		if err := acc.Accumulate(event); err != nil {
			t.Fatalf("accumulate event %q: %v", event.Type, err)
		}
		if event.Type == "message_stop" {
			sawStop = true
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("real-upstream stream error (events=%v): %v", eventList, err)
	}
	if frames < 3 {
		t.Fatalf("expected >=3 stream frames, got %d", frames)
	}
	if !sawStop {
		t.Fatalf("expected to see message_stop frame, got events=%v", eventList)
	}
	if len(acc.Content) == 0 {
		t.Fatalf("expected accumulated content blocks, got 0 (events=%v)", eventList)
	}
	if acc.Usage.InputTokens <= 0 || acc.Usage.OutputTokens <= 0 {
		t.Fatalf("expected accumulated usage > 0, got %+v", acc.Usage)
	}
}
