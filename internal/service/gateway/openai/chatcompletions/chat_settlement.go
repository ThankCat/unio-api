package gateway

import (
	"context"
	"errors"
	"fmt"
	"math/big"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// ChatTxBeginner 定义 chat settlement 开启数据库事务所需能力。
type ChatTxBeginner interface {
	Begin(ctx context.Context) (pgx.Tx, error)
}

// ChatLedgerCapturer 定义 chat settlement 确认扣费或释放冻结余额所需能力。
type ChatLedgerCapturer interface {
	CaptureWithQueries(ctx context.Context, queries *sqlc.Queries, params ledger.CaptureParams) (ledger.Reservation, error)
	ReleaseWithQueries(ctx context.Context, queries *sqlc.Queries, params ledger.ReleaseParams) (ledger.Reservation, error)
}

// ChatBillingCalculator 定义 chat settlement 计算请求金额所需能力。
type ChatBillingCalculator interface {
	CalculateCustomerCharge(usage billing.Usage, price billing.CustomerPriceSnapshot) (billing.CustomerCharge, error)
	CalculateProviderCost(usage billing.Usage, cost billing.ProviderCostSnapshot) (billing.ProviderCost, error)
}

// ChatSettlementService 负责 chat 请求成功后的 usage、price snapshot 和 ledger 结算。
type ChatSettlementService struct {
	db                ChatTxBeginner
	queries           *sqlc.Queries
	billingCalculator ChatBillingCalculator
	ledgerCapturer    ChatLedgerCapturer
}

// NewChatSettlementService 创建 chat 请求结算 service。
func NewChatSettlementService(db ChatTxBeginner, queries *sqlc.Queries, billingCalculator ChatBillingCalculator, ledgerCapturer ChatLedgerCapturer) *ChatSettlementService {
	if db == nil {
		panic("gateway: chat settlement tx beginner is required")
	}
	if queries == nil {
		panic("gateway: chat settlement queries is required")
	}
	if billingCalculator == nil {
		panic("gateway: chat billing calculator is required")
	}
	if ledgerCapturer == nil {
		panic("gateway: chat ledger capturer is required")
	}

	return &ChatSettlementService{
		db:                db,
		queries:           queries,
		billingCalculator: billingCalculator,
		ledgerCapturer:    ledgerCapturer,
	}
}

// ChatSettlementUsageSource 表示 usage_records.source 的业务来源。
// 它用于区分 usage 是来自非流式 response，还是来自流式 final usage chunk。
type ChatSettlementUsageSource string

const (
	// ChatSettlementUsageSourceUpstreamResponse 表示 usage 来自非流式上游响应。
	ChatSettlementUsageSourceUpstreamResponse ChatSettlementUsageSource = "upstream_response"

	// ChatSettlementUsageSourceUpstreamStream 表示 usage 来自流式 final usage chunk。
	ChatSettlementUsageSourceUpstreamStream ChatSettlementUsageSource = "upstream_stream"
)

// Valid 判断 usage source 是否是当前数据库允许的稳定枚举值。
func (s ChatSettlementUsageSource) Valid() bool {
	switch s {
	case ChatSettlementUsageSourceUpstreamResponse,
		ChatSettlementUsageSourceUpstreamStream:
		return true
	default:
		return false
	}
}

// ChatSettlementParams 表示一次成功 chat 请求结算所需的事实。
// 非流式 usage 来自 adapter.ChatResponse；流式 usage 来自 final usage stream chunk。
type ChatSettlementParams struct {
	RequestRecord         requestlog.RequestRecord
	AttemptRecord         requestlog.AttemptRecord
	Principal             *auth.APIKeyPrincipal
	Authorization         ChatAuthorization
	ResponseModelID       string
	ModelDBID             int64
	FinalProviderID       int64
	FinalChannelID        int64
	UpstreamResponseModel string

	// UpstreamStatusCode 是上游成功响应的 HTTP 状态码，写入 request attempt 用于渠道审计。
	// 必须落在 [100,599]，否则 request_attempts 的 CHECK 约束会拒绝写入。
	UpstreamStatusCode int

	// UpstreamRequestID 是上游返回的请求 ID；nil 表示上游未提供。
	UpstreamRequestID *string

	Usage       adapter.ChatUsage
	UsageSource ChatSettlementUsageSource
}

// upstreamRequestIDPtr 把上游 request id 字符串转成可选指针，空串视为上游未提供。
func upstreamRequestIDPtr(requestID string) *string {
	if requestID == "" {
		return nil
	}

	return &requestID
}

// SettleSuccessfulChat 对一次成功的 chat 请求执行结算。
func (s *ChatSettlementService) SettleSuccessfulChat(ctx context.Context, params ChatSettlementParams) error {
	tx, err := s.db.Begin(ctx)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("begin chat settlement transaction"),
		)
	}
	defer func() {
		_ = tx.Rollback(ctx)
	}()

	now := time.Now()
	txQueries := s.queries.WithTx(tx)

	lockedRequest, err := txQueries.GetRequestRecordForUpdate(ctx, params.RequestRecord.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lock request record for chat settlement"),
		)
	}

	if !params.UsageSource.Valid() {
		return failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("chat settlement usage source is invalid"),
			failure.WithField("usage_source", string(params.UsageSource)),
		)
	}

	switch requestlog.RequestStatus(lockedRequest.Status) {
	case requestlog.RequestStatusRunning:
		// running 是唯一允许首次执行 settlement 的状态。

	case requestlog.RequestStatusSucceeded:
		// 已成功的 request 不能再次写 usage/snapshot/ledger。
		// 只有既有结算事实和本次重放参数完全一致，才视为幂等成功。
		if err := s.ensureIdempotentSuccessfulChat(ctx, txQueries, lockedRequest, params); err != nil {
			return err
		}

		if err := tx.Commit(ctx); err != nil {
			return failure.Wrap(
				failure.CodeGatewayChatSettlementFailed,
				err,
				failure.WithMessage("commit idempotent chat settlement replay"),
			)
		}
		return nil

	default:
		return failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("request status does not allow chat settlement"),
			failure.WithField("request_status", lockedRequest.Status),
		)
	}

	txRequestLog := requestlog.NewStore(txQueries)
	usage := params.Usage

	// 从 adapter response metadata 写入真实 upstream status code 和 request id，
	// 用于渠道审计和 observability，而不是固定写 200/NULL。
	_, err = txRequestLog.MarkAttemptSucceeded(ctx, requestlog.MarkAttemptSucceededParams{
		ID:                    params.AttemptRecord.ID,
		UpstreamResponseModel: params.UpstreamResponseModel,
		UpstreamStatusCode:    params.UpstreamStatusCode,
		UpstreamRequestID:     params.UpstreamRequestID,
		CompletedAt:           now,
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("create usage record"),
		)
	}

	_, err = txQueries.CreateUsageRecord(ctx, sqlc.CreateUsageRecordParams{
		RequestRecordID:  params.RequestRecord.ID,
		PromptTokens:     int64(usage.PromptTokens),
		CompletionTokens: int64(usage.CompletionTokens),
		TotalTokens:      int64(usage.TotalTokens),
		CachedTokens:     int64(usage.CachedTokens),
		ReasoningTokens:  int64(usage.ReasoningTokens),
		Source:           string(params.UsageSource),
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("find active price for model"),
		)
	}

	if params.Authorization.PriceID <= 0 {
		return failure.New(
			failure.CodeGatewayChatSettlementFailed,
			failure.WithMessage("chat settlement missing authorization price id"),
		)
	}

	authorizationPrice := params.Authorization.Price

	// 将 authorization 使用的价格复制成请求级快照，保证冻结和结算使用同一份价格。
	snapshot, err := txQueries.CreatePriceSnapshot(ctx, sqlc.CreatePriceSnapshotParams{
		RequestRecordID:      params.RequestRecord.ID,
		PriceID:              pgtype.Int8{Int64: params.Authorization.PriceID, Valid: true},
		Currency:             authorizationPrice.Currency,
		PricingUnit:          authorizationPrice.PricingUnit,
		InputPrice:           authorizationPrice.InputPrice,
		OutputPrice:          authorizationPrice.OutputPrice,
		CachedInputPrice:     authorizationPrice.CachedInputPrice,
		ReasoningOutputPrice: authorizationPrice.ReasoningOutputPrice,
		FormulaVersion:       authorizationPrice.FormulaVersion,
	})
	if err != nil {
		return err
	}

	billingUsage := billing.Usage{
		PromptTokens:     int64(usage.PromptTokens),
		CompletionTokens: int64(usage.CompletionTokens),
		TotalTokens:      int64(usage.TotalTokens),
		CachedTokens:     int64(usage.CachedTokens),
		ReasoningTokens:  int64(usage.ReasoningTokens),
	}

	// 计算用户本次请求的花费。
	charge, err := s.billingCalculator.CalculateCustomerCharge(billingUsage, billing.CustomerPriceSnapshot{
		Currency:             snapshot.Currency,
		PricingUnit:          snapshot.PricingUnit,
		InputPrice:           snapshot.InputPrice,
		OutputPrice:          snapshot.OutputPrice,
		CachedInputPrice:     snapshot.CachedInputPrice,
		ReasoningOutputPrice: snapshot.ReasoningOutputPrice,
		FormulaVersion:       snapshot.FormulaVersion,
	})
	if err != nil {
		return err
	}

	// 获取本次请求命中的 channel/model 上游成本单价。
	costPrice, err := txQueries.FindActiveChannelCostPrice(ctx, sqlc.FindActiveChannelCostPriceParams{
		ChannelID: params.FinalChannelID,
		ModelID:   params.ModelDBID,
		AtTime:    pgtype.Timestamptz{Time: params.AttemptRecord.StartedAt, Valid: true},
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("find active channel cost price for chat settlement"),
		)
	}

	// 计算平台本次调用上游的实际成本。
	providerCost, err := s.billingCalculator.CalculateProviderCost(billingUsage, billing.ProviderCostSnapshot{
		Currency:            costPrice.Currency,
		PricingUnit:         costPrice.PricingUnit,
		InputCost:           costPrice.InputCost,
		OutputCost:          costPrice.OutputCost,
		CachedInputCost:     costPrice.CachedInputCost,
		ReasoningOutputCost: costPrice.ReasoningOutputCost,
		FormulaVersion:      billing.FormulaVersionV1,
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("calculate provider cost for chat settlement"),
		)
	}

	// 写入成本快照
	_, err = txQueries.CreateCostSnapshot(ctx, sqlc.CreateCostSnapshotParams{
		RequestRecordID:           params.RequestRecord.ID,
		CostPriceID:               costPrice.ID,
		ProviderID:                params.FinalProviderID,
		ChannelID:                 params.FinalChannelID,
		ModelID:                   params.ModelDBID,
		UpstreamModel:             params.AttemptRecord.UpstreamModel,
		Currency:                  costPrice.Currency,
		PricingUnit:               costPrice.PricingUnit,
		InputCost:                 costPrice.InputCost,
		OutputCost:                costPrice.OutputCost,
		CachedInputCost:           costPrice.CachedInputCost,
		ReasoningOutputCost:       costPrice.ReasoningOutputCost,
		InputCostAmount:           providerCost.InputCostAmount,
		OutputCostAmount:          providerCost.OutputCostAmount,
		CachedInputCostAmount:     providerCost.CachedInputCostAmount,
		ReasoningOutputCostAmount: providerCost.ReasoningOutputCostAmount,
		TotalCostAmount:           providerCost.TotalCostAmount,
		FormulaVersion:            providerCost.FormulaVersion,
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("create chat cost snapshot"),
		)
	}

	reservationID := params.Authorization.ReservationID

	// ledger_entries.amount 要求大于 0；零金额请求保留 usage 和 price snapshot，但不写余额流水。
	if numericIsZero(charge.Amount) {
		_, err := s.ledgerCapturer.ReleaseWithQueries(ctx, txQueries, ledger.ReleaseParams{
			RequestRecordID: params.RequestRecord.ID,
			ReservationID:   &reservationID,
		})
		if err != nil {
			return err
		}
	} else {
		_, err = s.ledgerCapturer.CaptureWithQueries(ctx, txQueries, ledger.CaptureParams{
			RequestRecordID: params.RequestRecord.ID,
			ReservationID:   &reservationID,
			ActualAmount:    charge.Amount,
			IdempotencyKey:  fmt.Sprintf("chat:settle:%d", params.RequestRecord.ID),
			Reason:          "chat completion settlement",
		})
		if err != nil {
			return err
		}
	}

	_, err = txRequestLog.MarkRequestSucceeded(ctx, requestlog.MarkRequestSucceededParams{
		ID:              params.RequestRecord.ID,
		ResponseModelID: params.ResponseModelID,
		FinalProviderID: params.FinalProviderID,
		FinalChannelID:  params.FinalChannelID,
		CompletedAt:     now,
	})
	if err != nil {
		return err
	}

	if err := tx.Commit(ctx); err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("commit chat settlement transaction"),
		)
	}

	return nil
}

