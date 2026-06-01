package gatewayapi

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// fakeAPIKeyAuthenticator 是 chat completions 测试使用的 API Key 认证器。
type fakeAPIKeyAuthenticator struct {
	principal *auth.APIKeyPrincipal
	err       error
	token     string
}

// AuthenticateAPIKey 记录收到的 token，并返回测试预设的认证结果。
func (a *fakeAPIKeyAuthenticator) AuthenticateAPIKey(ctx context.Context, plaintext string) (*auth.APIKeyPrincipal, error) {
	a.token = plaintext
	return a.principal, a.err
}

func TestRouterV1ChatCompletionWithMissingAPIKey(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := newTestRouter(authenticator, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("expected status %d, got %d", http.StatusUnauthorized, rec.Code)
	}
}

func TestRouterV1ChatCompletionWithAPIKey(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := newTestRouter(authenticator, nil, nil)

	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
	}

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	var body ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Object != "chat.completion" {
		t.Fatalf("expected object %q, got %q", "chat.completion", body.Object)
	}

	if body.Model != reqBody.Model {
		t.Fatalf("expected model %q, got %q", reqBody.Model, body.Model)
	}

	if len(body.Choices) != 1 {
		t.Fatalf("expected 1 choice, got %d", len(body.Choices))
	}
}

func TestRouterV1ChatCompletionWithInvalidBody(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := newTestRouter(authenticator, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader("{"))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", body.Error.Code)
	}

	if body.Error.Message != "invalid json body" {
		t.Fatalf("expected message %q, got %q", "invalid json body", body.Error.Message)
	}
}

func TestRouterV1ChatCompletionWithUnsupportedContentType(t *testing.T) {
	assertChatCompletionDecodeError(
		t,
		`{"model":"openai/gpt-4.1","messages":[{"role":"user","content":"Hello"}]}`,
		"text/plain",
		http.StatusUnsupportedMediaType,
		"content type must be application/json",
	)
}

func TestRouterV1ChatCompletionWithEmptyBody(t *testing.T) {
	assertChatCompletionDecodeError(
		t,
		"",
		httpx.ContentTypeJSON,
		http.StatusBadRequest,
		"request body is required",
	)
}

func TestRouterV1ChatCompletionWithTrailingJSONToken(t *testing.T) {
	assertChatCompletionDecodeError(
		t,
		`{"model":"openai/gpt-4.1","messages":[{"role":"user","content":"Hello"}]} {"extra":true}`,
		httpx.ContentTypeJSON,
		http.StatusBadRequest,
		"request body must contain a single JSON object",
	)
}

func TestRouterV1ChatCompletionWithTooLargeBody(t *testing.T) {
	largeContent := strings.Repeat("a", int(httpx.DefaultMaxJSONBodyBytes)+1)
	body := `{"model":"openai/gpt-4.1","messages":[{"role":"user","content":"` + largeContent + `"}]}`

	assertChatCompletionDecodeError(
		t,
		body,
		httpx.ContentTypeJSON,
		http.StatusRequestEntityTooLarge,
		"request body too large",
	)
}

func assertChatCompletionDecodeError(t *testing.T, reqBody string, contentType string, wantStatus int, wantMessage string) {
	t.Helper()

	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	service := &fakeChatCompletionService{}
	handler := newTestRouter(authenticator, service, nil)

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(reqBody))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != wantStatus {
		t.Fatalf("expected status %d, got %d", wantStatus, rec.Code)
	}

	if service.createCalled {
		t.Fatal("expected chat completion service not to be called")
	}

	if service.streamCalled {
		t.Fatal("expected stream chat completion service not to be called")
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", body.Error.Code)
	}

	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("expected error type %q, got %q", "invalid_request_error", body.Error.Type)
	}

	if body.Error.Message != wantMessage {
		t.Fatalf("expected message %q, got %q", wantMessage, body.Error.Message)
	}
}

