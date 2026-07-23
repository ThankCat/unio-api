package chatcompletions

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	gatewayapi "github.com/ThankCat/unio-gateway/internal/app/gatewayapi/openai/chatcompletions"
	openaiadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai"
	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// mockUpstream 记录最后一次 upstream chat/completions 请求体。
type mockUpstream struct {
	server          *httptest.Server
	lastRequestBody []byte
}

func newMockUpstream(t *testing.T, respond func(w http.ResponseWriter)) *mockUpstream {
	t.Helper()

	mock := &mockUpstream{}
	mock.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("unexpected upstream request: %s %s", r.Method, r.URL.Path)
		}

		body, err := io.ReadAll(r.Body)
		if err != nil {
			t.Fatalf("read upstream body: %v", err)
		}
		mock.lastRequestBody = body

		respond(w)
	}))

	return mock
}

func newOpenAIAdapterRegistry(client *http.Client) AdapterRegistry {
	openAIAdapter := chatcompletionsadapter.NewAdapter(client)
	reg, err := openaiadapter.NewRegistry(openaiadapter.Registration{
		Key:        "openai",
		Chat:       openAIAdapter,
		StreamChat: openAIAdapter,
	})
	if err != nil {
		panic(err)
	}

	return reg
}

func deepseekRouteCandidate(server *mockUpstream, channelID int64) routing.ChatRouteCandidate {
	candidate := routeCandidate("openai", channelID, "deepseek-v4-pro")
	candidate.Channel.BaseURL = server.server.URL
	candidate.Channel.ProviderSlug = "deepseek"
	return candidate
}

func newParityService(t *testing.T, upstream *mockUpstream) (*ChatCompletionService, *fakeChatSettlementExecutor) {
	t.Helper()

	settlement := newChatCompletionSettlementForTest()
	service := newChatCompletionServiceForTestWithAuthorizer(
		&fakeChatRouter{plan: routePlan(deepseekRouteCandidate(upstream, 123))},
		newOpenAIAdapterRegistry(upstream.server.Client()),
		nil,
		newFakeRequestLogService(),
		settlement,
		&fakeChatAuthorizer{authorization: lifecycle.ChatAuthorization{ReservationID: 9001}},
	)

	return service, settlement
}

func deepseekUsageJSON() string {
	return `{"prompt_tokens":6,"completion_tokens":20,"total_tokens":26,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":20}}`
}

func encodeUpstreamNonStreamResponse(t *testing.T, w http.ResponseWriter, message map[string]any, finishReason string) {
	t.Helper()

	w.Header().Set("Content-Type", "application/json")
	payload := map[string]any{
		"id":      "chatcmpl-deepseek",
		"object":  "chat.completion",
		"created": 1710000000,
		"model":   "deepseek-v4-pro",
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": json.RawMessage(deepseekUsageJSON()),
	}
	if err := json.NewEncoder(w).Encode(payload); err != nil {
		t.Fatalf("encode upstream response: %v", err)
	}
}

func writeDeepSeekStreamEvents(t *testing.T, w http.ResponseWriter, events []string) {
	t.Helper()

	w.Header().Set("Content-Type", "text/event-stream")
	for _, event := range events {
		if _, err := w.Write([]byte(event)); err != nil {
			t.Fatalf("write stream event: %v", err)
		}
	}
}

// TestOpenAISDKShapeNonStreamChatC1 模拟 OpenAI Python SDK 非流式请求形状（C1）。
func TestOpenAISDKShapeNonStreamChatC1(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		encodeUpstreamNonStreamResponse(t, w, map[string]any{
			"role":    "assistant",
			"content": "hello",
		}, "stop")
	})
	defer upstream.server.Close()

	service, _ := newParityService(t, upstream)

	var req gatewayapi.ChatCompletionRequest
	if err := json.Unmarshal([]byte(`{
		"model": "deepseek/deepseek-v4-pro",
		"messages": [{"role": "user", "content": "hi"}],
		"temperature": 0.7,
		"top_p": 0.9,
		"max_tokens": 128,
		"stop": ["END"],
		"user": "sdk-user-1"
	}`), &req); err != nil {
		t.Fatalf("unmarshal sdk-shaped request: %v", err)
	}

	result, err := service.CreateChatCompletion(contextWithPrincipal(42), req)
	if err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	got := result.Response
	if got.Choices[0].Message.ContentString() != "hello" {
		t.Fatalf("got content %q, want hello", got.Choices[0].Message.ContentString())
	}
	if got.Model != "deepseek/deepseek-v4-pro" {
		t.Fatalf("got model %q, want client model id", got.Model)
	}

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(upstream.lastRequestBody, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if _, ok := wire["temperature"]; !ok {
		t.Fatal("expected temperature forwarded to upstream")
	}
}

