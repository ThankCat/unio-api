package chatcompletions

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/channel"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// TestAdapterChatCompletionsClassifiesUpstreamStatus 验证上游不同状态码被映射成稳定上游分类，
// 并携带真实 status code 和 x-request-id，同时保持 failure.CodeOf 不变。
func TestAdapterChatCompletionsClassifiesUpstreamStatus(t *testing.T) {
	tests := []struct {
		name         string
		statusCode   int
		wantCategory adapter.UpstreamErrorCategory
	}{
		{"401 unauthorized", http.StatusUnauthorized, adapter.UpstreamErrorAuth},
		{"403 forbidden", http.StatusForbidden, adapter.UpstreamErrorPermission},
		{"429 too many requests", http.StatusTooManyRequests, adapter.UpstreamErrorRateLimit},
		{"408 request timeout", http.StatusRequestTimeout, adapter.UpstreamErrorTimeout},
		{"400 bad request", http.StatusBadRequest, adapter.UpstreamErrorBadRequest},
		{"422 unprocessable", http.StatusUnprocessableEntity, adapter.UpstreamErrorBadRequest},
		{"500 server error", http.StatusInternalServerError, adapter.UpstreamErrorServer},
		{"502 bad gateway", http.StatusBadGateway, adapter.UpstreamErrorServer},
		{"503 unavailable", http.StatusServiceUnavailable, adapter.UpstreamErrorServer},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("X-Request-Id", "req-upstream-1")
				http.Error(w, "upstream failed", tc.statusCode)
			}))
			defer server.Close()

			openAIAdapter := newTestAdapter(server.Client())

			_, err := openAIAdapter.ChatCompletions(context.Background(), channel.Runtime{
				BaseURL: server.URL,
				APIKey:  "test-secret",
				Timeout: 30 * time.Second,
			}, ChatRequest{
				Model: "gpt-4.1",
				Messages: []ChatMessage{
					{Role: "user", Content: jsonContent("hello")},
				},
			})
			if err == nil {
				t.Fatal("expected error, got nil")
			}

			// 上游分类必须正确。
			category, ok := adapter.UpstreamCategoryOf(err)
			if !ok {
				t.Fatal("expected error to carry upstream category")
			}
			if category != tc.wantCategory {
				t.Fatalf("category: got %q, want %q", category, tc.wantCategory)
			}

			// metadata 必须带真实 status code 和 x-request-id。
			meta, ok := adapter.UpstreamMetadataOf(err)
			if !ok {
				t.Fatal("expected error to carry upstream metadata")
			}
			if meta.StatusCode != tc.statusCode {
				t.Fatalf("metadata status: got %d, want %d", meta.StatusCode, tc.statusCode)
			}
			if meta.RequestID != "req-upstream-1" {
				t.Fatalf("metadata request id: got %q, want %q", meta.RequestID, "req-upstream-1")
			}

			// 既有 failure code 不变，request/attempt error_code 写入逻辑无需改动。
			if failure.CodeOf(err) != failure.CodeAdapterUpstreamStatus {
				t.Fatalf("failure code: got %q, want %q", failure.CodeOf(err), failure.CodeAdapterUpstreamStatus)
			}
		})
	}
}

// TestAdapterChatCompletionsClassifiesTimeoutAsUpstreamCategory 验证发送阶段 deadline 超时被归类为 timeout。
func TestAdapterChatCompletionsClassifiesTimeoutAsUpstreamCategory(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(200 * time.Millisecond)
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	_, err := openAIAdapter.ChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL,
		APIKey:  "test-secret",
		Timeout: 50 * time.Millisecond,
	}, ChatRequest{
		Model: "gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("hello")},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	category, ok := adapter.UpstreamCategoryOf(err)
	if !ok {
		t.Fatal("expected error to carry upstream category")
	}
	if category != adapter.UpstreamErrorTimeout {
		t.Fatalf("category: got %q, want %q", category, adapter.UpstreamErrorTimeout)
	}

	// 发送阶段没有 HTTP 响应，status code 必须为 0。
	meta, ok := adapter.UpstreamMetadataOf(err)
	if !ok {
		t.Fatal("expected error to carry upstream metadata")
	}
	if meta.StatusCode != 0 {
		t.Fatalf("metadata status: got %d, want 0", meta.StatusCode)
	}

	if failure.CodeOf(err) != failure.CodeAdapterSendRequestFailed {
		t.Fatalf("failure code: got %q, want %q", failure.CodeOf(err), failure.CodeAdapterSendRequestFailed)
	}
}