func TestRouterV1ChatCompletionWithMissingModel(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := newTestRouter(authenticator, nil, nil)

	reqBody := ChatCompletionRequest{}

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", body.Error.Code)
	}

	if body.Error.Message != "model is required" {
		t.Fatalf("expected message %q, got %q", "model is required", body.Error.Message)
	}
}

func TestRouterV1ChatCompletionWithMissingMessages(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	handler := newTestRouter(authenticator, nil, nil)

	reqBody := ChatCompletionRequest{
		Model:    "openai/gpt-4.1",
		Messages: []ChatMessage{},
	}

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", body.Error.Code)
	}

	if body.Error.Message != "messages is required" {
		t.Fatalf("expected message %q, got %q", "messages is required", body.Error.Message)
	}
}

// fakeChatCompletionService 是 chat completions 测试使用的 service 替身。
type fakeChatCompletionService struct {
	createCalled       bool
	streamCalled       bool
	req                ChatCompletionRequest
	createResp         *ChatCompletionResponse
	streamResp         []ChatCompletionStreamResponse
	err                error
	streamErrAfterEmit error
}

// CreateChatCompletion 记录 handler 传入的请求，并返回测试预设的响应。
func (s *fakeChatCompletionService) CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error) {
	s.createCalled = true
	s.req = req
	return s.createResp, s.err
}

// StreamChatCompletion 记录 handler 传入的流式请求，并逐个发出测试预设响应。
func (s *fakeChatCompletionService) StreamChatCompletion(ctx context.Context, req ChatCompletionRequest, emit func(ChatCompletionStreamResponse) error) error {
	s.streamCalled = true
	s.req = req

	if s.err != nil {
		return s.err
	}

	for _, chunk := range s.streamResp {
		if err := emit(chunk); err != nil {
			return err
		}
	}

	return s.streamErrAfterEmit
}

func TestRouterV1ChatCompletionCallsService(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}

	service := &fakeChatCompletionService{
		createResp: &ChatCompletionResponse{
			Object: "chat.completion",
			Model:  "openai/gpt-4.1",
			Choices: []ChatCompletionChoice{
				{
					Index: 0,
					Message: ChatMessage{
						Role:    "assistant",
						Content: jsonContent("mock response"),
					},
					FinishReason: "stop",
				},
			},
		},
	}

	handler := newTestRouter(authenticator, service, nil)

	buf := new(bytes.Buffer)
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
	}
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if !service.createCalled {
		t.Fatal("expected chat completion service to be called")
	}

	if service.req.Model != reqBody.Model {
		t.Fatalf("expected service model %q, got %q", reqBody.Model, service.req.Model)
	}

	if len(service.req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(service.req.Messages))
	}

	var recBody ChatCompletionResponse
	if err := json.NewDecoder(rec.Body).Decode(&recBody); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if recBody.Model != service.createResp.Model {
		t.Fatalf("expected response model %q, got %q", service.createResp.Model, recBody.Model)
	}
}