// TestOpenAISDKShapeNonStreamPreservesResponseFields 验证非流式响应顶层/choice/message 全量字段
// 端到端贯通到客户响应（created/service_tier/system_fingerprint/refusal/annotations/audio/logprobs）。
func TestOpenAISDKShapeNonStreamPreservesResponseFields(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		w.Header().Set("Content-Type", "application/json")
		body := `{
			"id": "chatcmpl-deepseek",
			"object": "chat.completion",
			"created": 1710000123,
			"model": "deepseek-v4-pro",
			"service_tier": "default",
			"system_fingerprint": "fp_ds_1",
			"choices": [{
				"index": 0,
				"message": {
					"role": "assistant",
					"content": "hi",
					"refusal": "no",
					"annotations": [{"type": "url_citation", "url_citation": {"url": "https://x"}}],
					"audio": {"id": "audio_1"}
				},
				"finish_reason": "stop",
				"logprobs": {"content": [{"token": "hi"}]}
			}],
			"usage": ` + deepseekUsageJSON() + `
		}`
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatalf("write upstream body: %v", err)
		}
	})
	defer upstream.server.Close()

	service, _ := newParityService(t, upstream)

	var req gatewayapi.ChatCompletionRequest
	if err := json.Unmarshal([]byte(`{
		"model": "deepseek/deepseek-v4-pro",
		"messages": [{"role": "user", "content": "hi"}]
	}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	result, err := service.CreateChatCompletion(contextWithPrincipal(42), req)
	if err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	got := result.Response
	if got.Created != 1710000123 {
		t.Fatalf("created = %d, want 1710000123", got.Created)
	}
	if got.ServiceTier == nil || *got.ServiceTier != "default" {
		t.Fatalf("service_tier = %#v", got.ServiceTier)
	}
	if got.SystemFingerprint == nil || *got.SystemFingerprint != "fp_ds_1" {
		t.Fatalf("system_fingerprint = %#v", got.SystemFingerprint)
	}
	choice := got.Choices[0]
	if choice.Message.Refusal == nil || *choice.Message.Refusal != "no" {
		t.Fatalf("refusal = %#v", choice.Message.Refusal)
	}
	if !strings.Contains(string(choice.Message.Annotations), "url_citation") {
		t.Fatalf("annotations = %s", choice.Message.Annotations)
	}
	if !strings.Contains(string(choice.Message.Audio), "audio_1") {
		t.Fatalf("audio = %s", choice.Message.Audio)
	}
	if !strings.Contains(string(choice.Logprobs), "token") {
		t.Fatalf("logprobs = %s", choice.Logprobs)
	}

	// 客户端 JSON 序列化必须真实带上这些字段（faithful OpenAI 形状）。
	encoded, err := json.Marshal(got)
	if err != nil {
		t.Fatalf("marshal response: %v", err)
	}
	for _, key := range []string{`"created":1710000123`, `"service_tier":"default"`, `"system_fingerprint":"fp_ds_1"`, `"refusal":"no"`, `"annotations"`, `"audio"`, `"logprobs"`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("serialized response missing %q: %s", key, encoded)
		}
	}
}

// TestOpenAISDKShapeStreamIncludeUsageC2 模拟 OpenAI SDK stream + include_usage（C2）。
func TestOpenAISDKShapeStreamIncludeUsageC2(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		writeDeepSeekStreamEvents(t, w, []string{
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000001,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":` + deepseekUsageJSON() + `}` + "\n\n",
			"data: [DONE]\n\n",
		})
	})
	defer upstream.server.Close()

	service, settlement := newParityService(t, upstream)

	var req gatewayapi.ChatCompletionRequest
	if err := json.Unmarshal([]byte(`{
		"model": "deepseek/deepseek-v4-pro",
		"messages": [{"role": "user", "content": "hi"}],
		"stream": true,
		"stream_options": {"include_usage": true}
	}`), &req); err != nil {
		t.Fatalf("unmarshal sdk-shaped request: %v", err)
	}

	chunks := make([]gatewayapi.ChatCompletionStreamResponse, 0)
	if err := service.StreamChatCompletion(contextWithPrincipal(42), req, func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatalf("StreamChatCompletion returned err: %v", err)
	}

	if len(chunks) != 3 {
		t.Fatalf("got %d client chunks, want 3 (content + finish + usage)", len(chunks))
	}
	if chunks[0].Choices[0].Delta.Content != "hi" {
		t.Fatalf("got first delta content %q, want hi", chunks[0].Choices[0].Delta.Content)
	}
	if chunks[2].Usage == nil || chunks[2].Usage.TotalTokens != 26 {
		t.Fatalf("got usage chunk %+v, want total_tokens=26", chunks[2].Usage)
	}
	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement, got %d", len(settlement.params))
	}
	if v, ok := settlement.params[0].Facts.Usage.ReasoningOutputTokens.BillableValue(); !ok || v != 20 {
		t.Fatalf("expected settlement reasoning output tokens=20, got %d (ok=%v)", v, ok)
	}
}

