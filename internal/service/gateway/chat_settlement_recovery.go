package gateway

import (
	"context"
	"errors"
	"math"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

const (
	defaultSettlementRecoveryDelay   = 30 * time.Second
	defaultSettlementRecoveryTimeout = 10 * time.Second
)

// ErrChatSettlementRecoveryScheduled 表示 settlement 失败后已有持久化 recovery job 接管。
var ErrChatSettlementRecoveryScheduled = errors.New("chat settlement recovery scheduled")

// ChatSettlementRecoveryRecorder 定义 gateway 写入和完成 settlement recovery job 的能力。
type ChatSettlementRecoveryRecorder interface {
	CreatePendingChatSettlementRecoveryJob(ctx context.Context, params ChatSettlementParams) (sqlc.SettlementRecoveryJob, error)
	MarkChatSettlementRecoveryJobSucceeded(ctx context.Context, jobID int64) error
}

// ChatSettlementRecoveryStore 使用 settlement_recovery_jobs 保存可恢复的结算事实。
type ChatSettlementRecoveryStore struct {
	queries      *sqlc.Queries
	nextRunDelay time.Duration
}

// NewChatSettlementRecoveryStore 创建 settlement recovery job store。
func NewChatSettlementRecoveryStore(queries *sqlc.Queries, nextRunDelay time.Duration) *ChatSettlementRecoveryStore {
	if queries == nil {
		panic("gateway: chat settlement recovery queries is required")
	}
	if nextRunDelay <= 0 {
		nextRunDelay = defaultSettlementRecoveryDelay
	}

	return &ChatSettlementRecoveryStore{
		queries:      queries,
		nextRunDelay: nextRunDelay,
	}
}

// RecoverableChatSettlementExecutor 在真实 settlement 前先写 recovery job。
type RecoverableChatSettlementExecutor struct {
	settlement        ChatSettlementExecutor
	recovery          ChatSettlementRecoveryRecorder
	settlementTimeout time.Duration
}

// NewRecoverableChatSettlementExecutor 创建带 recovery job 保护的 settlement executor。
func NewRecoverableChatSettlementExecutor(
	settlement ChatSettlementExecutor,
	recovery ChatSettlementRecoveryRecorder,
	settlementTimeout time.Duration,
) *RecoverableChatSettlementExecutor {
	if settlement == nil {
		panic("gateway: chat settlement executor is required")
	}
	if recovery == nil {
		panic("gateway: chat settlement recovery recorder is required")
	}
	if settlementTimeout <= 0 {
		settlementTimeout = defaultSettlementRecoveryTimeout
	}

	return &RecoverableChatSettlementExecutor{
		settlement:        settlement,
		recovery:          recovery,
		settlementTimeout: settlementTimeout,
	}
}

// SettleSuccessfulChat 先持久化 recovery job，再执行真实 settlement。
func (e *RecoverableChatSettlementExecutor) SettleSuccessfulChat(ctx context.Context, params ChatSettlementParams) error {
	settlementCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), e.settlementTimeout)
	defer cancel()

	job, err := e.recovery.CreatePendingChatSettlementRecoveryJob(settlementCtx, params)
	if err != nil {
		return err
	}

	if err := e.settlement.SettleSuccessfulChat(settlementCtx, params); err != nil {
		return chatSettlementRecoveryScheduled(params.RequestRecord.ID, err)
	}

	// job 标记失败不应让已成功的用户请求失败。
	// pending job 后续会被 worker 幂等重放 settlement 后标记 succeeded。
	_ = e.recovery.MarkChatSettlementRecoveryJobSucceeded(settlementCtx, job.ID)

	return nil
}