func TestRouterV1ChatCompletionMapsRoutingErrors(t *testing.T) {
	cases := []struct {
		name        string
		err         error
		wantStatus  int
		wantCode    string
		wantType    string
		wantMessage string
		wantParam   string
	}{
		{
			name:        "model not found",
			err:         routing.ErrModelNotFound,
			wantStatus:  http.StatusNotFound,
			wantCode:    "model_not_found",
			wantType:    "invalid_request_error",
			wantMessage: "The model \"openai/gpt-4.1\" does not exist or you do not have access to it.",
			wantParam:   "model",
		},
		{
			name:        "model not available",
			err:         routing.ErrModelNotAvailable,
			wantStatus:  http.StatusNotFound,
			wantCode:    "model_not_found",
			wantType:    "invalid_request_error",
			wantMessage: "The model \"openai/gpt-4.1\" does not exist or you do not have access to it.",
			wantParam:   "model",
		},
		{
			name:        "no available channel",
			err:         routing.ErrNoAvailableChannel,
			wantStatus:  http.StatusServiceUnavailable,
			wantCode:    "model_unavailable",
			wantType:    "api_error",
			wantMessage: "The model \"openai/gpt-4.1\" is temporarily unavailable.",
			wantParam:   "model",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			authenticator := &fakeAPIKeyAuthenticator{
				principal: &auth.APIKeyPrincipal{
					APIKeyID:  1,
					ProjectID: 1,
					KeyPrefix: "unio_sk_test",
				},
			}
			service := &fakeChatCompletionService{err: tc.err}
			handler := newTestRouter(authenticator, service, nil)

			buf := new(bytes.Buffer)
			reqBody := ChatCompletionRequest{
				Model: "openai/gpt-4.1",
				Messages: []ChatMessage{
					{Role: "user", Content: jsonContent("Hello")},
				},
			}
			if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
				t.Fatalf("encode request body: %v", err)
			}

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
			req.Header.Set("Authorization", "Bearer unio_sk_test")
			rec := httptest.NewRecorder()

			handler.ServeHTTP(rec, req)

			if rec.Code != tc.wantStatus {
				t.Fatalf("expected status %d, got %d", tc.wantStatus, rec.Code)
			}

			var body httpx.ErrorResponse
			if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
				t.Fatalf("decode response body: %v", err)
			}

			if body.Error.Code != tc.wantCode {
				t.Fatalf("expected error code %q, got %q", tc.wantCode, body.Error.Code)
			}
			if body.Error.Type != tc.wantType {
				t.Fatalf("expected error type %q, got %q", tc.wantType, body.Error.Type)
			}
			if body.Error.Message != tc.wantMessage {
				t.Fatalf("expected error message %q, got %q", tc.wantMessage, body.Error.Message)
			}
			if body.Error.Param == nil || *body.Error.Param != tc.wantParam {
				t.Fatalf("expected error param %q, got %#v", tc.wantParam, body.Error.Param)
			}
		})
	}
}

func TestRouterV1ChatCompletionMapsInsufficientQuota(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	service := &fakeChatCompletionService{
		err: failure.New(failure.CodeLedgerInsufficientBalance),
	}
	handler := newTestRouter(authenticator, service, nil)

	buf := new(bytes.Buffer)
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
	}
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status %d, got %d", http.StatusTooManyRequests, rec.Code)
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if body.Error.Code != "insufficient_quota" {
		t.Fatalf("expected error code %q, got %q", "insufficient_quota", body.Error.Code)
	}
	if body.Error.Type != "insufficient_quota" {
		t.Fatalf("expected error type %q, got %q", "insufficient_quota", body.Error.Type)
	}
	if body.Error.Message != "You exceeded your current quota. Please check your balance or billing details." {
		t.Fatalf("unexpected error message %q", body.Error.Message)
	}
	if body.Error.Param != nil {
		t.Fatalf("expected nil error param, got %#v", body.Error.Param)
	}
}

func TestRouterV1ChatCompletionPreservesExplicitZeroTemperature(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	service := &fakeChatCompletionService{
		createResp: &ChatCompletionResponse{
			Object: "chat.completion",
			Model:  "openai/gpt-4.1",
			Choices: []ChatCompletionChoice{
				{
					Index: 0,
					Message: ChatMessage{
						Role:    "assistant",
						Content: jsonContent("mock response"),
					},
					FinishReason: "stop",
				},
			},
		},
	}
	handler := newTestRouter(authenticator, service, nil)

	zero := 0.0
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		Temperature: &zero,
	}

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if !service.createCalled {
		t.Fatal("expected chat completion service to be called")
	}

	if service.req.Temperature == nil {
		t.Fatal("expected temperature to be preserved")
	}

	if *service.req.Temperature != 0 {
		t.Fatalf("expected temperature 0, got %v", *service.req.Temperature)
	}
}

func TestRouterV1ChatCompletionWithInvalidTemperature(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		Temperature: float64Ptr(2.1),
	}

	assertChatCompletionInvalidRequest(t, reqBody, "temperature must be between 0 and 2", "temperature")
}

