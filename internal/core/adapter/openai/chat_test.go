package openai

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/adapter/openai/normalizer"
	"github.com/ThankCat/unio-api/internal/core/channel"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// adapterChatRequestWithParams 创建带可透传 OpenAI-compatible 参数的 adapter 请求。
func adapterChatRequestWithParams() adapter.ChatRequest {
	temperature := 0.0
	topP := 0.8
	maxTokens := 128
	presencePenalty := 0.5
	frequencyPenalty := 0.25
	user := "end-user-1"

	return adapter.ChatRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hello"},
		},
		Temperature:      &temperature,
		TopP:             &topP,
		MaxTokens:        &maxTokens,
		PresencePenalty:  &presencePenalty,
		FrequencyPenalty: &frequencyPenalty,
		Stop:             []string{"END", "STOP"},
		User:             &user,
	}
}

// assertUpstreamChatRequestParams 断言 OpenAI wire DTO 带上 adapter contract 中的可透传参数。
func assertUpstreamChatRequestParams(t *testing.T, req chatCompletionRequest) {
	t.Helper()

	if req.Temperature == nil || *req.Temperature != 0 {
		t.Fatalf("expected temperature 0, got %v", req.Temperature)
	}
	if req.TopP == nil || *req.TopP != 0.8 {
		t.Fatalf("expected top_p 0.8, got %v", req.TopP)
	}
	if req.MaxTokens == nil || *req.MaxTokens != 128 {
		t.Fatalf("expected max_tokens 128, got %v", req.MaxTokens)
	}
	if req.PresencePenalty == nil || *req.PresencePenalty != 0.5 {
		t.Fatalf("expected presence_penalty 0.5, got %v", req.PresencePenalty)
	}
	if req.FrequencyPenalty == nil || *req.FrequencyPenalty != 0.25 {
		t.Fatalf("expected frequency_penalty 0.25, got %v", req.FrequencyPenalty)
	}
	if len(req.Stop) != 2 || req.Stop[0] != "END" || req.Stop[1] != "STOP" {
		t.Fatalf("expected stop [END STOP], got %#v", req.Stop)
	}
	if req.User == nil || *req.User != "end-user-1" {
		t.Fatalf("expected user end-user-1, got %v", req.User)
	}
}

func intPtr(v int) *int {
	return &v
}

func TestAdapterChatCompletionsCallsUpstream(t *testing.T) {
	var gotAuthorization string
	var gotContentType string
	var gotRequestBody chatCompletionRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("expected method %q, got %q", http.MethodPost, r.Method)
		}

		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("expected path %q, got %q", "/v1/chat/completions", r.URL.Path)
		}

		gotAuthorization = r.Header.Get("Authorization")
		gotContentType = r.Header.Get("Content-Type")

		if err := json.NewDecoder(r.Body).Decode(&gotRequestBody); err != nil {
			t.Fatalf("decode request body: %v", err)
		}

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)

		if err := json.NewEncoder(w).Encode(chatCompletionResponse{
			ID:    "chatcmpl_test",
			Model: "gpt-4.1",
			Choices: []chatChoice{
				{Message: chatMessage{Role: "assistant", Content: "hello from fake upstream"}},
			},
			Usage: &chatCompletionUsage{
				PromptTokens:     intPtr(11),
				CompletionTokens: intPtr(12),
				TotalTokens:      intPtr(23),
				PromptTokensDetails: chatPromptTokensDetails{
					CachedTokens: 7,
				},
				CompletionTokensDetails: chatCompletionTokensDetails{
					ReasoningTokens:          3,
					AcceptedPredictionTokens: 2,
					RejectedPredictionTokens: 1,
				},
			},
		}); err != nil {
			t.Fatalf("encode response: %v", err)
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	selectedChannel := channel.Runtime{
		ID:      123,
		BaseURL: server.URL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}

	got, err := openAIAdapter.ChatCompletions(context.Background(), selectedChannel, adapterChatRequestWithParams())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotAuthorization != "Bearer test-secret" {
		t.Fatalf("authorization header: got %q, want %q", gotAuthorization, "Bearer test-secret")
	}
	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Fatalf("Content-type header does not contain application/json")
	}
	if gotRequestBody.Model != "gpt-4.1" {
		t.Fatalf("body model: got %q, want %q", gotRequestBody.Model, "gpt-4.1")
	}
	if len(gotRequestBody.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(gotRequestBody.Messages))
	}
	if gotRequestBody.Messages[0].Role != "user" {
		t.Fatalf("got role %q, want %q", gotRequestBody.Messages[0].Role, "user")
	}
	if gotRequestBody.Messages[0].Content != "hello" {
		t.Fatalf("got %q, want %q", gotRequestBody.Messages[0].Content, "hello")
	}
	assertUpstreamChatRequestParams(t, gotRequestBody)

	if got.ID != "chatcmpl_test" {
		t.Fatalf("expected id %q, got %q", "chatcmpl_test", got.ID)
	}

	if got.Model != "gpt-4.1" {
		t.Fatalf("expected model %q, got %q", "gpt-4.1", got.Model)
	}

	if got.Content != "hello from fake upstream" {
		t.Fatalf("expected content %q, got %q", "hello from fake upstream", got.Content)
	}

	if got.Usage.PromptTokens != 11 {
		t.Fatalf("got prompt_tokens %d, want 11", got.Usage.PromptTokens)
	}
	if got.Usage.CompletionTokens != 12 {
		t.Fatalf("got completion_tokens %d, want 12", got.Usage.CompletionTokens)
	}
	if got.Usage.TotalTokens != 23 {
		t.Fatalf("got total_tokens %d, want 23", got.Usage.TotalTokens)
	}
	if got.Usage.CachedTokens != 7 {
		t.Fatalf("got cached_tokens %d, want 7", got.Usage.CachedTokens)
	}
	if got.Usage.ReasoningTokens != 3 {
		t.Fatalf("got reasoning_tokens %d, want 3", got.Usage.ReasoningTokens)
	}
}

