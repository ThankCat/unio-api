package model

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelrouting"
)

// ModelRoutingService 定义选路观测所需能力：此刻的候选运行态与历史选路分布。
type ModelRoutingService interface {
	Candidates(ctx context.Context, modelID int64) (modelrouting.CandidateView, error)
	Stats(ctx context.Context, modelID int64, from, to time.Time) (modelrouting.Stats, error)
	Traces(ctx context.Context, modelID int64, from, to time.Time, limit, offset int32) ([]modelrouting.TraceRow, int64, error)
	LiveTraffic(ctx context.Context) (modelrouting.LiveTraffic, error)
}

// routingCandidateDTO 的指针字段为 null 表示运行态读不到，与 0 语义不同：
// 0 意味着此刻没有请求，null 意味着不知道。前端据此显示占位符而非零值。
type routingCandidateDTO struct {
	ChannelID       int64  `json:"channel_id"`
	ChannelName     string `json:"channel_name"`
	ChannelStatus   string `json:"channel_status"`
	ProviderID      int64  `json:"provider_id"`
	ProviderName    string `json:"provider_name"`
	ProviderStatus  string `json:"provider_status"`
	BindingStatus   string `json:"binding_status"`
	CredentialValid bool   `json:"credential_valid"`
	Priority        int32  `json:"priority"`

	RuntimeStatus       string `json:"runtime_status"`
	BreakerState        string `json:"breaker_state"`
	BreakerOpenRemainMs *int64 `json:"breaker_open_remaining_ms"`
	ConcurrencyUsed     *int64 `json:"concurrency_used"`
	ConcurrencyLimit    *int64 `json:"concurrency_limit"`
	CooldownRemainingMs *int64 `json:"cooldown_remaining_ms"`
	PermissionPaused    bool   `json:"permission_paused"`
	// requests_this_minute 取自然 UTC 分钟桶，不是滚动 60 秒。
	RequestsThisMinute *int64 `json:"requests_this_minute"`
}

type routingCandidatesDTO struct {
	// runtime_available 为 false 表示 Redis 运行态没读齐，候选里只有数据库事实。
	RuntimeAvailable bool                  `json:"runtime_available"`
	RuntimeErrorCode string                `json:"runtime_error_code,omitempty"`
	ObservedAt       string                `json:"observed_at"`
	Candidates       []routingCandidateDTO `json:"candidates"`
}

type routingSelectionDTO struct {
	// channel_id 为 0 表示这些选路没能选出任何渠道。
	ChannelID   int64  `json:"channel_id"`
	ChannelName string `json:"channel_name"`
	Selections  int64  `json:"selections"`
}

type routingOutcomeDTO struct {
	FinalResult string `json:"final_result"`
	Occurrences int64  `json:"occurrences"`
}

type routingExclusionDTO struct {
	Reason            string `json:"reason"`
	Occurrences       int64  `json:"occurrences"`
	SampleChannelID   int64  `json:"sample_channel_id"`
	SampleChannelName string `json:"sample_channel_name"`
	ChannelsTouched   int64  `json:"channels_touched"`
}

type routingStatsDTO struct {
	// from/to 是实际统计窗口；window_truncated 为 true 时它比请求的窗口短。
	From            string `json:"from"`
	To              string `json:"to"`
	WindowTruncated bool   `json:"window_truncated"`

	Selections      []routingSelectionDTO `json:"selections"`
	TotalSelections int64                 `json:"total_selections"`
	Outcomes        []routingOutcomeDTO   `json:"outcomes"`
	Exclusions      []routingExclusionDTO `json:"exclusions"`
	TotalExclusions int64                 `json:"total_exclusions"`
}

type routingTraceDTO struct {
	At        string `json:"at"`
	RequestID string `json:"request_id"`
	// final_result 为空表示 trace 仍是 partial（请求进行中或进程崩溃遗留）。
	FinalResult         string `json:"final_result"`
	CandidateCount      int32  `json:"candidate_count"`
	EligibleCount       int32  `json:"eligible_count"`
	SelectedChannelID   *int64 `json:"selected_channel_id"`
	SelectedChannelName string `json:"selected_channel_name"`
	FallbackCount       int32  `json:"fallback_count"`
	CapacityWaitResult  string `json:"capacity_wait_result"`
}

