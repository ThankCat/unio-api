package chatcompletions

import (
	"context"
	"fmt"
	"time"

	"github.com/ThankCat/unio-api/internal/core/auth"
	"github.com/ThankCat/unio-api/internal/core/billing"
	"github.com/ThankCat/unio-api/internal/core/ledger"
	"github.com/ThankCat/unio-api/internal/core/requestlog"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
	"github.com/jackc/pgx/v5/pgtype"
)

const defaultAuthorizationMaxCompletionTokens int64 = 4096

// ChatAuthorizer 定义 chat 请求调用上游前冻结余额、失败后释放冻结余额的能力。
// gateway 只依赖这个边界，不直接知道 price snapshot 和 ledger reservation 的内部写法。
type ChatAuthorizer interface {
	// AuthorizeChat 在调用上游前为本次 chat 请求冻结余额。
	AuthorizeChat(ctx context.Context, params ChatAuthorizeParams) (ChatAuthorization, error)
	// ReleaseChat 在请求没有进入可扣费成功语义时释放冻结余额。
	ReleaseChat(ctx context.Context, params ChatReleaseAuthorizationParams) error
	// ReleaseChatForBillingException 释放冻结余额，并记录平台账务异常事实。
	// 它用于上游可能已经产生成本、但本次请求没有可靠 usage、不能向用户扣费的场景。
	ReleaseChatForBillingException(ctx context.Context, params ChatReleaseBillingExceptionParams) error
}

// ChatAuthorizeParams 表示一次 chat 请求冻结余额所需的业务事实。
// 它由 ChatCompletionService 在 request record 和保守 fallback plan 创建后组装。
type ChatAuthorizeParams struct {
	RequestRecord       requestlog.RequestRecord
	Principal           *auth.APIKeyPrincipal
	ModelDBID           int64
	InputTokens         int64
	MaxCompletionTokens int64
}

// ChatReleaseBillingExceptionParams 表示异常释放 chat 冻结余额所需参数。
// ReasonCode 是稳定原因码，供后续审计和告警聚合；Reason 是面向内部排查的说明。
type ChatReleaseBillingExceptionParams struct {
	RequestRecordID int64
	ReservationID   int64
	ReasonCode      string
	Reason          string
}

// ChatAuthorization 表示一次已经创建成功地请求级资金冻结。
// ReservationID 后续交给 settlement，用来 capture 同一笔冻结资金。
type ChatAuthorization struct {
	ReservationID    int64
	RequestRecordID  int64
	EstimatedAmount  pgtype.Numeric
	AuthorizedAmount pgtype.Numeric
	Currency         string

	// PriceID 是 authorization 时读取到的 prices.id，后续写入 price_snapshots.price_id。
	PriceID int64

	// Price 是 authorization 时使用的售卖价副本，后续 settlement 用它计算最终费用。
	Price billing.CustomerPriceSnapshot
}

// ChatReleaseAuthorizationParams 表示释放一次冻结余额所需参数。
// 只在没有可靠 usage、没有进入成功扣费语义时调用。
type ChatReleaseAuthorizationParams struct {
	RequestRecordID int64
	ReservationID   int64
}

// ChatAuthorizationPriceStore 定义读取当前有效价格的能力。
type ChatAuthorizationPriceStore interface {
	FindActivePriceForModel(ctx context.Context, arg sqlc.FindActivePriceForModelParams) (sqlc.Price, error)
}

// ChatAuthorizationBilling 定义冻结金额估算能力。
type ChatAuthorizationBilling interface {
	EstimateAuthorizationAmount(estimate billing.AuthorizationEstimate, price billing.CustomerPriceSnapshot) (billing.CustomerCharge, error)
}

// ChatAuthorizationLedger 定义创建和释放余额冻结的账本能力。
type ChatAuthorizationLedger interface {
	PreAuthorize(ctx context.Context, params ledger.PreAuthorizeParams) (ledger.Reservation, error)
	Release(ctx context.Context, params ledger.ReleaseParams) (ledger.Reservation, error)
	// ReleaseWithBillingException 在释放冻结余额的同时记录平台账务异常。
	// gateway 通过它保留“没有扣用户钱，但平台可能承担成本”的审计事实。
	ReleaseWithBillingException(ctx context.Context, params ledger.ReleaseWithBillingExceptionParams) (ledger.Reservation, error)
}

// ChatAuthorizationService 负责 chat 请求调用上游前的余额冻结。
type ChatAuthorizationService struct {
	priceStore ChatAuthorizationPriceStore
	billing    ChatAuthorizationBilling
	ledger     ChatAuthorizationLedger
}

// NewChatAuthorizationService 创建 chat 余额冻结 service。
func NewChatAuthorizationService(priceStore ChatAuthorizationPriceStore, billing ChatAuthorizationBilling, ledger ChatAuthorizationLedger) *ChatAuthorizationService {
	if priceStore == nil {
		panic("gateway: chat authorization price store is required")
	}
	if billing == nil {
		panic("gateway: chat authorization billing is required")
	}
	if ledger == nil {
		panic("gateway: chat authorization ledger is required")
	}

	return &ChatAuthorizationService{
		priceStore: priceStore,
		billing:    billing,
		ledger:     ledger,
	}
}

