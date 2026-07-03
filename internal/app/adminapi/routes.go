package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/httpx"
	"github.com/ThankCat/unio-api/internal/service/admin/route"
)

// RouteService 定义 adminapi 操作线路（routes / 渠道商品）所需的最小能力（阶段 15）。
type RouteService interface {
	List(ctx context.Context) ([]route.Route, error)
	Get(ctx context.Context, id int64) (route.Route, error)
	Create(ctx context.Context, in route.CreateInput) (route.Route, error)
	Update(ctx context.Context, in route.UpdateInput) (route.Route, error)
	Delete(ctx context.Context, id int64) error
	SetChannels(ctx context.Context, id int64, channelIDs []int64) (route.Route, error)
}

type routeDTO struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Mode     string `json:"mode"`
	PoolKind string `json:"pool_kind"`
	Status   string `json:"status"`
	// PriceRatio 客户售价倍率（DEC-026：客户售价 = 模型基准价 × 倍率），十进制字符串。
	PriceRatio string `json:"price_ratio"`
	// RPM/TPM/RPDLimit 线路级限流上限（DEC-027：按 (线路,用户) 计数）；null=继承全局默认，0=不限，>0=上限。
	RPMLimit    *int64            `json:"rpm_limit"`
	TPMLimit    *int64            `json:"tpm_limit"`
	RPDLimit    *int64            `json:"rpd_limit"`
	Description *string           `json:"description"`
	Channels    []routeChannelDTO `json:"channels"`
	CreatedAt   string            `json:"created_at"`
	UpdatedAt   string            `json:"updated_at"`
}

type routeChannelDTO struct {
	ChannelID    int64  `json:"channel_id"`
	ChannelName  string `json:"channel_name"`
	ProviderID   int64  `json:"provider_id"`
	ProviderSlug string `json:"provider_slug"`
}

type createRouteRequest struct {
	Name        string  `json:"name"`
	Mode        string  `json:"mode"`
	PoolKind    string  `json:"pool_kind"`
	Status      string  `json:"status"`
	PriceRatio  string  `json:"price_ratio"` // 客户售价倍率（十进制字符串，空=默认 1.0）
	RPMLimit    *int64  `json:"rpm_limit"`   // 线路级限流（null=继承默认，0=不限，>0=上限）
	TPMLimit    *int64  `json:"tpm_limit"`
	RPDLimit    *int64  `json:"rpd_limit"`
	Description *string `json:"description"`
	ChannelIDs  []int64 `json:"channel_ids"`
}

type updateRouteRequest struct {
	Name        string  `json:"name"`
	Mode        string  `json:"mode"`
	PoolKind    string  `json:"pool_kind"`
	Status      string  `json:"status"`
	PriceRatio  string  `json:"price_ratio"` // 客户售价倍率（十进制字符串，空=默认 1.0）
	RPMLimit    *int64  `json:"rpm_limit"`   // 线路级限流（null=继承默认，0=不限，>0=上限）
	TPMLimit    *int64  `json:"tpm_limit"`
	RPDLimit    *int64  `json:"rpd_limit"`
	Description *string `json:"description"`
	ChannelIDs  []int64 `json:"channel_ids"`
}

type setRouteChannelsRequest struct {
	ChannelIDs []int64 `json:"channel_ids"`
}

type routesHandler struct {
	service RouteService
}

func (h *routesHandler) list(w http.ResponseWriter, r *http.Request) {
	routes, err := h.service.List(r.Context())
	if err != nil {
		writeServiceError(w, err)
		return
	}
	dtos := make([]routeDTO, 0, len(routes))
	for _, rt := range routes {
		dtos = append(dtos, toRouteDTO(rt))
	}
	writeData(w, http.StatusOK, dtos)
}

func (h *routesHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	rt, err := h.service.Get(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, toRouteDTO(rt))
}

func (h *routesHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createRouteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}
	rt, err := h.service.Create(r.Context(), route.CreateInput{
		Name:        req.Name,
		Mode:        req.Mode,
		PoolKind:    req.PoolKind,
		Status:      req.Status,
		PriceRatio:  req.PriceRatio,
		RPMLimit:    req.RPMLimit,
		TPMLimit:    req.TPMLimit,
		RPDLimit:    req.RPDLimit,
		Description: req.Description,
		ChannelIDs:  req.ChannelIDs,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusCreated, toRouteDTO(rt))
}

func (h *routesHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	var req updateRouteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeServiceError(w, err)
		return
	}
	rt, err := h.service.Update(r.Context(), route.UpdateInput{
		ID:          id,
		Name:        req.Name,
		Mode:        req.Mode,
		PoolKind:    req.PoolKind,
		Status:      req.Status,
		PriceRatio:  req.PriceRatio,
		RPMLimit:    req.RPMLimit,
		TPMLimit:    req.TPMLimit,
		RPDLimit:    req.RPDLimit,
		Description: req.Description,
		ChannelIDs:  req.ChannelIDs,
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, toRouteDTO(rt))
}

func (h *routesHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	if err := h.service.Delete(r.Context(), id); err != nil {
		writeServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func toRouteDTO(rt route.Route) routeDTO {
	channels := make([]routeChannelDTO, 0, len(rt.Channels))
	for _, c := range rt.Channels {
		channels = append(channels, routeChannelDTO{
			ChannelID:    c.ChannelID,
			ChannelName:  c.ChannelName,
			ProviderID:   c.ProviderID,
			ProviderSlug: c.ProviderSlug,
		})
	}
	return routeDTO{
		ID:          rt.ID,
		Name:        rt.Name,
		Mode:        rt.Mode,
		PoolKind:    rt.PoolKind,
		Status:      rt.Status,
		PriceRatio:  rt.PriceRatio,
		RPMLimit:    rt.RPMLimit,
		TPMLimit:    rt.TPMLimit,
		RPDLimit:    rt.RPDLimit,
		Description: rt.Description,
		Channels:    channels,
		CreatedAt:   rt.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:   rt.UpdatedAt.UTC().Format(time.RFC3339),
	}
}
