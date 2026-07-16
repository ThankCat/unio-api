package route

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/route"
)

// RouteService 定义 adminapi 操作线路（routes / 渠道商品）所需的最小能力（阶段 15）。
type RouteService interface {
	List(ctx context.Context) ([]route.Route, error)
	Get(ctx context.Context, id int64) (route.Route, error)
	Create(ctx context.Context, in route.CreateInput) (route.Route, error)
	Update(ctx context.Context, in route.UpdateInput) (route.Route, error)
	Delete(ctx context.Context, id int64) error
	SetChannels(ctx context.Context, id int64, channelIDs []int64) (route.Route, error)
	Archive(ctx context.Context, id int64, migrateKeysTo *int64) ([]route.EmptyRouteWarning, error)
	Restore(ctx context.Context, id int64) error
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
	ArchivedAt  *string           `json:"archived_at"`
}

// archiveRouteRequest 归档线路入参：migrate_keys_to 非空时先把该线路全部 key 迁到目标线路再归档。
type archiveRouteRequest struct {
	MigrateKeysTo *int64 `json:"migrate_keys_to"`
}

type emptyRouteWarningDTO struct {
	RouteID  int64  `json:"route_id"`
	Name     string `json:"name"`
	KeyCount int64  `json:"key_count"`
}

type archiveRouteResponse struct {
	Warnings []emptyRouteWarningDTO `json:"warnings"`
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
		adminhttp.WriteServiceError(w, err)
		return
	}
	dtos := make([]routeDTO, 0, len(routes))
	for _, rt := range routes {
		dtos = append(dtos, toRouteDTO(rt))
	}
	adminhttp.WriteData(w, http.StatusOK, dtos)
}

func (h *routesHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	rt, err := h.service.Get(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toRouteDTO(rt))
}

func (h *routesHandler) create(w http.ResponseWriter, r *http.Request) {
	var req createRouteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
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
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, toRouteDTO(rt))
}

func (h *routesHandler) update(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req updateRouteRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
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
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toRouteDTO(rt))
}

func (h *routesHandler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Delete(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *routesHandler) archive(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var req archiveRouteRequest
	// body 可选：无 body 时按「不迁移」处理（有 key 则被拦截）。
	_ = httpx.DecodeJSON(w, r, &req)

	warnings, err := h.service.Archive(r.Context(), id, req.MigrateKeysTo)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	dtos := make([]emptyRouteWarningDTO, 0, len(warnings))
	for _, wn := range warnings {
		dtos = append(dtos, emptyRouteWarningDTO{RouteID: wn.RouteID, Name: wn.Name, KeyCount: wn.KeyCount})
	}
	adminhttp.WriteData(w, http.StatusOK, archiveRouteResponse{Warnings: dtos})
}

func (h *routesHandler) restore(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Restore(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
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
		ArchivedAt:  adminhttp.RFC3339Ptr(rt.ArchivedAt),
	}
}
