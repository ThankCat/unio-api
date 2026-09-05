package requests

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/app/adminapi/ledger"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-gateway/internal/service/admin/query"
)

// RequestQueryService 定义 adminapi 查询请求记录所需的最小能力（M6 只读查询台）。
type RequestQueryService interface {
	List(ctx context.Context, params query.RequestListParams) ([]query.RequestListItem, int64, error)
	Get(ctx context.Context, requestID string, includeInternal bool) (query.RequestDetail, error)
}

// requestSummaryDTO 是请求列表项响应体；不含 internal_error_detail。
type requestSummaryDTO struct {
	ID                    int64   `json:"id"`
	RequestID             string  `json:"request_id"`
	UserID                int64   `json:"user_id"`
	APIKeyID              int64   `json:"api_key_id"`
	RequestedModelID      string  `json:"requested_model_id"`
	IngressProtocol       string  `json:"ingress_protocol"`
	Endpoint              string  `json:"endpoint"`
	ResponseModelID       *string `json:"response_model_id"`
	ResponseProtocol      *string `json:"response_protocol"`
	ResponseID            *string `json:"response_id"`
	Stream                bool    `json:"stream"`
	Status                string  `json:"status"`
	FinalProviderID       *int64  `json:"final_provider_id"`
	FinalChannelID        *int64  `json:"final_channel_id"`
	FinalAccountID        *int64  `json:"final_account_id,omitempty"`
	FinalAccountName      string  `json:"final_account_name,omitempty"`
	ErrorCode             *string `json:"error_code"`
	ErrorMessage          *string `json:"error_message"`
	DeliveryStatus        string  `json:"delivery_status"`
	GatewayFirstTokenAt   *string `json:"gateway_first_token_at"`
	ResponseCompletedAt   *string `json:"response_completed_at"`
	StartedAt             string  `json:"started_at"`
	CompletedAt           *string `json:"completed_at"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
	RequestedServiceTier  *string `json:"requested_service_tier"`
	ActualServiceTier     *string `json:"actual_service_tier"`
	SettledServiceTier    *string `json:"settled_service_tier"`
	ServiceTierResolution *string `json:"service_tier_resolution"`
	ServiceTierDowngraded bool    `json:"service_tier_downgraded"`
}

// requestListItemDTO 是请求列表项（富化）：请求事实 + 用量/成本/扣费 + 线路/渠道链 + 时延。
type requestListItemDTO struct {
	requestSummaryDTO
	UncachedInputTokens         int64 `json:"uncached_input_tokens"`
	CacheReadInputTokens        int64 `json:"cache_read_input_tokens"`
	CacheCreation5mInputTokens  int64 `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hInputTokens  int64 `json:"cache_creation_1h_input_tokens"`
	CacheCreation30mInputTokens int64 `json:"cache_creation_30m_input_tokens"`
	OutputTokens                int64 `json:"output_tokens"`
	ReasoningOutputTokens       int64 `json:"reasoning_output_tokens"`
	// USD 十进制字符串；无结算快照 / 账本时为 null。
	UserChargeUsd *string `json:"user_charge_usd"`
	// 成本按 provider 结算币种记账（cost_currency，D2 修订）：*_cost_amount 为原币金额；
	// total_cost_usd 为结算钉档汇率（cost_fx_rate）折算的 USD 总额，毛利/汇总用它。
	CostCurrency                    *string `json:"cost_currency"`
	CostFxRate                      *string `json:"cost_fx_rate"`
	CostFxRateDate                  *string `json:"cost_fx_rate_date"`
	TotalCostUsd                    *string `json:"total_cost_usd"`
	TotalCostAmount                 *string `json:"total_cost_amount"`
	UncachedInputCostAmount         *string `json:"uncached_input_cost_amount"`
	CacheReadInputCostAmount        *string `json:"cache_read_input_cost_amount"`
	CacheCreation5mInputCostAmount  *string `json:"cache_creation_5m_input_cost_amount"`
	CacheCreation1hInputCostAmount  *string `json:"cache_creation_1h_input_cost_amount"`
	CacheCreation30mInputCostAmount *string `json:"cache_creation_30m_input_cost_amount"`
	OutputCostAmount                *string `json:"output_cost_amount"`
	ReasoningOutputCostAmount       *string `json:"reasoning_output_cost_amount"`
	// 计费单价快照（USD 字符串，per_1m_tokens）：平台成本单价×7 + 用户售价单价×7，供「单价×tokens=金额」计算过程展示。
	UncachedInputCostUnit             *string `json:"uncached_input_cost_unit"`
	CacheReadInputCostUnit            *string `json:"cache_read_input_cost_unit"`
	CacheCreation5mInputCostUnit      *string `json:"cache_creation_5m_input_cost_unit"`
	CacheCreation1hInputCostUnit      *string `json:"cache_creation_1h_input_cost_unit"`
	CacheCreation30mInputCostUnit     *string `json:"cache_creation_30m_input_cost_unit"`
	OutputCostUnit                    *string `json:"output_cost_unit"`
	ReasoningOutputCostUnit           *string `json:"reasoning_output_cost_unit"`
	UncachedInputPriceUnitUsd         *string `json:"uncached_input_price_unit_usd"`
	CacheReadInputPriceUnitUsd        *string `json:"cache_read_input_price_unit_usd"`
	CacheCreation5mInputPriceUnitUsd  *string `json:"cache_creation_5m_input_price_unit_usd"`
	CacheCreation1hInputPriceUnitUsd  *string `json:"cache_creation_1h_input_price_unit_usd"`
	CacheCreation30mInputPriceUnitUsd *string `json:"cache_creation_30m_input_price_unit_usd"`
	OutputPriceUnitUsd                *string `json:"output_price_unit_usd"`
	ReasoningOutputPriceUnitUsd       *string `json:"reasoning_output_price_unit_usd"`
	PriceServiceTier                  *string `json:"price_service_tier"`
	CostServiceTier                   *string `json:"cost_service_tier"`
	ModelPriceServiceTierID           *int64  `json:"model_price_service_tier_id"`
	ChannelPriceServiceTierID         *int64  `json:"channel_price_service_tier_id"`
	TierCostSource                    *string `json:"tier_cost_source"`
	// DEC-027 成本来源倍率（倍率路径有值，覆盖/旧数据为 null）：价格倍率 + 充值倍率。
	ChannelCostMultiplier *string `json:"channel_cost_multiplier"`
	ProviderRechargeRate  *string `json:"provider_recharge_rate"`
	// LongContextApplied 费用是否已按长上下文倍率结算。
	LongContextApplied bool `json:"long_context_applied"`
	// 用户/Key 展示口径同 api-keys 页：只给名称和前缀，明文不落库也就无从回显。
	ApiKeyName            *string  `json:"api_key_name"`
	ApiKeyPrefix          *string  `json:"api_key_prefix"`
	SaleDiscount        *string  `json:"sale_discount"`
	FinalChannelName      *string  `json:"final_channel_name"`
	ChannelChain          string   `json:"channel_chain"`
	ScoringAttemptID      *int64   `json:"scoring_attempt_id"`
	ScoringDimensions     []string `json:"scoring_dimensions"`
	ScoringErrorFailure   bool     `json:"scoring_error_failure"`
	ModelDisplayName      *string  `json:"model_display_name"`
	ModelOwnedBy          *string  `json:"model_owned_by"`
	ReasoningEffort       *string  `json:"reasoning_effort"`
	ReasoningBudgetTokens *int32   `json:"reasoning_budget_tokens"`
	ClientIP              *string  `json:"client_ip"`
	LatencyMs             *int64   `json:"latency_ms"`
	GatewayTTFTMs         *int64   `json:"gateway_ttft_ms"`
	Tps                   *float64 `json:"tps"`
	// Sticky 摘要：无 routing_decision_traces 时 sticky_key_present 为 null。
	StickyKeyPresent         *bool   `json:"sticky_key_present"`
	StickyAction             *string `json:"sticky_action"`
	StickyReason             *string `json:"sticky_reason"`
	StickyBeforeChannelID    *int64  `json:"sticky_before_channel_id"`
	StickyAfterChannelID     *int64  `json:"sticky_after_channel_id"`
	StickyPinned             *bool   `json:"sticky_pinned"`
	StickyPinnedNonPreferred *bool   `json:"sticky_pinned_non_preferred"`
	StickyBeforeChannelName  *string `json:"sticky_before_channel_name"`
	StickyAfterChannelName   *string `json:"sticky_after_channel_name"`
}

