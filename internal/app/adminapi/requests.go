package adminapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

// RequestQueryService 定义 adminapi 查询请求记录所需的最小能力（M6 只读查询台）。
type RequestQueryService interface {
	List(ctx context.Context, params query.RequestListParams) ([]query.RequestListItem, int64, error)
	Get(ctx context.Context, requestID string, includeInternal bool) (query.RequestDetail, error)
}

// requestSummaryDTO 是请求列表项响应体；不含 internal_error_detail。
type requestSummaryDTO struct {
	ID                  int64   `json:"id"`
	RequestID           string  `json:"request_id"`
	UserID              int64   `json:"user_id"`
	APIKeyID            int64   `json:"api_key_id"`
	RequestedModelID    string  `json:"requested_model_id"`
	IngressProtocol     string  `json:"ingress_protocol"`
	Operation           string  `json:"operation"`
	ResponseModelID     *string `json:"response_model_id"`
	ResponseProtocol    *string `json:"response_protocol"`
	ResponseID          *string `json:"response_id"`
	Stream              bool    `json:"stream"`
	Status              string  `json:"status"`
	FinalProviderID     *int64  `json:"final_provider_id"`
	FinalChannelID      *int64  `json:"final_channel_id"`
	ErrorCode           *string `json:"error_code"`
	ErrorMessage        *string `json:"error_message"`
	DeliveryStatus      string  `json:"delivery_status"`
	ResponseStartedAt   *string `json:"response_started_at"`
	ResponseCompletedAt *string `json:"response_completed_at"`
	StartedAt           string  `json:"started_at"`
	CompletedAt         *string `json:"completed_at"`
	CreatedAt           string  `json:"created_at"`
	UpdatedAt           string  `json:"updated_at"`
}

// requestListItemDTO 是请求列表项（富化）：请求事实 + 用量/成本/扣费 + 线路/渠道链 + 时延。
type requestListItemDTO struct {
	requestSummaryDTO
	UncachedInputTokens     int64 `json:"uncached_input_tokens"`
	CacheReadInputTokens    int64 `json:"cache_read_input_tokens"`
	CacheWrite5mInputTokens int64 `json:"cache_write_5m_input_tokens"`
	CacheWrite1hInputTokens int64 `json:"cache_write_1h_input_tokens"`
	OutputTokens            int64 `json:"output_tokens"`
	ReasoningOutputTokens   int64 `json:"reasoning_output_tokens"`
	// USD 十进制字符串；无结算快照 / 账本时为 null。
	UserChargeUsd            *string `json:"user_charge_usd"`
	TotalCostUsd             *string `json:"total_cost_usd"`
	UncachedInputCostUsd     *string `json:"uncached_input_cost_usd"`
	CacheReadInputCostUsd    *string `json:"cache_read_input_cost_usd"`
	CacheWrite5mInputCostUsd *string `json:"cache_write_5m_input_cost_usd"`
	CacheWrite1hInputCostUsd *string `json:"cache_write_1h_input_cost_usd"`
	OutputCostUsd            *string `json:"output_cost_usd"`
	ReasoningOutputCostUsd   *string `json:"reasoning_output_cost_usd"`
	// 计费单价快照（USD 字符串，per_1m_tokens）：平台成本单价×6 + 用户售价单价×6，供「单价×tokens=金额」计算过程展示。
	UncachedInputCostUnitUsd      *string `json:"uncached_input_cost_unit_usd"`
	CacheReadInputCostUnitUsd     *string `json:"cache_read_input_cost_unit_usd"`
	CacheWrite5mInputCostUnitUsd  *string `json:"cache_write_5m_input_cost_unit_usd"`
	CacheWrite1hInputCostUnitUsd  *string `json:"cache_write_1h_input_cost_unit_usd"`
	OutputCostUnitUsd             *string `json:"output_cost_unit_usd"`
	ReasoningOutputCostUnitUsd    *string `json:"reasoning_output_cost_unit_usd"`
	UncachedInputPriceUnitUsd     *string `json:"uncached_input_price_unit_usd"`
	CacheReadInputPriceUnitUsd    *string `json:"cache_read_input_price_unit_usd"`
	CacheWrite5mInputPriceUnitUsd *string `json:"cache_write_5m_input_price_unit_usd"`
	CacheWrite1hInputPriceUnitUsd *string `json:"cache_write_1h_input_price_unit_usd"`
	OutputPriceUnitUsd            *string `json:"output_price_unit_usd"`
	ReasoningOutputPriceUnitUsd   *string `json:"reasoning_output_price_unit_usd"`
	// 用户/Key（明文供列表点击复制，口径同 api-keys 页）。
	ApiKeyName            *string  `json:"api_key_name"`
	ApiKeyPrefix          *string  `json:"api_key_prefix"`
	ApiKeyPlaintext       *string  `json:"api_key_plaintext"`
	RouteName             *string  `json:"route_name"`
	RoutePriceRatio       *string  `json:"route_price_ratio"`
	RouteMode             *string  `json:"route_mode"`
	FinalChannelName      *string  `json:"final_channel_name"`
	ChannelChain          string   `json:"channel_chain"`
	ModelDisplayName      *string  `json:"model_display_name"`
	ModelOwnedBy          *string  `json:"model_owned_by"`
	ReasoningEffort       *string  `json:"reasoning_effort"`
	ReasoningBudgetTokens *int32   `json:"reasoning_budget_tokens"`
	ClientIP              *string  `json:"client_ip"`
	LatencyMs             *int64   `json:"latency_ms"`
	TtftMs                *int64   `json:"ttft_ms"`
	Tps                   *float64 `json:"tps"`
}

