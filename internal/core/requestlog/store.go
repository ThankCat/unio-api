package requestlog

import (
	"context"
	"errors"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
)

// Store 使用 sqlc 查询实现 request log 写入能力。
type Store struct {
	queries *sqlc.Queries
}

// NewStore 创建 request log store。
func NewStore(queries *sqlc.Queries) *Store {
	return &Store{queries: queries}
}

// CreateRequest 创建一条 pending request record。
func (s *Store) CreateRequest(ctx context.Context, params CreateRequestParams) (RequestRecord, error) {
	row, err := s.queries.CreateRequestRecord(ctx, sqlc.CreateRequestRecordParams{
		RequestID:           params.RequestID,
		UserID:              params.UserID,
		ApiKeyID:            params.APIKeyID,
		RequestedModelID:    params.RequestedModelID,
		IngressProtocol:     string(params.IngressProtocol),
		Operation:           string(params.Operation),
		ResponseModelID:     pgtype.Text{Valid: false},
		ResponseProtocol:    pgtype.Text{Valid: false},
		ResponseID:          pgtype.Text{Valid: false},
		Stream:              params.Stream,
		Status:              string(RequestStatusPending),
		FinalProviderID:     pgtype.Int8{Valid: false},
		FinalChannelID:      pgtype.Int8{Valid: false},
		ErrorCode:           pgtype.Text{Valid: false},
		ErrorMessage:        pgtype.Text{Valid: false},
		InternalErrorDetail: pgtype.Text{Valid: false},
		DeliveryStatus:      string(DeliveryStatusNotStarted),
		ResponseStartedAt:   pgtype.Timestamptz{Valid: false},
		ResponseCompletedAt: pgtype.Timestamptz{Valid: false},
		StartedAt:           timestamptz(params.StartedAt),
		CompletedAt:         pgtype.Timestamptz{Valid: false},
		// 批二富化：线路快照 / 推理强度归一 / 客户端 IP（均可空）。
		RouteID:               int8OrNull(params.RouteID),
		ReasoningEffort:       textOrNull(params.ReasoningEffort),
		ReasoningBudgetTokens: int4OrNull(params.ReasoningBudgetTokens),
		ClientIp:              textOrNull(params.ClientIP),
	})
	if err != nil {
		return RequestRecord{}, requestLogStoreFailure(err, "create request record")
	}

	return requestRecordFromSQLC(row), nil
}

// MarkRequestRunning 将 request record 标记为 running。
func (s *Store) MarkRequestRunning(ctx context.Context, id int64) (RequestRecord, error) {
	row, err := s.queries.MarkRequestRunning(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestRecord{}, requestLogStateTransitionFailure("mark request running")
		}

		return RequestRecord{}, requestLogStoreFailure(err, "mark request running")
	}

	return requestRecordFromSQLC(sqlc.RequestRecord(row)), nil
}

// MarkRequestResponseStarted 记录 request 的首次客户可见响应时间。
func (s *Store) MarkRequestResponseStarted(ctx context.Context, params MarkResponseStartedParams) (RequestRecord, error) {
	row, err := s.queries.MarkRequestResponseStarted(ctx, sqlc.MarkRequestResponseStartedParams{
		RequestRecordID:   params.ID,
		ResponseStartedAt: timestamptz(params.ResponseStartedAt),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestRecord{}, requestLogStateTransitionFailure("mark request response started")
		}
		return RequestRecord{}, requestLogStoreFailure(err, "mark request response started")
	}

	return requestRecordFromSQLC(sqlc.RequestRecord(row)), nil
}

// MarkRequestDeliveryCompleted 在响应完整交付后把交付状态推进到 completed。
// 与 response_completed_at 在同一语句写入，满足交付完成约束；幂等（已 completed 返回当前行）。
func (s *Store) MarkRequestDeliveryCompleted(ctx context.Context, id int64, completedAt time.Time) (RequestRecord, error) {
	row, err := s.queries.MarkRequestDeliveryCompleted(ctx, sqlc.MarkRequestDeliveryCompletedParams{
		ResponseCompletedAt: timestamptz(completedAt),
		RequestRecordID:     id,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestRecord{}, requestLogStateTransitionFailure("mark request delivery completed")
		}
		return RequestRecord{}, requestLogStoreFailure(err, "mark request delivery completed")
	}

	return requestRecordFromSQLC(sqlc.RequestRecord(row)), nil
}