func TestRouterV1ChatCompletionWithInvalidTopP(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		TopP: float64Ptr(1.1),
	}

	assertChatCompletionInvalidRequest(t, reqBody, "top_p must be between 0 and 1", "top_p")
}

func TestRouterV1ChatCompletionWithInvalidMaxTokens(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		MaxTokens: intPtr(0),
	}

	assertChatCompletionInvalidRequest(t, reqBody, "max_tokens must be greater than 0", "max_tokens")
}

func TestRouterV1ChatCompletionWithWhitespaceModel(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "   ",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
	}

	assertChatCompletionInvalidRequest(t, reqBody, "model is required", "model")
}

func TestRouterV1ChatCompletionWithMissingMessageRole(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Content: jsonContent("Hello")},
		},
	}

	assertChatCompletionInvalidRequest(t, reqBody, "message role is required", "messages.0.role")
}

func TestRouterV1ChatCompletionWithUnsupportedMessageRole(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "function", Content: jsonContent("Hello")},
		},
	}

	assertChatCompletionInvalidRequest(t, reqBody, "message role must be one of system, user, assistant, developer, tool", "messages.0.role")
}

func TestRouterV1ChatCompletionWithToolRoleRequiresToolCallID(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "tool", Content: jsonContent("result")},
		},
	}

	assertChatCompletionInvalidRequest(t, reqBody, "tool message requires tool_call_id", "messages.0.tool_call_id")
}

func TestRouterV1ChatCompletionWithEmptyMessageContent(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("   ")},
		},
	}

	assertChatCompletionInvalidRequest(t, reqBody, "message content is required", "messages.0.content")
}

func TestRouterV1ChatCompletionWithInvalidPresencePenalty(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		PresencePenalty: float64Ptr(2.1),
	}

	assertChatCompletionInvalidRequest(t, reqBody, "presence_penalty must be between -2 and 2", "presence_penalty")
}

func TestRouterV1ChatCompletionWithInvalidFrequencyPenalty(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		FrequencyPenalty: float64Ptr(-2.1),
	}

	assertChatCompletionInvalidRequest(t, reqBody, "frequency_penalty must be between -2 and 2", "frequency_penalty")
}

func TestRouterV1ChatCompletionWithTooManyStopSequences(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		Stop: []string{"a", "b", "c", "d", "e"},
	}

	assertChatCompletionInvalidRequest(t, reqBody, "stop must contain at most 4 sequences", "stop")
}

func TestRouterV1ChatCompletionWithEmptyStopSequence(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		Stop: []string{"END", "   "},
	}

	assertChatCompletionInvalidRequest(t, reqBody, "stop sequence must not be empty", "stop.1")
}

func TestRouterV1ChatCompletionWithEmptyUser(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		User: stringPtr("   "),
	}

	assertChatCompletionInvalidRequest(t, reqBody, "user must not be empty", "user")
}

func TestRouterV1ChatCompletionWithTooLongUser(t *testing.T) {
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		User: stringPtr(strings.Repeat("a", maxUserLength+1)),
	}

	assertChatCompletionInvalidRequest(t, reqBody, "user must be at most 512 characters", "user")
}

func assertChatCompletionInvalidRequest(t *testing.T, reqBody ChatCompletionRequest, wantMessage string, wantParam string) {
	t.Helper()

	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	service := &fakeChatCompletionService{}
	handler := newTestRouter(authenticator, service, nil)

	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}

	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	if service.createCalled {
		t.Fatal("expected chat completion service not to be called")
	}

	if service.streamCalled {
		t.Fatal("expected stream chat completion service not to be called")
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", body.Error.Code)
	}

	if body.Error.Message != wantMessage {
		t.Fatalf("expected message %q, got %q", wantMessage, body.Error.Message)
	}

	if body.Error.Type != "invalid_request_error" {
		t.Fatalf("expected error type %q, got %q", "invalid_request_error", body.Error.Type)
	}

	if body.Error.Param == nil {
		t.Fatal("expected error param to be set")
	}

	if *body.Error.Param != wantParam {
		t.Fatalf("expected error param %q, got %q", wantParam, *body.Error.Param)
	}
}