// costSnapshotDTO 是平台成本快照（单价 per_1m_tokens + 金额，USD 字符串）。
type costSnapshotDTO struct {
	UncachedInputCostUnit       *string `json:"uncached_input_cost_unit"`
	CacheReadInputCostUnit      *string `json:"cache_read_input_cost_unit"`
	CacheWrite5mInputCostUnit   *string `json:"cache_write_5m_input_cost_unit"`
	CacheWrite1hInputCostUnit   *string `json:"cache_write_1h_input_cost_unit"`
	OutputCostUnit              *string `json:"output_cost_unit"`
	ReasoningOutputCostUnit     *string `json:"reasoning_output_cost_unit"`
	UncachedInputCostAmount     *string `json:"uncached_input_cost_amount"`
	CacheReadInputCostAmount    *string `json:"cache_read_input_cost_amount"`
	CacheWrite5mInputCostAmount *string `json:"cache_write_5m_input_cost_amount"`
	CacheWrite1hInputCostAmount *string `json:"cache_write_1h_input_cost_amount"`
	OutputCostAmount            *string `json:"output_cost_amount"`
	ReasoningOutputCostAmount   *string `json:"reasoning_output_cost_amount"`
	TotalCostAmount             *string `json:"total_cost_amount"`
}

// priceSnapshotDTO 是客户售价快照（单价 per_1m_tokens，USD 字符串）。
type priceSnapshotDTO struct {
	UncachedInputPrice     *string `json:"uncached_input_price"`
	CacheReadInputPrice    *string `json:"cache_read_input_price"`
	CacheWrite5mInputPrice *string `json:"cache_write_5m_input_price"`
	CacheWrite1hInputPrice *string `json:"cache_write_1h_input_price"`
	OutputPrice            *string `json:"output_price"`
	ReasoningOutputPrice   *string `json:"reasoning_output_price"`
}

// attemptDTO 是请求详情中的一次上游尝试；internal_error_detail 仅在 ?include_internal=true 时出现。
type attemptDTO struct {
	ID                    int64   `json:"id"`
	AttemptIndex          int32   `json:"attempt_index"`
	ProviderID            int64   `json:"provider_id"`
	ChannelID             int64   `json:"channel_id"`
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
	ResponseStartedAt     *string `json:"response_started_at"`
	FinalUsageReceived    bool    `json:"final_usage_received"`
	StartedAt             string  `json:"started_at"`
	CompletedAt           *string `json:"completed_at"`
	CreatedAt             string  `json:"created_at"`
}