// TestOpenAISDKShapeStreamPreservesChunkFields 验证流式 chunk/choice/delta 全量字段端到端贯通到
// 客户 chunk（created 透传上游、service_tier、system_fingerprint、delta.refusal、choice.logprobs）。
func TestOpenAISDKShapeStreamPreservesChunkFields(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		writeDeepSeekStreamEvents(t, w, []string{
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710020001,"model":"deepseek-v4-pro","service_tier":"default","system_fingerprint":"fp_ds_stream","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"logprobs":{"content":[{"token":"hi"}]},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710020002,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"refusal":"no"},"finish_reason":"stop"}],"usage":` + deepseekUsageJSON() + `}` + "\n\n",
			"data: [DONE]\n\n",
		})
	})
	defer upstream.server.Close()

	service, _ := newParityService(t, upstream)

	var req gatewayapi.ChatCompletionRequest
	if err := json.Unmarshal([]byte(`{
		"model": "deepseek/deepseek-v4-pro",
		"messages": [{"role": "user", "content": "hi"}],
		"stream": true
	}`), &req); err != nil {
		t.Fatalf("unmarshal request: %v", err)
	}

	chunks := make([]gatewayapi.ChatCompletionStreamResponse, 0)
	if err := service.StreamChatCompletion(contextWithPrincipal(42), req, func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatalf("StreamChatCompletion returned err: %v", err)
	}

	if len(chunks) != 2 {
		t.Fatalf("got %d client chunks, want 2", len(chunks))
	}

	first := chunks[0]
	if first.Created != 1710020001 {
		t.Fatalf("first chunk created = %d, want upstream 1710020001", first.Created)
	}
	if first.ServiceTier == nil || *first.ServiceTier != "default" {
		t.Fatalf("service_tier = %#v", first.ServiceTier)
	}
	if first.SystemFingerprint == nil || *first.SystemFingerprint != "fp_ds_stream" {
		t.Fatalf("system_fingerprint = %#v", first.SystemFingerprint)
	}
	if !strings.Contains(string(first.Choices[0].Logprobs), "token") {
		t.Fatalf("logprobs = %s", first.Choices[0].Logprobs)
	}
	if second := chunks[1]; second.Choices[0].Delta.Refusal == nil || *second.Choices[0].Delta.Refusal != "no" {
		t.Fatalf("refusal = %#v", chunks[1].Choices[0].Delta.Refusal)
	}

	// 序列化客户 chunk 必须真实带上这些字段。
	encoded, err := json.Marshal(first)
	if err != nil {
		t.Fatalf("marshal chunk: %v", err)
	}
	for _, key := range []string{`"created":1710020001`, `"service_tier":"default"`, `"system_fingerprint":"fp_ds_stream"`, `"logprobs"`} {
		if !strings.Contains(string(encoded), key) {
			t.Fatalf("serialized chunk missing %q: %s", key, encoded)
		}
	}
}

