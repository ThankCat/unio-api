package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/core/usage"
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
		panic("lifecycle: chat settlement recovery queries is required")
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
		panic("lifecycle: chat settlement executor is required")
	}
	if recovery == nil {
		panic("lifecycle: chat settlement recovery recorder is required")
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
	if err := ValidateChatSettlementFacts(params); err != nil {
		return sqlc.SettlementRecoveryJob{}, err
	}

	// 阶段 15：补偿任务记录命中渠道的售价（审计快照 + price_id 指向 channel_prices）。
	// worker 重放 settlement 时会按 (channel, model, attemptStart) 重查同源价，故存储列仅作审计基线。
	channelPrice, err := s.queries.FindActiveChannelPrice(ctx, sqlc.FindActiveChannelPriceParams{
		ChannelID: params.FinalChannelID,
		ModelID:   params.ModelDBID,
		AtTime:    pgtype.Timestamptz{Time: params.AttemptRecord.StartedAt, Valid: true},
	})
	if err != nil {
		return sqlc.SettlementRecoveryJob{}, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("find active channel price for settlement recovery job"),
		)
	}

	facts := params.Facts
	serverWebSearchRequests, serverWebFetchRequests := settlementRecoveryServerToolQuantities(facts.Usage.ServerToolUsage)
	job, err := s.queries.CreateSettlementRecoveryJob(ctx, sqlc.CreateSettlementRecoveryJobParams{
		UserID:                            params.RequestRecord.UserID,
		RequestRecordID:                   params.RequestRecord.ID,
		AttemptID:                         params.AttemptRecord.ID,
		ReservationID:                     params.Authorization.ReservationID,
		ResponseProtocol:                  string(params.ResponseProtocol),
		ResponseID:                        params.ResponseID,
		ResponseModelID:                   params.ResponseModelID,
		ModelID:                           params.ModelDBID,
		ProviderID:                        params.FinalProviderID,
		ChannelID:                         params.FinalChannelID,
		UpstreamProtocol:                  facts.UpstreamProtocol,
		UpstreamResponseID:                facts.UpstreamResponseID,
		UpstreamModel:                     facts.UpstreamModel,
		FinishClass:                       string(facts.Finish.Class),
		UpstreamFinishReason:              facts.Finish.RawReason,
		UpstreamStatusCode:                int32(facts.Metadata.StatusCode),
		UpstreamRequestID:                 chatSettlementOptionalText(UpstreamRequestIDPtr(facts.Metadata.RequestID)),
		UsageUncachedInputTokens:          facts.Usage.UncachedInputTokens.Value,
		UsageUncachedInputTokensState:     string(facts.Usage.UncachedInputTokens.State),
		UsageCacheReadInputTokens:         facts.Usage.CacheReadInputTokens.Value,
		UsageCacheReadInputTokensState:    string(facts.Usage.CacheReadInputTokens.State),
		UsageCacheWrite5mInputTokens:      facts.Usage.CacheWrite5mInputTokens.Value,
		UsageCacheWrite5mInputTokensState: string(facts.Usage.CacheWrite5mInputTokens.State),
		UsageCacheWrite1hInputTokens:      facts.Usage.CacheWrite1hInputTokens.Value,
		UsageCacheWrite1hInputTokensState: string(facts.Usage.CacheWrite1hInputTokens.State),
		UsageOutputTokensTotal:            facts.Usage.OutputTokensTotal.Value,
		UsageOutputTokensTotalState:       string(facts.Usage.OutputTokensTotal.State),
		UsageReasoningOutputTokens:        facts.Usage.ReasoningOutputTokens.Value,
		UsageReasoningOutputTokensState:   string(facts.Usage.ReasoningOutputTokens.State),
		UsageServerWebSearchRequests:      serverWebSearchRequests,
		UsageServerWebFetchRequests:       serverWebFetchRequests,
		UsageSource:                       string(facts.UsageSource),
		UsageMappingVersion:               facts.UsageMappingVersion,
		PriceID:                           channelPrice.ID,
		Currency:                          channelPrice.Currency,
		PricingUnit:                       channelPrice.PricingUnit,
		UncachedInputPrice:                channelPrice.UncachedInputPrice,
		CacheReadInputPrice:               channelPrice.CacheReadInputPrice,
		CacheWrite5mInputPrice:            channelPrice.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:            channelPrice.CacheWrite1hInputPrice,
		OutputPrice:                       channelPrice.OutputPrice,
		ReasoningOutputPrice:              channelPrice.ReasoningOutputPrice,
		FormulaVersion:                    billing.FormulaVersionV1,
		EstimatedAmount:                   params.Authorization.EstimatedAmount,
		AuthorizedAmount:                  params.Authorization.AuthorizedAmount,
		NextRunAt: pgtype.Timestamptz{
			Time:  time.Now().Add(s.nextRunDelay),
			Valid: true,
		},
	})
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return sqlc.SettlementRecoveryJob{}, ChatSettlementIdempotencyConflict("settlement recovery job facts mismatch")
		}

		return sqlc.SettlementRecoveryJob{}, failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("create chat settlement recovery job"),
		)
	}

	return job, nil
}