// usageDTO 是请求详情中的协议无关用量事实。
type usageDTO struct {
	ID                      int64  `json:"id"`
	RequestRecordID         int64  `json:"request_record_id"`
	UncachedInputTokens     int64  `json:"uncached_input_tokens"`
	CacheReadInputTokens    int64  `json:"cache_read_input_tokens"`
	CacheWrite5mInputTokens int64  `json:"cache_write_5m_input_tokens"`
	CacheWrite1hInputTokens int64  `json:"cache_write_1h_input_tokens"`
	OutputTokensTotal       int64  `json:"output_tokens_total"`
	ReasoningOutputTokens   int64  `json:"reasoning_output_tokens"`
	UsageSource             string `json:"usage_source"`
	UsageMappingVersion     string `json:"usage_mapping_version"`
	CreatedAt               string `json:"created_at"`
}

// requestDetailDTO 是请求详情聚合响应体。
type requestDetailDTO struct {
	requestSummaryDTO
	InternalErrorDetail   *string              `json:"internal_error_detail,omitempty"`
	RouteID               *int64               `json:"route_id"`
	ReasoningEffort       *string              `json:"reasoning_effort"`
	ReasoningBudgetTokens *int32               `json:"reasoning_budget_tokens"`
	ClientIP              *string              `json:"client_ip"`
	CostSnapshot          *costSnapshotDTO     `json:"cost_snapshot"`
	PriceSnapshot         *priceSnapshotDTO    `json:"price_snapshot"`
	RoutePriceRatio       *string              `json:"route_price_ratio"`
	RouteMode             *string              `json:"route_mode"`
	Attempts              []attemptDTO         `json:"attempts"`
	Usage                 *usageDTO            `json:"usage"`
	LedgerEntries         []ledgerEntryDTO     `json:"ledger_entries"`
	BillingException      *billingExceptionDTO `json:"billing_exception"`
}

type requestsHandler struct {
	service RequestQueryService
}

func (h *requestsHandler) list(w http.ResponseWriter, r *http.Request) {
	userID, err := optionalInt64Query(r, "user_id")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	apiKeyID, err := optionalInt64Query(r, "api_key_id")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	from, err := optionalTimeQuery(r, "from")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	to, err := optionalTimeQuery(r, "to")
	if err != nil {
		writeServiceError(w, err)
		return
	}

	sort, err := parseListSort(r, map[string]struct{}{
		"created_at": {},
		"status":     {},
		"user_id":    {},
		"model":      {},
		"stream":     {},
	}, "created_at", true)
	if err != nil {
		writeSortError(w, err)
		return
	}

	page := parsePage(r)
	field, desc := sort.SQLParams()
	items, total, err := h.service.List(r.Context(), query.RequestListParams{
		UserID:    userID,
		APIKeyID:  apiKeyID,
		Status:    queryString(r, "status"),
		Model:     queryString(r, "model"),
		From:      from,
		To:        to,
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]requestListItemDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toRequestListItemDTO(item))
	}
	writeList(w, http.StatusOK, dtos, page, total)
}

func (h *requestsHandler) get(w http.ResponseWriter, r *http.Request) {
	requestID := chi.URLParam(r, "requestId")
	includeInternal := boolQuery(r, "include_internal")

	detail, err := h.service.Get(r.Context(), requestID, includeInternal)
	if err != nil {
		writeServiceError(w, err)
		return
	}

	writeData(w, http.StatusOK, toRequestDetailDTO(detail))
}