// MarkRequestDeliveryInterrupted 在交付中断时把交付状态推进到 interrupted；幂等（已 interrupted 返回当前行）。
func (s *Store) MarkRequestDeliveryInterrupted(ctx context.Context, id int64) (RequestRecord, error) {
	row, err := s.queries.MarkRequestDeliveryInterrupted(ctx, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestRecord{}, requestLogStateTransitionFailure("mark request delivery interrupted")
		}
		return RequestRecord{}, requestLogStoreFailure(err, "mark request delivery interrupted")
	}

	return requestRecordFromSQLC(sqlc.RequestRecord(row)), nil
}

// MarkRequestSucceeded 将 request record 标记为 succeeded。
func (s *Store) MarkRequestSucceeded(ctx context.Context, params MarkRequestSucceededParams) (RequestRecord, error) {
	row, err := s.queries.MarkRequestSucceeded(ctx, sqlc.MarkRequestSucceededParams{
		ResponseModelID:   pgtype.Text{String: params.ResponseModelID, Valid: true},
		ResponseProtocol:  pgtype.Text{String: string(params.ResponseProtocol), Valid: true},
		ResponseID:        pgtype.Text{String: params.ResponseID, Valid: true},
		FinalProviderID:   pgtype.Int8{Int64: params.FinalProviderID, Valid: true},
		FinalChannelID:    pgtype.Int8{Int64: params.FinalChannelID, Valid: true},
		ResponseStartedAt: optionalTimestamptz(params.ResponseStartedAt),
		CompletedAt:       timestamptz(params.CompletedAt),
		RequestRecordID:   params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestRecord{}, requestLogStateTransitionFailure("mark request succeeded")
		}
		return RequestRecord{}, requestLogStoreFailure(err, "mark request succeeded")
	}

	return requestRecordFromSQLC(sqlc.RequestRecord(row)), nil
}

// MarkSettledRequestCanceled 将 request record 标记为已结算的 canceled。
func (s *Store) MarkSettledRequestCanceled(ctx context.Context, params MarkSettledRequestCanceledParams) (RequestRecord, error) {
	row, err := s.queries.MarkSettledRequestCanceled(ctx, sqlc.MarkSettledRequestCanceledParams{
		ResponseModelID:     pgtype.Text{String: params.ResponseModelID, Valid: true},
		ResponseProtocol:    pgtype.Text{String: string(params.ResponseProtocol), Valid: true},
		ResponseID:          pgtype.Text{String: params.ResponseID, Valid: true},
		FinalProviderID:     pgtype.Int8{Int64: params.FinalProviderID, Valid: true},
		FinalChannelID:      pgtype.Int8{Int64: params.FinalChannelID, Valid: true},
		ErrorCode:           pgtype.Text{String: params.ErrorCode, Valid: true},
		ErrorMessage:        pgtype.Text{String: params.ErrorMessage, Valid: true},
		InternalErrorDetail: nullableText(params.InternalErrorDetail),
		ResponseStartedAt:   optionalTimestamptz(params.ResponseStartedAt),
		CompletedAt:         timestamptz(params.CompletedAt),
		RequestRecordID:     params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestRecord{}, requestLogStateTransitionFailure("mark settled request canceled")
		}
		return RequestRecord{}, requestLogStoreFailure(err, "mark settled request canceled")
	}

	return requestRecordFromSQLC(sqlc.RequestRecord(row)), nil
}