func float64Ptr(v float64) *float64 {
	return &v
}

func intPtr(v int) *int {
	return &v
}

func TestChatCompletionMissingModelReturnsOpenAIError(t *testing.T) {
	// 构造带认证的 router。
	authenticator := &fakeAPIKeyAuthenticator{principal: &auth.APIKeyPrincipal{
		APIKeyID:  1,
		ProjectID: 1,
		KeyPrefix: "unio_sk_test",
	}}
	router := newTestRouter(authenticator, nil, nil)

	// 发送缺少 model 的请求。
	reqBody := ChatCompletionRequest{
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
	}
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// 解析错误响应。
	var recBody httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&recBody); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	// 断言 HTTP status 是 400。
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	// 断言 error.code 是 invalid_request。
	if recBody.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", recBody.Error.Code)
	}

	// 断言 error.type 是 invalid_request_error。
	if recBody.Error.Type != "invalid_request_error" {
		t.Fatalf("expected error type %q, got %q", "invalid_request_error", recBody.Error.Type)
	}

	// 断言 error.param 是 model。
	if recBody.Error.Param == nil {
		t.Fatal("expected error param to be set")
	}

	if *recBody.Error.Param != "model" {
		t.Fatalf("expected parameter %q, got %q", "model", *recBody.Error.Param)
	}
}

func TestChatCompletionMissingMessagesReturnsOpenAIError(t *testing.T) {
	// 构造带认证的 router。
	authenticator := &fakeAPIKeyAuthenticator{principal: &auth.APIKeyPrincipal{
		APIKeyID:  1,
		ProjectID: 1,
		KeyPrefix: "unio_sk_test",
	}}
	router := newTestRouter(authenticator, nil, nil)

	// 发送缺少 messages 的请求。
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
	}
	buf := new(bytes.Buffer)
	if err := json.NewEncoder(buf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", buf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// 解析错误响应。
	var recBody httpx.ErrorResponse
	if err := json.NewDecoder(rec.Body).Decode(&recBody); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	// 断言 HTTP status 是 400。
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}

	// 断言 error.code 是 invalid_request。
	if recBody.Error.Code != "invalid_request" {
		t.Fatalf("expected error code %q, got %q", "invalid_request", recBody.Error.Code)
	}

	// 断言 error.type 是 invalid_request_error。
	if recBody.Error.Type != "invalid_request_error" {
		t.Fatalf("expected error type %q, got %q", "invalid_request_error", recBody.Error.Type)
	}

	// 断言 error.param 是 messages。
	if recBody.Error.Param == nil {
		t.Fatal("expected error param to be set")
	}

	if *recBody.Error.Param != "messages" {
		t.Fatalf("expected parameter %q, got %q", "messages", *recBody.Error.Param)
	}
}

