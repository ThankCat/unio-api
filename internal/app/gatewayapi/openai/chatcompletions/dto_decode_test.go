package chatcompletions

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ThankCat/unio-gateway/internal/core/auth"
)

func TestChatCompletionRequestUnmarshalPreservesVendorExtension(t *testing.T) {
	raw := `{
		"model": "deepseek/deepseek-v4-flash",
		"messages": [{"role": "user", "content": "hi"}],
		"thinking": {"type": "enabled"}
	}`

	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if req.Model != "deepseek/deepseek-v4-flash" {
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

// TestChatCompletionRequestUnmarshalTypesAllTopLevelFields 验证矩阵 §2 的剩余顶层字段
// 解码进 typed 字段，且不再泄漏进 Extensions（DEC-012 A 方案：OpenAI 规范字段全量 typed）。
func TestChatCompletionRequestUnmarshalTypesAllTopLevelFields(t *testing.T) {
	raw := `{
		"model": "openai/gpt-4.1",
		"messages": [{"role": "user", "content": "hi"}],
		"n": 2,
		"seed": 42,
		"logprobs": true,
		"top_logprobs": 5,
		"logit_bias": {"50256": -100},
		"modalities": ["text"],
		"audio": {"voice": "alloy", "format": "wav"},
		"prediction": {"type": "content", "content": "x"},
		"metadata": {"k": "v"},
		"store": true,
		"service_tier": "auto",
		"verbosity": "low",
		"prompt_cache_key": "ck",
		"prompt_cache_retention": "24h",
		"safety_identifier": "sid",
		"web_search_options": {},
		"function_call": "auto",
		"functions": [{"name": "f"}],
		"stream_options": {"include_usage": true, "include_obfuscation": false}
	}`

	var req ChatCompletionRequest
	if err := json.Unmarshal([]byte(raw), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	if req.N == nil || *req.N != 2 {
		t.Fatalf("n = %#v", req.N)
	}
	if req.Seed == nil || *req.Seed != 42 {
		t.Fatalf("seed = %#v", req.Seed)
	}
	if req.Logprobs == nil || !*req.Logprobs {
		t.Fatalf("logprobs = %#v", req.Logprobs)
	}
	if req.TopLogprobs == nil || *req.TopLogprobs != 5 {
		t.Fatalf("top_logprobs = %#v", req.TopLogprobs)
	}
	if len(req.LogitBias) == 0 || len(req.Modalities) != 1 || len(req.Audio) == 0 ||
		len(req.Prediction) == 0 || len(req.Metadata) == 0 || len(req.WebSearchOptions) == 0 ||
		len(req.FunctionCall) == 0 || len(req.Functions) == 0 {
		t.Fatalf("expected complex fields preserved as raw JSON: %#v", req)
	}
	if req.Store == nil || !*req.Store {
		t.Fatalf("store = %#v", req.Store)
	}
	if req.ServiceTier == nil || *req.ServiceTier != "auto" {
		t.Fatalf("service_tier = %#v", req.ServiceTier)
	}
	if req.Verbosity == nil || *req.Verbosity != "low" {
		t.Fatalf("verbosity = %#v", req.Verbosity)
	}
	if req.PromptCacheKey == nil || req.PromptCacheRetention == nil || req.SafetyIdentifier == nil {
		t.Fatalf("cache/safety scalars = %#v %#v %#v", req.PromptCacheKey, req.PromptCacheRetention, req.SafetyIdentifier)
	}
	if req.StreamOptions == nil || req.StreamOptions.IncludeObfuscation == nil || *req.StreamOptions.IncludeObfuscation {
		t.Fatalf("stream_options.include_obfuscation = %#v", req.StreamOptions)
	}

	// 这些字段都是 typed，不应再出现在 Extensions（避免双写）。
	for _, key := range []string{
		"n", "seed", "logprobs", "top_logprobs", "logit_bias", "modalities", "audio",
		"prediction", "metadata", "store", "service_tier", "verbosity",
		"prompt_cache_key", "prompt_cache_retention", "safety_identifier",
		"web_search_options", "function_call", "functions",
	} {
		if req.HasExtension(key) {
			t.Fatalf("typed field %q must not leak into extensions", key)
		}
	}
}

func TestRouterV1ChatCompletionPreservesThinkingExtension(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
			KeyPrefix: "unio_sk_test",
		},
	}
	service := &fakeChatCompletionService{
		createResp: &ChatCompletionResponse{
			Object: "chat.completion",
			Model:  "deepseek/deepseek-v4-flash",
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
		"model": "deepseek/deepseek-v4-flash",
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

func TestRouterV1ChatCompletionPassesTypedServiceTierToService(t *testing.T) {
	authenticator := &fakeAPIKeyAuthenticator{
		principal: &auth.APIKeyPrincipal{
			APIKeyID:  1,
			UserID:    1,
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
						Content: jsonContent("ok"),
					},
					FinishReason: "stop",
				},
			},
		},
	}
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

	if rec.Code != http.StatusOK {
		t.Fatalf("expected status %d, got %d body=%s", http.StatusOK, rec.Code, rec.Body.String())
	}
	if !service.createCalled {
		t.Fatal("expected service to be called")
	}
	if service.req.ServiceTier == nil || *service.req.ServiceTier != "auto" {
		t.Fatalf("expected handler to pass typed service_tier to service, got %#v", service.req.ServiceTier)
	}
	if service.req.HasExtension("service_tier") {
		t.Fatal("service_tier must be typed, not an extension")
	}
}