// MarkSettledRequestFailed 将 request record 标记为已结算的 failed。
func (s *Store) MarkSettledRequestFailed(ctx context.Context, params MarkSettledRequestFailedParams) (RequestRecord, error) {
	row, err := s.queries.MarkSettledRequestFailed(ctx, sqlc.MarkSettledRequestFailedParams{
		ResponseModelID:     pgtype.Text{String: params.ResponseModelID, Valid: true},
		ResponseProtocol:    pgtype.Text{String: string(params.ResponseProtocol), Valid: true},
		ResponseID:          pgtype.Text{String: params.ResponseID, Valid: true},
		FinalProviderID:     pgtype.Int8{Int64: params.FinalProviderID, Valid: true},
		FinalChannelID:      pgtype.Int8{Int64: params.FinalChannelID, Valid: true},
		ErrorCode:           pgtype.Text{String: params.ErrorCode, Valid: true},
		ErrorMessage:        pgtype.Text{String: params.ErrorMessage, Valid: true},
		InternalErrorDetail: nullableText(params.InternalErrorDetail),
		ResponseStartedAt:   optionalTimestamptz(params.ResponseStartedAt),
		CompletedAt:         timestamptz(params.CompletedAt),
		RequestRecordID:     params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestRecord{}, requestLogStateTransitionFailure("mark settled request failed")
		}
		return RequestRecord{}, requestLogStoreFailure(err, "mark settled request failed")
	}

	return requestRecordFromSQLC(sqlc.RequestRecord(row)), nil
}

// MarkRequestFailed 将 request record 标记为 failed。
func (s *Store) MarkRequestFailed(ctx context.Context, params MarkRequestFailedParams) (RequestRecord, error) {
	row, err := s.queries.MarkRequestFailed(ctx, sqlc.MarkRequestFailedParams{
		ErrorCode:           pgtype.Text{String: params.ErrorCode, Valid: true},
		ErrorMessage:        pgtype.Text{String: params.ErrorMessage, Valid: true},
		InternalErrorDetail: nullableText(params.InternalErrorDetail),
		CompletedAt:         timestamptz(params.CompletedAt),
		RequestRecordID:     params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestRecord{}, requestLogStateTransitionFailure("mark request failed")
		}
		return RequestRecord{}, requestLogStoreFailure(err, "mark request failed")
	}

	return requestRecordFromSQLC(sqlc.RequestRecord(row)), nil
}

// MarkRequestCanceled 将 request record 标记为 canceled。
func (s *Store) MarkRequestCanceled(ctx context.Context, params MarkRequestCanceledParams) (RequestRecord, error) {
	row, err := s.queries.MarkRequestCanceled(ctx, sqlc.MarkRequestCanceledParams{
		ErrorCode:           pgtype.Text{String: params.ErrorCode, Valid: true},
		ErrorMessage:        pgtype.Text{String: params.ErrorMessage, Valid: true},
		InternalErrorDetail: nullableText(params.InternalErrorDetail),
		CompletedAt:         timestamptz(params.CompletedAt),
		RequestRecordID:     params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return RequestRecord{}, requestLogStateTransitionFailure("mark request canceled")
		}
		return RequestRecord{}, requestLogStoreFailure(err, "mark request canceled")
	}

	return requestRecordFromSQLC(sqlc.RequestRecord(row)), nil
}