// ensureIdempotentSuccessfulChat 校验重复 settlement 是否等价于第一次成功结算。
func (s *ChatSettlementService) ensureIdempotentSuccessfulChat(ctx context.Context, queries *sqlc.Queries, request sqlc.RequestRecord, params ChatSettlementParams) error {
	if err := ensureSettlementRequestMatches(request, params); err != nil {
		return err
	}

	if err := ensureSettlementAttemptMatches(ctx, queries, params); err != nil {
		return err
	}

	usageRecord, err := queries.GetUsageRecordByRequest(ctx, request.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement usage"),
		)
	}

	if err := ensureSettlementUsageMatches(usageRecord, params.Usage, params.UsageSource); err != nil {
		return err
	}

	snapshot, err := queries.GetPriceSnapshotByRequest(ctx, request.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement price snapshot"),
		)
	}

	if err := ensureSettlementPriceSnapshotMatches(snapshot, params.Authorization); err != nil {
		return err
	}

	billingUsage := billing.Usage{
		PromptTokens:     usageRecord.PromptTokens,
		CompletionTokens: usageRecord.CompletionTokens,
		TotalTokens:      usageRecord.TotalTokens,
		CachedTokens:     usageRecord.CachedTokens,
		ReasoningTokens:  usageRecord.ReasoningTokens,
	}

	charge, err := s.billingCalculator.CalculateCustomerCharge(billingUsage, billing.CustomerPriceSnapshot{
		Currency:             snapshot.Currency,
		PricingUnit:          snapshot.PricingUnit,
		InputPrice:           snapshot.InputPrice,
		OutputPrice:          snapshot.OutputPrice,
		CachedInputPrice:     snapshot.CachedInputPrice,
		ReasoningOutputPrice: snapshot.ReasoningOutputPrice,
		FormulaVersion:       snapshot.FormulaVersion,
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("calculate idempotent chat settlement amount"),
		)
	}

	costSnapshot, err := queries.GetCostSnapshotByRequest(ctx, request.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement cost snapshot"),
		)
	}

	providerCost, err := s.billingCalculator.CalculateProviderCost(billingUsage, billing.ProviderCostSnapshot{
		Currency:            costSnapshot.Currency,
		PricingUnit:         costSnapshot.PricingUnit,
		InputCost:           costSnapshot.InputCost,
		OutputCost:          costSnapshot.OutputCost,
		CachedInputCost:     costSnapshot.CachedInputCost,
		ReasoningOutputCost: costSnapshot.ReasoningOutputCost,
		FormulaVersion:      costSnapshot.FormulaVersion,
	})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("calculate idempotent chat settlement provider cost"),
		)
	}

	if err := ensureSettlementCostSnapshotMatches(costSnapshot, params, providerCost); err != nil {
		return err
	}

	reservation, err := queries.GetLedgerReservationByRequestRecordID(ctx, request.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement reservation"),
		)
	}

	if err := ensureSettlementReservationBaseMatches(reservation, request, params.Authorization); err != nil {
		return err
	}

	if numericIsZero(charge.Amount) {
		return ensureSettlementReleasedReservationMatches(ctx, queries, reservation)
	}

	return ensureSettlementCapturedReservationMatches(ctx, queries, reservation, charge.Amount)
}