func TestRouterV1ChatCompletionWithStreamTrueWritesSSE(t *testing.T) {
	service := &fakeChatCompletionService{
		streamResp: []ChatCompletionStreamResponse{
			{
				ID:      "chatcmpl_stream_test",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "openai/gpt-4.1",
				Choices: []ChatCompletionStreamChoice{
					{
						Index: 0,
						Delta: ChatCompletionStreamDelta{
							Role:    "assistant",
							Content: "mock response",
						},
						FinishReason: nil,
					},
				},
			},
		},
	}

	// 构造带认证的 router。
	router := newTestRouter(&fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}, service, nil)

	// 发送 stream=true 的 chat completions 请求。
	stream := true
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		Stream: &stream,
	}
	reqBuf := new(bytes.Buffer)
	if err := json.NewEncoder(reqBuf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", reqBuf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	// 断言 HTTP status 是 200。
	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	// 断言 Content-Type 是 text/event-stream。
	if rec.Header().Get("Content-Type") != httpx.ContentTypeSSE {
		t.Fatalf("expected Content-Type %q, got %q", "text/event-stream", rec.Header().Get("Content-Type"))
	}

	// 读取响应 body。
	gotBody := rec.Body.String()

	// 断言 body 包含 data: 前缀。
	if !strings.Contains(gotBody, "data: ") {
		t.Fatalf("expected body %q, got %q", "data:", gotBody)
	}

	// 断言 body 包含 chat.completion.chunk。
	if !strings.Contains(gotBody, "chat.completion.chunk") {
		t.Fatalf("expected body %q, got %q", "chat.completion.chunk", gotBody)
	}

	// 断言 body 包含 mock response。
	if !strings.Contains(gotBody, "mock response") {
		t.Fatalf("expected body %q, got %q", "mock response", gotBody)
	}

	// 断言 body 包含 data: [DONE]。
	if !strings.Contains(gotBody, "data: [DONE]") {
		t.Fatalf("expected body to contain %q, got %q", "data: [DONE]", gotBody)
	}

	if !service.streamCalled {
		t.Fatal("expected stream service to be called")
	}

	if service.createCalled {
		t.Fatal("expected create service not to be called")
	}

	if service.req.Model != "openai/gpt-4.1" {
		t.Fatalf("expected model %q, got %q", "openai/gpt-4.1", service.req.Model)
	}

	if service.req.Stream == nil || !*service.req.Stream {
		t.Fatal("expected stream to be true")
	}

	if len(service.req.Messages) != 1 {
		t.Fatalf("expected 1 message, got %d", len(service.req.Messages))
	}

	if service.req.Messages[0].Role != "user" {
		t.Fatalf("expected message role %q, got %q", "user", service.req.Messages[0].Role)
	}

	if service.req.Messages[0].ContentString() != "Hello" {
		t.Fatalf("expected message content %q, got %q", "Hello", service.req.Messages[0].Content)
	}
}

func TestRouterV1ChatCompletionStreamReturnsJSONErrorBeforeFirstChunk(t *testing.T) {
	service := &fakeChatCompletionService{
		err: context.DeadlineExceeded,
	}
	router := newTestRouter(&fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}, service, nil)

	stream := true
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		Stream: &stream,
	}
	reqBuf := new(bytes.Buffer)
	if err := json.NewEncoder(reqBuf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", reqBuf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected status %d, got %d", http.StatusInternalServerError, rec.Code)
	}

	gotBody := rec.Body.String()

	var body httpx.ErrorResponse
	if err := json.NewDecoder(strings.NewReader(gotBody)).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "stream_chat_completion_error" {
		t.Fatalf("expected error code %q, got %q", "stream_chat_completion_error", body.Error.Code)
	}

	if strings.Contains(gotBody, "data:") {
		t.Fatalf("expected body not to contain %q, got %q", "data:", gotBody)
	}

	if !service.streamCalled {
		t.Fatal("expected stream service to be called")
	}

	if service.createCalled {
		t.Fatal("expected create service not to be called")
	}
}

func TestRouterV1ChatCompletionStreamMapsRoutingErrorBeforeFirstChunk(t *testing.T) {
	service := &fakeChatCompletionService{
		err: routing.ErrNoAvailableChannel,
	}
	router := newTestRouter(&fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}, service, nil)

	stream := true
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		Stream: &stream,
	}
	reqBuf := new(bytes.Buffer)
	if err := json.NewEncoder(reqBuf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", reqBuf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusServiceUnavailable {
		t.Fatalf("expected status %d, got %d", http.StatusServiceUnavailable, rec.Code)
	}

	gotBody := rec.Body.String()
	if strings.Contains(gotBody, "data:") {
		t.Fatalf("expected body not to contain %q, got %q", "data:", gotBody)
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(strings.NewReader(gotBody)).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}

	if body.Error.Code != "model_unavailable" {
		t.Fatalf("expected error code %q, got %q", "model_unavailable", body.Error.Code)
	}
	if body.Error.Param == nil || *body.Error.Param != "model" {
		t.Fatalf("expected error param %q, got %#v", "model", body.Error.Param)
	}
}