// CreateAttempt 创建一条 running request attempt。
func (s *Store) CreateAttempt(ctx context.Context, params CreateAttemptParams) (AttemptRecord, error) {
	if params.ProviderEndpointID == nil || *params.ProviderEndpointID <= 0 ||
		params.ProviderEndpointBaseURLRevision == nil || *params.ProviderEndpointBaseURLRevision <= 0 ||
		params.ProviderEndpointStatusRevision == nil || *params.ProviderEndpointStatusRevision <= 0 ||
		params.ChannelConfigRevision == nil || *params.ChannelConfigRevision <= 0 ||
		params.RoutingCandidateIndex == nil || *params.RoutingCandidateIndex < 0 ||
		params.UpstreamOperation == "" {
		return AttemptRecord{}, requestLogStoreFailure(
			errors.New("request attempt routing identity is incomplete"),
			"create request attempt",
		)
	}
	row, err := s.queries.CreateRequestAttempt(ctx, sqlc.CreateRequestAttemptParams{
		RequestRecordID:                 params.RequestRecordID,
		AttemptIndex:                    int32(params.AttemptIndex),
		ProviderID:                      params.ProviderID,
		ChannelID:                       params.ChannelID,
		AdapterKey:                      params.AdapterKey,
		UpstreamModel:                   params.UpstreamModel,
		UpstreamProtocol:                string(params.UpstreamProtocol),
		ProviderEndpointID:              *params.ProviderEndpointID,
		ProviderEndpointBaseUrlRevision: *params.ProviderEndpointBaseURLRevision,
		ProviderEndpointStatusRevision:  *params.ProviderEndpointStatusRevision,
		ChannelConfigRevision:           *params.ChannelConfigRevision,
		RoutingCandidateIndex:           int32(*params.RoutingCandidateIndex),
		UpstreamOperation:               string(params.UpstreamOperation),
		UpstreamResponseID:              pgtype.Text{Valid: false},
		UpstreamResponseModel:           pgtype.Text{Valid: false},
		UpstreamFinishReason:            pgtype.Text{Valid: false},
		FinishClass:                     pgtype.Text{Valid: false},
		Status:                          string(AttemptStatusRunning),
		UpstreamStatusCode:              pgtype.Int4{Valid: false},
		UpstreamRequestID:               pgtype.Text{Valid: false},
		ErrorCode:                       pgtype.Text{Valid: false},
		ErrorMessage:                    pgtype.Text{Valid: false},
		InternalErrorDetail:             pgtype.Text{Valid: false},
		ResponseStartedAt:               pgtype.Timestamptz{Valid: false},
		FinalUsageReceived:              false,
		UsageMappingVersion:             pgtype.Text{Valid: false},
		StartedAt:                       timestamptz(params.StartedAt),
		CompletedAt:                     pgtype.Timestamptz{Valid: false},
	})
	if err != nil {
		return AttemptRecord{}, requestLogStoreFailure(err, "create request attempt")
	}

	return attemptRecordFromSQLC(row), nil
}

// MarkAttemptResponseStarted 记录 attempt 的首次客户可见响应时间。
func (s *Store) MarkAttemptResponseStarted(ctx context.Context, params MarkAttemptResponseStartedParams) (AttemptRecord, error) {
	row, err := s.queries.MarkRequestAttemptResponseStarted(ctx, sqlc.MarkRequestAttemptResponseStartedParams{
		AttemptID:         params.ID,
		ResponseStartedAt: timestamptz(params.ResponseStartedAt),
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AttemptRecord{}, requestLogStateTransitionFailure("mark request attempt response started")
		}
		return AttemptRecord{}, requestLogStoreFailure(err, "mark request attempt response started")
	}

	return attemptRecordFromSQLC(sqlc.RequestAttempt(row)), nil
}

// RecordAttemptTiming first-write-wins 地保存一次真实 upstream transport 的时间边界。
func (s *Store) RecordAttemptTiming(ctx context.Context, params RecordAttemptTimingParams) (AttemptRecord, error) {
	row, err := s.queries.RecordRequestAttemptUpstreamTiming(ctx, sqlc.RecordRequestAttemptUpstreamTimingParams{
		UpstreamStartedAt:    optionalTimestamptz(params.UpstreamStartedAt),
		UpstreamFirstTokenAt: optionalTimestamptz(params.UpstreamFirstTokenAt),
		UpstreamCompletedAt:  optionalTimestamptz(params.UpstreamCompletedAt),
		AttemptID:            params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AttemptRecord{}, requestLogStateTransitionFailure("record request attempt upstream timing")
		}
		return AttemptRecord{}, requestLogStoreFailure(err, "record request attempt upstream timing")
	}

	return attemptRecordFromSQLC(sqlc.RequestAttempt(row)), nil
}

