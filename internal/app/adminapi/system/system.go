package system

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

// RecoveryJobQueryService 定义 adminapi 查询 settlement recovery job 所需的最小能力（M8 运营任务台）。
type RecoveryJobQueryService interface {
	List(ctx context.Context, params query.RecoveryJobListParams) ([]query.RecoveryJobSummary, int64, error)
	Get(ctx context.Context, id int64, includeInternal bool) (query.RecoveryJobDetail, error)
}

// recoveryJobSummaryDTO 是 recovery job 列表项响应体（绝不含内部诊断详情）。
type recoveryJobSummaryDTO struct {
	ID                 int64   `json:"id"`
	UserID             int64   `json:"user_id"`
	RequestRecordID    int64   `json:"request_record_id"`
	AttemptID          int64   `json:"attempt_id"`
	ReservationID      int64   `json:"reservation_id"`
	ResponseProtocol   string  `json:"response_protocol"`
	ResponseID         string  `json:"response_id"`
	ResponseModelID    string  `json:"response_model_id"`
	ModelID            int64   `json:"model_id"`
	ProviderID         int64   `json:"provider_id"`
	ChannelID          int64   `json:"channel_id"`
	UpstreamProtocol   string  `json:"upstream_protocol"`
	UpstreamModel      string  `json:"upstream_model"`
	FinishClass        string  `json:"finish_class"`
	UpstreamStatusCode int32   `json:"upstream_status_code"`
	Currency           string  `json:"currency"`
	EstimatedAmount    string  `json:"estimated_amount"`
	AuthorizedAmount   string  `json:"authorized_amount"`
	Status             string  `json:"status"`
	AttemptCount       int32   `json:"attempt_count"`
	MaxAttempts        int32   `json:"max_attempts"`
	NextRunAt          string  `json:"next_run_at"`
	LockedBy           *string `json:"locked_by"`
	LockedUntil        *string `json:"locked_until"`
	LastErrorCode      *string `json:"last_error_code"`
	LastErrorMessage   *string `json:"last_error_message"`
	LastAttemptedAt    *string `json:"last_attempted_at"`
	CompletedAt        *string `json:"completed_at"`
	CreatedAt          string  `json:"created_at"`
	UpdatedAt          string  `json:"updated_at"`

	// RequestPublicID 是对外 request_id(req_xxx),供前端跳转到请求详情;缺失为空串。
	RequestPublicID string `json:"request_public_id"`

	// 资金闭环(关联预授权行/超额补扣流水):冻结(authorized)→ 实扣(captured+overage)→ 释放(released)。
	// reservation_status: authorized=未结算 / captured=已实扣 / released=已全额释放(dead 收口)。
	ReservationStatus string `json:"reservation_status"`
	CapturedAmount    string `json:"captured_amount"`
	ReleasedAmount    string `json:"released_amount"`
	OverageAmount     string `json:"overage_amount"`
}

// recoveryJobDetailDTO 是 recovery job 详情响应体：摘要 + 审计补充 + 受控内部诊断详情。
type recoveryJobDetailDTO struct {
	recoveryJobSummaryDTO

	UpstreamResponseID   string  `json:"upstream_response_id"`
	UpstreamFinishReason string  `json:"upstream_finish_reason"`
	UpstreamRequestID    *string `json:"upstream_request_id"`
	UsageSource          string  `json:"usage_source"`
	UsageMappingVersion  string  `json:"usage_mapping_version"`
	FormulaVersion       string  `json:"formula_version"`
	PricingUnit          string  `json:"pricing_unit"`

	UncachedInputTokens      int64 `json:"uncached_input_tokens"`
	CacheReadInputTokens     int64 `json:"cache_read_input_tokens"`
	CacheWrite5mInputTokens  int64 `json:"cache_write_5m_input_tokens"`
	CacheWrite1hInputTokens  int64 `json:"cache_write_1h_input_tokens"`
	CacheWrite30mInputTokens int64 `json:"cache_write_30m_input_tokens"`
	OutputTokensTotal        int64 `json:"output_tokens_total"`
	ReasoningOutputTokens    int64 `json:"reasoning_output_tokens"`

	// 仅 ?include_internal=true 时回显内部诊断详情，否则为 null。
	LastInternalErrorDetail *string `json:"last_internal_error_detail"`
}

