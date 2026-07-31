package adminhttp

import (
	"encoding/json"

	"github.com/ThankCat/unio-gateway/internal/service/admin/routingtrace"
)

// RoutingDecisionDTO is the structured, admin-safe routing process for one request.
type RoutingDecisionDTO struct {
	ID               int64                  `json:"id"`
	RequestRecordID  int64                  `json:"request_record_id"`
	RequestID        string                 `json:"request_id"`
	RequestStatus    string                 `json:"request_status"`
	RouteID          int64                  `json:"route_id"`
	Mode             string                 `json:"mode"`
	RequestedModelID string                 `json:"requested_model_id"`
	Protocol         string                 `json:"protocol"`
	Endpoint         string                 `json:"endpoint"`
	TraceStatus      string                 `json:"trace_status"`
	SchemaVersion    int32                  `json:"schema_version"`
	AlgorithmVersion string                 `json:"algorithm_version"`
	Summary          RoutingTraceSummaryDTO `json:"summary"`
	Process          json.RawMessage        `json:"process"`
	CreatedAt        string                 `json:"created_at"`
	UpdatedAt        string                 `json:"updated_at"`
}

type RoutingTraceSummaryDTO struct {
	PoolSize            int32   `json:"pool_size"`
	EligibleCount       int32   `json:"eligible_count"`
	BaselineOrder       []int64 `json:"baseline_order"`
	ActualScanOrder     []int64 `json:"actual_scan_order"`
	AttemptedChannelIDs []int64 `json:"attempted_channel_ids"`
	SelectedChannelID   *int64  `json:"selected_channel_id"`
	FinalChannelID      *int64  `json:"final_channel_id"`
	FallbackCount       int32   `json:"fallback_count"`
	FinalResult         *string `json:"final_result"`
	StickyKeyPresent    bool    `json:"sticky_key_present"`
	StickyBeforeChannel *int64  `json:"sticky_before_channel_id"`
	StickyBeforeVersion *int64  `json:"sticky_before_version"`
	StickyAction        *string `json:"sticky_action"`
	StickyReason        *string `json:"sticky_reason"`
	StickyAfterChannel  *int64  `json:"sticky_after_channel_id"`
	StickyAfterVersion  *int64  `json:"sticky_after_version"`
	CapacityWaitMs      *int32  `json:"capacity_wait_ms"`
	CapacityWaitResult  *string `json:"capacity_wait_result"`
}

func NewRoutingDecisionDTO(d routingtrace.Decision) RoutingDecisionDTO {
	return RoutingDecisionDTO{
		ID: d.ID, RequestRecordID: d.RequestRecordID, RequestID: d.RequestID,
		RequestStatus: d.RequestStatus, RouteID: d.RouteID, Mode: d.Mode,
		RequestedModelID: d.RequestedModelID, Protocol: d.Protocol, Endpoint: d.Endpoint,
		TraceStatus: d.TraceStatus, SchemaVersion: d.SchemaVersion, AlgorithmVersion: d.AlgorithmVersion,
		Summary: RoutingTraceSummaryDTO{
			PoolSize: d.Summary.PoolSize, EligibleCount: d.Summary.EligibleCount,
			BaselineOrder: d.Summary.BaselineOrder, ActualScanOrder: d.Summary.ActualScanOrder,
			AttemptedChannelIDs: d.Summary.AttemptedChannelIDs,
			SelectedChannelID:   d.Summary.SelectedChannelID, FinalChannelID: d.Summary.FinalChannelID,
			FallbackCount: d.Summary.FallbackCount, FinalResult: d.Summary.FinalResult,
			StickyKeyPresent: d.Summary.StickyKeyPresent, StickyBeforeChannel: d.Summary.StickyBeforeChannel,
			StickyBeforeVersion: d.Summary.StickyBeforeVersion, StickyAction: d.Summary.StickyAction,
			StickyReason: d.Summary.StickyReason, StickyAfterChannel: d.Summary.StickyAfterChannel,
			StickyAfterVersion: d.Summary.StickyAfterVersion, CapacityWaitMs: d.Summary.CapacityWaitMs,
			CapacityWaitResult: d.Summary.CapacityWaitResult,
		},
		Process: d.Process, CreatedAt: RFC3339(d.CreatedAt), UpdatedAt: RFC3339(d.UpdatedAt),
	}
}