// AuthorizeChat 在调用上游前冻结本次请求的预估费用。
// 最终扣费仍以 settlement 阶段的真实 usage 为准。
func (s *ChatAuthorizationService) AuthorizeChat(ctx context.Context, params ChatAuthorizeParams) (ChatAuthorization, error) {
	now := time.Now()

	// 冻结余额只读取当前生效价格；price snapshot 留给成功 settlement 创建。
	price, err := s.priceStore.FindActivePriceForModel(ctx, sqlc.FindActivePriceForModelParams{
		ModelID: params.ModelDBID,
		AtTime:  pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return ChatAuthorization{}, failure.Wrap(
			failure.CodeGatewayChatAuthorizationFailed,
			err,
			failure.WithMessage("find active price for chat authorization"),
		)
	}

	authorizationPrice := customerPriceSnapshotFromActivePrice(price)

	// 这里是控损估算，不是最终 usage；但价格必须和最终 settlement 使用同一份。
	settlement, err := s.billing.EstimateAuthorizationAmount(
		billing.AuthorizationEstimate{
			InputTokens:         params.InputTokens,
			MaxCompletionTokens: params.MaxCompletionTokens,
		},
		authorizationPrice,
	)
	if err != nil {
		return ChatAuthorization{}, err
	}

	// 用 request_record_id 做幂等边界，避免同一请求重复冻结余额。
	reservation, err := s.ledger.PreAuthorize(ctx, ledger.PreAuthorizeParams{
		UserID:          params.Principal.UserID,
		RequestRecordID: params.RequestRecord.ID,
		EstimatedAmount: settlement.Amount,
		Currency:        settlement.Currency,
		IdempotencyKey:  fmt.Sprintf("chat:authorize:%d", params.RequestRecord.ID),
		Reason:          "chat completion authorization",
	})
	if err != nil {
		return ChatAuthorization{}, err
	}

	return ChatAuthorization{
		ReservationID:    reservation.ID,
		RequestRecordID:  reservation.RequestRecordID,
		EstimatedAmount:  reservation.EstimatedAmount,
		AuthorizedAmount: reservation.AuthorizedAmount,
		Currency:         reservation.Currency,
		PriceID:          price.ID,
		Price:            authorizationPrice,
	}, nil
}

// ReleaseChat 释放未进入成功结算语义的冻结余额。
func (s *ChatAuthorizationService) ReleaseChat(ctx context.Context, params ChatReleaseAuthorizationParams) error {
	reservationID := params.ReservationID

	_, err := s.ledger.Release(ctx, ledger.ReleaseParams{
		RequestRecordID: params.RequestRecordID,
		ReservationID:   &reservationID,
	})

	return err
}

// releaseChatAuthorization 脱离客户端取消上下文释放冻结余额。
func (s *ChatCompletionService) releaseChatAuthorization(ctx context.Context, authorization ChatAuthorization) error {
	if authorization.RequestRecordID == 0 || authorization.ReservationID == 0 {
		return nil
	}

	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	return s.chatAuthorizer.ReleaseChat(releaseCtx, ChatReleaseAuthorizationParams{
		RequestRecordID: authorization.RequestRecordID,
		ReservationID:   authorization.ReservationID,
	})
}

// ReleaseChatForBillingException 释放 chat 冻结余额，并记录平台账务异常事实。
// 它只处理无法可靠结算的异常路径，不用于正常失败释放或成功扣费。
func (s *ChatAuthorizationService) ReleaseChatForBillingException(ctx context.Context, params ChatReleaseBillingExceptionParams) error {
	reservationID := params.ReservationID
	_, err := s.ledger.ReleaseWithBillingException(ctx, ledger.ReleaseWithBillingExceptionParams{
		RequestRecordID: params.RequestRecordID,
		ReservationID:   &reservationID,
		ReasonCode:      params.ReasonCode,
		Reason:          params.Reason,
	})
	return err
}

// customerPriceSnapshotFromActivePrice 把当前生效售价转换为冻结和结算共用的客户售价快照。
// 这样请求过程中价格变化时，最终扣费仍使用 authorization 时看到的同一份价格。
func customerPriceSnapshotFromActivePrice(price sqlc.Price) billing.CustomerPriceSnapshot {
	return billing.CustomerPriceSnapshot{
		Currency:               price.Currency,
		PricingUnit:            price.PricingUnit,
		UncachedInputPrice:     price.UncachedInputPrice,
		CacheReadInputPrice:    price.CacheReadInputPrice,
		CacheWrite5mInputPrice: price.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice: price.CacheWrite1hInputPrice,
		OutputPrice:            price.OutputPrice,
		ReasoningOutputPrice:   price.ReasoningOutputPrice,
		FormulaVersion:         billing.FormulaVersionV1,
	}
}

// releaseChatAuthorizationForBillingException 脱离客户端取消上下文释放冻结余额并记录异常事实。
// 它用于 stream 已经可能产生上游成本、但没有 final usage，因而不能扣用户余额的边界。
func (s *ChatCompletionService) releaseChatAuthorizationForBillingException(ctx context.Context, authorization ChatAuthorization, reasonCode string, reason string) error {
	if authorization.RequestRecordID == 0 || authorization.ReservationID == 0 {
		return nil
	}

	releaseCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
	defer cancel()

	return s.chatAuthorizer.ReleaseChatForBillingException(releaseCtx, ChatReleaseBillingExceptionParams{
		RequestRecordID: authorization.RequestRecordID,
		ReservationID:   authorization.ReservationID,
		ReasonCode:      reasonCode,
		Reason:          reason,
	})
}