type recoveryJobsHandler struct {
	service RecoveryJobQueryService
}

func (h *recoveryJobsHandler) list(w http.ResponseWriter, r *http.Request) {
	userID, err := adminhttp.OptionalInt64Query(r, "user_id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
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

	page := adminhttp.ParsePage(r)
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"created_at": {},
		"status":     {},
		"user_id":    {},
	}, "created_at", true)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	items, total, err := h.service.List(r.Context(), query.RecoveryJobListParams{
		Status:    adminhttp.QueryString(r, "status"),
		UserID:    userID,
		From:      from,
		To:        to,
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]recoveryJobSummaryDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toRecoveryJobSummaryDTO(item))
	}
	adminhttp.WriteList(w, http.StatusOK, dtos, page, total)
}

func (h *recoveryJobsHandler) get(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	detail, err := h.service.Get(r.Context(), id, adminhttp.BoolQuery(r, "include_internal"))
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, toRecoveryJobDetailDTO(detail))
}

func toRecoveryJobSummaryDTO(j query.RecoveryJobSummary) recoveryJobSummaryDTO {
	return recoveryJobSummaryDTO{
		ID:                 j.ID,
		UserID:             j.UserID,
		RequestRecordID:    j.RequestRecordID,
		AttemptID:          j.AttemptID,
		ReservationID:      j.ReservationID,
		ResponseProtocol:   j.ResponseProtocol,
		ResponseID:         j.ResponseID,
		ResponseModelID:    j.ResponseModelID,
		ModelID:            j.ModelID,
		ProviderID:         j.ProviderID,
		ChannelID:          j.ChannelID,
		UpstreamProtocol:   j.UpstreamProtocol,
		UpstreamModel:      j.UpstreamModel,
		FinishClass:        j.FinishClass,
		UpstreamStatusCode: j.UpstreamStatusCode,
		Currency:           j.Currency,
		EstimatedAmount:    j.EstimatedAmount,
		AuthorizedAmount:   j.AuthorizedAmount,
		Status:             j.Status,
		AttemptCount:       j.AttemptCount,
		MaxAttempts:        j.MaxAttempts,
		NextRunAt:          adminhttp.RFC3339(j.NextRunAt),
		LockedBy:           j.LockedBy,
		LockedUntil:        adminhttp.RFC3339Ptr(j.LockedUntil),
		LastErrorCode:      j.LastErrorCode,
		LastErrorMessage:   j.LastErrorMessage,
		LastAttemptedAt:    adminhttp.RFC3339Ptr(j.LastAttemptedAt),
		CompletedAt:        adminhttp.RFC3339Ptr(j.CompletedAt),
		CreatedAt:          adminhttp.RFC3339(j.CreatedAt),
		UpdatedAt:          adminhttp.RFC3339(j.UpdatedAt),
		RequestPublicID:    j.RequestPublicID,
		ReservationStatus:  j.ReservationStatus,
		CapturedAmount:     j.CapturedAmount,
		ReleasedAmount:     j.ReleasedAmount,
		OverageAmount:      j.OverageAmount,
	}
}

func toRecoveryJobDetailDTO(j query.RecoveryJobDetail) recoveryJobDetailDTO {
	return recoveryJobDetailDTO{
		recoveryJobSummaryDTO:    toRecoveryJobSummaryDTO(j.RecoveryJobSummary),
		UpstreamResponseID:       j.UpstreamResponseID,
		UpstreamFinishReason:     j.UpstreamFinishReason,
		UpstreamRequestID:        j.UpstreamRequestID,
		UsageSource:              j.UsageSource,
		UsageMappingVersion:      j.UsageMappingVersion,
		FormulaVersion:           j.FormulaVersion,
		PricingUnit:              j.PricingUnit,
		UncachedInputTokens:      j.UncachedInputTokens,
		CacheReadInputTokens:     j.CacheReadInputTokens,
		CacheWrite5mInputTokens:  j.CacheWrite5mInputTokens,
		CacheWrite1hInputTokens:  j.CacheWrite1hInputTokens,
		CacheWrite30mInputTokens: j.CacheWrite30mInputTokens,
		OutputTokensTotal:        j.OutputTokensTotal,
		ReasoningOutputTokens:    j.ReasoningOutputTokens,
		LastInternalErrorDetail:  j.LastInternalErrorDetail,
	}
}
