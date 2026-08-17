package lifecycle

import (
	"context"
	"fmt"
	"math"

	"github.com/ThankCat/unio-gateway/internal/core/auth"
	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/ledger"
	"github.com/ThankCat/unio-gateway/internal/core/requestlog"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/jackc/pgx/v5/pgtype"
)

// DefaultAuthorizationMaxCompletionTokens 是估算冻结额度时的输出 token 兜底上限。
//
// 协议层（OpenAI/Anthropic）的 estimateMaxCompletionTokens 在客户未显式给出输出上限时使用它，
// 因此放在协议无关的共享 lifecycle 包并导出。
const DefaultAuthorizationMaxCompletionTokens int64 = 4096

// ChatAuthorizer 定义 chat 请求调用上游前冻结余额、失败后释放冻结余额的能力。
// gateway 只依赖这个边界，不直接知道 price snapshot 和 ledger reservation 的内部写法。
type ChatAuthorizer interface {
	// AuthorizeNewChat 原子创建持久请求并在调用上游前冻结余额。
	AuthorizeNewChat(ctx context.Context, params ChatAuthorizeNewRequestParams) (ChatAuthorizedRequest, error)
	// ReleaseChat 在请求没有进入可扣费成功语义时释放冻结余额。
	ReleaseChat(ctx context.Context, params ChatReleaseAuthorizationParams) error
	// ReleaseChatForBillingException 释放冻结余额，并记录平台账务异常事实。
	// 它用于上游可能已经产生成本、但本次请求没有可靠 usage、不能向用户扣费的场景。
	ReleaseChatForBillingException(ctx context.Context, params ChatReleaseBillingExceptionParams) error
}

// ChatAuthorizeNewRequestParams 表示原子创建 request record 与余额预授权所需事实。
type ChatAuthorizeNewRequestParams struct {
	Request requestlog.CreateRequestParams

	CandidatePrices          []billing.CustomerPriceSnapshot
	LongContextPolicy        billing.LongContextPolicy
	InputTokens              int64
	MaxCompletionTokens      int64
	CandidateMaxOutputTokens int64
}

// ChatAuthorizedRequest 是已经提交的持久请求与余额预授权。
type ChatAuthorizedRequest struct {
	RequestRecord requestlog.RequestRecord
	Authorization ChatAuthorization
}