// TestDeepSeekDS01NonStreamReasoning DS-01：非流式 reasoning_content 与 content 分离。
func TestDeepSeekDS01NonStreamReasoning(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		encodeUpstreamNonStreamResponse(t, w, map[string]any{
			"role":              "assistant",
			"content":           "final answer",
			"reasoning_content": "chain of thought",
		}, "stop")
	})
	defer upstream.server.Close()

	service, _ := newParityService(t, upstream)

	result, err := service.CreateChatCompletion(contextWithPrincipal(42), gatewayapi.ChatCompletionRequest{
		Model: "deepseek/deepseek-v4-pro",
		Messages: []gatewayapi.ChatMessage{
			{Role: "user", Content: jsonContent("question")},
		},
	})
	if err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	got := result.Response
	if got.Choices[0].Message.ContentString() != "final answer" {
		t.Fatalf("got content %q, want final answer", got.Choices[0].Message.ContentString())
	}
	if got.Choices[0].Message.ReasoningContent == nil || *got.Choices[0].Message.ReasoningContent != "chain of thought" {
		t.Fatalf("got reasoning %+v, want chain of thought", got.Choices[0].Message.ReasoningContent)
	}
	if got.Usage.CompletionTokensDetails == nil || got.Usage.CompletionTokensDetails.ReasoningTokens != 20 {
		t.Fatalf("got usage details %+v, want reasoning_tokens=20", got.Usage.CompletionTokensDetails)
	}
}

// TestDeepSeekDS02StreamReasoning DS-02：流式先 reasoning_content 再 content。
func TestDeepSeekDS02StreamReasoning(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		writeDeepSeekStreamEvents(t, w, []string{
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000001,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":null,"reasoning_content":"think"},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000002,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"answer","reasoning_content":null},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000003,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"","reasoning_content":null},"finish_reason":"stop"}],"usage":` + deepseekUsageJSON() + `}` + "\n\n",
			"data: [DONE]\n\n",
		})
	})
	defer upstream.server.Close()

	service, _ := newParityService(t, upstream)

	chunks := make([]gatewayapi.ChatCompletionStreamResponse, 0)
	if err := service.StreamChatCompletion(contextWithPrincipal(42), gatewayapi.ChatCompletionRequest{
		Model:  "deepseek/deepseek-v4-pro",
		Stream: boolPtr(true),
		Messages: []gatewayapi.ChatMessage{
			{Role: "user", Content: jsonContent("question")},
		},
	}, func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatalf("StreamChatCompletion returned err: %v", err)
	}

	if len(chunks) != 4 {
		t.Fatalf("got %d chunks, want 4 (role + reasoning + content + finish)", len(chunks))
	}
	if chunks[1].Choices[0].Delta.ReasoningContent == nil || *chunks[1].Choices[0].Delta.ReasoningContent != "think" {
		t.Fatalf("got chunk[1] reasoning %+v, want think", chunks[1].Choices[0].Delta.ReasoningContent)
	}
	if chunks[2].Choices[0].Delta.Content != "answer" {
		t.Fatalf("got chunk[2] content %q, want answer", chunks[2].Choices[0].Delta.Content)
	}
}

// TestDeepSeekDS03StreamIncludeUsage DS-03：stream + include_usage 尾包。
func TestDeepSeekDS03StreamIncludeUsage(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		writeDeepSeekStreamEvents(t, w, []string{
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"ok"},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000001,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":` + deepseekUsageJSON() + `}` + "\n\n",
			"data: [DONE]\n\n",
		})
	})
	defer upstream.server.Close()

	service, _ := newParityService(t, upstream)

	includeUsage := true
	chunks := make([]gatewayapi.ChatCompletionStreamResponse, 0)
	if err := service.StreamChatCompletion(contextWithPrincipal(42), gatewayapi.ChatCompletionRequest{
		Model:  "deepseek/deepseek-v4-pro",
		Stream: boolPtr(true),
		StreamOptions: &gatewayapi.ChatCompletionStreamOptions{
			IncludeUsage: &includeUsage,
		},
		Messages: []gatewayapi.ChatMessage{
			{Role: "user", Content: jsonContent("hi")},
		},
	}, func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		chunks = append(chunks, chunk)
		return nil
	}); err != nil {
		t.Fatalf("StreamChatCompletion returned err: %v", err)
	}

	if len(chunks) != 3 {
		t.Fatalf("got %d chunks, want content + finish + usage", len(chunks))
	}
	if chunks[2].Usage == nil {
		t.Fatal("expected final usage chunk")
	}
}