// costSnapshotDTO 是平台成本快照（单价 per_1m_tokens + 金额）。金额按 provider 结算币种记账
// （currency，D2 修订）；total_cost_amount_usd 为结算钉档汇率（fx_rate）折算的 USD 总额。
type costSnapshotDTO struct {
	Currency                        *string `json:"currency"`
	FxRate                          *string `json:"fx_rate"`
	FxRateDate                      *string `json:"fx_rate_date"`
	TotalCostAmountUsd              *string `json:"total_cost_amount_usd"`
	UncachedInputCostUnit           *string `json:"uncached_input_cost_unit"`
	CacheReadInputCostUnit          *string `json:"cache_read_input_cost_unit"`
	CacheCreation5mInputCostUnit    *string `json:"cache_creation_5m_input_cost_unit"`
	CacheCreation1hInputCostUnit    *string `json:"cache_creation_1h_input_cost_unit"`
	CacheCreation30mInputCostUnit   *string `json:"cache_creation_30m_input_cost_unit"`
	OutputCostUnit                  *string `json:"output_cost_unit"`
	ReasoningOutputCostUnit         *string `json:"reasoning_output_cost_unit"`
	UncachedInputCostAmount         *string `json:"uncached_input_cost_amount"`
	CacheReadInputCostAmount        *string `json:"cache_read_input_cost_amount"`
	CacheCreation5mInputCostAmount  *string `json:"cache_creation_5m_input_cost_amount"`
	CacheCreation1hInputCostAmount  *string `json:"cache_creation_1h_input_cost_amount"`
	CacheCreation30mInputCostAmount *string `json:"cache_creation_30m_input_cost_amount"`
	OutputCostAmount                *string `json:"output_cost_amount"`
	ReasoningOutputCostAmount       *string `json:"reasoning_output_cost_amount"`
	TotalCostAmount                 *string `json:"total_cost_amount"`
	// DEC-027 成本来源倍率（倍率路径有值，覆盖/旧数据为 null）：价格倍率 + 充值倍率，供费用处展示新旧倍率。
	ChannelCostMultiplier     *string `json:"channel_cost_multiplier"`
	ProviderRechargeRate      *string `json:"provider_recharge_rate"`
	ServiceTier               *string `json:"service_tier"`
	ModelPriceServiceTierID   *int64  `json:"model_price_service_tier_id"`
	ChannelPriceServiceTierID *int64  `json:"channel_price_service_tier_id"`
	TierCostSource            *string `json:"tier_cost_source"`
}

