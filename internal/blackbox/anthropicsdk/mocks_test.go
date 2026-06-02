//go:build blackbox

package anthropicsdk_test

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// mockUpstream 是 fixture 用的 Anthropic Messages 兼容上游。
// adapter 调上游路径固定为 POST <base>/v1/messages（无论 stream 或非流）。
type mockUpstream struct {
	*httptest.Server

	calls int
}

// mockResponder 在每次接收到合法 POST /v1/messages 时被调用。
// path 已校验为 /v1/messages，method=POST；body 是完整请求体。
type mockResponder func(t *testing.T, w http.ResponseWriter, r *http.Request, body []byte)

// newMockUpstream 启一个上游 mock，所有合法请求按 respond 处理。
func newMockUpstream(t *testing.T, respond mockResponder) *mockUpstream {
	t.Helper()

	mock := &mockUpstream{}
	mock.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/v1/messages" {
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

// writeMockMessageResponse 写一个最小可解析的 Anthropic Messages JSON 响应（非流式）。
//
//	id          → message id
//	text        → 单个 text content block 内容
//	inputTokens → usage.input_tokens（uncached）
//	outputTokens→ usage.output_tokens
func writeMockMessageResponse(w http.ResponseWriter, id string, text string, inputTokens int, outputTokens int) {
	resp := map[string]any{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"model":         "deepseek-v4-flash",
		"stop_reason":   "end_turn",
		"stop_sequence": nil,
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"usage": map[string]any{
			"input_tokens":  inputTokens,
			"output_tokens": outputTokens,
		},
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("request-id", "req-anthropic-mock-1")
	_ = json.NewEncoder(w).Encode(resp)
}

// writeMockMessageResponseRaw 把 resp 直接当响应体写出，供需要自定义 content blocks
// （thinking / tool_use / 多 block 混合）的用例使用。
func writeMockMessageResponseRaw(w http.ResponseWriter, resp map[string]any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("request-id", "req-anthropic-mock-1")
	_ = json.NewEncoder(w).Encode(resp)
}

// writeMockMessageStream 按 Anthropic SSE 命名事件序写一段标准流（单 text block）：
//
//	message_start → content_block_start → content_block_delta(*) → content_block_stop
//	→ message_delta(stop_reason+usage) → message_stop
//
// 不发送 ping 事件；usage.output_tokens 仅出现在 message_delta（匹配 Anthropic 文档）。
func writeMockMessageStream(w http.ResponseWriter, id string, deltas []string, inputTokens int, outputTokens int) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("request-id", "req-anthropic-mock-stream-1")
	flusher, _ := w.(http.Flusher)

	// message_start 携带初始 usage（input_tokens + output_tokens=0）。
	writeNamedEvent(w, flusher, "message_start", map[string]any{
		"type": "message_start",
		"message": map[string]any{
			"id":            id,
			"type":          "message",
			"role":          "assistant",
			"model":         "deepseek-v4-flash",
			"content":       []any{},
			"stop_reason":   nil,
			"stop_sequence": nil,
			"usage": map[string]any{
				"input_tokens":  inputTokens,
				"output_tokens": 0,
			},
		},
	})

	writeNamedEvent(w, flusher, "content_block_start", map[string]any{
		"type":          "content_block_start",
		"index":         0,
		"content_block": map[string]any{"type": "text", "text": ""},
	})

	for _, delta := range deltas {
		writeNamedEvent(w, flusher, "content_block_delta", map[string]any{
			"type":  "content_block_delta",
			"index": 0,
			"delta": map[string]any{"type": "text_delta", "text": delta},
		})
	}

	writeNamedEvent(w, flusher, "content_block_stop", map[string]any{
		"type":  "content_block_stop",
		"index": 0,
	})

	writeNamedEvent(w, flusher, "message_delta", map[string]any{
		"type":  "message_delta",
		"delta": map[string]any{"stop_reason": "end_turn", "stop_sequence": nil},
		"usage": map[string]any{"output_tokens": outputTokens},
	})

	writeNamedEvent(w, flusher, "message_stop", map[string]any{
		"type": "message_stop",
	})
}

// writeNamedEvent 按 Anthropic SSE 命名事件协议写一帧：
//
//	event: <name>\n
//	data: <json>\n\n
func writeNamedEvent(w http.ResponseWriter, flusher http.Flusher, eventName string, payload any) {
	data, _ := json.Marshal(payload)
	var sb strings.Builder
	sb.WriteString("event: ")
	sb.WriteString(eventName)
	sb.WriteString("\n")
	sb.WriteString("data: ")
	sb.Write(data)
	sb.WriteString("\n\n")
	_, _ = io.WriteString(w, sb.String())
	if flusher != nil {
		flusher.Flush()
	}
}