// ensureSettlementRequestMatches 校验成功 request 终态是否属于本次 settlement 参数。
func ensureSettlementRequestMatches(request sqlc.RequestRecord, params ChatSettlementParams) error {
	if request.ID != params.RequestRecord.ID ||
		request.UserID != params.RequestRecord.UserID ||
		request.ProjectID != params.RequestRecord.ProjectID ||
		request.ApiKeyID != params.RequestRecord.APIKeyID {
		return chatSettlementIdempotencyConflict("request identity mismatch")
	}
	if !requiredTextMatches(request.ResponseModelID, params.ResponseModelID) {
		return chatSettlementIdempotencyConflict("response model mismatch")
	}
	if !requiredInt8Matches(request.FinalProviderID, params.FinalProviderID) {
		return chatSettlementIdempotencyConflict("final provider mismatch")
	}
	if !requiredInt8Matches(request.FinalChannelID, params.FinalChannelID) {
		return chatSettlementIdempotencyConflict("final channel mismatch")
	}

	return nil

}

// ensureSettlementPriceSnapshotMatches 校验请求级价格快照是否等于 authorization 时冻结的价格。
func ensureSettlementPriceSnapshotMatches(snapshot sqlc.PriceSnapshot, authorization ChatAuthorization) error {
	price := authorization.Price

	if !snapshot.PriceID.Valid || snapshot.PriceID.Int64 != authorization.PriceID {
		return chatSettlementIdempotencyConflict("price snapshot id mismatch")
	}
	if snapshot.Currency != price.Currency ||
		snapshot.PricingUnit != price.PricingUnit ||
		snapshot.FormulaVersion != price.FormulaVersion {
		return chatSettlementIdempotencyConflict("price snapshot metadata mismatch")
	}
	if !chatSettlementSameNumeric(snapshot.InputPrice, price.InputPrice) ||
		!chatSettlementSameNumeric(snapshot.OutputPrice, price.OutputPrice) ||
		!chatSettlementSameNumeric(snapshot.CachedInputPrice, price.CachedInputPrice) ||
		!chatSettlementSameNumeric(snapshot.ReasoningOutputPrice, price.ReasoningOutputPrice) {
		return chatSettlementIdempotencyConflict("price snapshot amount mismatch")
	}

	return nil
}

