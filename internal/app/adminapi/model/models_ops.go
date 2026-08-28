package model

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/service/admin/modelops"
)

// ModelOpsService 定义模型商品控制台（§3.4）只读运维聚合所需能力。
type ModelOpsService interface {
	Table(ctx context.Context, p modelops.TableParams) ([]modelops.Row, int64, error)
	Detail(ctx context.Context, modelID int64, from, to time.Time) (modelops.Detail, error)
	Channels(ctx context.Context, modelID int64, from, to time.Time) ([]modelops.ChannelRow, error)
	PerformanceTimeseries(ctx context.Context, modelID int64, interval string, from, to time.Time) ([]modelops.PerfPoint, error)
	Requests(ctx context.Context, modelID int64, from, to time.Time, limit, offset int32) ([]modelops.RequestRow, int64, error)
	Errors(ctx context.Context, modelID int64, from, to time.Time) ([]modelops.ErrorRow, error)
}

type modelOpsHandler struct {
	service ModelOpsService
}

type modelOpsRowDTO struct {
	ID                        int64   `json:"id"`
	ModelID                   string  `json:"model_id"`
	DisplayName               string  `json:"display_name"`
	OwnedBy                   string  `json:"owned_by"`
	Status                    string  `json:"status"`
	Family                    string  `json:"family"`
	Description               string  `json:"description"`
	KnowledgeCutoff           string  `json:"knowledge_cutoff"`
	DisabledReason            *string `json:"disabled_reason"`
	CreatedAt                 string  `json:"created_at"`
	MaxOutputTokens           *int64  `json:"max_output_tokens"`
	ContextWindowTokens       *int64  `json:"context_window_tokens"`
	BindingsTotal             int64   `json:"bindings_total"`
	BindingsAvailable         int64   `json:"bindings_available"`
	CapabilitiesDeclaredCount int64   `json:"capabilities_declared_count"`
	HasPrice                  bool    `json:"has_price"`
	SupplyAvailable           bool    `json:"supply_available"`
	// 基准售价（DEC-026 model_prices，每 1M tokens；无基准价时为 null）。
	BaseCurrency                   *string `json:"base_currency"`
	BaseUncachedInputPrice         *string `json:"base_uncached_input_price"`
	BaseCacheReadInputPrice        *string `json:"base_cache_read_input_price"`
	BaseCacheCreation5mInputPrice  *string `json:"base_cache_creation_5m_input_price"`
	BaseCacheCreation1hInputPrice  *string `json:"base_cache_creation_1h_input_price"`
	BaseCacheCreation30mInputPrice *string `json:"base_cache_creation_30m_input_price"`
	BaseOutputPrice                *string `json:"base_output_price"`
	BaseReasoningOutputPrice       *string `json:"base_reasoning_output_price"`
	// 售价：绝对售价整组非空时整组覆盖，否则基准价 × 倍率。两套实体可共存，不能混算。
	BaseSalePriceRatio                 *string `json:"base_sale_price_ratio"`
	BaseSaleUncachedInputPrice         *string `json:"base_sale_uncached_input_price"`
	BaseSaleCacheReadInputPrice        *string `json:"base_sale_cache_read_input_price"`
	BaseSaleCacheCreation5mInputPrice  *string `json:"base_sale_cache_creation_5m_input_price"`
	BaseSaleCacheCreation1hInputPrice  *string `json:"base_sale_cache_creation_1h_input_price"`
	BaseSaleCacheCreation30mInputPrice *string `json:"base_sale_cache_creation_30m_input_price"`
	BaseSaleOutputPrice                *string `json:"base_sale_output_price"`
	BaseSaleReasoningOutputPrice       *string `json:"base_sale_reasoning_output_price"`
	// 当前生效基准价的长上下文阶梯；无基准价或未启用时 enabled=false。
	BaseLongContextEnabled                 bool    `json:"base_long_context_enabled"`
	BaseLongContextThreshold               *int64  `json:"base_long_context_threshold"`
	BaseLongContextInputMultiplier         *string `json:"base_long_context_input_multiplier"`
	BaseLongContextOutputMultiplier        *string `json:"base_long_context_output_multiplier"`
	BaseFastPriceConfigured                bool    `json:"base_fast_price_configured"`
	BaseFastUncachedInputPrice             *string `json:"base_fast_uncached_input_price"`
	BaseFastCacheReadInputPrice            *string `json:"base_fast_cache_read_input_price"`
	BaseFastCacheCreation5mInputPrice      *string `json:"base_fast_cache_creation_5m_input_price"`
	BaseFastCacheCreation1hInputPrice      *string `json:"base_fast_cache_creation_1h_input_price"`
	BaseFastCacheCreation30mInputPrice     *string `json:"base_fast_cache_creation_30m_input_price"`
	BaseFastOutputPrice                    *string `json:"base_fast_output_price"`
	BaseFastReasoningOutputPrice           *string `json:"base_fast_reasoning_output_price"`
	BaseFastSaleUncachedInputPrice         *string `json:"base_fast_sale_uncached_input_price"`
	BaseFastSaleCacheReadInputPrice        *string `json:"base_fast_sale_cache_read_input_price"`
	BaseFastSaleCacheCreation5mInputPrice  *string `json:"base_fast_sale_cache_creation_5m_input_price"`
	BaseFastSaleCacheCreation1hInputPrice  *string `json:"base_fast_sale_cache_creation_1h_input_price"`
	BaseFastSaleCacheCreation30mInputPrice *string `json:"base_fast_sale_cache_creation_30m_input_price"`
	BaseFastSaleOutputPrice                *string `json:"base_fast_sale_output_price"`
	BaseFastSaleReasoningOutputPrice       *string `json:"base_fast_sale_reasoning_output_price"`
}