// ChatAuthorizeParams 表示一次 chat 请求冻结余额所需的业务事实。
// 它由协议 service 在 request record 和保守 fallback plan 创建后组装。
type ChatAuthorizeParams struct {
	RequestRecord requestlog.RequestRecord
	Principal     *auth.APIKeyPrincipal

	// CandidatePrices 是本次请求保守 fallback 候选池各命中渠道的当前售价（阶段 15）。
	// 下单时最终渠道未定（可能 fallback），冻结取「按本次 token 估算最贵」的一条候选售价做上界，
	// 保证 balanced/fixed 实际命中任一候选都不会超过冻结额。
	// 此处为短上下文牌价；若 LongContextPolicy 对 InputTokens 触发，冻结前会先放大。
	CandidatePrices []billing.CustomerPriceSnapshot

	// LongContextPolicy 来自候选共用的 model_prices 窗口（同一请求模型基准价相同）。
	LongContextPolicy billing.LongContextPolicy

	InputTokens int64

	// MaxCompletionTokens 是客户显式给出的输出 token 上限；0 表示客户未给出。
	// 未给出时 authorization 改用候选模型 max_output_tokens（CandidateMaxOutputTokens）做保守冻结上界。
	MaxCompletionTokens int64

	// CandidateMaxOutputTokens 是本次候选池各模型 max_output_tokens 的最大值（0 表示候选均未配置）。
	// 仅当客户未给出输出上限时参与冻结估算；候选与客户都缺失时回退进程级 MaxOutputTokensFallback。
	CandidateMaxOutputTokens int64
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
//
// 阶段 15：authorization 只负责「保守冻结」，不再锁定结算售价；最终收入由 settlement
// 按实际命中渠道重查 channel_prices 决定（见 settlement.go）。
type ChatAuthorization struct {
	ReservationID    int64
	RequestRecordID  int64
	EstimatedAmount  pgtype.Numeric
	AuthorizedAmount pgtype.Numeric
	Currency         string
}

// ChatReleaseAuthorizationParams 表示释放一次冻结余额所需参数。
// 只在没有可靠 usage、没有进入成功扣费语义时调用。
type ChatReleaseAuthorizationParams struct {
	RequestRecordID int64
	ReservationID   int64
}

// ChatAuthorizationBilling 定义冻结金额估算能力。
type ChatAuthorizationBilling interface {
	EstimateAuthorizationAmount(estimate billing.AuthorizationEstimate, price billing.CustomerPriceSnapshot) (billing.CustomerCharge, error)
}

// ChatAuthorizationLedger 定义创建和释放余额冻结的账本能力。
type ChatAuthorizationLedger interface {
	PreAuthorize(ctx context.Context, params ledger.PreAuthorizeParams) (ledger.Reservation, error)
	CreateRequestAndPreAuthorize(ctx context.Context, params ledger.CreateRequestAndPreAuthorizeParams) (ledger.AuthorizedRequest, error)
	Release(ctx context.Context, params ledger.ReleaseParams) (ledger.Reservation, error)
	// ReleaseWithBillingException 在释放冻结余额的同时记录平台账务异常。
	// gateway 通过它保留“没有扣用户钱，但平台可能承担成本”的审计事实。
	ReleaseWithBillingException(ctx context.Context, params ledger.ReleaseWithBillingExceptionParams) (ledger.Reservation, error)
}

// ChatAuthorizationService 负责 chat 请求调用上游前的余额冻结。
type ChatAuthorizationService struct {
	billing                 ChatAuthorizationBilling
	ledger                  ChatAuthorizationLedger
	maxOutputTokensFallback int64
}

// NewChatAuthorizationService 创建 chat 余额冻结 service。
// maxOutputTokensFallback 是客户与候选模型都未给出输出上限时的兜底冻结上界（<=0 时回退内置默认）。
func NewChatAuthorizationService(billing ChatAuthorizationBilling, ledger ChatAuthorizationLedger, maxOutputTokensFallback int64) *ChatAuthorizationService {
	if billing == nil {
		panic("gateway: chat authorization billing is required")
	}
	if ledger == nil {
		panic("gateway: chat authorization ledger is required")
	}
	if maxOutputTokensFallback <= 0 {
		maxOutputTokensFallback = DefaultAuthorizationMaxCompletionTokens
	}

	return &ChatAuthorizationService{
		billing:                 billing,
		ledger:                  ledger,
		maxOutputTokensFallback: maxOutputTokensFallback,
	}
}

// resolveAuthorizationMaxOutputTokens 决定冻结额度估算用的输出 token 上限：
// 客户显式给出(>0)以客户值为准；否则取候选模型 max_output_tokens 最大值；候选也缺失时回退进程级兜底。
func (s *ChatAuthorizationService) resolveAuthorizationMaxOutputTokens(clientMaxOutputTokens int64, candidateMaxOutputTokens int64) int64 {
	if clientMaxOutputTokens > 0 {
		return clientMaxOutputTokens
	}
	if candidateMaxOutputTokens > 0 {
		return candidateMaxOutputTokens
	}
	return s.maxOutputTokensFallback
}

// AuthorizeChat 在调用上游前冻结本次请求的预估费用。
// 最终扣费仍以 settlement 阶段的真实 usage + 实际命中渠道售价为准。
func (s *ChatAuthorizationService) AuthorizeChat(ctx context.Context, params ChatAuthorizeParams) (ChatAuthorization, error) {
	worst, err := s.estimateAuthorizationAmount(
		params.CandidatePrices,
		params.LongContextPolicy,
		params.InputTokens,
		params.MaxCompletionTokens,
		params.CandidateMaxOutputTokens,
	)
	if err != nil {
		return ChatAuthorization{}, err
	}

	// 用 request_record_id 做幂等边界，避免同一请求重复冻结余额。
	reservation, err := s.ledger.PreAuthorize(ctx, ledger.PreAuthorizeParams{
		UserID:          params.Principal.UserID,
		RequestRecordID: params.RequestRecord.ID,
		EstimatedAmount: worst.Amount,
		Currency:        worst.Currency,
		IdempotencyKey:  fmt.Sprintf("chat:authorize:%d", params.RequestRecord.ID),
		Reason:          "chat completion authorization",
	})
	if err != nil {
		return ChatAuthorization{}, err
	}

	return chatAuthorizationFromReservation(reservation), nil
}

// AuthorizeNewChat 在一个 PostgreSQL 事务内创建 running request record 与余额 reservation。
func (s *ChatAuthorizationService) AuthorizeNewChat(ctx context.Context, params ChatAuthorizeNewRequestParams) (ChatAuthorizedRequest, error) {
	worst, err := s.estimateAuthorizationAmount(
		params.CandidatePrices,
		params.LongContextPolicy,
		params.InputTokens,
		params.MaxCompletionTokens,
		params.CandidateMaxOutputTokens,
	)
	if err != nil {
		return ChatAuthorizedRequest{}, err
	}

	result, err := s.ledger.CreateRequestAndPreAuthorize(ctx, ledger.CreateRequestAndPreAuthorizeParams{
		Request:              params.Request,
		EstimatedAmount:      worst.Amount,
		Currency:             worst.Currency,
		IdempotencyKeyPrefix: "chat:authorize:",
		Reason:               "chat completion authorization",
	})
	if err != nil {
		return ChatAuthorizedRequest{}, err
	}

	return ChatAuthorizedRequest{
		RequestRecord: result.Request,
		Authorization: chatAuthorizationFromReservation(result.Reservation),
	}, nil
}

func (s *ChatAuthorizationService) estimateAuthorizationAmount(
	candidatePrices []billing.CustomerPriceSnapshot,
	longContextPolicy billing.LongContextPolicy,
	inputTokens int64,
	maxCompletionTokens int64,
	candidateMaxOutputTokens int64,
) (billing.CustomerCharge, error) {
	if len(candidatePrices) == 0 {
		return billing.CustomerCharge{}, failure.New(
			failure.CodeGatewayChatAuthorizationFailed,
			failure.WithMessage("chat authorization requires at least one candidate price"),
		)
	}

	// 客户未给出输出上限时，用候选模型 max_output_tokens（取最大值）做保守冻结上界，
	// 候选也缺失才回退进程级兜底；不会改写转发给上游的请求体，仅影响预冻结额度。
	estimate := billing.AuthorizationEstimate{
		InputTokens:         inputTokens,
		MaxCompletionTokens: s.resolveAuthorizationMaxOutputTokens(maxCompletionTokens, candidateMaxOutputTokens),
	}

	// 保守上界：在候选池里取「按本次 token 估算」最贵的一条售价做冻结。
	// 启用长上下文阶梯时，同时估算普通价和阶梯价并取较高值，避免本地输入估算未过门槛、
	// 上游真实 usage 过门槛后才发现冻结金额不足。最终是否应用阶梯价仍由结算按真实 usage 决定。
	var worst billing.CustomerCharge
	found := false
	for _, price := range candidatePrices {
		prices, err := authorizationPriceScenarios(price, longContextPolicy)
		if err != nil {
			return billing.CustomerCharge{}, err
		}
		for _, priced := range prices {
			charge, err := s.billing.EstimateAuthorizationAmount(estimate, priced)
			if err != nil {
				return billing.CustomerCharge{}, err
			}
			if !found || chatSettlementNumericGreaterThan(charge.Amount, worst.Amount) {
				worst = charge
				found = true
			}
		}
	}
	return worst, nil
}

func chatAuthorizationFromReservation(reservation ledger.Reservation) ChatAuthorization {
	return ChatAuthorization{
		ReservationID:    reservation.ID,
		RequestRecordID:  reservation.RequestRecordID,
		EstimatedAmount:  reservation.EstimatedAmount,
		AuthorizedAmount: reservation.AuthorizedAmount,
		Currency:         reservation.Currency,
	}
}

// authorizationPriceScenarios 返回授权必须覆盖的普通价和长上下文价。
// 这里用 Threshold+1 只触发价格向量缩放；金额中的 token 数仍使用调用方的保守输入估算。
func authorizationPriceScenarios(price billing.CustomerPriceSnapshot, policy billing.LongContextPolicy) ([]billing.CustomerPriceSnapshot, error) {
	prices := []billing.CustomerPriceSnapshot{price}
	if !policy.Active() || policy.Threshold == math.MaxInt64 {
		return prices, nil
	}

	longContextPrice, _, err := billing.ApplyLongContextToCustomerPrice(price, policy, policy.Threshold+1)
	if err != nil {
		return nil, err
	}
	return append(prices, longContextPrice), nil
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
