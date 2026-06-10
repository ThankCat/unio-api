package adminapi

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/channel"
)

// ChannelService 定义 adminapi 操作 channel 所需的最小能力。
type ChannelService interface {
	List(ctx context.Context, params channel.ListParams) (channel.ListResult, error)
	Get(ctx context.Context, id int64) (channel.Channel, error)
	Create(ctx context.Context, in channel.CreateInput) (channel.Channel, error)
	Update(ctx context.Context, in channel.UpdateInput) (channel.Channel, error)
	RotateCredential(ctx context.Context, in channel.RotateCredentialInput) error
}

// channelDTO 是 channel 的 admin API 响应体；不含上游凭据（只写不回）。
// ProviderName 仅分页列表场景有值；单条读取/写入返回为空。
type channelDTO struct {
	ID           int64  `json:"id"`
	ProviderID   int64  `json:"provider_id"`
	ProviderName string `json:"provider_name"`
	Name         string `json:"name"`
	Protocol     string `json:"protocol"`
	AdapterKey   string `json:"adapter_key"`
	BaseURL      string `json:"base_url"`
	Status       string `json:"status"`
	Priority     int32  `json:"priority"`
	TimeoutMs    *int32 `json:"timeout_ms"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type createChannelRequest struct {
	ProviderID int64  `json:"provider_id"`
	Name       string `json:"name"`
	Protocol   string `json:"protocol"`
	AdapterKey string `json:"adapter_key"`
	BaseURL    string `json:"base_url"`
	Credential string `json:"credential"`
	Status     string `json:"status"`
	Priority   int32  `json:"priority"`
	TimeoutMs  *int32 `json:"timeout_ms"`
}

type updateChannelRequest struct {
	Name      string `json:"name"`
	BaseURL   string `json:"base_url"`
	Status    string `json:"status"`
	Priority  int32  `json:"priority"`
	TimeoutMs *int32 `json:"timeout_ms"`
}

type rotateChannelCredentialRequest struct {
	Credential string `json:"credential"`
}

type channelsHandler struct {
	service ChannelService
}

func (h *channelsHandler) list(w http.ResponseWriter, r *http.Request) {
	providerID := int64(0)
	if raw := strings.TrimSpace(r.URL.Query().Get("provider_id")); raw != "" {
		parsed, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || parsed <= 0 {
			writeServiceError(w, failure.New(
				failure.CodeAdminInvalidArgument,
				failure.WithMessage("provider_id query must be a positive integer"),
				failure.WithField("field", "provider_id"),
			))
			return
		}
		providerID = parsed
	}

	page := parsePage(r)

	result, err := h.service.List(r.Context(), channel.ListParams{
		ProviderID: providerID,
		Status:     listStatus(r),
		Query:      strings.TrimSpace(r.URL.Query().Get("q")),
		Limit:      page.Limit(),
		Offset:     page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]channelDTO, 0, len(result.Items))
	for _, c := range result.Items {
		dtos = append(dtos, toChannelDTO(c))
	}

	writeList(w, http.StatusOK, dtos, page, result.Total)
}

func (h *channelsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	c, err := h.service.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toChannelDTO(c))
}

func (h *channelsHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createChannelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	c, err := h.service.Create(r.Context(), channel.CreateInput{
		ProviderID: req.ProviderID,
		Name:       req.Name,
		Protocol:   req.Protocol,
		AdapterKey: req.AdapterKey,
		BaseURL:    req.BaseURL,
		Credential: req.Credential,
		Status:     req.Status,
		Priority:   req.Priority,
		TimeoutMs:  req.TimeoutMs,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusCreated, toChannelDTO(c))
}

func (h *channelsHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req updateChannelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	c, err := h.service.Update(r.Context(), channel.UpdateInput{
		ID:        id,
		Name:      req.Name,
		BaseURL:   req.BaseURL,
		Status:    req.Status,
		Priority:  req.Priority,
		TimeoutMs: req.TimeoutMs,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toChannelDTO(c))
}

func (h *channelsHandler) rotateCredential(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	var req rotateChannelCredentialRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}

	if err := h.service.RotateCredential(r.Context(), channel.RotateCredentialInput{
		ID:         id,
		Credential: req.Credential,
	}); err != nil {
		writeServiceError(w, err)
		return
	}

	// 凭据只写不回；轮换成功返回 204 无响应体。
	w.WriteHeader(http.StatusNoContent)
}

func toChannelDTO(c channel.Channel) channelDTO {
	return channelDTO{
		ID:           c.ID,
		ProviderID:   c.ProviderID,
		ProviderName: c.ProviderName,
		Name:         c.Name,
		Protocol:     c.Protocol,
		AdapterKey:   c.AdapterKey,
		BaseURL:      c.BaseURL,
		Status:       c.Status,
		Priority:     c.Priority,
		TimeoutMs:    c.TimeoutMs,
		CreatedAt:    c.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:    c.UpdatedAt.UTC().Format(time.RFC3339),
	}
}