// priceSnapshotDTO 是客户售价快照（单价 per_1m_tokens，USD 字符串）。
type priceSnapshotDTO struct {
	UncachedInputPrice         *string `json:"uncached_input_price"`
	CacheReadInputPrice        *string `json:"cache_read_input_price"`
	CacheCreation5mInputPrice  *string `json:"cache_creation_5m_input_price"`
	CacheCreation1hInputPrice  *string `json:"cache_creation_1h_input_price"`
	CacheCreation30mInputPrice *string `json:"cache_creation_30m_input_price"`
	OutputPrice                *string `json:"output_price"`
	ReasoningOutputPrice       *string `json:"reasoning_output_price"`
	ServiceTier                *string `json:"service_tier"`
	ModelPriceServiceTierID    *int64  `json:"model_price_service_tier_id"`
}

// attemptDTO 是请求详情中的一次上游尝试；internal_error_detail 仅在 ?include_internal=true 时出现。
type attemptDTO struct {
	ID                    int64   `json:"id"`
	AttemptIndex          int32   `json:"attempt_index"`
	ProviderID            int64   `json:"provider_id"`
	ChannelID             int64   `json:"channel_id"`
	ChannelName           string  `json:"channel_name"`
	ChannelCostMultiplier *string `json:"channel_cost_multiplier"`
	ProviderRechargeRate  *string `json:"provider_recharge_rate"`
	AdapterKey            string  `json:"adapter_key"`
	UpstreamModel         string  `json:"upstream_model"`
	UpstreamProtocol      string  `json:"upstream_protocol"`
	UpstreamResponseID    *string `json:"upstream_response_id"`
	UpstreamResponseModel *string `json:"upstream_response_model"`
	UpstreamFinishReason  *string `json:"upstream_finish_reason"`
	FinishClass           *string `json:"finish_class"`
	Status                string  `json:"status"`
	FaultParty            *string `json:"fault_party"`
	UpstreamStatusCode    *int32  `json:"upstream_status_code"`
	UpstreamRequestID     *string `json:"upstream_request_id"`
	ErrorCode             *string `json:"error_code"`
	ErrorMessage          *string `json:"error_message"`
	InternalErrorDetail   *string `json:"internal_error_detail,omitempty"`
	GatewayFirstTokenAt   *string `json:"gateway_first_token_at"`
	UpstreamTimeoutPhase  *string `json:"upstream_timeout_phase"`
	UpstreamTotalMs       *int64  `json:"upstream_total_ms"`
	UpstreamTTFTMs        *int64  `json:"upstream_ttft_ms"`
	TTFTScoringSample     bool    `json:"ttft_scoring_sample"`
	ErrorScoringSample    bool    `json:"error_scoring_sample"`
	ErrorScoringFailure   bool    `json:"error_scoring_failure"`
	FinalUsageReceived    bool    `json:"final_usage_received"`
	RequestedServiceTier  *string `json:"requested_service_tier"`
	ForwardedServiceTier  *string `json:"forwarded_service_tier"`
	UpstreamServiceTier   *string `json:"upstream_service_tier"`
	StartedAt             string  `json:"started_at"`
	CompletedAt           *string `json:"completed_at"`
	CreatedAt             string  `json:"created_at"`
}