func toRequestSummaryDTO(s query.RequestSummary) requestSummaryDTO {
	return requestSummaryDTO{
		ID:                  s.ID,
		RequestID:           s.RequestID,
		UserID:              s.UserID,
		APIKeyID:            s.APIKeyID,
		RequestedModelID:    s.RequestedModelID,
		IngressProtocol:     s.IngressProtocol,
		Operation:           s.Operation,
		ResponseModelID:     s.ResponseModelID,
		ResponseProtocol:    s.ResponseProtocol,
		ResponseID:          s.ResponseID,
		Stream:              s.Stream,
		Status:              s.Status,
		FinalProviderID:     s.FinalProviderID,
		FinalChannelID:      s.FinalChannelID,
		ErrorCode:           s.ErrorCode,
		ErrorMessage:        s.ErrorMessage,
		DeliveryStatus:      s.DeliveryStatus,
		ResponseStartedAt:   rfc3339Ptr(s.ResponseStartedAt),
		ResponseCompletedAt: rfc3339Ptr(s.ResponseCompletedAt),
		StartedAt:           rfc3339(s.StartedAt),
		CompletedAt:         rfc3339Ptr(s.CompletedAt),
		CreatedAt:           rfc3339(s.CreatedAt),
		UpdatedAt:           rfc3339(s.UpdatedAt),
	}
}

func toRequestListItemDTO(item query.RequestListItem) requestListItemDTO {
	return requestListItemDTO{
		requestSummaryDTO:        toRequestSummaryDTO(item.RequestSummary),
		UncachedInputTokens:      item.UncachedInputTokens,
		CacheReadInputTokens:     item.CacheReadInputTokens,
		CacheWrite5mInputTokens:  item.CacheWrite5mInputTokens,
		CacheWrite1hInputTokens:  item.CacheWrite1hInputTokens,
		OutputTokens:             item.OutputTokens,
		ReasoningOutputTokens:    item.ReasoningOutputTokens,
		UserChargeUsd:            item.UserChargeUSD,
		TotalCostUsd:             item.TotalCostUSD,
		UncachedInputCostUsd:     item.UncachedInputCostUSD,
		CacheReadInputCostUsd:    item.CacheReadInputCostUSD,
		CacheWrite5mInputCostUsd: item.CacheWrite5mInputCostUSD,
		CacheWrite1hInputCostUsd: item.CacheWrite1hInputCostUSD,
		OutputCostUsd:            item.OutputCostUSD,
		ReasoningOutputCostUsd:   item.ReasoningOutputCostUSD,

		UncachedInputCostUnitUsd:      item.UncachedInputCostUnitUSD,
		CacheReadInputCostUnitUsd:     item.CacheReadInputCostUnitUSD,
		CacheWrite5mInputCostUnitUsd:  item.CacheWrite5mInputCostUnitUSD,
		CacheWrite1hInputCostUnitUsd:  item.CacheWrite1hInputCostUnitUSD,
		OutputCostUnitUsd:             item.OutputCostUnitUSD,
		ReasoningOutputCostUnitUsd:    item.ReasoningOutputCostUnitUSD,
		UncachedInputPriceUnitUsd:     item.UncachedInputPriceUnitUSD,
		CacheReadInputPriceUnitUsd:    item.CacheReadInputPriceUnitUSD,
		CacheWrite5mInputPriceUnitUsd: item.CacheWrite5mInputPriceUnitUSD,
		CacheWrite1hInputPriceUnitUsd: item.CacheWrite1hInputPriceUnitUSD,
		OutputPriceUnitUsd:            item.OutputPriceUnitUSD,
		ReasoningOutputPriceUnitUsd:   item.ReasoningOutputPriceUnitUSD,

		ApiKeyName:      item.APIKeyName,
		ApiKeyPrefix:    item.APIKeyPrefix,
		ApiKeyPlaintext: item.APIKeyPlaintext,

		RouteName:             item.RouteName,
		RoutePriceRatio:       item.RoutePriceRatio,
		RouteMode:             item.RouteMode,
		FinalChannelName:      item.FinalChannelName,
		ChannelChain:          item.ChannelChain,
		ModelDisplayName:      item.ModelDisplayName,
		ModelOwnedBy:          item.ModelOwnedBy,
		ReasoningEffort:       item.ReasoningEffort,
		ReasoningBudgetTokens: item.ReasoningBudgetTokens,
		ClientIP:              item.ClientIP,
		LatencyMs:             item.LatencyMs,
		TtftMs:                item.TtftMs,
		Tps:                   item.TPS,
	}
}

