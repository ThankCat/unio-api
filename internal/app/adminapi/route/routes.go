package route

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/route"
	"github.com/ThankCat/unio-gateway/internal/service/admin/supply"
)

// RouteService 定义 adminapi 操作线路（routes / 渠道商品）所需的最小能力（阶段 15）。
type RouteService interface {
	List(ctx context.Context) ([]route.Route, error)
	Get(ctx context.Context, id int64) (route.Route, error)
	Create(ctx context.Context, in route.CreateInput) (route.Route, error)
	Update(ctx context.Context, in route.UpdateInput) (route.Route, error)
	Delete(ctx context.Context, id int64) error
	SetChannels(ctx context.Context, id int64, channelIDs []int64, confirmation supply.Confirmation) (route.Route, error)
	Archive(ctx context.Context, id int64, migrateKeysTo *int64) ([]route.EmptyRouteWarning, error)
	Restore(ctx context.Context, id int64) error
	OfferingCandidates(ctx context.Context, channelIDs []int64) ([]route.OfferingCandidate, error)
}

type routeDTO struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Mode   string `json:"mode"`
	Status string `json:"status"`
	// PriceRatio 客户售价倍率（DEC-026：客户售价 = 模型基准价 × 倍率），十进制字符串。
	PriceRatio string `json:"price_ratio"`
	// RPM/RPD/ConcurrencyLimit 线路级限流上限（按 (线路,用户) 计数）；null=继承默认，0=不限，>0=上限。
	// 没有 TPM：Unio 不限制 token 吞吐，只做观测。
	RPMLimit         *int64            `json:"rpm_limit"`
	RPDLimit         *int64            `json:"rpd_limit"`
	ConcurrencyLimit *int64            `json:"concurrency_limit"`
	Description      *string             `json:"description"`
	Channels         []routeChannelDTO   `json:"channels"`
	Offerings        []routeOfferingDTO  `json:"offerings"`
	CreatedAt        string              `json:"created_at"`
	UpdatedAt        string              `json:"updated_at"`
	ArchivedAt       *string             `json:"archived_at"`
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

// routeOfferingDTO 是线路一条 Model+协议售卖组合（ADR-0018 Offering 口径）。
type routeOfferingDTO struct {
	ModelID          int64   `json:"model_id"`
	PublicModelID    string  `json:"public_model_id"`
	DisplayName      string  `json:"display_name"`
	ModelStatus      string  `json:"model_status"`
	IngressProtocol  string  `json:"ingress_protocol"`
	Status           string  `json:"status"`
	DisabledReason   *string `json:"disabled_reason"`
	DisabledAt       *string `json:"disabled_at"`
	SupportAvailable bool    `json:"support_available"`
}

// offeringSelectionRequest 是保存时勾选的一条售卖组合。
type offeringSelectionRequest struct {
	ModelID         int64  `json:"model_id"`
	IngressProtocol string `json:"ingress_protocol"`
}

// offeringCandidateDTO 是按渠道池计算的可勾选组合候选。
type offeringCandidateDTO struct {
	ModelID            int64  `json:"model_id"`
	PublicModelID      string `json:"public_model_id"`
	DisplayName        string `json:"display_name"`
	IngressProtocol    string `json:"ingress_protocol"`
	SupportingChannels int64  `json:"supporting_channels"`
}

type createRouteRequest struct {
	Name             string                     `json:"name"`
	Mode             string                     `json:"mode"`
	Status           string                     `json:"status"`
	PriceRatio       string                     `json:"price_ratio"` // 客户售价倍率（十进制字符串，空=默认 1.0）
	RPMLimit         *int64                     `json:"rpm_limit"`   // 线路级限流（null=继承默认，0=不限，>0=上限）
	RPDLimit         *int64                     `json:"rpd_limit"`
	ConcurrencyLimit *int64                     `json:"concurrency_limit"`
	Description      *string                    `json:"description"`
	ChannelIDs       []int64                    `json:"channel_ids"`
	Offerings        []offeringSelectionRequest `json:"offerings"`
	// ConfirmSupplyImpact + ExpectedImpactFingerprint 是保存触发 Offering 联动时的
	// 二次确认参数（ADR-0018）；首次请求缺省，收到 409 影响预览后携带指纹重试。
	ConfirmSupplyImpact       bool   `json:"confirm_supply_impact"`
	ExpectedImpactFingerprint string `json:"expected_impact_fingerprint"`
}