// RecordAttemptBreakerDisposition first-write-wins 地保存 BreakerStore Finish disposition。
func (s *Store) RecordAttemptBreakerDisposition(ctx context.Context, params RecordAttemptBreakerDispositionParams) (AttemptRecord, error) {
	row, err := s.queries.RecordRequestAttemptBreakerDisposition(ctx, sqlc.RecordRequestAttemptBreakerDispositionParams{
		BreakerEndpointDisposition: pgtype.Text{String: params.EndpointDisposition, Valid: params.EndpointDisposition != ""},
		BreakerChannelDisposition:  pgtype.Text{String: params.ChannelDisposition, Valid: params.ChannelDisposition != ""},
		AttemptID:                  params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AttemptRecord{}, requestLogStateTransitionFailure("record request attempt breaker disposition")
		}
		return AttemptRecord{}, requestLogStoreFailure(err, "record request attempt breaker disposition")
	}
	return attemptRecordFromSQLC(sqlc.RequestAttempt(row)), nil
}

// MarkAttemptSucceeded 将 request attempt 标记为 succeeded。
func (s *Store) MarkAttemptSucceeded(ctx context.Context, params MarkAttemptSucceededParams) (AttemptRecord, error) {
	row, err := s.queries.MarkRequestAttemptSucceeded(ctx, sqlc.MarkRequestAttemptSucceededParams{
		UpstreamResponseID:    pgtype.Text{String: params.UpstreamResponseID, Valid: true},
		UpstreamResponseModel: pgtype.Text{String: params.UpstreamResponseModel, Valid: true},
		UpstreamFinishReason:  pgtype.Text{String: params.UpstreamFinishReason, Valid: true},
		FinishClass:           pgtype.Text{String: params.FinishClass, Valid: true},
		UpstreamStatusCode:    pgtype.Int4{Int32: int32(params.UpstreamStatusCode), Valid: true},
		UpstreamRequestID:     optionalText(params.UpstreamRequestID),
		ResponseStartedAt:     optionalTimestamptz(params.ResponseStartedAt),
		FinalUsageReceived:    params.FinalUsageReceived,
		UsageMappingVersion:   pgtype.Text{String: params.UsageMappingVersion, Valid: true},
		CompletedAt:           timestamptz(params.CompletedAt),
		AttemptID:             params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AttemptRecord{}, requestLogStateTransitionFailure("mark request attempt succeeded")
		}
		return AttemptRecord{}, requestLogStoreFailure(err, "mark request attempt succeeded")
	}

	return attemptRecordFromSQLC(sqlc.RequestAttempt(row)), nil
}

// MarkSettledAttemptCanceled 将 request attempt 标记为已结算的 canceled。
func (s *Store) MarkSettledAttemptCanceled(ctx context.Context, params MarkSettledAttemptCanceledParams) (AttemptRecord, error) {
	row, err := s.queries.MarkSettledRequestAttemptCanceled(ctx, sqlc.MarkSettledRequestAttemptCanceledParams{
		UpstreamResponseID:    pgtype.Text{String: params.UpstreamResponseID, Valid: true},
		UpstreamResponseModel: pgtype.Text{String: params.UpstreamResponseModel, Valid: true},
		UpstreamFinishReason:  pgtype.Text{String: params.UpstreamFinishReason, Valid: true},
		FinishClass:           pgtype.Text{String: params.FinishClass, Valid: true},
		UpstreamStatusCode:    pgtype.Int4{Int32: int32(params.UpstreamStatusCode), Valid: true},
		UpstreamRequestID:     optionalText(params.UpstreamRequestID),
		ErrorCode:             pgtype.Text{String: params.ErrorCode, Valid: true},
		ErrorMessage:          pgtype.Text{String: params.ErrorMessage, Valid: true},
		InternalErrorDetail:   nullableText(params.InternalErrorDetail),
		ResponseStartedAt:     optionalTimestamptz(params.ResponseStartedAt),
		FinalUsageReceived:    params.FinalUsageReceived,
		UsageMappingVersion:   pgtype.Text{String: params.UsageMappingVersion, Valid: true},
		CompletedAt:           timestamptz(params.CompletedAt),
		AttemptID:             params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AttemptRecord{}, requestLogStateTransitionFailure("mark settled request attempt canceled")
		}
		return AttemptRecord{}, requestLogStoreFailure(err, "mark settled request attempt canceled")
	}

	return attemptRecordFromSQLC(sqlc.RequestAttempt(row)), nil
}

