//go:build blackbox

package openaisdk_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	"github.com/openai/openai-go"
	"github.com/openai/openai-go/option"

	"github.com/ThankCat/unio-gateway/internal/blackbox/sdkfixture"
)

// OAI-SDK-Mock-08a：上游 429 rate_limit → unio 429 → openai-go SDK 触发 *openai.Error。
//
// TASK-10.11 / DEC-013 决策：上游 429 必须保持 429 透传给客户（语义不变）。
func TestOAISDKMockUpstreamRateLimitMapsTo429(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		_, _ = io.WriteString(w, `{"error":{"message":"rate limit exceeded","type":"rate_limit_exceeded","code":"rate_limit_exceeded"}}`)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
		option.WithMaxRetries(0), // 避免 SDK 自己重试影响断言
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	requireSDKErrorStatus(t, err, http.StatusTooManyRequests)
}

// OAI-SDK-Mock-08b：上游 400 invalid_request → unio 400 → SDK 触发 *openai.Error。
func TestOAISDKMockUpstreamBadRequestMapsTo400(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request","type":"invalid_request_error","code":"invalid_request_error"}}`)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
		option.WithMaxRetries(0),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	requireSDKErrorStatus(t, err, http.StatusBadRequest)
}

// OAI-SDK-Mock-08c：上游 401（channel 凭据问题）绝不渲染成 401。
//
// TASK-10.11 关键安全语义：upstream auth 是平台 channel 凭据问题，必须映射成 502 api_error。
// 不能让客户误判自己的 unio API key 失效。
func TestOAISDKMockUpstreamAuthMapsTo502NotClient401(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = io.WriteString(w, `{"error":{"message":"invalid api key","type":"authentication_error","code":"invalid_api_key"}}`)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
		option.WithMaxRetries(0),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	requireSDKErrorStatus(t, err, http.StatusBadGateway)

	// 强断言：返回 body 的 OpenAI error.type 不能是 authentication_error / invalid_request_error
	// （它必须是 api_error，证明 unio 没把上游 401 透传成客户 401）。
	requireOpenAIErrorType(t, err, "api_error")
}

// OAI-SDK-Mock-08d：上游 500 → unio 502 → SDK *openai.Error(502)。
func TestOAISDKMockUpstreamServerErrorMapsTo502(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, _ *http.Request, _ []byte) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = io.WriteString(w, `{"error":{"message":"internal error","type":"server_error","code":"server_error"}}`)
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:            sdkfixture.UpstreamMock,
		UpstreamBaseURL: mock.URL + "/v1",
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
		option.WithMaxRetries(0),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	requireSDKErrorStatus(t, err, http.StatusBadGateway)
}

// OAI-SDK-Mock-08e：上游 hang 至 channel timeout → unio 504。
func TestOAISDKMockUpstreamTimeoutMapsTo504(t *testing.T) {
	mock := newMockUpstream(t, func(_ *testing.T, w http.ResponseWriter, r *http.Request, _ []byte) {
		select {
		case <-r.Context().Done():
			// adapter 端已主动 cancel，无需返回
		case <-time.After(5 * time.Second):
			// 防卡死：5s 后还是写一个错误响应
			w.WriteHeader(http.StatusInternalServerError)
		}
	})
	t.Cleanup(mock.Close)

	f := sdkfixture.Setup(t, sdkfixture.SetupOptions{
		Mode:             sdkfixture.UpstreamMock,
		UpstreamBaseURL:  mock.URL + "/v1",
		ChannelTimeoutMS: 500, // 500ms 强制 channel 超时
	})

	client := openai.NewClient(
		option.WithBaseURL(f.BaseURL),
		option.WithAPIKey(f.APIKey),
		option.WithMaxRetries(0),
	)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()

	_, err := client.Chat.Completions.New(ctx, openai.ChatCompletionNewParams{
		Model:    openai.ChatModel(f.ModelID),
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage("hi")},
	})
	requireSDKErrorStatus(t, err, http.StatusGatewayTimeout)
}

// ---- helpers ----

// requireSDKErrorStatus 断言 openai-go SDK 返回的错误是 *openai.Error 且 status 匹配。
func requireSDKErrorStatus(t *testing.T, err error, wantStatus int) {
	t.Helper()
	if err == nil {
		t.Fatalf("expected error with status %d, got nil", wantStatus)
	}
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *openai.Error, got %T: %v", err, err)
	}
	if apiErr.StatusCode != wantStatus {
		t.Fatalf("expected status %d, got %d (raw: %s)", wantStatus, apiErr.StatusCode, apiErr.RawJSON())
	}
}

// requireOpenAIErrorType 断言返回 body 的 OpenAI error.type 等于 wantType。
//
// openai-go *openai.Error.RawJSON() 返回的是 error wrapper 内层（已剥离 {"error":{...}}），
// 因此结构体直接平铺。
func requireOpenAIErrorType(t *testing.T, err error, wantType string) {
	t.Helper()
	var apiErr *openai.Error
	if !errors.As(err, &apiErr) {
		t.Fatalf("expected *openai.Error, got %T", err)
	}
	var body struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal([]byte(apiErr.RawJSON()), &body); err != nil {
		t.Fatalf("parse error body: %v (raw: %s)", err, apiErr.RawJSON())
	}
	if body.Type != wantType {
		t.Fatalf("expected error.type %q, got %q (raw: %s)", wantType, body.Type, apiErr.RawJSON())
	}
}