func settlementRecoveryServerToolQuantities(items []usage.MeteredItem) (webSearchRequests int64, webFetchRequests int64) {
	for _, item := range items {
		switch item.Kind {
		case usage.MeteredServerWebSearchRequest:
			webSearchRequests = item.Quantity
		case usage.MeteredServerWebFetchRequest:
			webFetchRequests = item.Quantity
		}
	}

	return webSearchRequests, webFetchRequests
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
		panic("lifecycle: chat settlement recovery queries is required")
	}
	if settlement == nil {
		panic("lifecycle: chat settlement executor is required")
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
		},
		ResponseProtocol:  requestlog.Protocol(job.ResponseProtocol),
		ResponseID:        job.ResponseID,
		ResponseModelID:   job.ResponseModelID,
		ResponseStartedAt: attemptRecord.ResponseStartedAt,
		ModelDBID:         job.ModelID,
		FinalProviderID:   job.ProviderID,
		FinalChannelID:    job.ChannelID,
		Facts:             chatSettlementRecoveryFactsFromJob(job),
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

func chatSettlementRecoveryFactsFromJob(job sqlc.SettlementRecoveryJob) adapter.ResponseFacts {
	serverToolUsage := make([]usage.MeteredItem, 0, 2)
	if job.UsageServerWebSearchRequests > 0 {
		serverToolUsage = append(serverToolUsage, usage.MeteredItem{
			Kind:     usage.MeteredServerWebSearchRequest,
			Quantity: job.UsageServerWebSearchRequests,
		})
	}
	if job.UsageServerWebFetchRequests > 0 {
		serverToolUsage = append(serverToolUsage, usage.MeteredItem{
			Kind:     usage.MeteredServerWebFetchRequest,
			Quantity: job.UsageServerWebFetchRequests,
		})
	}

	return adapter.ResponseFacts{
		UpstreamProtocol:   job.UpstreamProtocol,
		UpstreamResponseID: job.UpstreamResponseID,
		UpstreamModel:      job.UpstreamModel,
		Finish: adapter.FinishFacts{
			Class:     adapter.FinishClass(job.FinishClass),
			RawReason: job.UpstreamFinishReason,
		},
		Usage: usage.Facts{
			UncachedInputTokens:     usage.TokenCount{Value: job.UsageUncachedInputTokens, State: usage.CountState(job.UsageUncachedInputTokensState)},
			CacheReadInputTokens:    usage.TokenCount{Value: job.UsageCacheReadInputTokens, State: usage.CountState(job.UsageCacheReadInputTokensState)},
			CacheWrite5mInputTokens: usage.TokenCount{Value: job.UsageCacheWrite5mInputTokens, State: usage.CountState(job.UsageCacheWrite5mInputTokensState)},
			CacheWrite1hInputTokens: usage.TokenCount{Value: job.UsageCacheWrite1hInputTokens, State: usage.CountState(job.UsageCacheWrite1hInputTokensState)},
			OutputTokensTotal:       usage.TokenCount{Value: job.UsageOutputTokensTotal, State: usage.CountState(job.UsageOutputTokensTotalState)},
			ReasoningOutputTokens:   usage.TokenCount{Value: job.UsageReasoningOutputTokens, State: usage.CountState(job.UsageReasoningOutputTokensState)},
			ServerToolUsage:         serverToolUsage,
		},
		UsageSource:         usage.Source(job.UsageSource),
		UsageMappingVersion: job.UsageMappingVersion,
		Metadata: adapter.UpstreamMetadata{
			StatusCode: int(job.UpstreamStatusCode),
			RequestID:  chatSettlementText(job.UpstreamRequestID),
		},
	}
}