type modelRoutingHandler struct {
	service ModelRoutingService
}

func (h *modelRoutingHandler) candidates(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	view, err := h.service.Candidates(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := routingCandidatesDTO{
		RuntimeAvailable: view.RuntimeAvailable,
		RuntimeErrorCode: view.RuntimeErrorCode,
		ObservedAt:       adminhttp.RFC3339(view.ObservedAt),
		Candidates:       make([]routingCandidateDTO, 0, len(view.Candidates)),
	}
	for _, candidate := range view.Candidates {
		out.Candidates = append(out.Candidates, routingCandidateDTO{
			ChannelID:           candidate.ChannelID,
			ChannelName:         candidate.ChannelName,
			ChannelStatus:       candidate.ChannelStatus,
			ProviderID:          candidate.ProviderID,
			ProviderName:        candidate.ProviderName,
			ProviderStatus:      candidate.ProviderStatus,
			BindingStatus:       candidate.BindingStatus,
			CredentialValid:     candidate.CredentialValid,
			Priority:            candidate.Priority,
			RuntimeStatus:       candidate.RuntimeStatus,
			BreakerState:        candidate.BreakerState,
			BreakerOpenRemainMs: candidate.BreakerOpenRemainMs,
			ConcurrencyUsed:     candidate.ConcurrencyUsed,
			ConcurrencyLimit:    candidate.ConcurrencyLimit,
			CooldownRemainingMs: candidate.CooldownRemainingMs,
			PermissionPaused:    candidate.PermissionPaused,
			RequestsThisMinute:  candidate.RPM,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *modelRoutingHandler) stats(w http.ResponseWriter, r *http.Request) {
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
	stats, err := h.service.Stats(r.Context(), id, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	out := routingStatsDTO{
		From:            adminhttp.RFC3339(stats.From),
		To:              adminhttp.RFC3339(stats.To),
		WindowTruncated: stats.WindowTruncated,
		TotalSelections: stats.TotalSelections,
		TotalExclusions: stats.TotalExclusions,
		Selections:      make([]routingSelectionDTO, 0, len(stats.Selections)),
		Outcomes:        make([]routingOutcomeDTO, 0, len(stats.Outcomes)),
		Exclusions:      make([]routingExclusionDTO, 0, len(stats.Exclusions)),
	}
	for _, row := range stats.Selections {
		out.Selections = append(out.Selections, routingSelectionDTO{
			ChannelID:   row.ChannelID,
			ChannelName: row.ChannelName,
			Selections:  row.Selections,
		})
	}
	for _, row := range stats.Outcomes {
		out.Outcomes = append(out.Outcomes, routingOutcomeDTO{
			FinalResult: row.FinalResult,
			Occurrences: row.Occurrences,
		})
	}
	for _, row := range stats.Exclusions {
		out.Exclusions = append(out.Exclusions, routingExclusionDTO{
			Reason:            row.Reason,
			Occurrences:       row.Occurrences,
			SampleChannelID:   row.SampleChannelID,
			SampleChannelName: row.SampleChannelName,
			ChannelsTouched:   row.ChannelsTouched,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}

func (h *modelRoutingHandler) traces(w http.ResponseWriter, r *http.Request) {
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
	rows, total, err := h.service.Traces(r.Context(), id, from, to, page.Limit(), page.Offset())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]routingTraceDTO, 0, len(rows))
	for _, row := range rows {
		out = append(out, routingTraceDTO{
			At:                  adminhttp.RFC3339(row.At),
			RequestID:           row.RequestID,
			FinalResult:         row.FinalResult,
			CandidateCount:      row.CandidateCount,
			EligibleCount:       row.EligibleCount,
			SelectedChannelID:   row.SelectedChannelID,
			SelectedChannelName: row.SelectedChannelName,
			FallbackCount:       row.FallbackCount,
			CapacityWaitResult:  row.CapacityWaitResult,
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}