// MarkSettledAttemptFailed 将 request attempt 标记为已结算的 failed。
func (s *Store) MarkSettledAttemptFailed(ctx context.Context, params MarkSettledAttemptFailedParams) (AttemptRecord, error) {
	row, err := s.queries.MarkSettledRequestAttemptFailed(ctx, sqlc.MarkSettledRequestAttemptFailedParams{
		UpstreamResponseID:    pgtype.Text{String: params.UpstreamResponseID, Valid: true},
		UpstreamResponseModel: pgtype.Text{String: params.UpstreamResponseModel, Valid: true},
		UpstreamFinishReason:  pgtype.Text{String: params.UpstreamFinishReason, Valid: true},
		FinishClass:           pgtype.Text{String: params.FinishClass, Valid: true},
		UpstreamStatusCode:    pgtype.Int4{Int32: int32(params.UpstreamStatusCode), Valid: true},
		UpstreamRequestID:     optionalText(params.UpstreamRequestID),
		ErrorCode:             pgtype.Text{String: params.ErrorCode, Valid: true},
		ErrorMessage:          pgtype.Text{String: params.ErrorMessage, Valid: true},
		InternalErrorDetail:   nullableText(params.InternalErrorDetail),
		ResponseStartedAt:     optionalTimestamptz(params.ResponseStartedAt),
		FinalUsageReceived:    params.FinalUsageReceived,
		UsageMappingVersion:   pgtype.Text{String: params.UsageMappingVersion, Valid: true},
		CompletedAt:           timestamptz(params.CompletedAt),
		AttemptID:             params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AttemptRecord{}, requestLogStateTransitionFailure("mark settled request attempt failed")
		}
		return AttemptRecord{}, requestLogStoreFailure(err, "mark settled request attempt failed")
	}

	return attemptRecordFromSQLC(sqlc.RequestAttempt(row)), nil
}

// MarkAttemptFailed 将 request attempt 标记为 failed。
func (s *Store) MarkAttemptFailed(ctx context.Context, params MarkAttemptFailedParams) (AttemptRecord, error) {
	row, err := s.queries.MarkRequestAttemptFailed(ctx, sqlc.MarkRequestAttemptFailedParams{
		UpstreamStatusCode:  optionalInt4(params.UpstreamStatusCode),
		UpstreamRequestID:   optionalText(params.UpstreamRequestID),
		ErrorCode:           pgtype.Text{String: params.ErrorCode, Valid: true},
		ErrorMessage:        pgtype.Text{String: params.ErrorMessage, Valid: true},
		InternalErrorDetail: nullableText(params.InternalErrorDetail),
		CompletedAt:         timestamptz(params.CompletedAt),
		AttemptID:           params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AttemptRecord{}, requestLogStateTransitionFailure("mark request attempt failed")
		}
		return AttemptRecord{}, requestLogStoreFailure(err, "mark request attempt failed")
	}

	return attemptRecordFromSQLC(sqlc.RequestAttempt(row)), nil
}

// MarkAttemptCanceled 将 request attempt 标记为 canceled。
func (s *Store) MarkAttemptCanceled(ctx context.Context, params MarkAttemptCanceledParams) (AttemptRecord, error) {
	row, err := s.queries.MarkRequestAttemptCanceled(ctx, sqlc.MarkRequestAttemptCanceledParams{
		ErrorCode:           pgtype.Text{String: params.ErrorCode, Valid: true},
		ErrorMessage:        pgtype.Text{String: params.ErrorMessage, Valid: true},
		InternalErrorDetail: nullableText(params.InternalErrorDetail),
		CompletedAt:         timestamptz(params.CompletedAt),
		AttemptID:           params.ID,
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return AttemptRecord{}, requestLogStateTransitionFailure("mark request attempt canceled")
		}
		return AttemptRecord{}, requestLogStoreFailure(err, "mark request attempt canceled")
	}

	return attemptRecordFromSQLC(sqlc.RequestAttempt(row)), nil
}