// usageDTO 是请求详情中的协议无关用量事实。
type usageDTO struct {
	ID                          int64  `json:"id"`
	RequestRecordID             int64  `json:"request_record_id"`
	UncachedInputTokens         int64  `json:"uncached_input_tokens"`
	CacheReadInputTokens        int64  `json:"cache_read_input_tokens"`
	CacheCreation5mInputTokens  int64  `json:"cache_creation_5m_input_tokens"`
	CacheCreation1hInputTokens  int64  `json:"cache_creation_1h_input_tokens"`
	CacheCreation30mInputTokens int64  `json:"cache_creation_30m_input_tokens"`
	OutputTokensTotal           int64  `json:"output_tokens_total"`
	ReasoningOutputTokens       int64  `json:"reasoning_output_tokens"`
	UsageSource                 string `json:"usage_source"`
	UsageMappingVersion         string `json:"usage_mapping_version"`
	CreatedAt                   string `json:"created_at"`
}

// requestDetailDTO 是请求详情聚合响应体。
type requestDetailDTO struct {
	requestSummaryDTO
	InternalErrorDetail   *string                     `json:"internal_error_detail,omitempty"`
	LatencyMs             *int64                      `json:"latency_ms"`
	GatewayTTFTMs         *int64                      `json:"gateway_ttft_ms"`
	Tps                   *float64                    `json:"tps"`
	ReasoningEffort       *string                     `json:"reasoning_effort"`
	ReasoningBudgetTokens *int32                      `json:"reasoning_budget_tokens"`
	ClientIP              *string                     `json:"client_ip"`
	CostSnapshot          *costSnapshotDTO            `json:"cost_snapshot"`
	PriceSnapshot         *priceSnapshotDTO           `json:"price_snapshot"`
	SaleDiscount        *string                     `json:"sale_discount"`
	Attempts              []attemptDTO                `json:"attempts"`
	Usage                 *usageDTO                   `json:"usage"`
	LedgerEntries         []ledger.LedgerEntryDTO     `json:"ledger_entries"`
	BillingException      *ledger.BillingExceptionDTO `json:"billing_exception"`
}

type requestsHandler struct {
	service RequestQueryService
}