type updateRouteRequest struct {
	Name             string                     `json:"name"`
	Mode             string                     `json:"mode"`
	Status           string                     `json:"status"`
	PriceRatio       string                     `json:"price_ratio"` // 客户售价倍率（十进制字符串，空=默认 1.0）
	RPMLimit         *int64                     `json:"rpm_limit"`   // 线路级限流（null=继承默认，0=不限，>0=上限）
	RPDLimit         *int64                     `json:"rpd_limit"`
	ConcurrencyLimit *int64                     `json:"concurrency_limit"`
	Description      *string                    `json:"description"`
	ChannelIDs       []int64                    `json:"channel_ids"`
	Offerings        []offeringSelectionRequest `json:"offerings"`
	ConfirmSupplyImpact       bool   `json:"confirm_supply_impact"`
	ExpectedImpactFingerprint string `json:"expected_impact_fingerprint"`
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
		Name:             req.Name,
		Mode:             req.Mode,
		Status:           req.Status,
		PriceRatio:       req.PriceRatio,
		RPMLimit:         req.RPMLimit,
		RPDLimit:         req.RPDLimit,
		ConcurrencyLimit: req.ConcurrencyLimit,
		Description:      req.Description,
		ChannelIDs:       req.ChannelIDs,
		Offerings:        toOfferingSelections(req.Offerings),
		Confirmation: supply.Confirmation{
			Confirm:             req.ConfirmSupplyImpact,
			ExpectedFingerprint: req.ExpectedImpactFingerprint,
		},
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
		ID:               id,
		Name:             req.Name,
		Mode:             req.Mode,
		Status:           req.Status,
		PriceRatio:       req.PriceRatio,
		RPMLimit:         req.RPMLimit,
		RPDLimit:         req.RPDLimit,
		ConcurrencyLimit: req.ConcurrencyLimit,
		Description:      req.Description,
		ChannelIDs:       req.ChannelIDs,
		Offerings:        toOfferingSelections(req.Offerings),
		Confirmation: supply.Confirmation{
			Confirm:             req.ConfirmSupplyImpact,
			ExpectedFingerprint: req.ExpectedImpactFingerprint,
		},
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
	if err := httpx.DecodeJSON(w, r, &req); err != nil && !errors.Is(err, httpx.ErrEmptyJSONBody) {
		adminhttp.WriteServiceError(w, err)
		return
	}

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

// offeringCandidates 按 query 参数 channel_ids（逗号分隔）计算可勾选售卖组合候选。
func (h *routesHandler) offeringCandidates(w http.ResponseWriter, r *http.Request) {
	ids, err := parseChannelIDsQuery(r.URL.Query().Get("channel_ids"))
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	candidates, err := h.service.OfferingCandidates(r.Context(), ids)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	dtos := make([]offeringCandidateDTO, 0, len(candidates))
	for _, c := range candidates {
		dtos = append(dtos, offeringCandidateDTO{
			ModelID:            c.ModelID,
			PublicModelID:      c.PublicModelID,
			DisplayName:        c.DisplayName,
			IngressProtocol:    c.IngressProtocol,
			SupportingChannels: c.SupportingChannels,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, dtos)
}

// parseChannelIDsQuery 解析逗号分隔的 channel_ids query 参数。
func parseChannelIDsQuery(raw string) ([]int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	parts := strings.Split(raw, ",")
	ids := make([]int64, 0, len(parts))
	for _, part := range parts {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		id, err := strconv.ParseInt(part, 10, 64)
		if err != nil || id <= 0 {
			return nil, adminhttp.InvalidRequestField("channel_ids", "channel_ids must be a comma separated list of positive integers")
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func toOfferingSelections(reqs []offeringSelectionRequest) []route.OfferingSelection {
	out := make([]route.OfferingSelection, 0, len(reqs))
	for _, sel := range reqs {
		out = append(out, route.OfferingSelection{
			ModelID:         sel.ModelID,
			IngressProtocol: sel.IngressProtocol,
		})
	}
	return out
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
	offerings := make([]routeOfferingDTO, 0, len(rt.Offerings))
	for _, o := range rt.Offerings {
		offerings = append(offerings, routeOfferingDTO{
			ModelID:          o.ModelID,
			PublicModelID:    o.PublicModelID,
			DisplayName:      o.DisplayName,
			ModelStatus:      o.ModelStatus,
			IngressProtocol:  o.IngressProtocol,
			Status:           o.Status,
			DisabledReason:   o.DisabledReason,
			DisabledAt:       adminhttp.RFC3339Ptr(o.DisabledAt),
			SupportAvailable: o.SupportAvailable,
		})
	}
	return routeDTO{
		ID:               rt.ID,
		Name:             rt.Name,
		Mode:             rt.Mode,
		Status:           rt.Status,
		PriceRatio:       rt.PriceRatio,
		RPMLimit:         rt.RPMLimit,
		RPDLimit:         rt.RPDLimit,
		ConcurrencyLimit: rt.ConcurrencyLimit,
		Description:      rt.Description,
		Channels:         channels,
		Offerings:        offerings,
		CreatedAt:        rt.CreatedAt.UTC().Format(time.RFC3339),
		UpdatedAt:        rt.UpdatedAt.UTC().Format(time.RFC3339),
		ArchivedAt:       adminhttp.RFC3339Ptr(rt.ArchivedAt),
	}
}
