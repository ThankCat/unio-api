//go:build blackbox

package openaisdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// mockUpstream 是 fixture 用的 OpenAI Chat Completions 兼容上游。
type mockUpstream struct {
	*httptest.Server

	// 调用次数（多 channel fallback 测试用）。
	calls int
}

// mockResponder 在每次接收到合法 POST /v1/chat/completions 时被调用。
// path 已校验为 /v1/chat/completions，method=POST。
// body 是完整请求体。
type mockResponder func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte)

// newMockUpstream 启一个上游 mock，所有合法请求按 respond 处理。
func newMockUpstream(t *testing.T, respond mockResponder) *mockUpstream {
	t.Helper()

	mock := &mockUpstream{}
	mock.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/chat/completions" {
			http.Error(w, fmt.Sprintf("unexpected upstream: %s %s", r.Method, r.URL.Path), http.StatusNotFound)
			return
		}
		body, err := io.ReadAll(r.Body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		mock.calls++
		respond(t, w, r, body)
	}))

	return mock
}

// writeMockChatCompletion 写一个最小可解析的 OpenAI Chat Completion JSON 响应。
func writeMockChatCompletion(w http.ResponseWriter, id string, content string, promptTokens int, completionTokens int) {
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "deepseek-v4-flash",
		"choices": []map[string]any{{
			"index": 0,
			"message": map[string]any{
				"role":    "assistant",
				"content": content,
			},
			"finish_reason": "stop",
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-ID", "req-mock-1")
	_ = json.NewEncoder(w).Encode(resp)
}

// writeMockChatCompletionWithMessage 写带自定义 message 的响应（用于 reasoning_content / tool_calls）。
func writeMockChatCompletionWithMessage(w http.ResponseWriter, id string, message map[string]any, finishReason string, promptTokens int, completionTokens int) {
	resp := map[string]any{
		"id":      id,
		"object":  "chat.completion",
		"created": time.Now().Unix(),
		"model":   "deepseek-v4-flash",
		"choices": []map[string]any{{
			"index":         0,
			"message":       message,
			"finish_reason": finishReason,
		}},
		"usage": map[string]any{
			"prompt_tokens":     promptTokens,
			"completion_tokens": completionTokens,
			"total_tokens":      promptTokens + completionTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// writeMockStreamChunks 把 chunks 按 SSE 协议（每条 `data: <json>\n\n`）写出，
// 最后写 `data: [DONE]\n\n`。usageChunk 不为 nil 时作为最后一条 data chunk 写出（含 usage）。
func writeMockStreamChunks(w http.ResponseWriter, id string, chunks []map[string]any, usageChunk map[string]any) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	flusher, _ := w.(http.Flusher)

	created := time.Now().Unix()
	for _, delta := range chunks {
		chunk := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   "deepseek-v4-flash",
			"choices": []map[string]any{{
				"index":         0,
				"delta":         delta,
				"finish_reason": nil,
			}},
		}
		writeSSEData(w, flusher, chunk)
	}

	// 最后一条带 finish_reason 的 chunk（OpenAI 协议：finish_reason 在 choices 上）。
	finishChunk := map[string]any{
		"id":      id,
		"object":  "chat.completion.chunk",
		"created": created,
		"model":   "deepseek-v4-flash",
		"choices": []map[string]any{{
			"index":         0,
			"delta":         map[string]any{},
			"finish_reason": "stop",
		}},
	}
	writeSSEData(w, flusher, finishChunk)

	if usageChunk != nil {
		usage := map[string]any{
			"id":      id,
			"object":  "chat.completion.chunk",
			"created": created,
			"model":   "deepseek-v4-flash",
			"choices": []map[string]any{},
			"usage":   usageChunk,
		}
		writeSSEData(w, flusher, usage)
	}

	_, _ = io.WriteString(w, "data: [DONE]\n\n")
	if flusher != nil {
		flusher.Flush()
	}
}

func writeSSEData(w http.ResponseWriter, flusher http.Flusher, payload any) {
	data, _ := json.Marshal(payload)
	var sb strings.Builder
	sb.WriteString("data: ")
	sb.Write(data)
	sb.WriteString("\n\n")
	_, _ = io.WriteString(w, sb.String())
	if flusher != nil {
		flusher.Flush()
	}
}