type modelOpsDetailDTO struct {
	RequestTotal     int64   `json:"request_total"`
	RequestSucceeded int64   `json:"request_succeeded"`
	SuccessRate      float64 `json:"success_rate"`
	LatencyAvg       float64 `json:"latency_avg"`
	LatencyP50       float64 `json:"latency_p50"`
	LatencyP90       float64 `json:"latency_p90"`
	LatencyP95       float64 `json:"latency_p95"`
	LatencyP99       float64 `json:"latency_p99"`
	// gateway_ttft_sample 为 0 时 p95 无意义：该区间没有流式请求。
	GatewayTTFTP95    float64 `json:"gateway_ttft_p95"`
	GatewayTTFTSample int64   `json:"gateway_ttft_sample"`
	OutputTokens      int64   `json:"output_tokens"`
	InputTokens       int64   `json:"input_tokens"`
	CacheReadRate     float64 `json:"cache_read_rate"`
	TPS               float64 `json:"tps"`
	RevenueUSD        string  `json:"revenue_usd"`
	CostUSD           string  `json:"cost_usd"`
	MarginUSD         string  `json:"margin_usd"`
	MarginRate        float64 `json:"margin_rate"`
	SupplyAvailable   bool    `json:"supply_available"`
	BindingsTotal     int64   `json:"bindings_total"`
	BindingsAvailable int64   `json:"bindings_available"`
	ModelStatus       string  `json:"model_status"`
}

type modelOpsChannelDTO struct {
	ChannelID        int64   `json:"channel_id"`
	ChannelName      string  `json:"channel_name"`
	ChannelStatus    string  `json:"channel_status"`
	BindingStatus    string  `json:"binding_status"`
	UpstreamModel    string  `json:"upstream_model"`
	Priority         int32   `json:"priority"`
	AttemptTotal     int64   `json:"attempt_total"`
	AttemptSucceeded int64   `json:"attempt_succeeded"`
	SuccessRate      float64 `json:"success_rate"`
	LatencyP95       float64 `json:"latency_p95"`
	HasPrice         bool    `json:"has_price"`
	InputCost        *string `json:"input_cost"`
	OutputCost       *string `json:"output_cost"`
	// Fast 档成本；两侧都没配 Fast 价时为 null，表示该渠道对本模型不区分 Fast。
	FastInputCost  *string `json:"fast_input_cost"`
	FastOutputCost *string `json:"fast_output_cost"`
}

type modelOpsPerfPointDTO struct {
	Bucket           string  `json:"bucket"`
	RequestTotal     int64   `json:"request_total"`
	RequestSucceeded int64   `json:"request_succeeded"`
	LatencyP95       float64 `json:"latency_p95"`
	RevenueUSD       string  `json:"revenue_usd"`
	CostUSD          string  `json:"cost_usd"`
	MarginUSD        string  `json:"margin_usd"`
}

// modelOpsErrorDTO 是排障分区的错误聚合行；occurrences 降序，占比由前端按总数换算。
type modelOpsErrorDTO struct {
	ErrorCode       string `json:"error_code"`
	Occurrences     int64  `json:"occurrences"`
	LastSeenAt      string `json:"last_seen_at"`
	SampleRequestID string `json:"sample_request_id"`
	ChannelsTouched int64  `json:"channels_touched"`
}

