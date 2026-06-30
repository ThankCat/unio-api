package models

import (
	"context"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/modelcatalog"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// ModelCatalogService 定义 /v1/models handler 依赖的模型目录能力。
type ModelCatalogService interface {
	ListAvailableModels(ctx context.Context, userID int64, requiredCapabilities []string) ([]modelcatalog.Model, error)
}

// modelsHandler 处理 OpenAI-compatible models API。
type modelsHandler struct {
	service ModelCatalogService
}

// NewModelsHandler 构造 OpenAI models HTTP handler，供 gatewayapi router 挂载。
func NewModelsHandler(service ModelCatalogService) http.HandlerFunc {
	return (&modelsHandler{service: service}).handleModels
}

// modelsResponse 表示 OpenAI-compatible 模型列表响应。
type modelsResponse struct {
	Object string          `json:"object"`
	Data   []modelResponse `json:"data"`
}

// modelResponse 表示 OpenAI-compatible 单个模型响应。
//
// Capabilities 是 Unio 扩展的 cap-tags 数组（OpenAI 标准字段之外，SDK 忽略未知字段不受影响），
// 让客户预检模型能力；未声明能力的模型为空数组。
type modelResponse struct {
	ID           string   `json:"id"`
	Object       string   `json:"object"`
	OwnedBy      string   `json:"owned_by"`
	Capabilities []string `json:"capabilities"`
}

// handleModels 返回当前可用模型列表的 OpenAI-compatible 响应。
func (h *modelsHandler) handleModels(w http.ResponseWriter, r *http.Request) {
	apiKeyPrincipal, ok := auth.APIKeyPrincipalFromContext(r.Context())
	if !ok {
		_ = httpx.WriteError(w, http.StatusUnauthorized, "unauthorized", "missing api key")
		return
	}

	requiredCapabilities := parseCapabilityFilter(r.URL.Query().Get("capability"))

	models, err := h.service.ListAvailableModels(r.Context(), apiKeyPrincipal.UserID, requiredCapabilities)
	if err != nil {
		_ = httpx.WriteError(w, http.StatusInternalServerError, "internal_error", "list models failed")
		return
	}

	data := make([]modelResponse, 0, len(models))
	for _, model := range models {
		capabilities := model.Capabilities
		if capabilities == nil {
			capabilities = []string{}
		}
		data = append(data, modelResponse{
			ID:           model.ID,
			Object:       "model",
			OwnedBy:      model.OwnedBy,
			Capabilities: capabilities,
		})
	}

	_ = httpx.WriteJSON(w, http.StatusOK, modelsResponse{
		Object: "list",
		Data:   data,
	})
}

// parseCapabilityFilter 解析 ?capability=a,b,c 查询参数为去空的 cap 过滤集（AND 语义在 service 应用）。
func parseCapabilityFilter(raw string) []string {
	if strings.TrimSpace(raw) == "" {
		return nil
	}

	parts := strings.Split(raw, ",")
	filter := make([]string, 0, len(parts))
	for _, part := range parts {
		if trimmed := strings.TrimSpace(part); trimmed != "" {
			filter = append(filter, trimmed)
		}
	}

	return filter
}