func TestAdapterChatCompletionsReturnsErrorForMissingUsage(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "missing usage object",
			body: `{
				"id": "chatcmpl_missing_usage",
				"model": "gpt-4.1",
				"choices": [
					{"message": {"role": "assistant", "content": "hello"}}
				]
			}`,
		},
		{
			name: "missing required usage token field",
			body: `{
				"id": "chatcmpl_missing_usage_field",
				"model": "gpt-4.1",
				"choices": [
					{"message": {"role": "assistant", "content": "hello"}}
				],
				"usage": {
					"completion_tokens": 12,
					"total_tokens": 23
				}
			}`,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusOK)
				if _, err := w.Write([]byte(tc.body)); err != nil {
					t.Fatalf("write response body: %v", err)
				}
			}))
			defer server.Close()

			openAIAdapter := newTestAdapter(server.Client())

			_, err := openAIAdapter.ChatCompletions(context.Background(), channel.Runtime{
				BaseURL: server.URL + "/v1",
				APIKey:  "test-secret",
				Timeout: 30 * time.Second,
			}, adapter.ChatRequest{
				Model: "gpt-4.1",
				Messages: []adapter.ChatMessage{
					{Role: "user", Content: "hello"},
				},
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			if failure.CodeOf(err) != failure.CodeAdapterInvalidResponse {
				t.Fatalf("expected failure code %q, got %q", failure.CodeAdapterInvalidResponse, failure.CodeOf(err))
			}
		})
	}
}

func TestAdapterChatCompletionsReturnsErrorForUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream failed", http.StatusBadGateway)
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	_, err := openAIAdapter.ChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, adapter.ChatRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hello"},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if failure.CodeOf(err) != failure.CodeAdapterUpstreamStatus {
		t.Fatalf("expected failure code %q, got %q", failure.CodeAdapterUpstreamStatus, failure.CodeOf(err))
	}
}

func TestAdapterChatCompletionsUsesChannelTimeout(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	_, err := openAIAdapter.ChatCompletions(
		context.Background(),
		channel.Runtime{
			BaseURL: server.URL + "/v1",
			APIKey:  "test-secret",
			Timeout: 50 * time.Millisecond,
		},
		adapter.ChatRequest{Model: "gpt-4.1",
			Messages: []adapter.ChatMessage{
				{Role: "user", Content: "hello"},
			}})

	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if failure.CodeOf(err) != failure.CodeAdapterSendRequestFailed {
		t.Fatalf("expected failure code %q, got %q", failure.CodeAdapterSendRequestFailed, failure.CodeOf(err))
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected timeout error wrapping context deadline exceeded, got %v", err)
	}
}

