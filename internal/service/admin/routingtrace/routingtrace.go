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
	GetRoutingDecisionTraceByRequestID(context.Context, string) (sqlc.GetRoutingDecisionTraceByRequestIDRow, error)
}

// Decision is the admin-safe representation of one persisted routing decision.
type Decision struct {
	ID               int64
	RequestRecordID  int64
	RequestID        string
	RequestStatus    string
	RouteID          int64
	Mode             string
	RequestedModelID string
	Protocol         string
	Endpoint         string
	TraceStatus      string
	SchemaVersion    int32
	AlgorithmVersion string
	Summary          Summary
	Process          json.RawMessage
	CreatedAt        time.Time
	UpdatedAt        time.Time
}

type Summary struct {
	PoolSize            int32
	EligibleCount       int32
	BaselineOrder       []int64
	ActualScanOrder     []int64
	AttemptedChannelIDs []int64
	SelectedChannelID   *int64
	FinalChannelID      *int64
	FallbackCount       int32
	FinalResult         *string
	StickyKeyPresent    bool
	StickyBeforeChannel *int64
	StickyBeforeVersion *int64
	StickyAction        *string
	StickyReason        *string
	StickyAfterChannel  *int64
	StickyAfterVersion  *int64
	CapacityWaitMs      *int32
	CapacityWaitResult  *string
}

type Service struct {
	store Store
}

func NewService(store Store) *Service {
	return &Service{store: store}
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

func fromGetRow(row sqlc.GetRoutingDecisionTraceByRequestIDRow) Decision {
	return Decision{
		ID: row.ID, RequestRecordID: row.RequestRecordID, RequestID: row.RequestID,
		RequestStatus: row.RequestStatus, RouteID: row.RouteID, Mode: row.Mode,
		RequestedModelID: row.RequestedModelID, Protocol: row.Protocol, Endpoint: row.Endpoint,
		TraceStatus: row.TraceStatus, SchemaVersion: row.SchemaVersion,
		AlgorithmVersion: row.AlgorithmVersion, Process: rawJSONObject(row.TracePayload),
		Summary: Summary{
			PoolSize: row.PoolSize, EligibleCount: row.EligibleCount,
			BaselineOrder: nonNilInt64s(row.BaselineOrder), ActualScanOrder: nonNilInt64s(row.ActualScanOrder),
			AttemptedChannelIDs: nonNilInt64s(row.AttemptedChannelIds),
			SelectedChannelID:   int8Ptr(row.SelectedChannelID), FinalChannelID: int8Ptr(row.FinalChannelID),
			FallbackCount: row.FallbackCount, FinalResult: textPtr(row.FinalResult),
			StickyKeyPresent: row.StickyKeyPresent, StickyBeforeChannel: int8Ptr(row.StickyBeforeChannelID),
			StickyBeforeVersion: int8Ptr(row.StickyBeforeVersion), StickyAction: textPtr(row.StickyAction),
			StickyReason: textPtr(row.StickyReason), StickyAfterChannel: int8Ptr(row.StickyAfterChannelID),
			StickyAfterVersion: int8Ptr(row.StickyAfterVersion), CapacityWaitMs: int4Ptr(row.CapacityWaitMs),
			CapacityWaitResult: textPtr(row.CapacityWaitResult),
		},
		CreatedAt: row.CreatedAt.Time, UpdatedAt: row.UpdatedAt.Time,
	}
}

func rawJSONObject(value []byte) json.RawMessage {
	if !json.Valid(value) || len(value) == 0 || value[0] != '{' {
		return json.RawMessage("{}")
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

func int4Ptr(value pgtype.Int4) *int32 {
	if !value.Valid {
		return nil
	}
	v := value.Int32
	return &v
}

func textPtr(value pgtype.Text) *string {
	if !value.Valid {
		return nil
	}
	v := value.String
	return &v
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

func storeFailed(err error, endpoint string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(endpoint+" failed"))
}
