package gatewayapi

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-api/internal/core/auth"
)

func TestChatCompletionRequestUnmarshalPreservesVendorExtension(t *testing.T) {
	raw := `{
		"model": "deepseek/deepseek-chat",
		"messages": [{"role": "user", "content": "hi"}],
		"thinking": {"type": "enabled"}
	}`

	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if req.Model != "deepseek/deepseek-chat" {
		t.Fatalf("expected model preserved, got %q", req.Model)
	}

	if !req.HasExtension("thinking") {
		t.Fatal("expected thinking extension to be preserved")
	}

	var thinking struct {
		Type string `json:"type"`
	}
	if err := json.Unmarshal(req.Extension("thinking"), &thinking); err != nil {
		t.Fatalf("decode thinking extension: %v", err)
	}
	if thinking.Type != "enabled" {
		t.Fatalf("expected thinking.type enabled, got %q", thinking.Type)
	}
}

func TestChatCompletionRequestUnmarshalPreservesResponseFormat(t *testing.T) {
	raw := `{
		"model": "openai/gpt-4.1",
		"messages": [{"role": "user", "content": "hi"}],
		"response_format": {"type": "json_object"}
	}`

	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if req.ResponseFormat == nil || req.ResponseFormat.Type != "json_object" {
		t.Fatalf("expected response_format json_object, got %+v", req.ResponseFormat)
	}
}

func TestChatCompletionRequestUnmarshalPreservesToolsField(t *testing.T) {
	raw := `{
		"model": "openai/gpt-4.1",
		"messages": [{"role": "user", "content": "hi"}],
		"tools": [{"type": "function", "function": {"name": "get_weather"}}]
	}`

	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if len(req.Tools) != 1 {
		t.Fatalf("expected 1 tool, got %d", len(req.Tools))
	}
	if req.Tools[0].Function.Name != "get_weather" {
		t.Fatalf("expected tool name get_weather, got %q", req.Tools[0].Function.Name)
	}
}

func TestChatCompletionRequestUnmarshalDoesNotDuplicateKnownFieldsInExtensions(t *testing.T) {
	raw := `{
		"model": "openai/gpt-4.1",
		"messages": [{"role": "user", "content": "hi"}],
		"temperature": 0.7
	}`

	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if req.Temperature == nil || *req.Temperature != 0.7 {
		t.Fatalf("expected temperature 0.7, got %#v", req.Temperature)
	}

	if req.HasExtension("model") || req.HasExtension("messages") || req.HasExtension("temperature") {
		t.Fatalf("expected known fields not in extensions, got %#v", req.Extensions)
	}
}

func TestChatCompletionRequestUnmarshalRejectsServiceTier(t *testing.T) {
	assertChatCompletionRequestReject(t, `{
		"model": "openai/gpt-4.1",
		"messages": [{"role": "user", "content": "hi"}],
		"service_tier": "auto"
	}`, "service_tier")
}

func TestChatCompletionRequestUnmarshalRejectsStore(t *testing.T) {
	assertChatCompletionRequestReject(t, `{
		"model": "openai/gpt-4.1",
		"messages": [{"role": "user", "content": "hi"}],
		"store": true
	}`, "store")
}

func TestChatCompletionRequestUnmarshalRejectsWebSearchOptions(t *testing.T) {
	assertChatCompletionRequestReject(t, `{
		"model": "openai/gpt-4.1",
		"messages": [{"role": "user", "content": "hi"}],
		"web_search_options": {}
	}`, "web_search_options")
}

func TestRouterV1ChatCompletionPreservesThinkingExtension(t *testing.T) {
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
			Model:  "deepseek/deepseek-chat",
			Choices: []ChatCompletionChoice{
				{
					Index: 0,
					Message: ChatMessage{
						Role:    "assistant",
						Content: jsonContent("ok"),
					},
					FinishReason: "stop",
				},
			},
		},
	}
	handler := newTestRouter(authenticator, service, nil)

	body := `{
		"model": "deepseek/deepseek-chat",
		"messages": [{"role": "user", "content": "hi"}],
		"thinking": {"type": "enabled"}
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !service.createCalled {
		t.Fatal("expected service to be called")
	}
	if !service.req.HasExtension("thinking") {
		t.Fatal("expected handler to pass thinking extension to service")
	}
}

func TestRouterV1ChatCompletionRejectsServiceTier(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			ProjectID: 1,
			KeyPrefix: "unio_sk_test",
		},
	}
	service := &fakeChatCompletionService{}
	handler := newTestRouter(authenticator, service, nil)

	body := `{
		"model": "openai/gpt-4.1",
		"messages": [{"role": "user", "content": "hi"}],
		"service_tier": "auto"
	}`
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(body))
	req.Header.Set("Authorization", "Bearer unio_sk_test")
	rec := httptest.NewRecorder()

	handler.ServeHTTP(rec, req)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("expected status %d, got %d", http.StatusBadRequest, rec.Code)
	}
	if service.createCalled || service.streamCalled {
		t.Fatal("expected service not to be called")
	}

	var resp httpxErrorResponse
	decodeOpenAIErrorResponse(t, rec.Body.String(), &resp)
	if resp.Error.Param == nil || *resp.Error.Param != "service_tier" {
		t.Fatalf("expected param service_tier, got %#v", resp.Error.Param)
	}
	if resp.Error.Message != "unsupported parameter: service_tier" {
		t.Fatalf("unexpected message %q", resp.Error.Message)
	}
}

func assertChatCompletionRequestReject(t *testing.T, raw string, wantParam string) {
	t.Helper()

	var req ChatCompletionRequest
	err := json.Unmarshal([]byte(raw), &req)
	if err == nil {
		t.Fatalf("expected reject error for %s, got nil", wantParam)
	}

	var rejectErr *chatRequestRejectError
	if !errors.As(err, &rejectErr) {
		t.Fatalf("expected chatRequestRejectError, got %T: %v", err, err)
	}
	if rejectErr.param != wantParam {
		t.Fatalf("expected param %q, got %q", wantParam, rejectErr.param)
	}
}

// httpxErrorResponse 是 decode OpenAI error 响应用的本地别名，避免与 httpx 包循环依赖测试细节。
type httpxErrorResponse struct {
	Error struct {
		Message string  `json:"message"`
		Type    string  `json:"type"`
		Param   *string `json:"param"`
		Code    string  `json:"code"`
	} `json:"error"`
}

func decodeOpenAIErrorResponse(t *testing.T, body string, dst *httpxErrorResponse) {
	t.Helper()

	if err := json.Unmarshal([]byte(body), dst); err != nil {
		t.Fatalf("decode error response: %v", err)
	}
}