// requestRecordFromSQLC 将 sqlc request row 转成 requestlog 领域 DTO。
func requestRecordFromSQLC(row sqlc.RequestRecord) RequestRecord {
	return RequestRecord{
		ID:                  row.ID,
		RequestID:           row.RequestID,
		UserID:              row.UserID,
		APIKeyID:            row.ApiKeyID,
		RequestedModelID:    row.RequestedModelID,
		IngressProtocol:     Protocol(row.IngressProtocol),
		Operation:           Operation(row.Operation),
		ResponseModelID:     textPtr(row.ResponseModelID),
		ResponseProtocol:    textPtr(row.ResponseProtocol),
		ResponseID:          textPtr(row.ResponseID),
		Stream:              row.Stream,
		Status:              RequestStatus(row.Status),
		FinalProviderID:     int64Ptr(row.FinalProviderID),
		FinalChannelID:      int64Ptr(row.FinalChannelID),
		ErrorCode:           textPtr(row.ErrorCode),
		ErrorMessage:        textPtr(row.ErrorMessage),
		InternalErrorDetail: textPtr(row.InternalErrorDetail),
		DeliveryStatus:      DeliveryStatus(row.DeliveryStatus),
		ResponseStartedAt:   timePtr(row.ResponseStartedAt),
		ResponseCompletedAt: timePtr(row.ResponseCompletedAt),
		StartedAt:           row.StartedAt.Time,
		CompletedAt:         timePtr(row.CompletedAt),
	}
}

// attemptRecordFromSQLC 将 sqlc attempt row 转成 requestlog 领域 DTO。
func attemptRecordFromSQLC(row sqlc.RequestAttempt) AttemptRecord {
	return AttemptRecord{
		ID:                              row.ID,
		RequestRecordID:                 row.RequestRecordID,
		AttemptIndex:                    int(row.AttemptIndex),
		ProviderID:                      row.ProviderID,
		ChannelID:                       row.ChannelID,
		AdapterKey:                      row.AdapterKey,
		UpstreamModel:                   row.UpstreamModel,
		UpstreamProtocol:                Protocol(row.UpstreamProtocol),
		ProviderEndpointID:              int64ValuePtr(row.ProviderEndpointID),
		ProviderEndpointBaseURLRevision: int64ValuePtr(row.ProviderEndpointBaseUrlRevision),
		ProviderEndpointStatusRevision:  int64ValuePtr(row.ProviderEndpointStatusRevision),
		ChannelConfigRevision:           int64ValuePtr(row.ChannelConfigRevision),
		RoutingCandidateIndex:           int32ValuePtr(row.RoutingCandidateIndex),
		UpstreamOperation:               UpstreamOperation(row.UpstreamOperation),
		UpstreamResponseID:              textPtr(row.UpstreamResponseID),
		UpstreamResponseModel:           textPtr(row.UpstreamResponseModel),
		UpstreamFinishReason:            textPtr(row.UpstreamFinishReason),
		FinishClass:                     textPtr(row.FinishClass),
		Status:                          AttemptStatus(row.Status),
		UpstreamStatusCode:              intPtr(row.UpstreamStatusCode),
		UpstreamRequestID:               textPtr(row.UpstreamRequestID),
		ErrorCode:                       textPtr(row.ErrorCode),
		ErrorMessage:                    textPtr(row.ErrorMessage),
		InternalErrorDetail:             textPtr(row.InternalErrorDetail),
		ResponseStartedAt:               timePtr(row.ResponseStartedAt),
		UpstreamStartedAt:               timePtr(row.UpstreamStartedAt),
		UpstreamFirstTokenAt:            timePtr(row.UpstreamFirstTokenAt),
		UpstreamCompletedAt:             timePtr(row.UpstreamCompletedAt),
		BreakerEndpointDisposition:      textPtr(row.BreakerEndpointDisposition),
		BreakerChannelDisposition:       textPtr(row.BreakerChannelDisposition),
		FinalUsageReceived:              row.FinalUsageReceived,
		UsageMappingVersion:             textPtr(row.UsageMappingVersion),
		StartedAt:                       row.StartedAt.Time,
		CompletedAt:                     timePtr(row.CompletedAt),
	}
}