// ensureSettlementCostSnapshotMatches 校验请求级成本快照是否和本次 settlement 参数、自身重算金额一致。
func ensureSettlementCostSnapshotMatches(snapshot sqlc.CostSnapshot, params ChatSettlementParams, cost billing.ProviderCost) error {
	if snapshot.RequestRecordID != params.RequestRecord.ID ||
		snapshot.ProviderID != params.FinalProviderID ||
		snapshot.ChannelID != params.FinalChannelID ||
		snapshot.ModelID != params.ModelDBID {
		return chatSettlementIdempotencyConflict("cost snapshot route mismatch")
	}

	if snapshot.UpstreamModel != params.AttemptRecord.UpstreamModel {
		return chatSettlementIdempotencyConflict("cost snapshot upstream model mismatch")
	}

	if snapshot.CostPriceID <= 0 {
		return chatSettlementIdempotencyConflict("cost snapshot price id mismatch")
	}

	if snapshot.Currency != cost.Currency ||
		snapshot.FormulaVersion != cost.FormulaVersion ||
		snapshot.PricingUnit != billing.PricingUnitPer1MTokens {
		return chatSettlementIdempotencyConflict("cost snapshot metadata mismatch")
	}

	if !chatSettlementSameNumeric(snapshot.InputCostAmount, cost.InputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.OutputCostAmount, cost.OutputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.CachedInputCostAmount, cost.CachedInputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.ReasoningOutputCostAmount, cost.ReasoningOutputCostAmount) ||
		!chatSettlementSameNumeric(snapshot.TotalCostAmount, cost.TotalCostAmount) {
		return chatSettlementIdempotencyConflict("cost snapshot amount mismatch")
	}

	return nil
}