func (h *requestsHandler) list(w http.ResponseWriter, r *http.Request) {
	userID, err := adminhttp.OptionalInt64Query(r, "user_id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	apiKeyID, err := adminhttp.OptionalInt64Query(r, "api_key_id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	channelID, err := adminhttp.OptionalInt64Query(r, "channel_id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	attemptID, err := adminhttp.OptionalInt64Query(r, "attempt_id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	accountID, err := adminhttp.OptionalInt64Query(r, "account_id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	scoringSample := adminhttp.QueryString(r, "scoring_sample")
	switch scoringSample {
	case "", "ttft", "error", "any":
	default:
		adminhttp.WriteServiceError(w, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("scoring_sample must be one of ttft, error, any"),
			failure.WithField("field", "scoring_sample"),
		))
		return
	}
	from, err := adminhttp.OptionalTimeQuery(r, "from")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	to, err := adminhttp.OptionalTimeQuery(r, "to")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"created_at": {},
		"status":     {},
		"user_id":    {},
		"model":      {},
		"stream":     {},
	}, "created_at", true)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}

	page := adminhttp.ParsePage(r)
	field, desc := sort.SQLParams()
	items, total, err := h.service.List(r.Context(), query.RequestListParams{
		UserID:        userID,
		APIKeyID:      apiKeyID,
		RequestID:     adminhttp.QueryString(r, "request_id"),
		Status:        adminhttp.QueryString(r, "status"),
		Model:         adminhttp.QueryString(r, "model"),
		ChannelID:     channelID,
		AttemptID:     attemptID,
		AccountID:     accountID,
		ScoringSample: scoringSample,
		From:          from,
		To:            to,
		SortField:     field,
		SortDesc:      desc,
		Limit:         page.Limit(),
		Offset:        page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]requestListItemDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toRequestListItemDTO(item))
	}
	adminhttp.WriteList(w, http.StatusOK, dtos, page, total)
}

func (h *requestsHandler) get(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "requestId")
	includeInternal := adminhttp.BoolQuery(r, "include_internal")

	detail, err := h.service.Get(r.Context(), requestID, includeInternal)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusOK, toRequestDetailDTO(detail))
}

func toRequestSummaryDTO(s query.RequestSummary) requestSummaryDTO {
	return requestSummaryDTO{
		ID:                    s.ID,
		RequestID:             s.RequestID,
		UserID:                s.UserID,
		APIKeyID:              s.APIKeyID,
		RequestedModelID:      s.RequestedModelID,
		IngressProtocol:       s.IngressProtocol,
		Endpoint:              s.Endpoint,
		ResponseModelID:       s.ResponseModelID,
		ResponseProtocol:      s.ResponseProtocol,
		ResponseID:            s.ResponseID,
		Stream:                s.Stream,
		Status:                s.Status,
		FinalProviderID:       s.FinalProviderID,
		FinalChannelID:        s.FinalChannelID,
		FinalAccountID:        s.FinalAccountID,
		FinalAccountName:      s.FinalAccountName,
		ErrorCode:             s.ErrorCode,
		ErrorMessage:          s.ErrorMessage,
		DeliveryStatus:        s.DeliveryStatus,
		GatewayFirstTokenAt:   adminhttp.RFC3339Ptr(s.GatewayFirstTokenAt),
		ResponseCompletedAt:   adminhttp.RFC3339Ptr(s.ResponseCompletedAt),
		StartedAt:             adminhttp.RFC3339(s.StartedAt),
		CompletedAt:           adminhttp.RFC3339Ptr(s.CompletedAt),
		CreatedAt:             adminhttp.RFC3339(s.CreatedAt),
		UpdatedAt:             adminhttp.RFC3339(s.UpdatedAt),
		RequestedServiceTier:  s.RequestedServiceTier,
		ActualServiceTier:     s.ActualServiceTier,
		SettledServiceTier:    s.SettledServiceTier,
		ServiceTierResolution: s.ServiceTierResolution,
		ServiceTierDowngraded: s.ServiceTierDowngraded,
	}
}

