package lifecycle

import (
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/core/servicetier"
	"github.com/ThankCat/unio-gateway/internal/core/usage"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

const (
	defaultSettlementRecoveryDelay   = 30 * time.Second
	defaultSettlementRecoveryTimeout = 10 * time.Second
	// defaultSettlementRecoveryMaxAttempts 是补偿任务自动重试次数的回退默认；与退避上限一起决定总覆盖窗口。
	// 与 settlement_recovery_jobs.max_attempts 列默认一致，配置缺省/非法时使用该值。
	defaultSettlementRecoveryMaxAttempts int32 = 20
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
	maxAttempts  int32
}

// NewChatSettlementRecoveryStore 创建 settlement recovery job store。
// maxAttempts 写入每条 job 的 max_attempts，与 worker 退避一起决定补偿总覆盖窗口；<=0 时回退默认。
func NewChatSettlementRecoveryStore(queries *sqlc.Queries, nextRunDelay time.Duration, maxAttempts int32) *ChatSettlementRecoveryStore {
	if queries == nil {
		panic("lifecycle: chat settlement recovery queries is required")
	}
	if nextRunDelay <= 0 {
		nextRunDelay = defaultSettlementRecoveryDelay
	}
	if maxAttempts <= 0 {
		maxAttempts = defaultSettlementRecoveryMaxAttempts
	}

	return &ChatSettlementRecoveryStore{
		queries:      queries,
		nextRunDelay: nextRunDelay,
		maxAttempts:  maxAttempts,
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

	// 仅本地账单 E2E（P2-6：仅 `-tags billing_e2e` 构建生效，生产恒 false）：让「内联首次结算」失败
	// 但保留 pending job，由 worker 幂等重放成功（REC-01）。
	if faultInjectSettlementOnce() {
		return chatSettlementRecoveryScheduled(params.RequestRecord.ID, errors.New("billing e2e injected inline settlement failure (once)"))
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

	// P1-3 + DEC-027：补偿任务持久化成本来源 pin（覆盖 price_id 或 倍率三来源 id）+ 售价向量。
	// worker 重放 settlement 时按这些不可改行确定性复算成本/售价，避免改价/改倍率竞态导致重放漂移。
	facts := params.Facts
	tierSelection := resolveSettlementTierSelection(params)
	requestFinalStatus, attemptFinalStatus := chatSettlementFinalStatuses(params)
	serverWebSearchRequests, serverWebFetchRequests := settlementRecoveryServerToolQuantities(facts.Usage.ServerToolUsage)
	job, err := s.queries.CreateSettlementRecoveryJob(ctx, sqlc.CreateSettlementRecoveryJobParams{
		UserID:                             params.RequestRecord.UserID,
		RequestRecordID:                    params.RequestRecord.ID,
		AttemptID:                          params.AttemptRecord.ID,
		ReservationID:                      params.Authorization.ReservationID,
		ResponseProtocol:                   string(params.ResponseProtocol),
		ResponseID:                         params.ResponseID,
		ResponseModelID:                    params.ResponseModelID,
		RequestFinalStatus:                 string(requestFinalStatus),
		AttemptFinalStatus:                 string(attemptFinalStatus),
		SettlementErrorCode:                params.ErrorCode,
		SettlementErrorMessage:             params.ErrorMessage,
		SettlementInternalErrorDetail:      params.InternalErrorDetail,
		ModelID:                            params.ModelDBID,
		ProviderID:                         params.FinalProviderID,
		ChannelID:                          params.FinalChannelID,
		UpstreamProtocol:                   facts.UpstreamProtocol,
		UpstreamResponseID:                 facts.UpstreamResponseID,
		UpstreamModel:                      facts.UpstreamModel,
		FinishClass:                        string(facts.Finish.Class),
		UpstreamFinishReason:               facts.Finish.RawReason,
		UpstreamStatusCode:                 int32(facts.Metadata.StatusCode),
		UpstreamRequestID:                  chatSettlementOptionalText(UpstreamRequestIDPtr(facts.Metadata.RequestID)),
		UsageUncachedInputTokens:           facts.Usage.UncachedInputTokens.Value,
		UsageUncachedInputTokensState:      string(facts.Usage.UncachedInputTokens.State),
		UsageCacheReadInputTokens:          facts.Usage.CacheReadInputTokens.Value,
		UsageCacheReadInputTokensState:     string(facts.Usage.CacheReadInputTokens.State),
		UsageCacheWrite5mInputTokens:       facts.Usage.CacheWrite5mInputTokens.Value,
		UsageCacheWrite5mInputTokensState:  string(facts.Usage.CacheWrite5mInputTokens.State),
		UsageCacheWrite1hInputTokens:       facts.Usage.CacheWrite1hInputTokens.Value,
		UsageCacheWrite1hInputTokensState:  string(facts.Usage.CacheWrite1hInputTokens.State),
		UsageCacheWrite30mInputTokens:      facts.Usage.CacheWrite30mInputTokens.Value,
		UsageCacheWrite30mInputTokensState: string(facts.Usage.CacheWrite30mInputTokens.State),
		UsageOutputTokensTotal:             facts.Usage.OutputTokensTotal.Value,
		UsageOutputTokensTotalState:        string(facts.Usage.OutputTokensTotal.State),
		UsageReasoningOutputTokens:         facts.Usage.ReasoningOutputTokens.Value,
		UsageReasoningOutputTokensState:    string(facts.Usage.ReasoningOutputTokens.State),
		UsageServerWebSearchRequests:       serverWebSearchRequests,
		UsageServerWebFetchRequests:        serverWebFetchRequests,
		UsageSource:                        string(facts.UsageSource),
		UsageMappingVersion:                facts.UsageMappingVersion,
		PriceID:                            nullableInt8(params.ChannelPriceID),
		CostBaseModelPriceID:               nullableInt8(params.CostBaseModelPriceID),
		ChannelCostMultiplierID:            nullableInt8(params.ChannelCostMultiplierID),
		ChannelRechargeFactorID:            nullableInt8(params.ChannelRechargeFactorID),
		Currency:                           tierSelection.salePrice.Currency,
		PricingUnit:                        tierSelection.salePrice.PricingUnit,
		UncachedInputPrice:                 tierSelection.salePrice.UncachedInputPrice,
		CacheReadInputPrice:                tierSelection.salePrice.CacheReadInputPrice,
		CacheWrite5mInputPrice:             tierSelection.salePrice.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:             tierSelection.salePrice.CacheWrite1hInputPrice,
		CacheWrite30mInputPrice:            tierSelection.salePrice.CacheWrite30mInputPrice,
		OutputPrice:                        tierSelection.salePrice.OutputPrice,
		ReasoningOutputPrice:               tierSelection.salePrice.ReasoningOutputPrice,
		FormulaVersion:                     billing.FormulaVersionV1,
		PriceRatio:                         params.PriceRatio,
		LongContextEnabled:                 params.LongContextPolicy.Enabled,
		LongContextThreshold:               settlementRecoveryLongContextThreshold(params.LongContextPolicy),
		LongContextInputMultiplier:         params.LongContextPolicy.InputMultiplier,
		LongContextOutputMultiplier:        params.LongContextPolicy.OutputMultiplier,
		RequestedServiceTier:               chatSettlementTierText(tierSelection.requested),
		ActualServiceTier:                  chatSettlementTierText(tierSelection.actual),
		SettledServiceTier:                 chatSettlementTierText(tierSelection.settled),
		UpstreamServiceTier:                chatSettlementOptionalText(UpstreamRequestIDPtr(tierSelection.upstreamRaw)),
		ServiceTierResolution:              chatSettlementResolutionText(tierSelection.resolution),
		ModelPriceServiceTierID:            nullableInt8(params.FastModelPriceServiceTierID),
		ChannelPriceServiceTierID:          nullableInt8(params.FastChannelPriceServiceTierID),
		EstimatedAmount:                    params.Authorization.EstimatedAmount,
		AuthorizedAmount:                   params.Authorization.AuthorizedAmount,
		MaxAttempts:                        s.maxAttempts,
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

func settlementRecoveryLongContextThreshold(policy billing.LongContextPolicy) pgtype.Int8 {
	if policy.Threshold <= 0 {
		return pgtype.Int8{Valid: false}
	}
	return pgtype.Int8{Int64: policy.Threshold, Valid: true}
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

// ChatSettlementRecoveryExecutor 是 recovery service 依赖的结算能力：
// 既能幂等重放成功结算（RecoverChatSettlement 用），也能收口无法恢复的死信补偿任务
// （worker 把 dead 任务的请求/资金残留收口时用）。由 *ChatSettlementService 实现。
type ChatSettlementRecoveryExecutor interface {
	ChatSettlementExecutor
	FinalizeDeadChatSettlement(ctx context.Context, job sqlc.SettlementRecoveryJob) error
}

// ChatSettlementRecoveryService 负责把 worker claim 到的 recovery job 重放为正式 settlement。
type ChatSettlementRecoveryService struct {
	queries    *sqlc.Queries
	settlement ChatSettlementRecoveryExecutor
}

// NewChatSettlementRecoveryService 创建 settlement recovery 业务服务。
func NewChatSettlementRecoveryService(queries *sqlc.Queries, settlement ChatSettlementRecoveryExecutor) *ChatSettlementRecoveryService {
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

// FinalizeDeadChatSettlement 收口一条已 dead 的补偿任务对应的请求/资金残留，委托给 settlement service。
func (s *ChatSettlementRecoveryService) FinalizeDeadChatSettlement(ctx context.Context, job sqlc.SettlementRecoveryJob) error {
	return s.settlement.FinalizeDeadChatSettlement(ctx, job)
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
			UserID:   requestRecord.UserID,
			APIKeyID: requestRecord.APIKeyID,
		},
		Authorization: ChatAuthorization{
			ReservationID:    job.ReservationID,
			RequestRecordID:  job.RequestRecordID,
			EstimatedAmount:  job.EstimatedAmount,
			AuthorizedAmount: job.AuthorizedAmount,
			Currency:         job.Currency,
		},
		ResponseProtocol:    requestlog.Protocol(job.ResponseProtocol),
		ResponseID:          job.ResponseID,
		ResponseModelID:     job.ResponseModelID,
		GatewayFirstTokenAt: attemptRecord.GatewayFirstTokenAt,
		RequestFinalStatus:  requestlog.RequestStatus(job.RequestFinalStatus),
		AttemptFinalStatus:  requestlog.AttemptStatus(job.AttemptFinalStatus),
		ErrorCode:           job.SettlementErrorCode,
		ErrorMessage:        job.SettlementErrorMessage,
		InternalErrorDetail: job.SettlementInternalErrorDetail,
		ModelDBID:           job.ModelID,
		FinalProviderID:     job.ProviderID,
		FinalChannelID:      job.ChannelID,
		// 重放 settlement 沿用 job 落库时锁定的成本来源 pin（覆盖 price_id 或 倍率三来源 id，P1-3 + DEC-027）；
		// 客户售价用 job 落库时算好的售价向量（= 基准 × 倍率，DEC-026），保证重放账单与首次一致、不受改价/改倍率竞态影响。
		ChannelPriceID:                int8OrZero(job.PriceID),
		FastModelPriceServiceTierID:   int8OrZero(job.ModelPriceServiceTierID),
		FastChannelPriceServiceTierID: int8OrZero(job.ChannelPriceServiceTierID),
		CostBaseModelPriceID:          int8OrZero(job.CostBaseModelPriceID),
		ChannelCostMultiplierID:       int8OrZero(job.ChannelCostMultiplierID),
		ChannelRechargeFactorID:       int8OrZero(job.ChannelRechargeFactorID),
		SalePrice: billing.CustomerPriceSnapshot{
			Currency:                job.Currency,
			PricingUnit:             job.PricingUnit,
			UncachedInputPrice:      job.UncachedInputPrice,
			CacheReadInputPrice:     job.CacheReadInputPrice,
			CacheWrite5mInputPrice:  job.CacheWrite5mInputPrice,
			CacheWrite1hInputPrice:  job.CacheWrite1hInputPrice,
			CacheWrite30mInputPrice: job.CacheWrite30mInputPrice,
			OutputPrice:             job.OutputPrice,
			ReasoningOutputPrice:    job.ReasoningOutputPrice,
			FormulaVersion:          job.FormulaVersion,
		},
		FastSalePrice: billing.CustomerPriceSnapshot{
			Currency:                job.Currency,
			PricingUnit:             job.PricingUnit,
			UncachedInputPrice:      job.UncachedInputPrice,
			CacheReadInputPrice:     job.CacheReadInputPrice,
			CacheWrite5mInputPrice:  job.CacheWrite5mInputPrice,
			CacheWrite1hInputPrice:  job.CacheWrite1hInputPrice,
			CacheWrite30mInputPrice: job.CacheWrite30mInputPrice,
			OutputPrice:             job.OutputPrice,
			ReasoningOutputPrice:    job.ReasoningOutputPrice,
			FormulaVersion:          job.FormulaVersion,
		},
		PriceRatio: job.PriceRatio,
		LongContextPolicy: billing.LongContextPolicy{
			Enabled:          job.LongContextEnabled,
			Threshold:        int8OrZero(job.LongContextThreshold),
			InputMultiplier:  job.LongContextInputMultiplier,
			OutputMultiplier: job.LongContextOutputMultiplier,
		},
		Facts: chatSettlementRecoveryFactsFromJob(job),
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
			UncachedInputTokens:      usage.TokenCount{Value: job.UsageUncachedInputTokens, State: usage.CountState(job.UsageUncachedInputTokensState)},
			CacheReadInputTokens:     usage.TokenCount{Value: job.UsageCacheReadInputTokens, State: usage.CountState(job.UsageCacheReadInputTokensState)},
			CacheWrite5mInputTokens:  usage.TokenCount{Value: job.UsageCacheWrite5mInputTokens, State: usage.CountState(job.UsageCacheWrite5mInputTokensState)},
			CacheWrite1hInputTokens:  usage.TokenCount{Value: job.UsageCacheWrite1hInputTokens, State: usage.CountState(job.UsageCacheWrite1hInputTokensState)},
			CacheWrite30mInputTokens: usage.TokenCount{Value: job.UsageCacheWrite30mInputTokens, State: usage.CountState(job.UsageCacheWrite30mInputTokensState)},
			OutputTokensTotal:        usage.TokenCount{Value: job.UsageOutputTokensTotal, State: usage.CountState(job.UsageOutputTokensTotalState)},
			ReasoningOutputTokens:    usage.TokenCount{Value: job.UsageReasoningOutputTokens, State: usage.CountState(job.UsageReasoningOutputTokensState)},
			ServerToolUsage:          serverToolUsage,
		},
		UsageSource:         usage.Source(job.UsageSource),
		UsageMappingVersion: job.UsageMappingVersion,
		ServiceTier: servicetier.Response{
			Actual:      servicetier.Tier(chatSettlementText(job.ActualServiceTier)),
			Settled:     servicetier.Tier(chatSettlementText(job.SettledServiceTier)),
			UpstreamRaw: chatSettlementText(job.UpstreamServiceTier),
			Resolution:  servicetier.Resolution(chatSettlementText(job.ServiceTierResolution)),
		},
		Metadata: adapter.UpstreamMetadata{
			StatusCode: int(job.UpstreamStatusCode),
			RequestID:  chatSettlementText(job.UpstreamRequestID),
		},
	}
}

func chatSettlementRecoveryRequestRecordFromSQLC(row sqlc.RequestRecord) requestlog.RequestRecord {
	return requestlog.RequestRecord{
		ID:                    row.ID,
		RequestID:             row.RequestID,
		UserID:                row.UserID,
		APIKeyID:              row.ApiKeyID,
		RequestedModelID:      row.RequestedModelID,
		IngressProtocol:       requestlog.Protocol(row.IngressProtocol),
		Endpoint:              requestlog.Endpoint(row.Endpoint),
		ResponseModelID:       chatSettlementTextPtr(row.ResponseModelID),
		ResponseProtocol:      chatSettlementTextPtr(row.ResponseProtocol),
		ResponseID:            chatSettlementTextPtr(row.ResponseID),
		Stream:                row.Stream,
		Status:                requestlog.RequestStatus(row.Status),
		FinalProviderID:       chatSettlementInt64Ptr(row.FinalProviderID),
		FinalChannelID:        chatSettlementInt64Ptr(row.FinalChannelID),
		ErrorCode:             chatSettlementTextPtr(row.ErrorCode),
		ErrorMessage:          chatSettlementTextPtr(row.ErrorMessage),
		InternalErrorDetail:   chatSettlementTextPtr(row.InternalErrorDetail),
		DeliveryStatus:        requestlog.DeliveryStatus(row.DeliveryStatus),
		GatewayFirstTokenAt:   chatSettlementTimePtr(row.GatewayFirstTokenAt),
		ResponseCompletedAt:   chatSettlementTimePtr(row.ResponseCompletedAt),
		StartedAt:             row.StartedAt.Time,
		CompletedAt:           chatSettlementTimePtr(row.CompletedAt),
		RequestedServiceTier:  servicetier.Tier(chatSettlementText(row.RequestedServiceTier)),
		ActualServiceTier:     servicetier.Tier(chatSettlementText(row.ActualServiceTier)),
		SettledServiceTier:    servicetier.Tier(chatSettlementText(row.SettledServiceTier)),
		ServiceTierResolution: chatSettlementText(row.ServiceTierResolution),
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
		GatewayFirstTokenAt:   chatSettlementTimePtr(row.GatewayFirstTokenAt),
		FinalUsageReceived:    row.FinalUsageReceived,
		UsageMappingVersion:   chatSettlementTextPtr(row.UsageMappingVersion),
		StartedAt:             row.StartedAt.Time,
		CompletedAt:           chatSettlementTimePtr(row.CompletedAt),
		RequestedServiceTier:  servicetier.Tier(chatSettlementText(row.RequestedServiceTier)),
		UpstreamServiceTier:   chatSettlementTextPtr(row.UpstreamServiceTier),
	}
}

func chatSettlementTierText(tier servicetier.Tier) pgtype.Text {
	return pgtype.Text{String: string(tier), Valid: tier != ""}
}

func chatSettlementResolutionText(resolution servicetier.Resolution) pgtype.Text {
	return pgtype.Text{String: string(resolution), Valid: resolution != ""}
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
