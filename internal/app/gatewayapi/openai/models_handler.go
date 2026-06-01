package gatewayapi

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// ModelCatalogService 定义 /v1/models handler 依赖的模型目录能力。
type ModelCatalogService interface {
	ListAvailableModels(ctx context.Context, projectID int64) ([]modelcatalog.Model, error)
}

// modelsHandler 处理 OpenAI-compatible models API。
type modelsHandler struct {
	service ModelCatalogService
}

// modelsResponse 表示 OpenAI-compatible 模型列表响应。
type modelsResponse struct {
	Object string          `json:"object"`
	Data   []modelResponse `json:"data"`
}

// modelResponse 表示 OpenAI-compatible 单个模型响应。
type modelResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	OwnedBy string `json:"owned_by"`
}

// handleModels 返回当前可用模型列表的 OpenAI-compatible 响应。
func (h *modelsHandler) handleModels(w http.ResponseWriter, r *http.Request) {
	apiKeyPrincipal, ok := auth.APIKeyPrincipalFromContext(r.Context())
	if !ok {
		_ = httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing api key")
		return
	}

	models, err := h.service.ListAvailableModels(r.Context(), apiKeyPrincipal.ProjectID)
	if err != nil {
		_ = httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "list models failed")
		return
	}

	data := make([]modelResponse, 0, len(models))
	for _, model := range models {
		data = append(data, modelResponse{
			ID:      model.ID,
			Object:  "model",
			OwnedBy: model.OwnedBy,
		})
	}

	_ = httpx.WriteJSON(w, http.StatusOK, modelsResponse{
		Object: "list",
		Data:   data,
	})
}
