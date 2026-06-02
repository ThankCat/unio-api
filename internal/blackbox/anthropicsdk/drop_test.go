//go:build blackbox

package anthropicsdk_test

import (
	"context"
	"encoding/json"
	"net/http"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/ThankCat/unio-api/internal/blackbox/sdkfixture"
)

// ANT-SDK-Mock-05：DEC-012「协议为先」+ DEC-013「anthropic-beta 宽进出站 Drop」
// 端到端验证。
//
// 客户 SDK 故意：
//   - 透过 WithJSONSet 加入 DeepSeek 不支持的顶层字段（service_tier / mcp_servers / top_k）；
//   - 透过 WithHeader 加入若干 anthropic-beta 值；
//   - 透过 WithJSONSet 加入 output_config.format（adapter 出站 Drop format 但保留 effort）。
//
// 验证：
//   - unio 不返回 400 或 422；SDK 成功收到响应；
//   - mock 上游 request body 不含 service_tier / mcp_servers / top_k；
//   - mock 上游 request body 不含 output_config.format（但 effort 保留）；
//   - mock 上游 request headers 不含 anthropic-beta；
//   - mock 上游 request headers 含 anthropic-version（adapter 强制设置）。
func TestANTSDKMockDropUnsupportedFieldsAndBetaHeader(t *testing.T) {
	var capturedBody []byte
	var capturedHeader http.Header

	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, r *http.Request, body []byte) {
		capturedBody = body
		capturedHeader = r.Header.Clone()
		writeMockMessageResponse(w, "msg_drop_1", "ok", 6, 2)
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
		option.WithHeader("anthropic-beta", "prompt-caching-2024-07-31,fine-grained-tool-streaming-2025-05-14"),
		option.WithJSONSet("service_tier", "priority"),
		option.WithJSONSet("mcp_servers", []any{map[string]any{"name": "x", "url": "https://x"}}),
		option.WithJSONSet("top_k", 5),
		option.WithJSONSet("output_config", map[string]any{
			"effort": "high",
			"format": "json",
		}),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	resp, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 16,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("hi")),
		},
	})
	if err != nil {
		t.Fatalf("unio rejected unsupported fields (expected drop, not reject): %v", err)
	}
	if len(resp.Content) == 0 || resp.Content[0].Text != "ok" {
		t.Fatalf("unexpected response: %+v", resp)
	}

	if capturedBody == nil {
		t.Fatal("mock upstream did not capture body")
	}
	var upstream map[string]any
	if err := json.Unmarshal(capturedBody, &upstream); err != nil {
		t.Fatalf("upstream body not valid json: %v (raw: %s)", err, string(capturedBody))
	}
	for _, dropped := range []string{"service_tier", "mcp_servers", "top_k"} {
		if _, exists := upstream[dropped]; exists {
			t.Fatalf("expected upstream body to NOT contain %q (DEC-012 Drop), got: %v", dropped, upstream[dropped])
		}
	}
	if oc, ok := upstream["output_config"].(map[string]any); ok {
		if _, hasFormat := oc["format"]; hasFormat {
			t.Fatalf("expected output_config.format dropped, got %v", oc["format"])
		}
		if eff, _ := oc["effort"].(string); eff != "high" {
			t.Fatalf("expected output_config.effort preserved, got %v", eff)
		}
	}

	if capturedHeader == nil {
		t.Fatal("mock upstream did not capture headers")
	}
	if got := capturedHeader.Get("anthropic-beta"); got != "" {
		t.Fatalf("expected upstream anthropic-beta dropped (DEC-013), got %q", got)
	}
	if got := capturedHeader.Get("anthropic-version"); got == "" {
		t.Fatalf("expected adapter to set anthropic-version header, got empty")
	}
	if auth := capturedHeader.Get("Authorization"); auth != "" {
		t.Fatalf("expected upstream Authorization to be empty (anthropic uses x-api-key), got %q", auth)
	}
	if xKey := capturedHeader.Get("X-Api-Key"); xKey == "" {
		t.Fatalf("expected upstream x-api-key set by adapter, got empty")
	}
}