func toRequestListItemDTO(item query.RequestListItem) requestListItemDTO {
	return requestListItemDTO{
		requestSummaryDTO:               toRequestSummaryDTO(item.RequestSummary),
		UncachedInputTokens:             item.UncachedInputTokens,
		CacheReadInputTokens:            item.CacheReadInputTokens,
		CacheCreation5mInputTokens:      item.CacheCreation5mInputTokens,
		CacheCreation1hInputTokens:      item.CacheCreation1hInputTokens,
		CacheCreation30mInputTokens:     item.CacheCreation30mInputTokens,
		OutputTokens:                    item.OutputTokens,
		ReasoningOutputTokens:           item.ReasoningOutputTokens,
		UserChargeUsd:                   item.UserChargeUSD,
		CostCurrency:                    item.CostCurrency,
		CostFxRate:                      item.CostFxRate,
		CostFxRateDate:                  item.CostFxRateDate,
		TotalCostUsd:                    item.TotalCostUSD,
		TotalCostAmount:                 item.TotalCostAmount,
		UncachedInputCostAmount:         item.UncachedInputCostAmount,
		CacheReadInputCostAmount:        item.CacheReadInputCostAmount,
		CacheCreation5mInputCostAmount:  item.CacheCreation5mInputCostAmount,
		CacheCreation1hInputCostAmount:  item.CacheCreation1hInputCostAmount,
		CacheCreation30mInputCostAmount: item.CacheCreation30mInputCostAmount,
		OutputCostAmount:                item.OutputCostAmount,
		ReasoningOutputCostAmount:       item.ReasoningOutputCostAmount,

		UncachedInputCostUnit:             item.UncachedInputCostUnit,
		CacheReadInputCostUnit:            item.CacheReadInputCostUnit,
		CacheCreation5mInputCostUnit:      item.CacheCreation5mInputCostUnit,
		CacheCreation1hInputCostUnit:      item.CacheCreation1hInputCostUnit,
		CacheCreation30mInputCostUnit:     item.CacheCreation30mInputCostUnit,
		OutputCostUnit:                    item.OutputCostUnit,
		ReasoningOutputCostUnit:           item.ReasoningOutputCostUnit,
		UncachedInputPriceUnitUsd:         item.UncachedInputPriceUnitUSD,
		CacheReadInputPriceUnitUsd:        item.CacheReadInputPriceUnitUSD,
		CacheCreation5mInputPriceUnitUsd:  item.CacheCreation5mInputPriceUnitUSD,
		CacheCreation1hInputPriceUnitUsd:  item.CacheCreation1hInputPriceUnitUSD,
		CacheCreation30mInputPriceUnitUsd: item.CacheCreation30mInputPriceUnitUSD,
		OutputPriceUnitUsd:                item.OutputPriceUnitUSD,
		ReasoningOutputPriceUnitUsd:       item.ReasoningOutputPriceUnitUSD,
		PriceServiceTier:                  item.PriceServiceTier,
		CostServiceTier:                   item.CostServiceTier,
		ModelPriceServiceTierID:           item.ModelPriceServiceTierID,
		ChannelPriceServiceTierID:         item.ChannelPriceServiceTierID,
		TierCostSource:                    item.TierCostSource,

		ChannelCostMultiplier: item.ChannelCostMultiplier,
		ProviderRechargeRate:  item.ProviderRechargeRate,
		LongContextApplied:    item.LongContextApplied,

		ApiKeyName:   item.APIKeyName,
		ApiKeyPrefix: item.APIKeyPrefix,

		SaleDiscount:        item.SaleDiscount,
		FinalChannelName:      item.FinalChannelName,
		ChannelChain:          item.ChannelChain,
		ScoringAttemptID:      item.ScoringAttemptID,
		ScoringDimensions:     item.ScoringDimensions,
		ScoringErrorFailure:   item.ScoringErrorFailure,
		ModelDisplayName:      item.ModelDisplayName,
		ModelOwnedBy:          item.ModelOwnedBy,
		ReasoningEffort:       item.ReasoningEffort,
		ReasoningBudgetTokens: item.ReasoningBudgetTokens,
		ClientIP:              item.ClientIP,
		LatencyMs:             item.LatencyMs,
		GatewayTTFTMs:         item.GatewayTTFTMs,
		Tps:                   item.TPS,

		StickyKeyPresent:         item.StickyKeyPresent,
		StickyAction:             item.StickyAction,
		StickyReason:             item.StickyReason,
		StickyBeforeChannelID:    item.StickyBeforeChannelID,
		StickyAfterChannelID:     item.StickyAfterChannelID,
		StickyPinned:             item.StickyPinned,
		StickyPinnedNonPreferred: item.StickyPinnedNonPreferred,
		StickyBeforeChannelName:  item.StickyBeforeChannelName,
		StickyAfterChannelName:   item.StickyAfterChannelName,
	}
}