func chatSettlementRecoveryRequestRecordFromSQLC(row sqlc.RequestRecord) requestlog.RequestRecord {
	return requestlog.RequestRecord{
		ID:                  row.ID,
		RequestID:           row.RequestID,
		UserID:              row.UserID,
		ProjectID:           row.ProjectID,
		APIKeyID:            row.ApiKeyID,
		RequestedModelID:    row.RequestedModelID,
		IngressProtocol:     requestlog.Protocol(row.IngressProtocol),
		Operation:           requestlog.Operation(row.Operation),
		ResponseModelID:     chatSettlementTextPtr(row.ResponseModelID),
		ResponseProtocol:    chatSettlementTextPtr(row.ResponseProtocol),
		ResponseID:          chatSettlementTextPtr(row.ResponseID),
		Stream:              row.Stream,
		Status:              requestlog.RequestStatus(row.Status),
		FinalProviderID:     chatSettlementInt64Ptr(row.FinalProviderID),
		FinalChannelID:      chatSettlementInt64Ptr(row.FinalChannelID),
		ErrorCode:           chatSettlementTextPtr(row.ErrorCode),
		ErrorMessage:        chatSettlementTextPtr(row.ErrorMessage),
		InternalErrorDetail: chatSettlementTextPtr(row.InternalErrorDetail),
		DeliveryStatus:      requestlog.DeliveryStatus(row.DeliveryStatus),
		ResponseStartedAt:   chatSettlementTimePtr(row.ResponseStartedAt),
		ResponseCompletedAt: chatSettlementTimePtr(row.ResponseCompletedAt),
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
		UpstreamProtocol:      requestlog.Protocol(row.UpstreamProtocol),
		UpstreamResponseID:    chatSettlementTextPtr(row.UpstreamResponseID),
		UpstreamResponseModel: chatSettlementTextPtr(row.UpstreamResponseModel),
		UpstreamFinishReason:  chatSettlementTextPtr(row.UpstreamFinishReason),
		FinishClass:           chatSettlementTextPtr(row.FinishClass),
		Status:                requestlog.AttemptStatus(row.Status),
		UpstreamStatusCode:    chatSettlementIntPtr(row.UpstreamStatusCode),
		UpstreamRequestID:     chatSettlementTextPtr(row.UpstreamRequestID),
		ErrorCode:             chatSettlementTextPtr(row.ErrorCode),
		ErrorMessage:          chatSettlementTextPtr(row.ErrorMessage),
		InternalErrorDetail:   chatSettlementTextPtr(row.InternalErrorDetail),
		ResponseStartedAt:     chatSettlementTimePtr(row.ResponseStartedAt),
		FinalUsageReceived:    row.FinalUsageReceived,
		UsageMappingVersion:   chatSettlementTextPtr(row.UsageMappingVersion),
		StartedAt:             row.StartedAt.Time,
		CompletedAt:           chatSettlementTimePtr(row.CompletedAt),
	}
}

// IsChatSettlementRecoveryScheduled 判断 settlement 失败是否已经由 recovery job 接管。
func IsChatSettlementRecoveryScheduled(err error) bool {
	return errors.Is(err, ErrChatSettlementRecoveryScheduled)
}

// ChatSettlementRecoveryScheduledError 构造 settlement 失败且 recovery job 已接管时的错误。
// 编排层单测在绕过 RecoverableChatSettlementExecutor 时可用此函数模拟同等错误形态。
func ChatSettlementRecoveryScheduledError(requestRecordID int64, cause error) error {
	return chatSettlementRecoveryScheduled(requestRecordID, cause)
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

func chatSettlementText(s pgtype.Text) string {
	if !s.Valid {
		return ""
	}

	return s.String
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
