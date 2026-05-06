package httpapi

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ThankCat/unio-api/internal/httpx"
)

// ChatCompletionService 定义 chat completions handler 依赖的业务能力。
type ChatCompletionService interface {
	CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error)
}

// chatCompletionsHandler 处理 OpenAI-compatible chat completions 请求。
type chatCompletionsHandler struct {
	service ChatCompletionService
}

// ServeHTTP 解析请求、调用 service，并写出 HTTP 响应。
func (h *chatCompletionsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
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

	resp, err := h.service.CreateChatCompletion(r.Context(), req)
	if err != nil {
		_ = httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "chat completion failed")
		return
	}

	_ = httpx.WriteJSON(w, http.StatusOK, resp)

}