// TestDeepSeekDS04ThinkingDisabled DS-04：无 reasoning_content 时只有 content。
func TestDeepSeekDS04ThinkingDisabled(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		encodeUpstreamNonStreamResponse(t, w, map[string]any{
			"role":    "assistant",
			"content": "plain answer",
		}, "stop")
	})
	defer upstream.server.Close()

	service, _ := newParityService(t, upstream)

	result, err := service.CreateChatCompletion(contextWithPrincipal(42), gatewayapi.ChatCompletionRequest{
		Model: "deepseek/deepseek-v4-pro",
		Messages: []gatewayapi.ChatMessage{
			{Role: "user", Content: jsonContent("hi")},
		},
		Extensions: map[string]json.RawMessage{
			"thinking": json.RawMessage(`{"type":"disabled"}`),
		},
	})
	if err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	got := result.Response
	if got.Choices[0].Message.ReasoningContent != nil {
		t.Fatalf("expected no reasoning_content, got %+v", got.Choices[0].Message.ReasoningContent)
	}
	if got.Choices[0].Message.ContentString() != "plain answer" {
		t.Fatalf("got content %q, want plain answer", got.Choices[0].Message.ContentString())
	}
}

// TestDeepSeekDS05ToolsMultiTurn DS-05：tool 多轮 assistant 历史 reasoning_content 回传 upstream。
func TestDeepSeekDS05ToolsMultiTurn(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		encodeUpstreamNonStreamResponse(t, w, map[string]any{
			"role":    "assistant",
			"content": "done",
		}, "stop")
	})
	defer upstream.server.Close()

	service, _ := newParityService(t, upstream)

	reasoning := "tool-thought"
	toolCallID := "call_abc"
	_, err := service.CreateChatCompletion(contextWithPrincipal(42), gatewayapi.ChatCompletionRequest{
		Model: "deepseek/deepseek-v4-pro",
		Messages: []gatewayapi.ChatMessage{
			{Role: "user", Content: jsonContent("weather?")},
			{
				Role:             "assistant",
				Content:          jsonContent(""),
				ReasoningContent: &reasoning,
				ToolCalls: []gatewayapi.ChatCompletionToolCall{{
					ID:   "call_abc",
					Type: "function",
					Function: gatewayapi.ChatCompletionToolCallFunction{
						Name:      "get_weather",
						Arguments: "{}",
					},
				}},
			},
			{Role: "tool", ToolCallID: &toolCallID, Content: jsonContent(`{"temp":20}`)},
		},
		Tools: []gatewayapi.ChatCompletionTool{{
			Type: "function",
			Function: gatewayapi.ChatCompletionFunctionTool{
				Name:       "get_weather",
				Parameters: json.RawMessage(`{"type":"object"}`),
			},
		}},
	})
	if err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	var wire struct {
		Messages []map[string]json.RawMessage `json:"messages"`
		Tools    json.RawMessage              `json:"tools"`
	}
	if err := json.Unmarshal(upstream.lastRequestBody, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if len(wire.Messages) != 3 {
		t.Fatalf("got %d upstream messages, want 3", len(wire.Messages))
	}
	if _, ok := wire.Messages[1]["reasoning_content"]; !ok {
		t.Fatal("expected assistant reasoning_content in upstream messages")
	}
	if _, ok := wire.Messages[1]["tool_calls"]; !ok {
		t.Fatal("expected assistant tool_calls in upstream messages")
	}
	if len(wire.Tools) == 0 {
		t.Fatal("expected tools forwarded to upstream")
	}
}

// TestDeepSeekDS06ThinkingPassthrough DS-06：thinking 扩展 passthrough upstream。
func TestDeepSeekDS06ThinkingPassthrough(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		encodeUpstreamNonStreamResponse(t, w, map[string]any{
			"role":    "assistant",
			"content": "ok",
		}, "stop")
	})
	defer upstream.server.Close()

	service, _ := newParityService(t, upstream)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), gatewayapi.ChatCompletionRequest{
		Model: "deepseek/deepseek-v4-pro",
		Messages: []gatewayapi.ChatMessage{
			{Role: "user", Content: jsonContent("hi")},
		},
		Extensions: map[string]json.RawMessage{
			"thinking": json.RawMessage(`{"type":"enabled"}`),
		},
	})
	if err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	var wire map[string]json.RawMessage
	if err := json.Unmarshal(upstream.lastRequestBody, &wire); err != nil {
		t.Fatalf("unmarshal upstream body: %v", err)
	}
	if _, ok := wire["thinking"]; !ok {
		t.Fatal("expected thinking extension merged into upstream body")
	}
}

