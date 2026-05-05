package httpapi

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/httpx"
)

// handleChatCompletions 解析并校验 chat completions 请求，暂时返回 mock 响应。
func handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	var req ChatCompletionRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		_ = httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "invalid json body")
		return
	}

	if req.Model == "" {
		_ = httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "model is required")
		return
	}

	if len(req.Messages) == 0 {
		_ = httpx.WriteError(w, http.StatusBadRequest, "invalid_request", "messages is required")
		return
	}

	_ = httpx.WriteJSON(w, http.StatusOK, ChatCompletionResponse{
		ID:      "chatcmpl_mock",
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   req.Model,
		Choices: []ChatCompletionChoice{
			{
				Index: 0,
				Message: ChatMessage{
					Role:    "assistant",
					Content: "mock response",
				},
				FinishReason: "stop",
			},
		},
		Usage: ChatCompletionUsage{
			PromptTokens:     0,
			CompletionTokens: 0,
			TotalTokens:      0,
		},
	})
}
