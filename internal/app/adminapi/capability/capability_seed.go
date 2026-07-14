package capability

import (
	"context"
	"encoding/json"
	"net/http"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	capsvc "github.com/ThankCat/unio-api/internal/service/admin/capability"
)

// CapabilitySeedService 定义 adminapi 列出/物化 adapter 画像所需的最小能力（M5）。
type CapabilitySeedService interface {
	Profiles() []capsvc.Profile
	Materialize(ctx context.Context, modelID int64, profileKey, actor string) (capsvc.MaterializeResult, error)
}

// adapterProfileDTO 是可物化的 adapter 能力画像响应体。
type adapterProfileDTO struct {
	Key          string                  `json:"key"`
	Provider     string                  `json:"provider"`
	Protocol     string                  `json:"protocol"`
	Declarations []profileDeclarationDTO `json:"declarations"`
}

type profileDeclarationDTO struct {
	CapabilityKey string          `json:"capability_key"`
	SupportLevel  string          `json:"support_level"`
	Limits        json.RawMessage `json:"limits"`
}

// seedResultDTO 是一次 adapter 画像物化的结果摘要。
type seedResultDTO struct {
	ModelID      int64  `json:"model_id"`
	ProfileKey   string `json:"profile_key"`
	Provider     string `json:"provider"`
	Protocol     string `json:"protocol"`
	Materialized int    `json:"materialized"`
}

type seedRequest struct {
	ModelID    int64  `json:"model_id"`
	ProfileKey string `json:"profile_key"`
}

type capabilitySeedHandler struct {
	service CapabilitySeedService
}

func (h *capabilitySeedHandler) listProfiles(w http.ResponseWriter, _ *http.Request) {
	profiles := h.service.Profiles()
	dtos := make([]adapterProfileDTO, 0, len(profiles))
	for _, p := range profiles {
		dtos = append(dtos, toAdapterProfileDTO(p))
	}
	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *capabilitySeedHandler) materialize(w http.ResponseWriter, r *http.Request) {
	var req seedRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	result, err := h.service.Materialize(r.Context(), req.ModelID, req.ProfileKey, adminhttp.AdminActor(r))
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, seedResultDTO{
		ModelID:      result.ModelID,
		ProfileKey:   result.ProfileKey,
		Provider:     result.Provider,
		Protocol:     result.Protocol,
		Materialized: result.Materialized,
	})
}

func toAdapterProfileDTO(p capsvc.Profile) adapterProfileDTO {
	decls := make([]profileDeclarationDTO, 0, len(p.Declarations))
	for _, d := range p.Declarations {
		decls = append(decls, profileDeclarationDTO{
			CapabilityKey: string(d.Key),
			SupportLevel:  string(d.SupportLevel),
			Limits:        d.Limits,
		})
	}
	return adapterProfileDTO{
		Key:          p.Key,
		Provider:     p.Provider,
		Protocol:     p.Protocol,
		Declarations: decls,
	}
}