// CreatePendingChatSettlementRecoveryJob 保存 worker 重放 settlement 所需的最小事实。
func (s *ChatSettlementRecoveryStore) CreatePendingChatSettlementRecoveryJob(ctx context.Context, params ChatSettlementParams) (sqlc.SettlementRecoveryJob, error) {
	if !params.UsageSource.Valid() {
		return sqlc.SettlementRecoveryJob{}, failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("chat settlement recovery usage source is invalid"),
			failure.WithField("usage_source", string(params.UsageSource)),
		)
	}
	if params.Authorization.PriceID <= 0 {
		return sqlc.SettlementRecoveryJob{}, failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("chat settlement recovery missing authorization price id"),
		)
	}

	price := params.Authorization.Price
	job, err := s.queries.CreateSettlementRecoveryJob(ctx, sqlc.CreateSettlementRecoveryJobParams{
		UserID:                params.RequestRecord.UserID,
		RequestRecordID:       params.RequestRecord.ID,
		AttemptID:             params.AttemptRecord.ID,
		ReservationID:         params.Authorization.ReservationID,
		ResponseModelID:       params.ResponseModelID,
		ModelID:               params.ModelDBID,
		ProviderID:            params.FinalProviderID,
		ChannelID:             params.FinalChannelID,
		UpstreamResponseModel: params.UpstreamResponseModel,
		UpstreamStatusCode:    int32(params.UpstreamStatusCode),
		UpstreamRequestID:     chatSettlementOptionalText(params.UpstreamRequestID),
		UsagePromptTokens:     int64(params.Usage.PromptTokens),
		UsageCompletionTokens: int64(params.Usage.CompletionTokens),
		UsageTotalTokens:      int64(params.Usage.TotalTokens),
		UsageCachedTokens:     int64(params.Usage.CachedTokens),
		UsageReasoningTokens:  int64(params.Usage.ReasoningTokens),
		UsageSource:           string(params.UsageSource),
		PriceID:               params.Authorization.PriceID,
		Currency:              price.Currency,
		PricingUnit:           price.PricingUnit,
		InputPrice:            price.InputPrice,
		OutputPrice:           price.OutputPrice,
		CachedInputPrice:      price.CachedInputPrice,
		ReasoningOutputPrice:  price.ReasoningOutputPrice,
		FormulaVersion:        price.FormulaVersion,
		EstimatedAmount:       params.Authorization.EstimatedAmount,
		AuthorizedAmount:      params.Authorization.AuthorizedAmount,
		NextRunAt: pgtype.Timestamptz{
			Time:  time.Now().Add(s.nextRunDelay),
			Valid: true,
		},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.SettlementRecoveryJob{}, chatSettlementIdempotencyConflict("settlement recovery job facts mismatch")
		}

		return sqlc.SettlementRecoveryJob{}, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("create chat settlement recovery job"),
		)
	}

	return job, nil
}

// MarkChatSettlementRecoveryJobSucceeded 标记 recovery job 已由正常 settlement 收口。
func (s *ChatSettlementRecoveryStore) MarkChatSettlementRecoveryJobSucceeded(ctx context.Context, jobID int64) error {
	_, err := s.queries.MarkSettlementRecoveryJobSucceeded(ctx, sqlc.MarkSettlementRecoveryJobSucceededParams{
		ID: jobID,
		CompletedAt: pgtype.Timestamptz{
			Time:  time.Now(),
			Valid: true,
		},
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("mark chat settlement recovery job succeeded"),
		)
	}

	return nil
}

// ChatSettlementRecoveryService 负责把 worker claim 到的 recovery job 重放为正式 settlement。
type ChatSettlementRecoveryService struct {
	queries    *sqlc.Queries
	settlement ChatSettlementExecutor
}

// NewChatSettlementRecoveryService 创建 settlement recovery 业务服务。
func NewChatSettlementRecoveryService(queries *sqlc.Queries, settlement ChatSettlementExecutor) *ChatSettlementRecoveryService {
	if queries == nil {
		panic("gateway: chat settlement recovery queries is required")
	}
	if settlement == nil {
		panic("gateway: chat settlement executor is required")
	}

	return &ChatSettlementRecoveryService{
		queries:    queries,
		settlement: settlement,
	}
}

// RecoverChatSettlement 使用 recovery job 保存的事实幂等重放一次 chat settlement。
func (s *ChatSettlementRecoveryService) RecoverChatSettlement(ctx context.Context, job sqlc.SettlementRecoveryJob) error {
	params, err := s.chatSettlementParamsFromJob(ctx, job)
	if err != nil {
		return err
	}

	return s.settlement.SettleSuccessfulChat(ctx, params)
}