func TestRouterV1ChatCompletionStreamMapsInsufficientQuotaBeforeFirstChunk(t *testing.T) {
	service := &fakeChatCompletionService{
		err: failure.New(failure.CodeLedgerInsufficientBalance),
	}
	router := newTestRouter(&fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}, service, nil)

	stream := true
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		Stream: &stream,
	}
	reqBuf := new(bytes.Buffer)
	if err := json.NewEncoder(reqBuf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", reqBuf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusTooManyRequests {
		t.Fatalf("expected status %d, got %d", http.StatusTooManyRequests, rec.Code)
	}

	gotBody := rec.Body.String()
	if strings.Contains(gotBody, "data:") {
		t.Fatalf("expected body not to contain %q, got %q", "data:", gotBody)
	}

	var body httpx.ErrorResponse
	if err := json.NewDecoder(strings.NewReader(gotBody)).Decode(&body); err != nil {
		t.Fatalf("decode response body: %v", err)
	}
	if body.Error.Code != "insufficient_quota" {
		t.Fatalf("expected error code %q, got %q", "insufficient_quota", body.Error.Code)
	}
	if body.Error.Type != "insufficient_quota" {
		t.Fatalf("expected error type %q, got %q", "insufficient_quota", body.Error.Type)
	}
	if body.Error.Param != nil {
		t.Fatalf("expected nil error param, got %#v", body.Error.Param)
	}
}

func TestRouterV1ChatCompletionStreamWritesSSEErrorAfterChunkStarted(t *testing.T) {
	service := &fakeChatCompletionService{
		streamResp: []ChatCompletionStreamResponse{
			{
				ID:      "chatcmpl_stream_test",
				Object:  "chat.completion.chunk",
				Created: 123,
				Model:   "openai/gpt-4.1",
				Choices: []ChatCompletionStreamChoice{
					{
						Index: 0,
						Delta: ChatCompletionStreamDelta{
							Role:    "assistant",
							Content: "mock response",
						},
						FinishReason: nil,
					},
				},
			},
		},
		streamErrAfterEmit: context.Canceled,
	}
	router := newTestRouter(&fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}, service, nil)

	stream := true
	reqBody := ChatCompletionRequest{
		Model: "openai/gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("Hello")},
		},
		Stream: &stream,
	}
	reqBuf := new(bytes.Buffer)
	if err := json.NewEncoder(reqBuf).Encode(reqBody); err != nil {
		t.Fatalf("encode request body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", reqBuf)
	req.Header.Set("Authorization", "Bearer unio_sk_test")

	rec := httptest.NewRecorder()
	router.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d", http.StatusOK, rec.Code)
	}

	if rec.Header().Get("Content-Type") != httpx.ContentTypeSSE {
		t.Fatalf("expected Content-Type %q, got %q", httpx.ContentTypeSSE, rec.Header().Get("Content-Type"))
	}

	gotBody := rec.Body.String()
	if !strings.Contains(gotBody, "mock response") {
		t.Fatalf("expected body to contain %q, got %q", "mock response", gotBody)
	}

	if !strings.Contains(gotBody, `"error":`) {
		t.Fatalf("expected body to contain stream error payload, got %q", gotBody)
	}

	if !strings.Contains(gotBody, `"code":"stream_error"`) {
		t.Fatalf("expected body to contain stream error code, got %q", gotBody)
	}

	if !strings.Contains(gotBody, `"type":"api_error"`) {
		t.Fatalf("expected body to contain stream error type, got %q", gotBody)
	}

	if strings.Contains(gotBody, "data: [DONE]") {
		t.Fatalf("expected body not to contain %q, got %q", "data: [DONE]", gotBody)
	}

	if !service.streamCalled {
		t.Fatal("expected stream service to be called")
	}

	if service.createCalled {
		t.Fatal("expected create service not to be called")
	}
}
