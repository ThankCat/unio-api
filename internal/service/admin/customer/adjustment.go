package customer

import (
	"context"
	"fmt"
	"strings"

	"github.com/google/uuid"

	"github.com/ThankCat/unio-api/internal/core/ledger"
)

// 调额方向。
const (
	AdjustmentDirectionCredit = "credit"
	AdjustmentDirectionDebit  = "debit"
)

// Adjustment 表示一次手工调额的结果视图（金额为十进制字符串）。
type Adjustment struct {
	EntryID      int64
	UserID       int64
	EntryType    string
	Amount       string
	Currency     string
	BalanceAfter string
	Reason       string
}

// AdjustParams 表示手工调额的业务参数。
// IdempotencyKey 为空时服务端生成；调用方传入可保证重试不重复入账。
type AdjustParams struct {
	UserID         int64
	Direction      string
	Amount         string
	Currency       string
	Reason         string
	IdempotencyKey string
}

// AdjustmentLedger 定义手工调额所需的账本能力。
type AdjustmentLedger interface {
	AdjustCredit(ctx context.Context, params ledger.AdjustParams) (ledger.Entry, error)
	AdjustDebit(ctx context.Context, params ledger.AdjustParams) (ledger.Entry, error)
}

// AdjustmentService 提供 admin 手工调额（充值 / 扣款），一律走账本留痕。
type AdjustmentService struct {
	ledger AdjustmentLedger
}

// NewAdjustmentService 创建调额 service。
func NewAdjustmentService(ledgerSvc AdjustmentLedger) *AdjustmentService {
	if ledgerSvc == nil {
		panic("customer: adjustment ledger is required")
	}
	return &AdjustmentService{ledger: ledgerSvc}
}

// Adjust 对用户余额执行手工充值或扣款，并写入 adjustment_* 账本流水。
func (s *AdjustmentService) Adjust(ctx context.Context, params AdjustParams) (Adjustment, error) {
	currency := strings.TrimSpace(params.Currency)
	if currency == "" {
		return Adjustment{}, invalidArgument("currency", "currency must not be empty")
	}

	reason := strings.TrimSpace(params.Reason)
	if reason == "" {
		return Adjustment{}, invalidArgument("reason", "reason must not be empty")
	}

	amount, err := parseMoney("amount", params.Amount)
	if err != nil {
		return Adjustment{}, err
	}
	// 金额必须为正：方向由 direction 决定，amount 只表示绝对值。
	if amount.Int == nil || amount.Int.Sign() == 0 {
		return Adjustment{}, invalidArgument("amount", "amount must be greater than zero")
	}

	idempotencyKey := strings.TrimSpace(params.IdempotencyKey)
	if idempotencyKey == "" {
		idempotencyKey = fmt.Sprintf("admin:adjust:%d:%s", params.UserID, uuid.NewString())
	}

	ledgerParams := ledger.AdjustParams{
		UserID:         params.UserID,
		Amount:         amount,
		Currency:       currency,
		IdempotencyKey: idempotencyKey,
		Reason:         reason,
	}

	var entry ledger.Entry
	switch params.Direction {
	case AdjustmentDirectionCredit:
		entry, err = s.ledger.AdjustCredit(ctx, ledgerParams)
	case AdjustmentDirectionDebit:
		entry, err = s.ledger.AdjustDebit(ctx, ledgerParams)
	default:
		return Adjustment{}, invalidArgument("direction", "direction must be credit or debit")
	}
	if err != nil {
		return Adjustment{}, err
	}

	return Adjustment{
		EntryID:      entry.ID,
		UserID:       entry.UserID,
		EntryType:    string(entry.EntryType),
		Amount:       numericString(entry.Amount),
		Currency:     entry.Currency,
		BalanceAfter: numericString(entry.BalanceAfter),
		Reason:       entry.Reason,
	}, nil
}