// ensureSettlementReservationBaseMatches 校验 reservation 基础身份和 authorization 事实一致。
func ensureSettlementReservationBaseMatches(reservation sqlc.LedgerReservation, request sqlc.RequestRecord, authorization ChatAuthorization) error {
	if reservation.ID != authorization.ReservationID ||
		reservation.UserID != request.UserID ||
		reservation.RequestRecordID != request.ID ||
		reservation.Currency != authorization.Currency {
		return chatSettlementIdempotencyConflict("reservation identity mismatch")
	}
	if !chatSettlementSameNumeric(reservation.EstimatedAmount, authorization.EstimatedAmount) ||
		!chatSettlementSameNumeric(reservation.AuthorizedAmount, authorization.AuthorizedAmount) {
		return chatSettlementIdempotencyConflict("reservation authorization amount mismatch")
	}

	return nil
}

// ensureSettlementReleasedReservationMatches 校验 0 金额 settlement 是否已经释放冻结余额。
func ensureSettlementReleasedReservationMatches(ctx context.Context, queries *sqlc.Queries, reservation sqlc.LedgerReservation) error {
	if reservation.Status != string(ledger.ReservationStatusReleased) {
		return chatSettlementIdempotencyConflict("reservation release status mismatch")
	}
	if !numericIsZero(reservation.CapturedAmount) ||
		!chatSettlementSameNumeric(reservation.ReleasedAmount, reservation.AuthorizedAmount) ||
		reservation.CaptureLedgerEntryID.Valid {
		return chatSettlementIdempotencyConflict("released reservation amount mismatch")
	}

	entries, err := queries.ListLedgerEntriesByRequest(ctx, pgtype.Int8{Int64: reservation.RequestRecordID, Valid: true})
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent released settlement ledger entries"),
		)
	}
	for _, entry := range entries {
		if entry.EntryType == string(ledger.EntryTypeDebit) {
			return chatSettlementIdempotencyConflict("released settlement has debit ledger entry")
		}
	}

	return nil
}