// TestDeepSeekDS07SettlementUsage DS-07：settlement 消费 reasoning_tokens。
func TestDeepSeekDS07SettlementUsage(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		encodeUpstreamNonStreamResponse(t, w, map[string]any{
			"role":              "assistant",
			"content":           "answer",
			"reasoning_content": "thought",
		}, "stop")
	})
	defer upstream.server.Close()

	service, settlement := newParityService(t, upstream)

	_, err := service.CreateChatCompletion(contextWithPrincipal(42), gatewayapi.ChatCompletionRequest{
		Model: "deepseek/deepseek-v4-pro",
		Messages: []gatewayapi.ChatMessage{
			{Role: "user", Content: jsonContent("hi")},
		},
	})
	if err != nil {
		t.Fatalf("CreateChatCompletion returned err: %v", err)
	}

	if len(settlement.params) != 1 {
		t.Fatalf("expected one settlement, got %d", len(settlement.params))
	}
	if v, ok := settlement.params[0].Facts.Usage.OutputTokensTotal.BillableValue(); !ok || v != 20 {
		t.Fatalf("got output tokens %d, want 20 (ok=%v)", v, ok)
	}
	if v, ok := settlement.params[0].Facts.Usage.ReasoningOutputTokens.BillableValue(); !ok || v != 20 {
		t.Fatalf("got reasoning tokens %d, want 20 (ok=%v)", v, ok)
	}
}

func boolPtr(v bool) *bool {
	return &v
}

// TestOpenAIParityStreamUsageNullIntermediateChunk 中间 content chunk 在 include_usage 时写出 usage:null。
func TestOpenAIParityStreamUsageNullIntermediateChunk(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		writeDeepSeekStreamEvents(t, w, []string{
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":"hi"},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000001,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":""},"finish_reason":"stop"}],"usage":` + deepseekUsageJSON() + `}` + "\n\n",
			"data: [DONE]\n\n",
		})
	})
	defer upstream.server.Close()

	service, _ := newParityService(t, upstream)

	includeUsage := true
	var intermediateRaw []byte
	chunkIndex := 0
	if err := service.StreamChatCompletion(contextWithPrincipal(42), gatewayapi.ChatCompletionRequest{
		Model:  "deepseek/deepseek-v4-pro",
		Stream: boolPtr(true),
		StreamOptions: &gatewayapi.ChatCompletionStreamOptions{
			IncludeUsage: &includeUsage,
		},
		Messages: []gatewayapi.ChatMessage{
			{Role: "user", Content: jsonContent("hi")},
		},
	}, func(chunk gatewayapi.ChatCompletionStreamResponse) error {
		if chunkIndex == 0 {
			intermediateRaw, _ = json.Marshal(chunk)
		}
		chunkIndex++
		return nil
	}); err != nil {
		t.Fatalf("StreamChatCompletion returned err: %v", err)
	}

	if !strings.Contains(string(intermediateRaw), `"usage":null`) {
		t.Fatalf("expected intermediate chunk to contain usage:null, got %s", intermediateRaw)
	}
}

// 确保 deepseek route 注入 ProviderSlug，否则 stream translate 不会走 DeepSeek 规则。
func TestDeepSeekRouteCandidateSetsProviderSlug(t *testing.T) {
	upstream := newMockUpstream(t, func(w http.ResponseWriter) {
		w.WriteHeader(http.StatusOK)
	})
	defer upstream.server.Close()

	candidate := deepseekRouteCandidate(upstream, 1)
	if candidate.Channel.ProviderSlug != "deepseek" {
		t.Fatalf("got provider slug %q, want deepseek", candidate.Channel.ProviderSlug)
	}
	if candidate.Channel.BaseURL != upstream.server.URL {
		t.Fatalf("unexpected base url %q", candidate.Channel.BaseURL)
	}
	_ = channel.Runtime{ProviderSlug: candidate.Channel.ProviderSlug}
}
