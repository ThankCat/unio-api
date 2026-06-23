package adminapi

import (
	"context"
	"net/http"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

// RequestQueryService 定义 adminapi 查询请求记录所需的最小能力（M6 只读查询台）。
type RequestQueryService interface {
	List(ctx context.Context, params query.RequestListParams) ([]query.RequestSummary, int64, error)
	Get(ctx context.Context, requestID string, includeInternal bool) (query.RequestDetail, error)
}

// requestSummaryDTO 是请求列表项响应体；不含 internal_error_detail。
type requestSummaryDTO struct {
	ID                    int64   `json:"id"`
	RequestID             string  `json:"request_id"`
	UserID                int64   `json:"user_id"`
	ProjectID             int64   `json:"project_id"`
	APIKeyID              int64   `json:"api_key_id"`
	RequestedModelID      string  `json:"requested_model_id"`
	IngressProtocol       string  `json:"ingress_protocol"`
	Operation             string  `json:"operation"`
	ResponseModelID       *string `json:"response_model_id"`
	ResponseProtocol      *string `json:"response_protocol"`
	ResponseID            *string `json:"response_id"`
	Stream                bool    `json:"stream"`
	Status                string  `json:"status"`
	FinalProviderID       *int64  `json:"final_provider_id"`
	FinalChannelID        *int64  `json:"final_channel_id"`
	ErrorCode             *string `json:"error_code"`
	ErrorMessage          *string `json:"error_message"`
	DeliveryStatus        string  `json:"delivery_status"`
	ResponseStartedAt     *string `json:"response_started_at"`
	ResponseCompletedAt   *string `json:"response_completed_at"`
	StartedAt             string  `json:"started_at"`
	CompletedAt           *string `json:"completed_at"`
	CreatedAt             string  `json:"created_at"`
	UpdatedAt             string  `json:"updated_at"`
}

// attemptDTO 是请求详情中的一次上游尝试；internal_error_detail 仅在 ?include_internal=true 时出现。
type attemptDTO struct {
	ID                    int64    `json:"id"`
	AttemptIndex          int32    `json:"attempt_index"`
	ProviderID            int64    `json:"provider_id"`
	ChannelID             int64    `json:"channel_id"`
	AdapterKey            string   `json:"adapter_key"`
	UpstreamModel         string   `json:"upstream_model"`
	UpstreamProtocol      string   `json:"upstream_protocol"`
	UpstreamResponseID    *string  `json:"upstream_response_id"`
	UpstreamResponseModel *string  `json:"upstream_response_model"`
	UpstreamFinishReason  *string  `json:"upstream_finish_reason"`
	FinishClass           *string  `json:"finish_class"`
	Status                string   `json:"status"`
	UpstreamStatusCode    *int32   `json:"upstream_status_code"`
	UpstreamRequestID     *string  `json:"upstream_request_id"`
	ErrorCode             *string  `json:"error_code"`
	ErrorMessage          *string  `json:"error_message"`
	InternalErrorDetail   *string  `json:"internal_error_detail,omitempty"`
	ResponseStartedAt     *string  `json:"response_started_at"`
	FinalUsageReceived    bool     `json:"final_usage_received"`
	StartedAt             string   `json:"started_at"`
	CompletedAt           *string  `json:"completed_at"`
	CreatedAt             string   `json:"created_at"`
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
	InternalErrorDetail *string              `json:"internal_error_detail,omitempty"`
	Attempts            []attemptDTO         `json:"attempts"`
	Usage               *usageDTO            `json:"usage"`
	LedgerEntries       []ledgerEntryDTO     `json:"ledger_entries"`
	BillingException    *billingExceptionDTO `json:"billing_exception"`
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
	projectID, err := optionalInt64Query(r, "project_id")
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

	page := parsePage(r)
	items, total, err := h.service.List(r.Context(), query.RequestListParams{
		UserID:    userID,
		ProjectID: projectID,
		APIKeyID:  apiKeyID,
		Status:    queryString(r, "status"),
		Model:     queryString(r, "model"),
		From:      from,
		To:        to,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]requestSummaryDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toRequestSummaryDTO(item))
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
		ID:                    s.ID,
		RequestID:             s.RequestID,
		UserID:                s.UserID,
		ProjectID:             s.ProjectID,
		APIKeyID:              s.APIKeyID,
		RequestedModelID:      s.RequestedModelID,
		IngressProtocol:       s.IngressProtocol,
		Operation:             s.Operation,
		ResponseModelID:       s.ResponseModelID,
		ResponseProtocol:      s.ResponseProtocol,
		ResponseID:            s.ResponseID,
		Stream:                s.Stream,
		Status:                s.Status,
		FinalProviderID:     s.FinalProviderID,
		FinalChannelID:      s.FinalChannelID,
		ErrorCode:           s.ErrorCode,
		ErrorMessage:        s.ErrorMessage,
		DeliveryStatus:      s.DeliveryStatus,
		ResponseStartedAt:     rfc3339Ptr(s.ResponseStartedAt),
		ResponseCompletedAt:   rfc3339Ptr(s.ResponseCompletedAt),
		StartedAt:             rfc3339(s.StartedAt),
		CompletedAt:           rfc3339Ptr(s.CompletedAt),
		CreatedAt:             rfc3339(s.CreatedAt),
		UpdatedAt:             rfc3339(s.UpdatedAt),
	}
}

func toRequestDetailDTO(d query.RequestDetail) requestDetailDTO {
	dto := requestDetailDTO{
		requestSummaryDTO:   toRequestSummaryDTO(d.RequestSummary),
		InternalErrorDetail: d.InternalErrorDetail,
		Attempts:            make([]attemptDTO, 0, len(d.Attempts)),
		LedgerEntries:       make([]ledgerEntryDTO, 0, len(d.LedgerEntries)),
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