// ensureSettlementCapturedReservationMatches 校验非 0 金额 settlement 是否已经形成扣费流水。
func ensureSettlementCapturedReservationMatches(ctx context.Context, queries *sqlc.Queries, reservation sqlc.LedgerReservation, actualAmount pgtype.Numeric) error {
	if reservation.Status != string(ledger.ReservationStatusCaptured) {
		return chatSettlementIdempotencyConflict("reservation capture status mismatch")
	}

	capturedAmount := chatSettlementMinNumeric(actualAmount, reservation.AuthorizedAmount)
	if !chatSettlementSameNumeric(reservation.CapturedAmount, capturedAmount) ||
		!chatSettlementNumericDiffMatches(reservation.ReleasedAmount, reservation.AuthorizedAmount, capturedAmount) ||
		!reservation.CaptureLedgerEntryID.Valid {
		return chatSettlementIdempotencyConflict("captured reservation amount mismatch")
	}

	entry, err := queries.GetLedgerEntryByIdempotencyKey(ctx, fmt.Sprintf("chat:settle:%d", reservation.RequestRecordID))
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent capture ledger entry"),
		)
	}
	if entry.ID != reservation.CaptureLedgerEntryID.Int64 ||
		entry.UserID != reservation.UserID ||
		!entry.RequestRecordID.Valid ||
		entry.RequestRecordID.Int64 != reservation.RequestRecordID ||
		entry.EntryType != string(ledger.EntryTypeDebit) ||
		entry.Currency != reservation.Currency ||
		!chatSettlementSameNumeric(entry.Amount, capturedAmount) {
		return chatSettlementIdempotencyConflict("capture ledger entry mismatch")
	}

	return ensureSettlementWriteOffMatches(ctx, queries, reservation, actualAmount, capturedAmount)
}