func (s *ChatSettlementRecoveryService) chatSettlementParamsFromJob(ctx context.Context, job sqlc.SettlementRecoveryJob) (ChatSettlementParams, error) {
	requestRow, err := s.queries.GetRequestRecordForUpdate(ctx, job.RequestRecordID)
	if err != nil {
		return ChatSettlementParams{}, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("load settlement recovery request record"),
		)
	}

	attemptRow, err := s.loadRecoveryAttempt(ctx, job)
	if err != nil {
		return ChatSettlementParams{}, err
	}

	usage, err := chatSettlementRecoveryUsageFromJob(job)
	if err != nil {
		return ChatSettlementParams{}, err
	}

	requestRecord := chatSettlementRecoveryRequestRecordFromSQLC(requestRow)
	attemptRecord := chatSettlementRecoveryAttemptRecordFromSQLC(attemptRow)

	return ChatSettlementParams{
		RequestRecord: requestRecord,
		AttemptRecord: attemptRecord,
		Principal: &auth.APIKeyPrincipal{
			UserID:    requestRecord.UserID,
			ProjectID: requestRecord.ProjectID,
			APIKeyID:  requestRecord.APIKeyID,
		},
		Authorization: ChatAuthorization{
			ReservationID:    job.ReservationID,
			RequestRecordID:  job.RequestRecordID,
			EstimatedAmount:  job.EstimatedAmount,
			AuthorizedAmount: job.AuthorizedAmount,
			Currency:         job.Currency,
			PriceID:          job.PriceID,
			Price: billing.CustomerPriceSnapshot{
				Currency:             job.Currency,
				PricingUnit:          job.PricingUnit,
				InputPrice:           job.InputPrice,
				OutputPrice:          job.OutputPrice,
				CachedInputPrice:     job.CachedInputPrice,
				ReasoningOutputPrice: job.ReasoningOutputPrice,
				FormulaVersion:       job.FormulaVersion,
			},
		},
		ResponseModelID:       job.ResponseModelID,
		ModelDBID:             job.ModelID,
		FinalProviderID:       job.ProviderID,
		FinalChannelID:        job.ChannelID,
		UpstreamResponseModel: job.UpstreamResponseModel,
		UpstreamStatusCode:    int(job.UpstreamStatusCode),
		UpstreamRequestID:     chatSettlementTextPtr(job.UpstreamRequestID),
		Usage:                 usage,
		UsageSource:           ChatSettlementUsageSource(job.UsageSource),
	}, nil
}

func (s *ChatSettlementRecoveryService) loadRecoveryAttempt(ctx context.Context, job sqlc.SettlementRecoveryJob) (sqlc.RequestAttempt, error) {
	attempts, err := s.queries.ListRequestAttemptsByRequest(ctx, job.RequestRecordID)
	if err != nil {
		return sqlc.RequestAttempt{}, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("load settlement recovery request attempts"),
		)
	}

	for _, attempt := range attempts {
		if attempt.ID == job.AttemptID {
			return attempt, nil
		}
	}

	return sqlc.RequestAttempt{}, failure.New(
		failure.CodeGatewayChatSettlementFailed,
		failure.WithMessage("settlement recovery attempt not found"),
		failure.WithField("request_record_id", job.RequestRecordID),
		failure.WithField("attempt_id", job.AttemptID),
	)
}

func chatSettlementRecoveryUsageFromJob(job sqlc.SettlementRecoveryJob) (adapter.ChatUsage, error) {
	promptTokens, err := int64ToChatUsageInt(job.UsagePromptTokens, "usage_prompt_tokens")
	if err != nil {
		return adapter.ChatUsage{}, err
	}
	completionTokens, err := int64ToChatUsageInt(job.UsageCompletionTokens, "usage_completion_tokens")
	if err != nil {
		return adapter.ChatUsage{}, err
	}
	totalTokens, err := int64ToChatUsageInt(job.UsageTotalTokens, "usage_total_tokens")
	if err != nil {
		return adapter.ChatUsage{}, err
	}
	cachedTokens, err := int64ToChatUsageInt(job.UsageCachedTokens, "usage_cached_tokens")
	if err != nil {
		return adapter.ChatUsage{}, err
	}
	reasoningTokens, err := int64ToChatUsageInt(job.UsageReasoningTokens, "usage_reasoning_tokens")
	if err != nil {
		return adapter.ChatUsage{}, err
	}

	return adapter.ChatUsage{
		PromptTokens:     promptTokens,
		CompletionTokens: completionTokens,
		TotalTokens:      totalTokens,
		CachedTokens:     cachedTokens,
		ReasoningTokens:  reasoningTokens,
	}, nil
}

