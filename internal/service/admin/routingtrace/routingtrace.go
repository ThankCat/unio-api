// Package routingtrace provides admin-only routing decision trace queries.
package routingtrace

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// Store is the database surface needed by Service.
type Store interface {
	ListRouteRoutingDecisionTraces(context.Context, sqlc.ListRouteRoutingDecisionTracesParams) ([]sqlc.ListRouteRoutingDecisionTracesRow, error)
	CountRouteRoutingDecisionTraces(context.Context, int64) (int64, error)
	GetRoutingDecisionTraceByRequestID(context.Context, string) (sqlc.GetRoutingDecisionTraceByRequestIDRow, error)
}

// Decision is the admin-safe representation of one persisted routing decision.
type Decision struct {
	ID                   int64
	RequestRecordID      int64
	RequestID            string
	RequestStatus        string
	RouteID              int64
	Mode                 string
	RequestedModelID     string
	Protocol             string
	Operation            string
	PoolSize             int32
	CandidateCount       int32
	StickyChannelID      *int64
	StickyPinned         bool
	StickyInvalid        bool
	AllCapacityZero      bool
	MarginGuardTriggered bool
	Abnormal             bool
	AbnormalReasons      []string
	CandidateScores      json.RawMessage
	SelectedOrder        []int64
	FallbackChain        json.RawMessage
	FinalChannelID       *int64
	AlgorithmVersion     string
	Sampled              bool
	CreatedAt            time.Time
	UpdatedAt            time.Time
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
}

func (s *Service) ListByRoute(ctx context.Context, routeID int64, limit, offset int32) ([]Decision, int64, error) {
	if routeID <= 0 {
		return nil, 0, invalidArgument("route_id", "route_id must be a positive integer")
	}
	rows, err := s.store.ListRouteRoutingDecisionTraces(ctx, sqlc.ListRouteRoutingDecisionTracesParams{
		RouteID: routeID, PageLimit: limit, PageOffset: offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list routing decision traces")
	}
	total, err := s.store.CountRouteRoutingDecisionTraces(ctx, routeID)
	if err != nil {
		return nil, 0, storeFailed(err, "count routing decision traces")
	}
	out := make([]Decision, 0, len(rows))
	for _, row := range rows {
		out = append(out, fromListRow(row))
	}
	return out, total, nil
}

func (s *Service) GetByRequestID(ctx context.Context, requestID string) (Decision, error) {
	if requestID == "" {
		return Decision{}, invalidArgument("request_id", "request_id is required")
	}
	row, err := s.store.GetRoutingDecisionTraceByRequestID(ctx, requestID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Decision{}, failure.New(failure.CodeAdminNotFound, failure.WithMessage("routing decision trace not found"))
		}
		return Decision{}, storeFailed(err, "get routing decision trace")
	}
	return fromGetRow(row), nil
}

func fromListRow(row sqlc.ListRouteRoutingDecisionTracesRow) Decision {
	return decisionFromFields(
		row.ID, row.RequestRecordID, row.RequestID, row.RequestStatus, row.RouteID,
		row.Mode, row.RequestedModelID, row.Protocol, row.Operation, row.PoolSize,
		row.CandidateCount, row.StickyChannelID, row.StickyPinned, row.StickyInvalid,
		row.AllCapacityZero, row.MarginGuardTriggered, row.Abnormal,
		row.AbnormalReasons, row.CandidateScores, row.SelectedOrder, row.FallbackChain,
		row.FinalChannelID, row.AlgorithmVersion, row.Sampled, row.CreatedAt, row.UpdatedAt,
	)
}

func fromGetRow(row sqlc.GetRoutingDecisionTraceByRequestIDRow) Decision {
	return decisionFromFields(
		row.ID, row.RequestRecordID, row.RequestID, row.RequestStatus, row.RouteID,
		row.Mode, row.RequestedModelID, row.Protocol, row.Operation, row.PoolSize,
		row.CandidateCount, row.StickyChannelID, row.StickyPinned, row.StickyInvalid,
		row.AllCapacityZero, row.MarginGuardTriggered, row.Abnormal,
		row.AbnormalReasons, row.CandidateScores, row.SelectedOrder, row.FallbackChain,
		row.FinalChannelID, row.AlgorithmVersion, row.Sampled, row.CreatedAt, row.UpdatedAt,
	)
}

func decisionFromFields(
	id, requestRecordID int64,
	requestID, requestStatus string,
	routeID int64,
	mode, requestedModelID, protocol, operation string,
	poolSize, candidateCount int32,
	stickyChannelID pgtype.Int8,
	stickyPinned, stickyInvalid, allCapacityZero, marginGuardTriggered, abnormal bool,
	abnormalReasons []string,
	candidateScores []byte,
	selectedOrder []int64,
	fallbackChain []byte,
	finalChannelID pgtype.Int8,
	algorithmVersion string,
	sampled bool,
	createdAt, updatedAt pgtype.Timestamptz,
) Decision {
	return Decision{
		ID:                   id,
		RequestRecordID:      requestRecordID,
		RequestID:            requestID,
		RequestStatus:        requestStatus,
		RouteID:              routeID,
		Mode:                 mode,
		RequestedModelID:     requestedModelID,
		Protocol:             protocol,
		Operation:            operation,
		PoolSize:             poolSize,
		CandidateCount:       candidateCount,
		StickyChannelID:      int8Ptr(stickyChannelID),
		StickyPinned:         stickyPinned,
		StickyInvalid:        stickyInvalid,
		AllCapacityZero:      allCapacityZero,
		MarginGuardTriggered: marginGuardTriggered,
		Abnormal:             abnormal,
		AbnormalReasons:      nonNilStrings(abnormalReasons),
		CandidateScores:      rawJSON(candidateScores),
		SelectedOrder:        nonNilInt64s(selectedOrder),
		FallbackChain:        rawJSON(fallbackChain),
		FinalChannelID:       int8Ptr(finalChannelID),
		AlgorithmVersion:     algorithmVersion,
		Sampled:              sampled,
		CreatedAt:            createdAt.Time,
		UpdatedAt:            updatedAt.Time,
	}
}

func rawJSON(value []byte) json.RawMessage {
	if !json.Valid(value) {
		return json.RawMessage("[]")
	}
	return json.RawMessage(value)
}

func int8Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}

func nonNilStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}

func nonNilInt64s(values []int64) []int64 {
	if values == nil {
		return []int64{}
	}
	return values
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func storeFailed(err error, operation string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(operation+" failed"))
}