// chatSettlementIdempotencyConflict 返回重复 settlement 事实不一致的稳定错误。
func chatSettlementIdempotencyConflict(message string) error {
	return failure.New(
		failure.CodeGatewayChatSettlementIdempotencyConflict,
		failure.WithMessage(message),
	)
}

func requiredTextMatches(value pgtype.Text, want string) bool {
	return value.Valid && value.String == want
}

func requiredInt8Matches(value pgtype.Int8, want int64) bool {
	return value.Valid && value.Int64 == want
}

// ensureSettlementAttemptMatches 校验已成功 attempt 是否和本次 settlement 参数一致。
func ensureSettlementAttemptMatches(ctx context.Context, queries *sqlc.Queries, params ChatSettlementParams) error {
	attempts, err := queries.ListRequestAttemptsByRequest(ctx, params.RequestRecord.ID)
	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent chat settlement attempts"),
		)
	}

	for _, attempt := range attempts {
		if attempt.ID != params.AttemptRecord.ID {
			continue
		}

		if attempt.RequestRecordID != params.RequestRecord.ID ||
			attempt.ProviderID != params.FinalProviderID ||
			attempt.ChannelID != params.FinalChannelID {
			return chatSettlementIdempotencyConflict("attempt route mismatch")
		}
		if attempt.Status != string(requestlog.AttemptStatusSucceeded) {
			return chatSettlementIdempotencyConflict("attempt status mismatch")
		}
		if attempt.AdapterKey != params.AttemptRecord.AdapterKey ||
			attempt.UpstreamModel != params.AttemptRecord.UpstreamModel {
			return chatSettlementIdempotencyConflict("attempt upstream request mismatch")
		}
		if !requiredTextMatches(attempt.UpstreamResponseModel, params.UpstreamResponseModel) {
			return chatSettlementIdempotencyConflict("attempt upstream response model mismatch")
		}
		if !requiredInt4Matches(attempt.UpstreamStatusCode, int32(params.UpstreamStatusCode)) {
			return chatSettlementIdempotencyConflict("attempt upstream status mismatch")
		}
		if !optionalTextMatches(attempt.UpstreamRequestID, params.UpstreamRequestID) {
			return chatSettlementIdempotencyConflict("attempt upstream request id mismatch")
		}

		return nil
	}

	return chatSettlementIdempotencyConflict("settlement attempt not found")
}

// ensureSettlementUsageMatches 校验 usage record 是否和本次上游 usage 一致。
func ensureSettlementUsageMatches(row sqlc.UsageRecord, usage adapter.ChatUsage, source ChatSettlementUsageSource) error {
	if row.PromptTokens != int64(usage.PromptTokens) ||
		row.CompletionTokens != int64(usage.CompletionTokens) ||
		row.TotalTokens != int64(usage.TotalTokens) ||
		row.CachedTokens != int64(usage.CachedTokens) ||
		row.ReasoningTokens != int64(usage.ReasoningTokens) {
		return chatSettlementIdempotencyConflict("usage mismatch")
	}

	if row.Source != string(source) {
		return chatSettlementIdempotencyConflict("usage source mismatch")
	}

	return nil
}