func toCostSnapshotDTO(c query.CostSnapshotView) costSnapshotDTO {
	return costSnapshotDTO{
		UncachedInputCostUnit:       c.UncachedInputCostUnit,
		CacheReadInputCostUnit:      c.CacheReadInputCostUnit,
		CacheWrite5mInputCostUnit:   c.CacheWrite5mInputCostUnit,
		CacheWrite1hInputCostUnit:   c.CacheWrite1hInputCostUnit,
		OutputCostUnit:              c.OutputCostUnit,
		ReasoningOutputCostUnit:     c.ReasoningOutputCostUnit,
		UncachedInputCostAmount:     c.UncachedInputCostAmount,
		CacheReadInputCostAmount:    c.CacheReadInputCostAmount,
		CacheWrite5mInputCostAmount: c.CacheWrite5mInputCostAmount,
		CacheWrite1hInputCostAmount: c.CacheWrite1hInputCostAmount,
		OutputCostAmount:            c.OutputCostAmount,
		ReasoningOutputCostAmount:   c.ReasoningOutputCostAmount,
		TotalCostAmount:             c.TotalCostAmount,
	}
}

func toPriceSnapshotDTO(p query.PriceSnapshotView) priceSnapshotDTO {
	return priceSnapshotDTO{
		UncachedInputPrice:     p.UncachedInputPrice,
		CacheReadInputPrice:    p.CacheReadInputPrice,
		CacheWrite5mInputPrice: p.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice: p.CacheWrite1hInputPrice,
		OutputPrice:            p.OutputPrice,
		ReasoningOutputPrice:   p.ReasoningOutputPrice,
	}
}

func toRequestDetailDTO(d query.RequestDetail) requestDetailDTO {
	dto := requestDetailDTO{
		requestSummaryDTO:     toRequestSummaryDTO(d.RequestSummary),
		InternalErrorDetail:   d.InternalErrorDetail,
		RouteID:               d.RouteID,
		ReasoningEffort:       d.ReasoningEffort,
		ReasoningBudgetTokens: d.ReasoningBudgetTokens,
		ClientIP:              d.ClientIP,
		RoutePriceRatio:       d.RoutePriceRatio,
		RouteMode:             d.RouteMode,
		Attempts:              make([]attemptDTO, 0, len(d.Attempts)),
		LedgerEntries:         make([]ledgerEntryDTO, 0, len(d.LedgerEntries)),
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
		dto.LedgerEntries = append(dto.LedgerEntries, toLedgerEntryDTO(e))
	}
	if d.Usage != nil {
		u := toUsageDTO(*d.Usage)
		dto.Usage = &u
	}
	if d.BillingException != nil {
		be := toBillingExceptionDTO(*d.BillingException)
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
		ResponseStartedAt:     rfc3339Ptr(a.ResponseStartedAt),
		FinalUsageReceived:    a.FinalUsageReceived,
		StartedAt:             rfc3339(a.StartedAt),
		CompletedAt:           rfc3339Ptr(a.CompletedAt),
		CreatedAt:             rfc3339(a.CreatedAt),
	}
}

func toUsageDTO(u query.Usage) usageDTO {
	return usageDTO{
		ID:                      u.ID,
		RequestRecordID:         u.RequestRecordID,
		UncachedInputTokens:     u.UncachedInputTokens,
		CacheReadInputTokens:    u.CacheReadInputTokens,
		CacheWrite5mInputTokens: u.CacheWrite5mInputTokens,
		CacheWrite1hInputTokens: u.CacheWrite1hInputTokens,
		OutputTokensTotal:       u.OutputTokensTotal,
		ReasoningOutputTokens:   u.ReasoningOutputTokens,
		UsageSource:             u.UsageSource,
		UsageMappingVersion:     u.UsageMappingVersion,
		CreatedAt:               rfc3339(u.CreatedAt),
	}
}