func toCostSnapshotDTO(c query.CostSnapshotView) costSnapshotDTO {
	return costSnapshotDTO{
		Currency:                        c.Currency,
		FxRate:                          c.FxRate,
		FxRateDate:                      c.FxRateDate,
		TotalCostAmountUsd:              c.TotalCostAmountUSD,
		UncachedInputCostUnit:           c.UncachedInputCostUnit,
		CacheReadInputCostUnit:          c.CacheReadInputCostUnit,
		CacheCreation5mInputCostUnit:    c.CacheCreation5mInputCostUnit,
		CacheCreation1hInputCostUnit:    c.CacheCreation1hInputCostUnit,
		CacheCreation30mInputCostUnit:   c.CacheCreation30mInputCostUnit,
		OutputCostUnit:                  c.OutputCostUnit,
		ReasoningOutputCostUnit:         c.ReasoningOutputCostUnit,
		UncachedInputCostAmount:         c.UncachedInputCostAmount,
		CacheReadInputCostAmount:        c.CacheReadInputCostAmount,
		CacheCreation5mInputCostAmount:  c.CacheCreation5mInputCostAmount,
		CacheCreation1hInputCostAmount:  c.CacheCreation1hInputCostAmount,
		CacheCreation30mInputCostAmount: c.CacheCreation30mInputCostAmount,
		OutputCostAmount:                c.OutputCostAmount,
		ReasoningOutputCostAmount:       c.ReasoningOutputCostAmount,
		TotalCostAmount:                 c.TotalCostAmount,
		ChannelCostMultiplier:           c.ChannelCostMultiplier,
		ProviderRechargeRate:            c.ProviderRechargeRate,
		ServiceTier:                     c.ServiceTier,
		ModelPriceServiceTierID:         c.ModelPriceServiceTierID,
		ChannelPriceServiceTierID:       c.ChannelPriceServiceTierID,
		TierCostSource:                  c.TierCostSource,
	}
}

func toPriceSnapshotDTO(p query.PriceSnapshotView) priceSnapshotDTO {
	return priceSnapshotDTO{
		UncachedInputPrice:         p.UncachedInputPrice,
		CacheReadInputPrice:        p.CacheReadInputPrice,
		CacheCreation5mInputPrice:  p.CacheCreation5mInputPrice,
		CacheCreation1hInputPrice:  p.CacheCreation1hInputPrice,
		CacheCreation30mInputPrice: p.CacheCreation30mInputPrice,
		OutputPrice:                p.OutputPrice,
		ReasoningOutputPrice:       p.ReasoningOutputPrice,
		ServiceTier:                p.ServiceTier,
		ModelPriceServiceTierID:    p.ModelPriceServiceTierID,
	}
}

