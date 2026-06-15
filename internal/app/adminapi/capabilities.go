package adminapi

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/core/adminauth"
	corecap "github.com/ThankCat/unio-api/internal/core/capability"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	capsvc "github.com/ThankCat/unio-api/internal/service/admin/capability"
)

// CapabilityService 定义 adminapi 操作能力数据所需的最小能力（M5）。
type CapabilityService interface {
	Keys() []string
	ListModelCapabilities(ctx context.Context, modelID int64) ([]corecap.ModelCapability, error)
	SetModelCapability(ctx context.Context, in capsvc.SetModelCapabilityInput) (corecap.ModelCapability, error)
	DeleteModelCapability(ctx context.Context, modelID int64, key string) error
	ListChannelOverrides(ctx context.Context, channelID int64) ([]corecap.ChannelOverride, error)
	SetChannelOverride(ctx context.Context, in capsvc.SetChannelOverrideInput) (corecap.ChannelOverride, error)
	DeleteChannelOverride(ctx context.Context, channelID int64, key string) error
}

// modelCapabilityDTO 是模型能力声明响应体；limits 原样回传（无则为 null）。
type modelCapabilityDTO struct {
	ModelID       int64           `json:"model_id"`
	CapabilityKey string          `json:"capability_key"`
	SupportLevel  string          `json:"support_level"`
	Limits        json.RawMessage `json:"limits"`
	UpdatedBy     *string         `json:"updated_by"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

// channelOverrideDTO 是渠道收紧策略响应体；support_level 只会是 limited / unsupported。
type channelOverrideDTO struct {
	ChannelID     int64           `json:"channel_id"`
	CapabilityKey string          `json:"capability_key"`
	SupportLevel  string          `json:"support_level"`
	Limits        json.RawMessage `json:"limits"`
	Reason        *string         `json:"reason"`
	UpdatedBy     *string         `json:"updated_by"`
	CreatedAt     string          `json:"created_at"`
	UpdatedAt     string          `json:"updated_at"`
}

type setModelCapabilityRequest struct {
	SupportLevel string          `json:"support_level"`
	Limits       json.RawMessage `json:"limits"`
}

type setChannelOverrideRequest struct {
	SupportLevel string          `json:"support_level"`
	Limits       json.RawMessage `json:"limits"`
	Reason       string          `json:"reason"`
}

type capabilitiesHandler struct {
	service CapabilityService
}

func (h *capabilitiesHandler) listKeys(w http.ResponseWriter, _ *http.Request) {
	writeData(w, http.StatusOK, h.service.Keys())
}

func (h *capabilitiesHandler) listModelCapabilities(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	items, err := h.service.ListModelCapabilities(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]modelCapabilityDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toModelCapabilityDTO(item))
	}
	writeData(w, http.StatusOK, dtos)
}

func (h *capabilitiesHandler) setModelCapability(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req setModelCapabilityRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	item, err := h.service.SetModelCapability(r.Context(), capsvc.SetModelCapabilityInput{
		ModelID:      id,
		Key:          chi.URLParam(r, "key"),
		SupportLevel: req.SupportLevel,
		Limits:       req.Limits,
		Actor:        adminActor(r),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toModelCapabilityDTO(item))
}

func (h *capabilitiesHandler) deleteModelCapability(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	if err := h.service.DeleteModelCapability(r.Context(), id, chi.URLParam(r, "key")); err != nil {
		writeServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func (h *capabilitiesHandler) listChannelOverrides(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	items, err := h.service.ListChannelOverrides(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]channelOverrideDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toChannelOverrideDTO(item))
	}
	writeData(w, http.StatusOK, dtos)
}

func (h *capabilitiesHandler) setChannelOverride(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req setChannelOverrideRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	item, err := h.service.SetChannelOverride(r.Context(), capsvc.SetChannelOverrideInput{
		ChannelID:    id,
		Key:          chi.URLParam(r, "key"),
		SupportLevel: req.SupportLevel,
		Limits:       req.Limits,
		Reason:       req.Reason,
		Actor:        adminActor(r),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toChannelOverrideDTO(item))
}

func (h *capabilitiesHandler) deleteChannelOverride(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	if err := h.service.DeleteChannelOverride(r.Context(), id, chi.URLParam(r, "key")); err != nil {
		writeServiceError(w, err)
		return
	}

	w.WriteHeader(http.StatusNoContent)
}

func toModelCapabilityDTO(c corecap.ModelCapability) modelCapabilityDTO {
	return modelCapabilityDTO{
		ModelID:       c.ModelID,
		CapabilityKey: string(c.Key),
		SupportLevel:  string(c.SupportLevel),
		Limits:        c.Limits,
		UpdatedBy:     c.UpdatedBy,
		CreatedAt:     c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

func toChannelOverrideDTO(c corecap.ChannelOverride) channelOverrideDTO {
	return channelOverrideDTO{
		ChannelID:     c.ChannelID,
		CapabilityKey: string(c.Key),
		SupportLevel:  string(c.SupportLevel),
		Limits:        c.Limits,
		Reason:        c.Reason,
		UpdatedBy:     c.UpdatedBy,
		CreatedAt:     c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:     c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}

// adminActor 从 admin 认证身份取调用者标识，用于能力写入的 updated_by 审计；缺失回退空串。
func adminActor(r *http.Request) string {
	if principal, ok := adminauth.PrincipalFromContext(r.Context()); ok && principal != nil {
		return principal.Subject
	}
	return ""
}
