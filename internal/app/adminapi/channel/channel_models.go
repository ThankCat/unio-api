package channel

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
)

// ChannelModelService 定义 adminapi 操作 channel↔model 绑定所需的最小能力。
type ChannelModelService interface {
	List(ctx context.Context, channelID int64) ([]channelmodel.Binding, error)
	Create(ctx context.Context, in channelmodel.CreateInput) (channelmodel.Binding, error)
	Update(ctx context.Context, in channelmodel.UpdateInput) (channelmodel.Binding, error)
	Delete(ctx context.Context, channelID, modelID int64, confirmation supply.Confirmation) error
}

// channelModelDTO 是 channel↔model 绑定的 admin API 响应体。
// ModelExternalID / ModelDisplayName 仅列表场景有值；单条写入返回为空。
type channelModelDTO struct {
	ID                int64    `json:"id"`
	ChannelID         int64    `json:"channel_id"`
	ModelID           int64    `json:"model_id"`
	ModelExternalID   string   `json:"model_external_id"`
	ModelDisplayName  string   `json:"model_display_name"`
	ModelStatus       string   `json:"model_status"`
	UpstreamModel     string   `json:"upstream_model"`
	Status            string   `json:"status"`
	EffectiveBlockers []string `json:"effective_blockers"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

type createChannelModelRequest struct {
	ModelID       int64  `json:"model_id"`
	UpstreamModel string `json:"upstream_model"`
	Status        string `json:"status"`
}

type updateChannelModelRequest struct {
	UpstreamModel      string `json:"upstream_model"`
	Status             string `json:"status"`
	VerificationItemID *int64 `json:"verification_item_id"`
	// ConfirmSupplyImpact + ExpectedImpactFingerprint 是停用触发 Offering 联动时的
	// ADR-0019 影响确认与显式 Offering 选择；首次请求缺省，收到 409 后携带最新指纹重试。
	ConfirmSupplyImpact       bool                                       `json:"confirm_supply_impact"`
	ExpectedImpactFingerprint string                                     `json:"expected_impact_fingerprint"`
	SelectedOfferings         []adminhttp.SupplyOfferingSelectionRequest `json:"selected_offerings"`
}

type deleteChannelModelRequest struct {
	ConfirmSupplyImpact       bool                                       `json:"confirm_supply_impact"`
	ExpectedImpactFingerprint string                                     `json:"expected_impact_fingerprint"`
	SelectedOfferings         []adminhttp.SupplyOfferingSelectionRequest `json:"selected_offerings"`
}

type channelModelsHandler struct {
	service ChannelModelService
}

func (h *channelModelsHandler) list(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	bindings, err := h.service.List(r.Context(), channelID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]channelModelDTO, 0, len(bindings))
	for _, b := range bindings {
		dtos = append(dtos, toChannelModelDTO(b))
	}

	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *channelModelsHandler) create(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req createChannelModelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	b, err := h.service.Create(r.Context(), channelmodel.CreateInput{
		ChannelID:     channelID,
		ModelID:       req.ModelID,
		UpstreamModel: req.UpstreamModel,
		Status:        req.Status,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, toChannelModelDTO(b))
}

func (h *channelModelsHandler) update(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	modelID, err := pathModelID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req updateChannelModelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	b, err := h.service.Update(r.Context(), channelmodel.UpdateInput{
		ChannelID:          channelID,
		ModelID:            modelID,
		UpstreamModel:      req.UpstreamModel,
		Status:             req.Status,
		VerificationItemID: req.VerificationItemID,
		Confirmation: supply.Confirmation{
			Confirm:             req.ConfirmSupplyImpact,
			ExpectedFingerprint: req.ExpectedImpactFingerprint,
			SelectedOfferings:   adminhttp.SupplyOfferingSelections(req.SelectedOfferings),
		},
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toChannelModelDTO(b))
}

func (h *channelModelsHandler) delete(w http.ResponseWriter, r *http.Request) {
	channelID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	modelID, err := pathModelID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req deleteChannelModelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil && !errors.Is(err, httpx.ErrEmptyJSONBody) {
		adminhttp.WriteServiceError(w, err)
		return
	}
	// 兼容旧客户端的 query 确认参数；新客户端使用 JSON body 传递显式 Offering 选择。
	confirmation := supply.Confirmation{
		Confirm:             req.ConfirmSupplyImpact || adminhttp.BoolQuery(r, "confirm_supply_impact"),
		ExpectedFingerprint: req.ExpectedImpactFingerprint,
		SelectedOfferings:   adminhttp.SupplyOfferingSelections(req.SelectedOfferings),
	}
	if confirmation.ExpectedFingerprint == "" {
		confirmation.ExpectedFingerprint = adminhttp.QueryString(r, "expected_impact_fingerprint")
	}
	if err := h.service.Delete(r.Context(), channelID, modelID, confirmation); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func toChannelModelDTO(b channelmodel.Binding) channelModelDTO {
	return channelModelDTO{
		ID:                b.ID,
		ChannelID:         b.ChannelID,
		ModelID:           b.ModelID,
		ModelExternalID:   b.ModelExternalID,
		ModelDisplayName:  b.ModelDisplayName,
		ModelStatus:       b.ModelStatus,
		UpstreamModel:     b.UpstreamModel,
		Status:            b.Status,
		EffectiveBlockers: b.EffectiveBlockers,
		CreatedAt:         b.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:         b.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// pathModelID 解析路径参数 {modelId}（绑定子资源里的 Unio 模型 id）。
func pathModelID(r *http.Request) (int64, error) {
	raw := chi.URLParam(r, "modelId")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("modelId path parameter must be a positive integer"),
			failure.WithField("field", "modelId"),
		)
	}
	return id, nil
}