// ensureSettlementWriteOffMatches 校验 actual 超过 authorized 时的平台核销事实。
func ensureSettlementWriteOffMatches(ctx context.Context, queries *sqlc.Queries, reservation sqlc.LedgerReservation, actualAmount pgtype.Numeric, capturedAmount pgtype.Numeric) error {
	exception, err := queries.GetLedgerBillingExceptionByReservationID(ctx, reservation.ID)

	if !chatSettlementNumericGreaterThan(actualAmount, reservation.AuthorizedAmount) {
		if err == nil {
			return chatSettlementIdempotencyConflict("unexpected billing exception")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent billing exception"),
		)
	}

	if err != nil {
		return failure.Wrap(
			failure.CodeGatewayChatSettlementFailed,
			err,
			failure.WithMessage("lookup idempotent write off exception"),
		)
	}
	if exception.EventType != "write_off" ||
		exception.UserID != reservation.UserID ||
		exception.RequestRecordID != reservation.RequestRecordID ||
		exception.ReservationID != reservation.ID ||
		exception.Currency != reservation.Currency ||
		!chatSettlementSameNumeric(exception.ActualAmount, actualAmount) ||
		!chatSettlementSameNumeric(exception.CapturedAmount, capturedAmount) ||
		!chatSettlementNumericDiffMatches(exception.PlatformAmount, actualAmount, capturedAmount) {
		return chatSettlementIdempotencyConflict("write off exception mismatch")
	}

	return nil
}

func requiredInt4Matches(value pgtype.Int4, want int32) bool {
	return value.Valid && value.Int32 == want
}

// optionalTextMatches 校验可空 TEXT 列是否与可选字符串一致。
// 两者都缺失视为一致；一有一无或值不同视为不一致。
func optionalTextMatches(value pgtype.Text, want *string) bool {
	if want == nil {
		return !value.Valid
	}

	return value.Valid && value.String == *want
}

func chatSettlementSameNumeric(left pgtype.Numeric, right pgtype.Numeric) bool {
	leftRat, leftOK := chatSettlementNumericRat(left)
	rightRat, rightOK := chatSettlementNumericRat(right)
	if !leftOK || !rightOK {
		return leftOK == rightOK
	}

	return leftRat.Cmp(rightRat) == 0
}

func chatSettlementNumericGreaterThan(left pgtype.Numeric, right pgtype.Numeric) bool {
	cmp, ok := chatSettlementCompareNumeric(left, right)
	return ok && cmp > 0
}

func chatSettlementMinNumeric(left pgtype.Numeric, right pgtype.Numeric) pgtype.Numeric {
	cmp, ok := chatSettlementCompareNumeric(left, right)
	if ok && cmp <= 0 {
		return left
	}

	return right
}

func chatSettlementNumericDiffMatches(value pgtype.Numeric, left pgtype.Numeric, right pgtype.Numeric) bool {
	valueRat, valueOK := chatSettlementNumericRat(value)
	leftRat, leftOK := chatSettlementNumericRat(left)
	rightRat, rightOK := chatSettlementNumericRat(right)
	if !valueOK || !leftOK || !rightOK {
		return false
	}

	return valueRat.Cmp(new(big.Rat).Sub(leftRat, rightRat)) == 0
}

func chatSettlementCompareNumeric(left pgtype.Numeric, right pgtype.Numeric) (int, bool) {
	leftRat, leftOK := chatSettlementNumericRat(left)
	rightRat, rightOK := chatSettlementNumericRat(right)
	if !leftOK || !rightOK {
		return 0, false
	}

	return leftRat.Cmp(rightRat), true
}

func chatSettlementNumericRat(value pgtype.Numeric) (*big.Rat, bool) {
	if !value.Valid || value.NaN || value.InfinityModifier != pgtype.Finite || value.Int == nil {
		return nil, false
	}

	rat := new(big.Rat).SetInt(new(big.Int).Set(value.Int))
	if value.Exp > 0 {
		rat.Mul(rat, new(big.Rat).SetInt(chatSettlementPow10(value.Exp)))
	}
	if value.Exp < 0 {
		rat.Quo(rat, new(big.Rat).SetInt(chatSettlementPow10(-value.Exp)))
	}

	return rat, true
}

func chatSettlementPow10(exp int32) *big.Int {
	return new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(exp)), nil)
}

// numericIsZero 判断 NUMERIC 金额是否表示 0。
func numericIsZero(value pgtype.Numeric) bool {
	if !value.Valid || value.Int == nil {
		return true
	}
	return value.Int.Sign() == 0
}
