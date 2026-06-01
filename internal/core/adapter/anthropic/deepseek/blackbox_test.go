package deepseek

import (
	"context"
	"encoding/json"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	anthropicadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/core/usage"
)

// newBlackboxAdapter 在显式开启黑盒回归时构造真实 DeepSeek Anthropic adapter 与 channel runtime。
//
// 为避免普通 go test 误触发计费请求，要求 DEEPSEEK_BLACKBOX=1 且 DEEPSEEK_API_KEY 提供可用 key，
// 任一缺失即 skip。
func newBlackboxAdapter(t *testing.T) (*Adapter, channel.Runtime) {
	t.Helper()

	if os.Getenv("DEEPSEEK_BLACKBOX") != "1" {
		t.Skip("DEEPSEEK_BLACKBOX is not set to 1")
	}
	apiKey := os.Getenv("DEEPSEEK_API_KEY")
	if apiKey == "" {
		t.Skip("DEEPSEEK_API_KEY is not set")
	}

	a := NewAdapter(&http.Client{Timeout: 30 * time.Second})
	runtime := channel.Runtime{
		BaseURL:      "https://api.deepseek.com/anthropic",
		APIKey:       apiKey,
		Timeout:      30 * time.Second,
		ProviderSlug: "deepseek",
	}

	return a, runtime
}

func intPtr(v int) *int { return &v }

// DS-ANT-01：基础非流式 text，校验响应内容、usage 与 ResponseFacts。
func TestDeepSeekAnthropicBlackboxNonStream(t *testing.T) {
	a, runtime := newBlackboxAdapter(t)

	resp, err := a.Messages(context.Background(), runtime, anthropicadapter.MessageRequest{
		Model:     "deepseek-chat",
		MaxTokens: intPtr(16),
		Messages: []anthropicadapter.Message{
			{Role: "user", Content: json.RawMessage(`"Reply with the single word: ok"`)},
		},
	})
	if err != nil {
		t.Fatalf("Messages: %v", err)
	}

	if len(resp.Content) == 0 {
		t.Fatal("expected non-empty content")
	}
	if resp.Usage.InputTokens <= 0 || resp.Usage.OutputTokens <= 0 {
		t.Fatalf("unexpected usage: %+v", resp.Usage)
	}
	if resp.Facts.UpstreamProtocol != "anthropic" {
		t.Fatalf("facts protocol = %q", resp.Facts.UpstreamProtocol)
	}
	if resp.Facts.UsageSource != usage.SourceUpstreamResponse {
		t.Fatalf("facts source = %q", resp.Facts.UsageSource)
	}
	if got, ok := resp.Facts.Usage.OutputTokensTotal.BillableValue(); !ok || got != int64(resp.Usage.OutputTokens) {
		t.Fatalf("facts output = %d ok=%v vs usage %d", got, ok, resp.Usage.OutputTokens)
	}
}

// DS-ANT-02：基础流式 text 事件顺序，校验 message_stop 被截留且返回终态 facts。
func TestDeepSeekAnthropicBlackboxStream(t *testing.T) {
	a, runtime := newBlackboxAdapter(t)

	var types []string
	var finalUsage *anthropicadapter.MessageUsage
	outcome, err := a.StreamMessages(context.Background(), runtime, anthropicadapter.MessageRequest{
		Model:     "deepseek-chat",
		MaxTokens: intPtr(32),
		Messages: []anthropicadapter.Message{
			{Role: "user", Content: json.RawMessage(`"Count one two three"`)},
		},
	}, func(ev anthropicadapter.MessageStreamEvent) error {
		types = append(types, ev.Type)
		if ev.Usage != nil {
			finalUsage = ev.Usage
		}
		return nil
	})
	if err != nil {
		t.Fatalf("StreamMessages: %v", err)
	}

	if len(types) == 0 || types[0] != "message_start" {
		t.Fatalf("first event = %v", types)
	}
	if types[len(types)-1] != "message_delta" {
		t.Fatalf("last emitted event = %q, want message_delta", types[len(types)-1])
	}
	if finalUsage == nil || finalUsage.OutputTokens <= 0 {
		t.Fatalf("missing/empty final usage: %+v", finalUsage)
	}
	if outcome.Facts == nil || outcome.Facts.UsageSource != usage.SourceUpstreamStream {
		t.Fatalf("unexpected stream outcome facts: %+v", outcome.Facts)
	}
}

// DS-ANT-09：image content block 必须前置 Reject（DeepSeek 静默忽略图片，绝不能透传）。
func TestDeepSeekAnthropicBlackboxImageRejectedPreflight(t *testing.T) {
	a, runtime := newBlackboxAdapter(t)

	_, err := a.Messages(context.Background(), runtime, anthropicadapter.MessageRequest{
		Model:     "deepseek-chat",
		MaxTokens: intPtr(16),
		Messages: []anthropicadapter.Message{
			{Role: "user", Content: json.RawMessage(`[{"type":"image","source":{"type":"base64","media_type":"image/png","data":"iVBORw0KGgo="}}]`)},
		},
	})
	if err == nil {
		t.Fatal("expected image content to be rejected before upstream")
	}
	if _, ok := adapter.UpstreamCategoryOf(err); ok {
		t.Fatal("image reject must be a local pre-flight reject, not an upstream error")
	}
}