func int64ToChatUsageInt(value int64, field string) (int, error) {
	if strconv.IntSize == 32 && (value > math.MaxInt32 || value < math.MinInt32) {
		return 0, failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("settlement recovery usage value overflows int"),
			failure.WithField("field", field),
		)
	}

	return int(value), nil
}

func chatSettlementRecoveryRequestRecordFromSQLC(row sqlc.RequestRecord) requestlog.RequestRecord {
	return requestlog.RequestRecord{
		ID:                  row.ID,
		RequestID:           row.RequestID,
		UserID:              row.UserID,
		ProjectID:           row.ProjectID,
		APIKeyID:            row.ApiKeyID,
		RequestedModelID:    row.RequestedModelID,
		ResponseModelID:     chatSettlementTextPtr(row.ResponseModelID),
		Stream:              row.Stream,
		Status:              requestlog.RequestStatus(row.Status),
		FinalProviderID:     chatSettlementInt64Ptr(row.FinalProviderID),
		FinalChannelID:      chatSettlementInt64Ptr(row.FinalChannelID),
		ErrorCode:           chatSettlementTextPtr(row.ErrorCode),
		ErrorMessage:        chatSettlementTextPtr(row.ErrorMessage),
		InternalErrorDetail: chatSettlementTextPtr(row.InternalErrorDetail),
		StartedAt:           row.StartedAt.Time,
		CompletedAt:         chatSettlementTimePtr(row.CompletedAt),
	}
}

func chatSettlementRecoveryAttemptRecordFromSQLC(row sqlc.RequestAttempt) requestlog.AttemptRecord {
	return requestlog.AttemptRecord{
		ID:                    row.ID,
		RequestRecordID:       row.RequestRecordID,
		AttemptIndex:          int(row.AttemptIndex),
		ProviderID:            row.ProviderID,
		ChannelID:             row.ChannelID,
		AdapterKey:            row.AdapterKey,
		UpstreamModel:         row.UpstreamModel,
		UpstreamResponseModel: chatSettlementTextPtr(row.UpstreamResponseModel),
		Status:                requestlog.AttemptStatus(row.Status),
		UpstreamStatusCode:    chatSettlementIntPtr(row.UpstreamStatusCode),
		UpstreamRequestID:     chatSettlementTextPtr(row.UpstreamRequestID),
		ErrorCode:             chatSettlementTextPtr(row.ErrorCode),
		ErrorMessage:          chatSettlementTextPtr(row.ErrorMessage),
		InternalErrorDetail:   chatSettlementTextPtr(row.InternalErrorDetail),
		StartedAt:             row.StartedAt.Time,
		CompletedAt:           chatSettlementTimePtr(row.CompletedAt),
	}
}

// IsChatSettlementRecoveryScheduled 判断 settlement 失败是否已经由 recovery job 接管。
func IsChatSettlementRecoveryScheduled(err error) bool {
	return errors.Is(err, ErrChatSettlementRecoveryScheduled)
}

func chatSettlementRecoveryScheduled(requestRecordID int64, cause error) error {
	return failure.Wrap(
		failure.CodeGatewayChatSettlementFailed,
		errors.Join(ErrChatSettlementRecoveryScheduled, cause),
		failure.WithMessage("chat settlement recovery job scheduled after settlement failure"),
		failure.WithField("request_record_id", requestRecordID),
	)
}

func chatSettlementTextPtr(s pgtype.Text) *string {
	if !s.Valid {
		return nil
	}

	return &s.String
}

// chatSettlementOptionalText 把可选字符串转换成可空 TEXT 列值。
func chatSettlementOptionalText(s *string) pgtype.Text {
	if s == nil {
		return pgtype.Text{Valid: false}
	}

	return pgtype.Text{String: *s, Valid: true}
}

func chatSettlementInt64Ptr(i pgtype.Int8) *int64 {
	if !i.Valid {
		return nil
	}

	return &i.Int64
}

func chatSettlementIntPtr(value pgtype.Int4) *int {
	if !value.Valid {
		return nil
	}

	n := int(value.Int32)
	return &n
}

func chatSettlementTimePtr(value pgtype.Timestamptz) *time.Time {
	if !value.Valid {
		return nil
	}

	return &value.Time
}