func TestAdapterStreamChatCompletionsParsesUpstreamSSE(t *testing.T) {
	var gotAuthorization string
	var gotAccept string
	var gotContentType string
	var gotRequestBody chatCompletionRequest

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Fatalf("got %q, want %q", r.Method, http.MethodPost)
		}

		if r.URL.Path != "/v1/chat/completions" {
			t.Fatalf("got %q, want %q", r.URL.Path, "/v1/chat/completions")
		}

		gotAuthorization = r.Header.Get("Authorization")
		gotAccept = r.Header.Get("Accept")
		gotContentType = r.Header.Get("Content-Type")

		if err := json.NewDecoder(r.Body).Decode(&gotRequestBody); err != nil {
			t.Fatalf("failed to decode body: %v", err)
		}

		if !gotRequestBody.Stream {
			t.Fatal("expected stream request body to be true")
		}
		if gotRequestBody.StreamOptions == nil {
			t.Fatal("expected stream_options to be set")
		}
		if !gotRequestBody.StreamOptions.IncludeUsage {
			t.Fatal("expected stream_options.include_usage to be true")
		}

		w.Header().Set("Content-Type", "text/event-stream")

		flusher, ok := w.(http.Flusher)
		if !ok {
			t.Fatal("expected response writer to support flush")
		}
		stop := "stop"
		chunks := []chatCompletionStreamResponse{
			{
				ID:    "chatcmpl_stream_test",
				Model: "gpt-4.1",
				Choices: []chatStreamChoice{
					{
						Delta:        chatStreamDelta{},
						FinishReason: nil,
					},
				},
			},
			{
				ID:    "chatcmpl_stream_test",
				Model: "gpt-4.1",
				Choices: []chatStreamChoice{
					{
						Delta: chatStreamDelta{
							Role:    "assistant",
							Content: "hello ",
						},
						FinishReason: nil,
					},
				},
			},
			{
				ID:    "chatcmpl_stream_test",
				Model: "gpt-4.1",
				Choices: []chatStreamChoice{
					{
						Delta: chatStreamDelta{
							Content: "world",
						},
						FinishReason: &stop,
					},
				},
			},
			{
				ID:      "chatcmpl_stream_test",
				Model:   "gpt-4.1",
				Choices: []chatStreamChoice{},
			},
			{
				ID:      "chatcmpl_stream_test",
				Model:   "gpt-4.1",
				Choices: []chatStreamChoice{},
				Usage: &chatCompletionUsage{
					PromptTokens:     intPtr(11),
					CompletionTokens: intPtr(12),
					TotalTokens:      intPtr(23),
					PromptTokensDetails: chatPromptTokensDetails{
						CachedTokens: 7,
					},
					CompletionTokensDetails: chatCompletionTokensDetails{
						ReasoningTokens:          3,
						AcceptedPredictionTokens: 2,
						RejectedPredictionTokens: 1,
					},
				},
			},
		}

		for _, chunk := range chunks {
			payload, err := json.Marshal(chunk)
			if err != nil {
				t.Fatalf("failed to marshal chunk: %v", err)
			}

			if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
				t.Fatalf("write stream chunk: %v", err)
			}
			flusher.Flush()
		}

		if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
			t.Fatalf("write done chunk: %v", err)
		}
		flusher.Flush()
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())
	selectedChannel := channel.Runtime{
		ID:      123,
		BaseURL: server.URL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}

	got := make([]adapter.ChatStreamChunk, 0)
	err := openAIAdapter.StreamChatCompletions(context.Background(), selectedChannel, adapterChatRequestWithParams(), func(chunk adapter.ChatStreamChunk) error {
		got = append(got, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("StreamChatCompletions returned err: %v", err)
	}

	if gotAuthorization != "Bearer test-secret" {
		t.Fatalf("authorization header: got %q, want %q", gotAuthorization, "Bearer test-secret")
	}

	if gotAccept != "text/event-stream" {
		t.Fatalf("accept header: got %q, want %q", gotAccept, "text/event-stream")
	}

	if !strings.HasPrefix(gotContentType, "application/json") {
		t.Fatalf("content type: got %q, want application/json", gotContentType)
	}

	if gotRequestBody.Model != "gpt-4.1" {
		t.Fatalf("body model: got %q, want %q", gotRequestBody.Model, "gpt-4.1")
	}

	if len(gotRequestBody.Messages) != 1 {
		t.Fatalf("got %d messages, want 1", len(gotRequestBody.Messages))
	}

	if gotRequestBody.Messages[0].Role != "user" {
		t.Fatalf("got role %q, want %q", gotRequestBody.Messages[0].Role, "user")
	}

	if gotRequestBody.Messages[0].Content != "hello" {
		t.Fatalf("got content %q, want %q", gotRequestBody.Messages[0].Content, "hello")
	}
	assertUpstreamChatRequestParams(t, gotRequestBody)

	if len(got) != 3 {
		t.Fatalf("got %d chunks, want 3", len(got))
	}

	if got[0].ID != "chatcmpl_stream_test" {
		t.Fatalf("got id %q, want %q", got[0].ID, "chatcmpl_stream_test")
	}
	if got[0].Model != "gpt-4.1" {
		t.Fatalf("got model %q, want %q", got[0].Model, "gpt-4.1")
	}
	if got[0].Role != "assistant" {
		t.Fatalf("got role %q, want %q", got[0].Role, "assistant")
	}
	if got[0].Content != "hello " {
		t.Fatalf("got content %q, want %q", got[0].Content, "hello ")
	}
	if got[0].Usage != nil {
		t.Fatalf("got first chunk usage %+v, want nil", got[0].Usage)
	}

	if got[1].Content != "world" {
		t.Fatalf("got content %q, want %q", got[1].Content, "world")
	}
	if got[1].FinishReason == nil {
		t.Fatal("got nil finish reason, want stop")
	}
	if *got[1].FinishReason != "stop" {
		t.Fatalf("got finish reason %q, want %q", *got[1].FinishReason, "stop")
	}
	if got[1].Usage != nil {
		t.Fatalf("got second chunk usage %+v, want nil", got[1].Usage)
	}

	if got[2].ID != "chatcmpl_stream_test" {
		t.Fatalf("got usage chunk id %q, want %q", got[2].ID, "chatcmpl_stream_test")
	}
	if got[2].Model != "gpt-4.1" {
		t.Fatalf("got usage chunk model %q, want %q", got[2].Model, "gpt-4.1")
	}
	if got[2].Role != "" {
		t.Fatalf("got usage chunk role %q, want empty", got[2].Role)
	}
	if got[2].Content != "" {
		t.Fatalf("got usage chunk content %q, want empty", got[2].Content)
	}
	if got[2].FinishReason != nil {
		t.Fatalf("got usage chunk finish reason %+v, want nil", got[2].FinishReason)
	}
	if got[2].Usage == nil {
		t.Fatal("got nil usage chunk usage, want usage")
	}
	if got[2].Usage.PromptTokens != 11 {
		t.Fatalf("got prompt_tokens %d, want 11", got[2].Usage.PromptTokens)
	}
	if got[2].Usage.CompletionTokens != 12 {
		t.Fatalf("got completion_tokens %d, want 12", got[2].Usage.CompletionTokens)
	}
	if got[2].Usage.TotalTokens != 23 {
		t.Fatalf("got total_tokens %d, want 23", got[2].Usage.TotalTokens)
	}
	if got[2].Usage.CachedTokens != 7 {
		t.Fatalf("got cached_tokens %d, want 7", got[2].Usage.CachedTokens)
	}
	if got[2].Usage.ReasoningTokens != 3 {
		t.Fatalf("got reasoning_tokens %d, want 3", got[2].Usage.ReasoningTokens)
	}
}

func TestAdapterStreamChatCompletionsParsesMultilineSSEEvent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		// 一个合法 SSE event 可以包含多行 data；parser 必须先按 event 聚合，再 JSON decode。
		if _, err := w.Write([]byte("data: {\"id\":\"chatcmpl_multiline\",\"model\":\"gpt-4.1\",\n")); err != nil {
			t.Fatalf("write stream chunk first line: %v", err)
		}
		if _, err := w.Write([]byte("data: \"choices\":[{\"delta\":{\"content\":\"hello\"},\"finish_reason\":null}]}\n\n")); err != nil {
			t.Fatalf("write stream chunk second line: %v", err)
		}
		if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
			t.Fatalf("write done chunk: %v", err)
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	got := make([]adapter.ChatStreamChunk, 0)
	err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, adapter.ChatRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hello"},
		},
	}, func(chunk adapter.ChatStreamChunk) error {
		got = append(got, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if got[0].Content != "hello" {
		t.Fatalf("got content %q, want hello", got[0].Content)
	}
}

func TestAdapterStreamChatCompletionsParsesOpenAIRawSSEFixture(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		rawEvents := []string{
			`data: {"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1710000000,"model":"gpt-4.1","system_fingerprint":"fp_fixture","choices":[{"index":0,"delta":{"role":"assistant","content":""},"logprobs":null,"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1710000001,"model":"gpt-4.1","choices":[{"index":0,"delta":{"content":"hello"},"logprobs":null,"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1710000002,"model":"gpt-4.1","choices":[{"index":0,"delta":{},"logprobs":null,"finish_reason":"stop"}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-fixture","object":"chat.completion.chunk","created":1710000003,"model":"gpt-4.1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11,"prompt_tokens_details":{"cached_tokens":2},"completion_tokens_details":{"reasoning_tokens":1}}}` + "\n\n",
			"data: [DONE]\n\n",
		}

		for _, event := range rawEvents {
			if _, err := w.Write([]byte(event)); err != nil {
				t.Fatalf("write raw fixture event: %v", err)
			}
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	got := make([]adapter.ChatStreamChunk, 0)
	err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, adapter.ChatRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hello"},
		},
	}, func(chunk adapter.ChatStreamChunk) error {
		got = append(got, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("got %d chunks, want 4", len(got))
	}
	if got[0].Role != "assistant" {
		t.Fatalf("got role %q, want assistant", got[0].Role)
	}
	if got[1].Content != "hello" {
		t.Fatalf("got content %q, want hello", got[1].Content)
	}
	if got[2].FinishReason == nil || *got[2].FinishReason != "stop" {
		t.Fatalf("got finish reason %+v, want stop", got[2].FinishReason)
	}
	if got[3].Usage == nil {
		t.Fatal("got nil usage, want final usage")
	}
	if got[3].Usage.PromptTokens != 5 {
		t.Fatalf("got prompt tokens %d, want 5", got[3].Usage.PromptTokens)
	}
	if got[3].Usage.CompletionTokens != 6 {
		t.Fatalf("got completion tokens %d, want 6", got[3].Usage.CompletionTokens)
	}
	if got[3].Usage.TotalTokens != 11 {
		t.Fatalf("got total tokens %d, want 11", got[3].Usage.TotalTokens)
	}
	if got[3].Usage.CachedTokens != 2 {
		t.Fatalf("got cached tokens %d, want 2", got[3].Usage.CachedTokens)
	}
	if got[3].Usage.ReasoningTokens != 1 {
		t.Fatalf("got reasoning tokens %d, want 1", got[3].Usage.ReasoningTokens)
	}
}

func TestAdapterStreamChatCompletionsParsesDeepSeekUsageTail(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		rawEvents := []string{
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000001,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"hi","reasoning_content":null},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000002,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"","reasoning_content":null},"finish_reason":"length"}],"usage":{"prompt_tokens":6,"completion_tokens":20,"total_tokens":26,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":20}}}` + "\n\n",
			"data: [DONE]\n\n",
		}

		for _, event := range rawEvents {
			if _, err := w.Write([]byte(event)); err != nil {
				t.Fatalf("write deepseek fixture event: %v", err)
			}
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	got := make([]adapter.ChatStreamChunk, 0)
	err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		ProviderSlug: "deepseek",
		BaseURL:      server.URL + "/v1",
		APIKey:       "test-secret",
		Timeout:      30 * time.Second,
	}, adapter.ChatRequest{
		Model: "deepseek-v4-pro",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hi"},
		},
	}, func(chunk adapter.ChatStreamChunk) error {
		got = append(got, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 4 {
		t.Fatalf("got %d chunks, want 4", len(got))
	}
	if got[1].Content != "hi" {
		t.Fatalf("got content %q, want hi", got[1].Content)
	}
	if got[2].FinishReason == nil || *got[2].FinishReason != "length" {
		t.Fatalf("got finish reason %+v, want length", got[2].FinishReason)
	}
	if got[3].Usage == nil || got[3].Usage.TotalTokens != 26 {
		t.Fatalf("got final usage %+v, want total_tokens=26", got[3].Usage)
	}
}

func TestAdapterStreamChatCompletionsForwardsDeepSeekReasoningContent(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		rawEvents := []string{
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000000,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"role":"assistant","content":null,"reasoning_content":""},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000001,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":null,"reasoning_content":"We"},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000002,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":null,"reasoning_content":" are"},"finish_reason":null}],"usage":null}` + "\n\n",
			`data: {"id":"chatcmpl-deepseek","object":"chat.completion.chunk","created":1710000003,"model":"deepseek-v4-pro","choices":[{"index":0,"delta":{"content":"","reasoning_content":null},"finish_reason":"length"}],"usage":{"prompt_tokens":6,"completion_tokens":3,"total_tokens":9,"prompt_tokens_details":{"cached_tokens":0},"completion_tokens_details":{"reasoning_tokens":3}}}` + "\n\n",
			"data: [DONE]\n\n",
		}

		for _, event := range rawEvents {
			if _, err := w.Write([]byte(event)); err != nil {
				t.Fatalf("write deepseek reasoning fixture event: %v", err)
			}
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	got := make([]adapter.ChatStreamChunk, 0)
	err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		ProviderSlug: "deepseek",
		BaseURL:      server.URL + "/v1",
		APIKey:       "test-secret",
		Timeout:      30 * time.Second,
	}, adapter.ChatRequest{
		Model: "deepseek-v4-pro",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hi"},
		},
	}, func(chunk adapter.ChatStreamChunk) error {
		got = append(got, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 5 {
		t.Fatalf("got %d chunks, want 5", len(got))
	}
	if got[1].Content != "We" {
		t.Fatalf("got chunk[1].Content %q, want We", got[1].Content)
	}
	if got[2].Content != " are" {
		t.Fatalf("got chunk[2].Content %q, want  are", got[2].Content)
	}
	if got[4].Usage == nil || got[4].Usage.TotalTokens != 9 {
		t.Fatalf("got final usage %+v, want total_tokens=9", got[4].Usage)
	}
}

func TestAdapterStreamChatCompletionsDoesNotForwardReasoningWithoutDeepSeekNormalizer(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		if _, err := w.Write([]byte(`data: {"id":"chatcmpl-openai","object":"chat.completion.chunk","created":1710000000,"model":"gpt-4.1","choices":[{"index":0,"delta":{"content":"","reasoning_content":"hidden"}}],"usage":null}` + "\n\n")); err != nil {
			t.Fatalf("write fixture event: %v", err)
		}
		if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
			t.Fatalf("write done chunk: %v", err)
		}
	}))
	defer server.Close()

	openAIAdapter := NewAdapter(server.Client(), normalizer.NewRegistry(normalizer.Default{}))

	got := make([]adapter.ChatStreamChunk, 0)
	err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		ProviderSlug: "openai",
		BaseURL:      server.URL + "/v1",
		APIKey:       "test-secret",
		Timeout:      30 * time.Second,
	}, adapter.ChatRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hi"},
		},
	}, func(chunk adapter.ChatStreamChunk) error {
		got = append(got, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if got[0].Content != "" {
		t.Fatalf("got content %q, want empty content without deepseek normalizer", got[0].Content)
	}
}

func TestAdapterStreamChatCompletionsParsesLargeSSEEvent(t *testing.T) {
	largeContent := strings.Repeat("x", 1024*1024+128)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		chunk := chatCompletionStreamResponse{
			ID:    "chatcmpl_large",
			Model: "gpt-4.1",
			Choices: []chatStreamChoice{
				{Delta: chatStreamDelta{Content: largeContent}},
			},
		}
		payload, err := json.Marshal(chunk)
		if err != nil {
			t.Fatalf("marshal large chunk: %v", err)
		}
		if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
			t.Fatalf("write large stream chunk: %v", err)
		}
		if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
			t.Fatalf("write done chunk: %v", err)
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	got := make([]adapter.ChatStreamChunk, 0)
	err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, adapter.ChatRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hello"},
		},
	}, func(chunk adapter.ChatStreamChunk) error {
		got = append(got, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}
	if got[0].Content != largeContent {
		t.Fatalf("got large content length %d, want %d", len(got[0].Content), len(largeContent))
	}
}

func TestAdapterStreamChatCompletionsReturnsErrorForUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "upstream stream failed", http.StatusBadGateway)
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, adapter.ChatRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hello"},
		},
	}, func(chunk adapter.ChatStreamChunk) error {
		t.Fatalf("unexpected stream chunk: %+v", chunk)
		return nil
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if failure.CodeOf(err) != failure.CodeAdapterUpstreamStatus {
		t.Fatalf("expected failure code %q, got %q", failure.CodeAdapterUpstreamStatus, failure.CodeOf(err))
	}
}

func TestAdapterStreamChatCompletionsReturnsErrorForBadSSEJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		if _, err := w.Write([]byte("data: {bad json}\n\n")); err != nil {
			t.Fatalf("write stream chunk: %v", err)
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, adapter.ChatRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hello"},
		},
	}, func(chunk adapter.ChatStreamChunk) error {
		t.Fatalf("unexpected stream chunk: %+v", chunk)
		return nil
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if failure.CodeOf(err) != failure.CodeAdapterDecodeResponseFailed {
		t.Fatalf("expected failure code %q, got %q", failure.CodeAdapterDecodeResponseFailed, failure.CodeOf(err))
	}
}

func TestAdapterStreamChatCompletionsStopsWhenEmitReturnsError(t *testing.T) {
	emitErr := errors.New("emit failed")

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		chunks := []chatCompletionStreamResponse{
			{
				ID:    "chatcmpl_stream_test",
				Model: "gpt-4.1",
				Choices: []chatStreamChoice{
					{Delta: chatStreamDelta{Content: "first"}},
				},
			},
			{
				ID:    "chatcmpl_stream_test",
				Model: "gpt-4.1",
				Choices: []chatStreamChoice{
					{Delta: chatStreamDelta{Content: "second"}},
				},
			},
		}

		for _, chunk := range chunks {
			payload, err := json.Marshal(chunk)
			if err != nil {
				t.Fatalf("failed to marshal chunk: %v", err)
			}

			if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
				t.Fatalf("write stream chunk: %v", err)
			}
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	emitCalls := 0
	err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, adapter.ChatRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hello"},
		},
	}, func(chunk adapter.ChatStreamChunk) error {
		emitCalls++
		return emitErr
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	if !errors.Is(err, emitErr) {
		t.Fatalf("expected emit error wrapping %v, got %v", emitErr, err)
	}

	if failure.CodeOf(err) != failure.CodeAdapterEmitFailed {
		t.Fatalf("expected failure code %q, got %q", failure.CodeAdapterEmitFailed, failure.CodeOf(err))
	}

	if emitCalls != 1 {
		t.Fatalf("got %d emit calls, want 1", emitCalls)
	}
}

func TestAdapterStreamChatCompletionsStopsAtDone(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")

		firstChunk := chatCompletionStreamResponse{
			ID:    "chatcmpl_stream_test",
			Model: "gpt-4.1",
			Choices: []chatStreamChoice{
				{Delta: chatStreamDelta{Content: "before done"}},
			},
		}
		payload, err := json.Marshal(firstChunk)
		if err != nil {
			t.Fatalf("failed to marshal chunk: %v", err)
		}

		if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
			t.Fatalf("write first chunk: %v", err)
		}

		if _, err := w.Write([]byte("data: [DONE]\n\n")); err != nil {
			t.Fatalf("write done chunk: %v", err)
		}

		afterDoneChunk := chatCompletionStreamResponse{
			ID:    "chatcmpl_stream_test",
			Model: "gpt-4.1",
			Choices: []chatStreamChoice{
				{Delta: chatStreamDelta{Content: "after done"}},
			},
		}
		payload, err = json.Marshal(afterDoneChunk)
		if err != nil {
			t.Fatalf("failed to marshal chunk after done: %v", err)
		}

		// 这段模拟异常上游在 [DONE] 后继续输出，adapter 应该忽略后续内容。
		if _, err := w.Write([]byte("data: " + string(payload) + "\n\n")); err != nil {
			t.Fatalf("write chunk after done: %v", err)
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	got := make([]adapter.ChatStreamChunk, 0)
	err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL + "/v1",
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, adapter.ChatRequest{
		Model: "gpt-4.1",
		Messages: []adapter.ChatMessage{
			{Role: "user", Content: "hello"},
		},
	}, func(chunk adapter.ChatStreamChunk) error {
		got = append(got, chunk)
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if len(got) != 1 {
		t.Fatalf("got %d chunks, want 1", len(got))
	}

	if got[0].Content != "before done" {
		t.Fatalf("got content %q, want %q", got[0].Content, "before done")
	}
}
