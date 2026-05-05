package httpapi

import (
	"net/http"

	"github.com/ThankCat/unio-api/internal/httpx"
)

// handleModels 返回当前可用模型列表的 OpenAI-compatible 占位响应。
func handleModels(w http.ResponseWriter, r *http.Request) {
	_ = httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"object": "list",
		"data":   []any{},
	})
}