// TestAdapterChatCompletionsClassifiesCanceledAsUpstreamCategory 验证客户端取消被归类为 canceled。
func TestAdapterChatCompletionsClassifiesCanceledAsUpstreamCategory(t *testing.T) {
	started := make(chan struct{})
	release := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		<-release
		w.WriteHeader(http.StatusOK)
	}))
	defer server.Close()
	defer close(release)

	openAIAdapter := newTestAdapter(server.Client())

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		<-started
		cancel()
	}()

	_, err := openAIAdapter.ChatCompletions(ctx, channel.Runtime{
		BaseURL: server.URL,
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, ChatRequest{
		Model: "gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("hello")},
		},
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	category, ok := adapter.UpstreamCategoryOf(err)
	if !ok {
		t.Fatal("expected error to carry upstream category")
	}
	if category != adapter.UpstreamErrorCanceled {
		t.Fatalf("category: got %q, want %q", category, adapter.UpstreamErrorCanceled)
	}
}

// TestAdapterChatCompletionsPopulatesSuccessMetadata 验证非流式成功响应带真实 upstream status/request id。
func TestAdapterChatCompletionsPopulatesSuccessMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-success-1")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		if _, err := w.Write([]byte(`{
			"id": "chatcmpl_success",
			"model": "gpt-4.1",
			"choices": [{"message": {"role": "assistant", "content": "hi"}}],
			"usage": {
				"prompt_tokens": 5,
				"completion_tokens": 6,
				"total_tokens": 11
			}
		}`)); err != nil {
			t.Fatalf("write response body: %v", err)
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	got, err := openAIAdapter.ChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL,
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, ChatRequest{
		Model: "gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("hello")},
		},
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if got.Upstream.StatusCode != http.StatusOK {
		t.Fatalf("upstream status: got %d, want %d", got.Upstream.StatusCode, http.StatusOK)
	}
	if got.Upstream.RequestID != "req-success-1" {
		t.Fatalf("upstream request id: got %q, want %q", got.Upstream.RequestID, "req-success-1")
	}
}

// TestAdapterStreamChatCompletionsPopulatesUsageChunkMetadata 验证流式 final usage chunk 带真实 upstream metadata。
func TestAdapterStreamChatCompletionsPopulatesUsageChunkMetadata(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-stream-success-1")
		w.Header().Set("Content-Type", "text/event-stream")
		events := []string{
			`data: {"id":"chatcmpl_stream","model":"gpt-4.1","choices":[{"delta":{"role":"assistant","content":"hi"},"finish_reason":null}]}` + "\n\n",
			`data: {"id":"chatcmpl_stream","model":"gpt-4.1","choices":[],"usage":{"prompt_tokens":5,"completion_tokens":6,"total_tokens":11}}` + "\n\n",
			"data: [DONE]\n\n",
		}
		for _, event := range events {
			if _, err := w.Write([]byte(event)); err != nil {
				t.Fatalf("write stream event: %v", err)
			}
		}
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	var usageChunk *ChatStreamChunk
	_, err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL,
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, ChatRequest{
		Model: "gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("hello")},
		},
	}, func(chunk ChatStreamChunk) error {
		if chunk.Usage != nil {
			copied := chunk
			usageChunk = &copied
		}
		return nil
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if usageChunk == nil {
		t.Fatal("expected a final usage chunk")
	}
	if usageChunk.Upstream == nil {
		t.Fatal("expected usage chunk to carry upstream metadata")
	}
	if usageChunk.Upstream.StatusCode != http.StatusOK {
		t.Fatalf("upstream status: got %d, want %d", usageChunk.Upstream.StatusCode, http.StatusOK)
	}
	if usageChunk.Upstream.RequestID != "req-stream-success-1" {
		t.Fatalf("upstream request id: got %q, want %q", usageChunk.Upstream.RequestID, "req-stream-success-1")
	}
}

// TestAdapterStreamChatCompletionsClassifiesUpstreamStatus 验证流式上游非 2xx 也带稳定分类和 metadata。
func TestAdapterStreamChatCompletionsClassifiesUpstreamStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", "req-stream-1")
		http.Error(w, "upstream stream failed", http.StatusTooManyRequests)
	}))
	defer server.Close()

	openAIAdapter := newTestAdapter(server.Client())

	_, err := openAIAdapter.StreamChatCompletions(context.Background(), channel.Runtime{
		BaseURL: server.URL,
		APIKey:  "test-secret",
		Timeout: 30 * time.Second,
	}, ChatRequest{
		Model: "gpt-4.1",
		Messages: []ChatMessage{
			{Role: "user", Content: jsonContent("hello")},
		},
	}, func(chunk ChatStreamChunk) error {
		t.Fatalf("unexpected stream chunk: %+v", chunk)
		return nil
	})
	if err == nil {
		t.Fatal("expected error, got nil")
	}

	category, ok := adapter.UpstreamCategoryOf(err)
	if !ok {
		t.Fatal("expected error to carry upstream category")
	}
	if category != adapter.UpstreamErrorRateLimit {
		t.Fatalf("category: got %q, want %q", category, adapter.UpstreamErrorRateLimit)
	}

	meta, ok := adapter.UpstreamMetadataOf(err)
	if !ok {
		t.Fatal("expected error to carry upstream metadata")
	}
	if meta.StatusCode != http.StatusTooManyRequests {
		t.Fatalf("metadata status: got %d, want %d", meta.StatusCode, http.StatusTooManyRequests)
	}
	if meta.RequestID != "req-stream-1" {
		t.Fatalf("metadata request id: got %q, want %q", meta.RequestID, "req-stream-1")
	}

	if failure.CodeOf(err) != failure.CodeAdapterUpstreamStatus {
		t.Fatalf("failure code: got %q, want %q", failure.CodeOf(err), failure.CodeAdapterUpstreamStatus)
	}
}
