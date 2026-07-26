package channel

import (
	"net/http"
	"strings"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelcostmultiplier"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelmodel"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelprice"
	"github.com/ThankCat/unio-gateway/internal/service/admin/channelrechargefactor"
)

// duplicateChannelRequest 把已有渠道整份复制到同服务商下的另一源站（新行，非引用）。
// 复制：渠道壳（含凭据/限流）、模型绑定、当前仍生效的成本价/价格倍率/充值倍率。
// 不复制：线路池成员、检测历史、熔断运行态。
type duplicateChannelRequest struct {
	ProviderOriginID int64  `json:"provider_origin_id"`
	Name             string `json:"name"`
	// Status 可选；省略则沿用源渠道 status（archived 源渠道拒绝复制）。
	Status *string `json:"status"`
}

type channelDuplicateHandler struct {
	channels  ChannelService
	models    ChannelModelService
	prices    ChannelPriceService
	costs     ChannelCostMultiplierService
	recharges ChannelRechargeFactorService
}

func (h *channelDuplicateHandler) duplicate(w http.ResponseWriter, r *http.Request) {
	if h.channels == nil {
		adminhttp.WriteServiceError(w, failure.New(failure.CodeAdminStoreFailed, failure.WithMessage("channel service unavailable")))
		return
	}

	sourceID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req duplicateChannelRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	name := strings.TrimSpace(req.Name)
	if req.ProviderOriginID <= 0 {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("provider_origin_id must be positive"),
			failure.WithField("provider_origin_id", "required"),
		))
		return
	}
	if name == "" {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("name is required"),
			failure.WithField("name", "required"),
		))
		return
	}

	src, err := h.channels.Get(r.Context(), sourceID)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if src.Status == "archived" {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("cannot duplicate an archived channel"),
			failure.WithField("status", "archived"),
		))
		return
	}
	if req.ProviderOriginID == src.ProviderOriginID {
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("target origin must differ from the source channel origin"),
			failure.WithField("provider_origin_id", "must_differ"),
		))
		return
	}

	status := src.Status
	if req.Status != nil && strings.TrimSpace(*req.Status) != "" {
		status = strings.TrimSpace(*req.Status)
	}

	bills := src.BillsOnDisconnect
	created, err := h.channels.Create(r.Context(), channel.CreateInput{
		ProviderID:         src.ProviderID,
		ProviderOriginID:   req.ProviderOriginID,
		Name:               name,
		Protocol:           src.Protocol,
		AdapterKey:         src.AdapterKey,
		Credential:         src.Credential,
		Status:             status,
		Priority:           src.Priority,
		TimeoutMs:          src.TimeoutMs,
		RateLimitsProvided: true,
		RPMLimit:           src.RPMLimit,
		TPMLimit:           src.TPMLimit,
		RPDLimit:           src.RPDLimit,
		ConcurrencyLimit:   src.ConcurrencyLimit,
		BillsOnDisconnect:  &bills,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	// 子资源复制失败时返回已创建的渠道 + 警告字段不合适；尽量继续复制，单步失败则整体返回错误
	//（调用方可删掉半成品）。当前策略：任一步失败即 500/业务错误，前端提示。
	if err := h.copyChildren(r, src.ID, created.ID); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, toChannelDTO(created))
}

func (h *channelDuplicateHandler) copyChildren(r *http.Request, sourceID, destID int64) error {
	ctx := r.Context()
	now := time.Now().UTC()

	if h.models != nil {
		bindings, err := h.models.List(ctx, sourceID)
		if err != nil {
			return err
		}
		for _, b := range bindings {
			if _, err := h.models.Create(ctx, channelmodel.CreateInput{
				ChannelID:     destID,
				ModelID:       b.ModelID,
				UpstreamModel: b.UpstreamModel,
				Status:        b.Status,
			}); err != nil {
				return err
			}
		}
	}

	if h.prices != nil {
		prices, err := h.prices.List(ctx, sourceID)
		if err != nil {
			return err
		}
		for _, p := range prices {
			if !windowCurrentlyActive(p.Status, p.EffectiveFrom, p.EffectiveTo, now) {
				continue
			}
			if _, err := h.prices.Create(ctx, channelprice.CreateInput{
				ChannelID:              destID,
				ModelID:                p.ModelID,
				Currency:               p.Currency,
				PricingUnit:            p.PricingUnit,
				UncachedInputCost:      p.UncachedInputCost,
				CacheReadInputCost:     p.CacheReadInputCost,
				CacheWrite5mInputCost:  p.CacheWrite5mInputCost,
				CacheWrite1hInputCost:  p.CacheWrite1hInputCost,
				CacheWrite30mInputCost: p.CacheWrite30mInputCost,
				OutputCost:             p.OutputCost,
				ReasoningOutputCost:    p.ReasoningOutputCost,
				Status:                 channelprice.StatusEnabled,
				EffectiveFrom:          now,
				EffectiveTo:            nil,
			}); err != nil {
				return err
			}
		}
	}

	if h.costs != nil {
		items, err := h.costs.List(ctx, sourceID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if !windowCurrentlyActive(item.Status, item.EffectiveFrom, item.EffectiveTo, now) {
				continue
			}
			if _, err := h.costs.Create(ctx, channelcostmultiplier.CreateInput{
				ChannelID:     destID,
				ModelID:       item.ModelID,
				Multiplier:    item.Multiplier,
				Status:        channelcostmultiplier.StatusEnabled,
				EffectiveFrom: now,
				EffectiveTo:   nil,
			}); err != nil {
				return err
			}
		}
	}

	if h.recharges != nil {
		items, err := h.recharges.List(ctx, sourceID)
		if err != nil {
			return err
		}
		for _, item := range items {
			if !windowCurrentlyActive(item.Status, item.EffectiveFrom, item.EffectiveTo, now) {
				continue
			}
			if _, err := h.recharges.Create(ctx, channelrechargefactor.CreateInput{
				ChannelID:     destID,
				Factor:        item.Factor,
				Status:        channelrechargefactor.StatusEnabled,
				EffectiveFrom: now,
				EffectiveTo:   nil,
			}); err != nil {
				return err
			}
		}
	}

	return nil
}

func windowCurrentlyActive(status string, from time.Time, to *time.Time, now time.Time) bool {
	if status != "enabled" {
		return false
	}
	if from.After(now) {
		return false
	}
	if to != nil && !to.After(now) {
		return false
	}
	return true
}
