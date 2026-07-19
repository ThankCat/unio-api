package adminhttp

import (
	"encoding/json"

	"github.com/ThankCat/unio-gateway/internal/service/admin/routingtrace"
)

// RoutingDecisionDTO is shared by the route decisions list and request detail endpoint.
type RoutingDecisionDTO struct {
	ID                   int64           `json:"id"`
	RequestRecordID      int64           `json:"request_record_id"`
	RequestID            string          `json:"request_id"`
	RequestStatus        string          `json:"request_status"`
	RouteID              int64           `json:"route_id"`
	Mode                 string          `json:"mode"`
	RequestedModelID     string          `json:"requested_model_id"`
	Protocol             string          `json:"protocol"`
	Operation            string          `json:"operation"`
	PoolSize             int32           `json:"pool_size"`
	CandidateCount       int32           `json:"candidate_count"`
	StickyChannelID      *int64          `json:"sticky_channel_id"`
	StickyPinned         bool            `json:"sticky_pinned"`
	StickyInvalid        bool            `json:"sticky_invalid"`
	CapacityDegraded     bool            `json:"capacity_degraded"`
	AllCapacityZero      bool            `json:"all_capacity_zero"`
	MarginGuardTriggered bool            `json:"margin_guard_triggered"`
	Abnormal             bool            `json:"abnormal"`
	AbnormalReasons      []string        `json:"abnormal_reasons"`
	CandidateScores      json.RawMessage `json:"candidate_scores"`
	SelectedOrder        []int64         `json:"selected_order"`
	FallbackChain        json.RawMessage `json:"fallback_chain"`
	FinalChannelID       *int64          `json:"final_channel_id"`
	AlgorithmVersion     string          `json:"algorithm_version"`
	Sampled              bool            `json:"sampled"`
	CreatedAt            string          `json:"created_at"`
	UpdatedAt            string          `json:"updated_at"`
}

func NewRoutingDecisionDTO(d routingtrace.Decision) RoutingDecisionDTO {
	return RoutingDecisionDTO{
		ID: d.ID, RequestRecordID: d.RequestRecordID, RequestID: d.RequestID,
		RequestStatus: d.RequestStatus, RouteID: d.RouteID, Mode: d.Mode,
		RequestedModelID: d.RequestedModelID, Protocol: d.Protocol, Operation: d.Operation,
		PoolSize: d.PoolSize, CandidateCount: d.CandidateCount, StickyChannelID: d.StickyChannelID,
		StickyPinned: d.StickyPinned, StickyInvalid: d.StickyInvalid,
		CapacityDegraded: d.CapacityDegraded, AllCapacityZero: d.AllCapacityZero,
		MarginGuardTriggered: d.MarginGuardTriggered, Abnormal: d.Abnormal,
		AbnormalReasons: d.AbnormalReasons, CandidateScores: d.CandidateScores,
		SelectedOrder: d.SelectedOrder, FallbackChain: d.FallbackChain,
		FinalChannelID: d.FinalChannelID, AlgorithmVersion: d.AlgorithmVersion,
		Sampled: d.Sampled, CreatedAt: RFC3339(d.CreatedAt), UpdatedAt: RFC3339(d.UpdatedAt),
	}
}
