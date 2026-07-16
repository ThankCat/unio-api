package capability

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/go-chi/chi/v5"

	corecap "github.com/ThankCat/unio-gateway/internal/core/capability"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	capsvc "github.com/ThankCat/unio-gateway/internal/service/admin/capability"
)

// CapabilityService 定义 adminapi 操作能力数据所需的最小能力（M5）。
type CapabilityService interface {
	ListKeys(ctx context.Context) ([]corecap.CapabilityKey, error)
	GetKey(ctx context.Context, key string) (corecap.CapabilityKey, error)
	CreateKey(ctx context.Context, in capsvc.CreateCapabilityKeyInput) (corecap.CapabilityKey, error)
	UpdateKey(ctx context.Context, in capsvc.UpdateCapabilityKeyInput) (corecap.CapabilityKey, error)
	DeleteKey(ctx context.Context, key string) error
	ListModelCapabilities(ctx context.Context, modelID int64) ([]corecap.ModelCapability, error)
	SetModelCapability(ctx context.Context, in capsvc.SetModelCapabilityInput) (corecap.ModelCapability, error)
	ReplaceModelCapabilities(ctx context.Context, in capsvc.ReplaceModelCapabilitiesInput) ([]corecap.ModelCapability, error)
	DeleteModelCapability(ctx context.Context, modelID int64, key string) error
}

// capabilityKeyDTO 是能力 key 字典响应体（含中文描述与协议归属，供运维区分）。
type capabilityKeyDTO struct {
	Key           string `json:"key"`
	Domain        string `json:"domain"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
	SortOrder     int32  `json:"sort_order"`
	Deprecated    bool   `json:"deprecated"`
	ProtocolScope string `json:"protocol_scope"`
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

// replaceModelCapabilitiesRequest 是「批量整表覆盖」模型能力的请求体（DEC-024 §6.2）。
type replaceModelCapabilitiesRequest struct {
	Capabilities []modelCapabilityItemRequest `json:"capabilities"`
}

type modelCapabilityItemRequest struct {
	CapabilityKey string          `json:"capability_key"`
	SupportLevel  string          `json:"support_level"`
	Limits        json.RawMessage `json:"limits"`
}

type capabilitiesHandler struct {
	service CapabilityService
}

type createCapabilityKeyRequest struct {
	Key           string `json:"key"`
	Domain        string `json:"domain"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
	SortOrder     int32  `json:"sort_order"`
	Deprecated    bool   `json:"deprecated"`
	ProtocolScope string `json:"protocol_scope"`
}

type updateCapabilityKeyRequest struct {
	Domain        string `json:"domain"`
	DisplayName   string `json:"display_name"`
	Description   string `json:"description"`
	SortOrder     int32  `json:"sort_order"`
	Deprecated    bool   `json:"deprecated"`
	ProtocolScope string `json:"protocol_scope"`
}

func toCapabilityKeyDTO(k corecap.CapabilityKey) capabilityKeyDTO {
	return capabilityKeyDTO{
		Key:           string(k.Key),
		Domain:        k.Domain,
		DisplayName:   k.DisplayName,
		Description:   k.Description,
		SortOrder:     k.SortOrder,
		Deprecated:    k.Deprecated,
		ProtocolScope: string(k.ProtocolScope),
	}
}

func (h *capabilitiesHandler) listKeys(w http.ResponseWriter, r *http.Request) {
	keys, err := h.service.ListKeys(r.Context())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]capabilityKeyDTO, 0, len(keys))
	for _, k := range keys {
		dtos = append(dtos, toCapabilityKeyDTO(k))
	}
	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *capabilitiesHandler) createKey(w http.ResponseWriter, r *http.Request) {
	var req createCapabilityKeyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	item, err := h.service.CreateKey(r.Context(), capsvc.CreateCapabilityKeyInput{
		Key:           req.Key,
		Domain:        req.Domain,
		DisplayName:   req.DisplayName,
		Description:   req.Description,
		SortOrder:     req.SortOrder,
		Deprecated:    req.Deprecated,
		ProtocolScope: req.ProtocolScope,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, toCapabilityKeyDTO(item))
}

func (h *capabilitiesHandler) updateKey(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	var req updateCapabilityKeyRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	item, err := h.service.UpdateKey(r.Context(), capsvc.UpdateCapabilityKeyInput{
		Key:           key,
		Domain:        req.Domain,
		DisplayName:   req.DisplayName,
		Description:   req.Description,
		SortOrder:     req.SortOrder,
		Deprecated:    req.Deprecated,
		ProtocolScope: req.ProtocolScope,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toCapabilityKeyDTO(item))
}

func (h *capabilitiesHandler) deleteKey(w http.ResponseWriter, r *http.Request) {
	key := chi.URLParam(r, "key")
	if err := h.service.DeleteKey(r.Context(), key); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *capabilitiesHandler) listModelCapabilities(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	items, err := h.service.ListModelCapabilities(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]modelCapabilityDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toModelCapabilityDTO(item))
	}
	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *capabilitiesHandler) replaceModelCapabilities(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req replaceModelCapabilitiesRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	items := make([]capsvc.ModelCapabilityItem, 0, len(req.Capabilities))
	for _, c := range req.Capabilities {
		items = append(items, capsvc.ModelCapabilityItem{
			Key:          c.CapabilityKey,
			SupportLevel: c.SupportLevel,
			Limits:       c.Limits,
		})
	}

	result, err := h.service.ReplaceModelCapabilities(r.Context(), capsvc.ReplaceModelCapabilitiesInput{
		ModelID: id,
		Items:   items,
		Actor:   adminhttp.AdminActor(r),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]modelCapabilityDTO, 0, len(result))
	for _, item := range result {
		dtos = append(dtos, toModelCapabilityDTO(item))
	}
	adminhttp.WriteData(w, http.StatusOK, dtos)
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
