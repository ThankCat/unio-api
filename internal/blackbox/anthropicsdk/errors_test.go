//go:build blackbox

package anthropicsdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"testing"
	"time"

	anthropic "github.com/anthropics/anthropic-sdk-go"
	"github.com/anthropics/anthropic-sdk-go/option"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// writeUpstreamError 写一个上游错误响应。
// DeepSeek 的 Anthropic endpoint 错误体是 OpenAI 风格信封，但 adapter 只按 HTTP status 分类，
// gatewayapi/anthropic 再渲染原生 Anthropic error shape，所以 mock 端写什么 body 都不影响
// 客户视角的 type/status 映射。这里写 OpenAI 风格保持真实性。
func writeUpstreamError(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	body, _ := json.Marshal(map[string]any{
		"error": map[string]any{
			"message": message,
			"type":    "upstream_error",
		},
	})
	_, _ = w.Write(body)
}

// ANT-SDK-Mock-06a：上游 429 → 客户看到 429 + type=rate_limit_error。
func TestANTSDKMockUpstream429MapsToClient429RateLimit(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeUpstreamError(w, http.StatusTooManyRequests, "upstream rate limited")
	})
	t.Cleanup(mock.Close)

	requireAnthropicError(t, mock, http.StatusTooManyRequests, "rate_limit_error")
}

// ANT-SDK-Mock-06b：上游 400 → 客户看到 400 + type=invalid_request_error。
func TestANTSDKMockUpstream400MapsToClient400InvalidRequest(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeUpstreamError(w, http.StatusBadRequest, "invalid prompt")
	})
	t.Cleanup(mock.Close)

	requireAnthropicError(t, mock, http.StatusBadRequest, "invalid_request_error")
}

// ANT-SDK-Mock-06c：上游 401（provider credential issue）→ 客户看到 502 + type=api_error。
// 关键不变量：上游凭证问题绝不能让客户看到 401 而误以为「我的 unio api key 错了」。
// 这是 TASK-10.11（安全输出）+ adapter.UpstreamErrorCategoryAuth 的核心契约。
func TestANTSDKMockUpstream401MapsToClient502NotClient401(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeUpstreamError(w, http.StatusUnauthorized, "Authentication Fails, Your api key: ****abc is invalid")
	})
	t.Cleanup(mock.Close)

	apiErr := requireAnthropicError(t, mock, http.StatusBadGateway, "api_error")

	// 上游原始 message 绝不能透传给客户。
	if got := apiErr.RawJSON(); contains(got, "Your api key") || contains(got, "****abc") {
		t.Fatalf("upstream credential leaked into client error: %s", got)
	}
}

// ANT-SDK-Mock-06d：上游 500 → 客户看到 502 + type=api_error。
func TestANTSDKMockUpstream500MapsToClient502APIError(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		writeUpstreamError(w, http.StatusInternalServerError, "upstream went boom")
	})
	t.Cleanup(mock.Close)

	requireAnthropicError(t, mock, http.StatusBadGateway, "api_error")
}

// ANT-SDK-Mock-06e：上游超时 → 客户看到 504 + type=api_error。
//
// 通过 ChannelTimeoutMS 把 channel 出站超时设为 100ms，上游 mock 阻塞 1s，
// 触发 adapter 上下文超时 → lifecycle 映射到 UpstreamErrorCategoryTimeout
// → gatewayapi/anthropic 渲染 504 + api_error。
func TestANTSDKMockUpstreamTimeoutMapsToClient504(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, r *http.Request, _ []byte) {
		select {
		case <-r.Context().Done():
		case <-time.After(1 * time.Second):
		}
		writeMockMessageResponse(w, "msg_should_not_reach", "should not arrive", 1, 1)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:             sdkfixture.UpstreamMock,
		Protocol:         "anthropic",
		AdapterKey:       "deepseek",
		UpstreamBaseURL:  mock.URL,
		ChannelTimeoutMS: 100,
	})

	client := anthropic.NewClient(
		option.WithBaseURL(f.AnthropicBaseURL),
		option.WithAPIKey(f.APIKey),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 4,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("hi")),
		},
	})
	if err == nil {
		t.Fatal("expected timeout error, got nil")
	}
	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *anthropic.Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != http.StatusGatewayTimeout {
		t.Fatalf("status = %d, want 504", apiErr.StatusCode)
	}
	if got := string(apiErr.Type()); got != "api_error" {
		t.Fatalf("type = %q, want api_error", got)
	}
}

// requireAnthropicError 跑一次最简调用，断言客户拿到的 SDK 错误满足 status + type。
// 返回 apiErr，让单测可在其基础上做更深的断言（如校验 message 不泄漏）。
func requireAnthropicError(t *testing.T, mock *mockUpstream, expectStatus int, expectType string) *anthropic.Error {
	t.Helper()

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

	_, err := client.Messages.New(ctx, anthropic.MessageNewParams{
		Model:     anthropic.Model(f.ModelID),
		MaxTokens: 4,
		Messages: []anthropic.MessageParam{
			anthropic.NewUserMessage(anthropic.NewTextBlock("ping")),
		},
	})
	if err == nil {
		t.Fatal("expected SDK error, got nil")
	}

	var apiErr *anthropic.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *anthropic.Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != expectStatus {
		t.Fatalf("status = %d, want %d (body: %s)", apiErr.StatusCode, expectStatus, apiErr.RawJSON())
	}
	if got := string(apiErr.Type()); got != expectType {
		t.Fatalf("type = %q, want %q (body: %s)", got, expectType, apiErr.RawJSON())
	}
	return apiErr
}

func contains(haystack, needle string) bool {
	if needle == "" {
		return true
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}