func toRequestDetailDTO(d query.RequestDetail) requestDetailDTO {
	dto := requestDetailDTO{
		requestSummaryDTO:     toRequestSummaryDTO(d.RequestSummary),
		InternalErrorDetail:   d.InternalErrorDetail,
		LatencyMs:             d.LatencyMs,
		GatewayTTFTMs:         d.GatewayTTFTMs,
		Tps:                   d.TPS,
		ReasoningEffort:       d.ReasoningEffort,
		ReasoningBudgetTokens: d.ReasoningBudgetTokens,
		ClientIP:              d.ClientIP,
		SaleDiscount:        d.SaleDiscount,
		Attempts:              make([]attemptDTO, 0, len(d.Attempts)),
		LedgerEntries:         make([]ledger.LedgerEntryDTO, 0, len(d.LedgerEntries)),
	}
	if d.CostSnapshot != nil {
		c := toCostSnapshotDTO(*d.CostSnapshot)
		dto.CostSnapshot = &c
	}
	if d.PriceSnapshot != nil {
		p := toPriceSnapshotDTO(*d.PriceSnapshot)
		dto.PriceSnapshot = &p
	}
	for _, a := range d.Attempts {
		dto.Attempts = append(dto.Attempts, toAttemptDTO(a))
	}
	for _, e := range d.LedgerEntries {
		dto.LedgerEntries = append(dto.LedgerEntries, ledger.ToLedgerEntryDTO(e))
	}
	if d.Usage != nil {
		u := toUsageDTO(*d.Usage)
		dto.Usage = &u
	}
	if d.BillingException != nil {
		be := ledger.ToBillingExceptionDTO(*d.BillingException)
		dto.BillingException = &be
	}
	return dto
}

func toAttemptDTO(a query.Attempt) attemptDTO {
	return attemptDTO{
		ID:                    a.ID,
		AttemptIndex:          a.AttemptIndex,
		ProviderID:            a.ProviderID,
		ChannelID:             a.ChannelID,
		ChannelName:           a.ChannelName,
		ChannelCostMultiplier: a.ChannelCostMultiplier,
		ProviderRechargeRate:  a.ProviderRechargeRate,
		AdapterKey:            a.AdapterKey,
		UpstreamModel:         a.UpstreamModel,
		UpstreamProtocol:      a.UpstreamProtocol,
		UpstreamResponseID:    a.UpstreamResponseID,
		UpstreamResponseModel: a.UpstreamResponseModel,
		UpstreamFinishReason:  a.UpstreamFinishReason,
		FinishClass:           a.FinishClass,
		Status:                a.Status,
		FaultParty:            a.FaultParty,
		UpstreamStatusCode:    a.UpstreamStatusCode,
		UpstreamRequestID:     a.UpstreamRequestID,
		ErrorCode:             a.ErrorCode,
		ErrorMessage:          a.ErrorMessage,
		InternalErrorDetail:   a.InternalErrorDetail,
		GatewayFirstTokenAt:   adminhttp.RFC3339Ptr(a.GatewayFirstTokenAt),
		UpstreamTimeoutPhase:  a.UpstreamTimeoutPhase,
		UpstreamTotalMs:       a.UpstreamTotalMs,
		UpstreamTTFTMs:        a.UpstreamTTFTMs,
		TTFTScoringSample:     a.TTFTScoringSample,
		ErrorScoringSample:    a.ErrorScoringSample,
		ErrorScoringFailure:   a.ErrorScoringFailure,
		FinalUsageReceived:    a.FinalUsageReceived,
		RequestedServiceTier:  a.RequestedServiceTier,
		ForwardedServiceTier:  a.ForwardedServiceTier,
		UpstreamServiceTier:   a.UpstreamServiceTier,
		StartedAt:             adminhttp.RFC3339(a.StartedAt),
		CompletedAt:           adminhttp.RFC3339Ptr(a.CompletedAt),
		CreatedAt:             adminhttp.RFC3339(a.CreatedAt),
	}
}

func toUsageDTO(u query.Usage) usageDTO {
	return usageDTO{
		ID:                          u.ID,
		RequestRecordID:             u.RequestRecordID,
		UncachedInputTokens:         u.UncachedInputTokens,
		CacheReadInputTokens:        u.CacheReadInputTokens,
		CacheCreation5mInputTokens:  u.CacheCreation5mInputTokens,
		CacheCreation1hInputTokens:  u.CacheCreation1hInputTokens,
		CacheCreation30mInputTokens: u.CacheCreation30mInputTokens,
		OutputTokensTotal:           u.OutputTokensTotal,
		ReasoningOutputTokens:       u.ReasoningOutputTokens,
		UsageSource:                 u.UsageSource,
		UsageMappingVersion:         u.UsageMappingVersion,
		CreatedAt:                   adminhttp.RFC3339(u.CreatedAt),
	}
}
