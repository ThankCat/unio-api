//go:build blackbox

package anthropicsdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/ThankCat/unio-api/internal/blackbox/sdkfixture"
)

// ANT-SDK-Mock-02：anthropic-sdk-go SDK 流式 messages 完整序成功。
//
// 验证：
//   - SDK 不报错；
//   - 累加后的 message.Content[0].Text 等于所有 content_block_delta 文本拼接；
//   - 累加后的 usage（input_tokens / output_tokens）来自 message_start + message_delta；
//   - 累加后的 stop_reason = end_turn；
//   - upstream mock 收到的 request body 包含 stream=true。
func TestANTSDKMockStreamSucceeds(t *testing.T) {
	var capturedBody []byte
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, body []byte) {
		capturedBody = body
		writeMockMessageStream(w, "msg_mock_stream_1",
			[]string{"hello", ", ", "world"},
			11, // input_tokens
			5,  // output_tokens
		)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		Protocol:        "anthropic",
		AdapterKey:      "deepseek",
		UpstreamBaseURL: mock.URL,
	})

	client := anthropic.NewClient(
		option.WithBaseURL(f.AnthropicBaseURL),
		option.WithAPIKey(f.APIKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	stream := client.Messages.NewStreaming(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 16,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("hi")),
		},
	})

	var acc anthropic.Message
	var eventTypes []string
	for stream.Next() {
		event := stream.Current()
		eventTypes = append(eventTypes, event.Type)
		if err := acc.Accumulate(event); err != nil {
			t.Fatalf("accumulate event %q: %v", event.Type, err)
		}
	}
	if err := stream.Err(); err != nil {
		t.Fatalf("stream err: %v", err)
	}

	if !containsAll(eventTypes, []string{
		"message_start",
		"content_block_start",
		"content_block_delta",
		"content_block_stop",
		"message_delta",
		"message_stop",
	}) {
		t.Fatalf("missing expected event types in stream; got: %v", eventTypes)
	}

	if len(acc.Content) == 0 {
		t.Fatal("expected at least one accumulated content block")
	}
	if got := acc.Content[0].Text; got != "hello, world" {
		t.Fatalf("accumulated text = %q, want %q", got, "hello, world")
	}
	if string(acc.StopReason) != "end_turn" {
		t.Fatalf("stop_reason = %q, want end_turn", acc.StopReason)
	}
	if acc.Usage.InputTokens != 11 || acc.Usage.OutputTokens != 5 {
		t.Fatalf("usage = %+v, want input=11 output=5", acc.Usage)
	}

	var upstreamBody map[string]any
	if err := json.Unmarshal(capturedBody, &upstreamBody); err != nil {
		t.Fatalf("upstream body not valid json: %v", err)
	}
	if stream, _ := upstreamBody["stream"].(bool); !stream {
		t.Fatalf("expected upstream stream=true, got %v", upstreamBody["stream"])
	}
}

func containsAll(haystack []string, needles []string) bool {
	for _, n := range needles {
		found := false
		for _, h := range haystack {
			if strings.EqualFold(h, n) {
				found = true
				break
			}
		}
		if !found {
			return false
		}
	}
	return true
}
