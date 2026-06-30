package adminapi

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

// UsageQueryService 定义 adminapi 查询用量所需的最小能力（M6 只读查询台）。
type UsageQueryService interface {
	List(ctx context.Context, params query.UsageListParams) ([]query.UsageSummary, int64, error)
}

// usageSummaryDTO 是用量列表项响应体：用量事实 + 请求归属维度。
type usageSummaryDTO struct {
	ID                      int64   `json:"id"`
	RequestRecordID         int64   `json:"request_record_id"`
	RequestID               string  `json:"request_id"`
	UserID                  int64   `json:"user_id"`
	APIKeyID                int64   `json:"api_key_id"`
	RequestedModelID        string  `json:"requested_model_id"`
	ResponseModelID         *string `json:"response_model_id"`
	Status                  string  `json:"status"`
	UncachedInputTokens     int64   `json:"uncached_input_tokens"`
	CacheReadInputTokens    int64   `json:"cache_read_input_tokens"`
	CacheWrite5mInputTokens int64   `json:"cache_write_5m_input_tokens"`
	CacheWrite1hInputTokens int64   `json:"cache_write_1h_input_tokens"`
	OutputTokensTotal       int64   `json:"output_tokens_total"`
	ReasoningOutputTokens   int64   `json:"reasoning_output_tokens"`
	UsageSource             string  `json:"usage_source"`
	UsageMappingVersion     string  `json:"usage_mapping_version"`
	CreatedAt               string  `json:"created_at"`
}

type usageHandler struct {
	service UsageQueryService
}

func (h *usageHandler) list(w http.ResponseWriter, r *http.Request) {
	userID, err := optionalInt64Query(r, "user_id")
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
	sort, err := parseListSort(r, map[string]struct{}{
		"created_at": {},
		"model":      {},
		"user_id":    {},
	}, "created_at", true)
	if err != nil {
		writeSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	items, total, err := h.service.List(r.Context(), query.UsageListParams{
		UserID:    userID,
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

	dtos := make([]usageSummaryDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toUsageSummaryDTO(item))
	}
	writeList(w, http.StatusOK, dtos, page, total)
}

func toUsageSummaryDTO(u query.UsageSummary) usageSummaryDTO {
	return usageSummaryDTO{
		ID:                      u.ID,
		RequestRecordID:         u.RequestRecordID,
		RequestID:               u.RequestID,
		UserID:                  u.UserID,
		APIKeyID:                u.APIKeyID,
		RequestedModelID:        u.RequestedModelID,
		ResponseModelID:         u.ResponseModelID,
		Status:                  u.Status,
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