type modelOpsRequestDTO struct {
	RequestID      string   `json:"request_id"`
	At             string   `json:"at"`
	Status         string   `json:"status"`
	ErrorCode      string   `json:"error_code"`
	FinalChannelID *int64   `json:"final_channel_id"`
	LatencyMs      *float64 `json:"latency_ms"`
}

func (h *modelOpsHandler) table(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	page := adminhttp.ParsePage(r)
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"name":       {},
		"bindings":   {},
		"context":    {},
		"max_output": {},
		"created_at": {},
	}, "name", false)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.Table(r.Context(), modelops.TableParams{
		From:      from,
		To:        to,
		Status:    adminhttp.ListStatus(r),
		Search:    adminhttp.QueryString(r, "search"),
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]modelOpsRowDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, modelOpsRowDTO{
			ID:                                     row.ID,
			ModelID:                                row.ModelID,
			DisplayName:                            row.DisplayName,
			OwnedBy:                                row.OwnedBy,
			Family:                                 row.Family,
			Description:                            row.Description,
			KnowledgeCutoff:                        row.KnowledgeCutoff,
			DisabledReason:                         row.DisabledReason,
			Status:                                 row.Status,
			CreatedAt:                              adminhttp.RFC3339(row.CreatedAt),
			MaxOutputTokens:                        row.MaxOutputTokens,
			ContextWindowTokens:                    row.ContextWindowTokens,
			BindingsTotal:                          row.BindingsTotal,
			BindingsAvailable:                      row.BindingsAvailable,
			CapabilitiesDeclaredCount:              row.CapabilitiesDeclaredCount,
			HasPrice:                               row.HasPrice,
			SupplyAvailable:                        row.SupplyAvailable,
			BaseCurrency:                           row.BaseCurrency,
			BaseUncachedInputPrice:                 row.BaseUncachedInputPrice,
			BaseCacheReadInputPrice:                row.BaseCacheReadInputPrice,
			BaseCacheCreation5mInputPrice:          row.BaseCacheCreation5mInputPrice,
			BaseCacheCreation1hInputPrice:          row.BaseCacheCreation1hInputPrice,
			BaseCacheCreation30mInputPrice:         row.BaseCacheCreation30mInputPrice,
			BaseOutputPrice:                        row.BaseOutputPrice,
			BaseReasoningOutputPrice:               row.BaseReasoningOutputPrice,
			BaseSalePriceRatio:                     row.BaseSalePriceRatio,
			BaseSaleUncachedInputPrice:             row.BaseSaleUncachedInputPrice,
			BaseSaleCacheReadInputPrice:            row.BaseSaleCacheReadInputPrice,
			BaseSaleCacheCreation5mInputPrice:      row.BaseSaleCacheCreation5mInputPrice,
			BaseSaleCacheCreation1hInputPrice:      row.BaseSaleCacheCreation1hInputPrice,
			BaseSaleCacheCreation30mInputPrice:     row.BaseSaleCacheCreation30mInputPrice,
			BaseSaleOutputPrice:                    row.BaseSaleOutputPrice,
			BaseSaleReasoningOutputPrice:           row.BaseSaleReasoningOutputPrice,
			BaseLongContextEnabled:                 row.BaseLongContextEnabled,
			BaseLongContextThreshold:               row.BaseLongContextThreshold,
			BaseLongContextInputMultiplier:         row.BaseLongContextInputMultiplier,
			BaseLongContextOutputMultiplier:        row.BaseLongContextOutputMultiplier,
			BaseFastPriceConfigured:                row.BaseFastPriceConfigured,
			BaseFastUncachedInputPrice:             row.BaseFastUncachedInputPrice,
			BaseFastCacheReadInputPrice:            row.BaseFastCacheReadInputPrice,
			BaseFastCacheCreation5mInputPrice:      row.BaseFastCacheCreation5mInputPrice,
			BaseFastCacheCreation1hInputPrice:      row.BaseFastCacheCreation1hInputPrice,
			BaseFastCacheCreation30mInputPrice:     row.BaseFastCacheCreation30mInputPrice,
			BaseFastOutputPrice:                    row.BaseFastOutputPrice,
			BaseFastReasoningOutputPrice:           row.BaseFastReasoningOutputPrice,
			BaseFastSaleUncachedInputPrice:         row.BaseFastSaleUncachedInputPrice,
			BaseFastSaleCacheReadInputPrice:        row.BaseFastSaleCacheReadInputPrice,
			BaseFastSaleCacheCreation5mInputPrice:  row.BaseFastSaleCacheCreation5mInputPrice,
			BaseFastSaleCacheCreation1hInputPrice:  row.BaseFastSaleCacheCreation1hInputPrice,
			BaseFastSaleCacheCreation30mInputPrice: row.BaseFastSaleCacheCreation30mInputPrice,
			BaseFastSaleOutputPrice:                row.BaseFastSaleOutputPrice,
			BaseFastSaleReasoningOutputPrice:       row.BaseFastSaleReasoningOutputPrice,
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *modelOpsHandler) detail(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	d, err := h.service.Detail(r.Context(), id, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, modelOpsDetailDTO{
		RequestTotal:      d.RequestTotal,
		RequestSucceeded:  d.RequestSucceeded,
		SuccessRate:       d.SuccessRate,
		LatencyAvg:        d.LatencyAvg,
		LatencyP50:        d.LatencyP50,
		LatencyP90:        d.LatencyP90,
		LatencyP95:        d.LatencyP95,
		LatencyP99:        d.LatencyP99,
		GatewayTTFTP95:    d.GatewayTTFTP95,
		GatewayTTFTSample: d.GatewayTTFTSample,
		OutputTokens:      d.OutputTokens,
		InputTokens:       d.InputTokens,
		CacheReadRate:     d.CacheReadRate,
		TPS:               d.TPS,
		RevenueUSD:        d.RevenueUSD,
		CostUSD:           d.CostUSD,
		MarginUSD:         d.MarginUSD,
		MarginRate:        d.MarginRate,
		SupplyAvailable:   d.SupplyAvailable,
		BindingsTotal:     d.BindingsTotal,
		BindingsAvailable: d.BindingsAvailable,
		ModelStatus:       d.ModelStatus,
	})
}

func (h *modelOpsHandler) channels(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	rows, err := h.service.Channels(r.Context(), id, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]modelOpsChannelDTO, 0, len(rows))
	for _, c := range rows {
		out = append(out, modelOpsChannelDTO{
			ChannelID:        c.ChannelID,
			ChannelName:      c.ChannelName,
			ChannelStatus:    c.ChannelStatus,
			BindingStatus:    c.BindingStatus,
			UpstreamModel:    c.UpstreamModel,
			Priority:         c.Priority,
			AttemptTotal:     c.AttemptTotal,
			AttemptSucceeded: c.AttemptSucceeded,
			SuccessRate:      c.SuccessRate,
			LatencyP95:       c.LatencyP95,
			HasPrice:         c.HasPrice,
			InputCost:        c.InputCost,
			OutputCost:       c.OutputCost,
			FastInputCost:    c.FastInputCost,
			FastOutputCost:   c.FastOutputCost,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *modelOpsHandler) performance(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, interval, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if q := adminhttp.QueryString(r, "interval"); q != "" {
		interval = q
	}
	points, err := h.service.PerformanceTimeseries(r.Context(), id, interval, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]modelOpsPerfPointDTO, 0, len(points))
	for _, p := range points {
		out = append(out, modelOpsPerfPointDTO{
			Bucket:           adminhttp.RFC3339(p.Bucket),
			RequestTotal:     p.RequestTotal,
			RequestSucceeded: p.RequestSucceeded,
			LatencyP95:       p.LatencyP95,
			RevenueUSD:       p.RevenueUSD,
			CostUSD:          p.CostUSD,
			MarginUSD:        p.MarginUSD,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *modelOpsHandler) requests(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	page := adminhttp.ParsePage(r)
	rows, total, err := h.service.Requests(r.Context(), id, from, to, page.Limit(), page.Offset())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]modelOpsRequestDTO, 0, len(rows))
	for _, rr := range rows {
		out = append(out, modelOpsRequestDTO{
			RequestID:      rr.RequestID,
			At:             adminhttp.RFC3339(rr.At),
			Status:         rr.Status,
			ErrorCode:      rr.ErrorCode,
			FinalChannelID: rr.FinalChannelID,
			LatencyMs:      rr.LatencyMs,
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *modelOpsHandler) errors(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	rows, err := h.service.Errors(r.Context(), id, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]modelOpsErrorDTO, 0, len(rows))
	for _, e := range rows {
		out = append(out, modelOpsErrorDTO{
			ErrorCode:       e.ErrorCode,
			Occurrences:     e.Occurrences,
			LastSeenAt:      adminhttp.RFC3339(e.LastSeenAt),
			SampleRequestID: e.SampleRequestID,
			ChannelsTouched: e.ChannelsTouched,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}