// timestamptz 将 time.Time 包装成有效的 pgtype.Timestamptz。
func timestamptz(t time.Time) pgtype.Timestamptz {
	return pgtype.Timestamptz{Time: t, Valid: true}
}

// int8OrNull / int4OrNull / textOrNull 把可选值转成可空 pgtype（nil → NULL）。
func int8OrNull(v *int64) pgtype.Int8 {
	if v == nil {
		return pgtype.Int8{Valid: false}
	}
	return pgtype.Int8{Int64: *v, Valid: true}
}

func int4OrNull(v *int32) pgtype.Int4 {
	if v == nil {
		return pgtype.Int4{Valid: false}
	}
	return pgtype.Int4{Int32: *v, Valid: true}
}

func textValue(v pgtype.Text) string {
	if !v.Valid {
		return ""
	}
	return v.String
}

func textOrNull(v *string) pgtype.Text {
	if v == nil || *v == "" {
		return pgtype.Text{Valid: false}
	}
	return pgtype.Text{String: *v, Valid: true}
}

// optionalTimestamptz 把可选时间转换为 pgtype.Timestamptz。
func optionalTimestamptz(t *time.Time) pgtype.Timestamptz {
	if t == nil {
		return pgtype.Timestamptz{Valid: false}
	}

	return pgtype.Timestamptz{Time: *t, Valid: true}
}

// optionalText 把可选字符串转换为 pgtype.Text，避免把未知字段写成空字符串。
func optionalText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{Valid: false}
	}

	return pgtype.Text{String: *s, Valid: true}
}

// nullableText 把空字符串写成 NULL，避免无内部详情时保存无意义空值。
func nullableText(value string) pgtype.Text {
	if value == "" {
		return pgtype.Text{Valid: false}
	}

	return pgtype.Text{String: value, Valid: true}
}

// optionalInt4 把可选整数转换为 pgtype.Int4，避免把未知 HTTP 状态写成 0。
func optionalInt4(value *int) pgtype.Int4 {
	if value == nil {
		return pgtype.Int4{Valid: false}
	}

	return pgtype.Int4{Int32: int32(*value), Valid: true}
}

func requestLogStoreFailure(err error, message string) error {
	return failure.Wrap(
		failure.CodeRequestLogStoreFailed,
		err,
		failure.WithMessage(message),
	)
}

func requestLogStateTransitionFailure(message string) error {
	return failure.Wrap(
		failure.CodeRequestLogInvalidStateTransition,
		ErrInvalidStateTransition,
		failure.WithMessage(message),
	)
}

// textPtr 将 pgtype.Text 转成可选字符串。
func textPtr(s pgtype.Text) *string {
	if !s.Valid {
		return nil
	}

	return &s.String
}

// int64Ptr 将 pgtype.Int8 转成可选 int64。
func int64Ptr(i pgtype.Int8) *int64 {
	if !i.Valid {
		return nil
	}

	return &i.Int64
}

func int64ValuePtr(value int64) *int64 {
	return &value
}

// intPtr 将 pgtype.Int4 转成可选 int。
func intPtr(value pgtype.Int4) *int {
	if !value.Valid {
		return nil
	}

	n := int(value.Int32)
	return &n
}

func int32ValuePtr(value int32) *int {
	n := int(value)
	return &n
}

// timePtr 将 pgtype.Timestamptz 转成可选 time.Time。
func timePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}

	return &value.Time
}
