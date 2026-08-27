// Package wallet 提供 Console 钱包流水的只读查询。
//
// 权威事实在 ledger_entries：充值、扣费、退款、调额都是一行不可变流水。
// 这里只做「当前用户自己的流水」分页——UserID 一律来自会话主体，
// 不接受任何来自请求参数的用户标识。
package wallet

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
)

// Console 钱包只结 USD；与 auth 模块读余额的口径保持一致。
const walletCurrency = "USD"

// EntryTypes 是钱包页可见/可筛选的流水类型。
//
// 刻意不含 "debit"：那是每笔请求的用量结算，一天可能上千条，会把充值、
// 赠金这类真正的资金往来淹没——单笔消费明细归请求记录页管。
var EntryTypes = map[string]struct{}{
	"credit":            {},
	"refund":            {},
	"adjustment_credit": {},
	"adjustment_debit":  {},
}

// visibleEntryTypes 是不筛选时的默认类型集合（全部可见类型）。
var visibleEntryTypes = []string{"credit", "refund", "adjustment_credit", "adjustment_debit"}

// Store 是钱包流水查询所需的存储能力。
type Store interface {
	ListConsoleLedgerEntries(context.Context, sqlc.ListConsoleLedgerEntriesParams) ([]sqlc.ListConsoleLedgerEntriesRow, error)
	CountConsoleLedgerEntries(context.Context, sqlc.CountConsoleLedgerEntriesParams) (int64, error)
}

// ListParams 是当前用户钱包流水的查询条件。
type ListParams struct {
	UserID     int64
	EntryTypes []string
	From       *time.Time
	To         *time.Time
	Limit      int32
	Offset     int32
}

// Entry 是客户可见的一条钱包流水。
type Entry struct {
	ID              int64
	EntryType       string
	Amount          string
	Currency        string
	BalanceAfter    string
	RequestRecordID *int64
	Reason          string
	CreatedAt       time.Time
}

// Service 提供 Console 钱包流水只读查询。
type Service struct {
	store Store
}

// NewService 创建钱包流水服务。
func NewService(store Store) *Service {
	return &Service{store: store}
}

var _ Store = (*sqlc.Queries)(nil)

// List 返回当前用户的钱包流水分页列表。
func (s *Service) List(ctx context.Context, params ListParams) ([]Entry, int64, *consoleservice.Error) {
	limit := params.Limit
	if limit <= 0 {
		limit = 20
	}
	// 不筛选时也只查可见类型：用量结算的 debit 永远不进钱包页。
	entryTypes := params.EntryTypes
	if len(entryTypes) == 0 {
		entryTypes = visibleEntryTypes
	}
	rows, err := s.store.ListConsoleLedgerEntries(ctx, sqlc.ListConsoleLedgerEntriesParams{
		UserID:     params.UserID,
		Currency:   walletCurrency,
		EntryTypes: entryTypes,
		FromTime:   tsNarg(params.From),
		ToTime:     tsNarg(params.To),
		PageLimit:  limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, consoleservice.RequestUnavailable("list wallet ledger entries", err)
	}
	total := int64(0)
	if len(rows) > 0 {
		total = rows[0].TotalCount
	} else if params.Offset > 0 {
		// offset 超出末页时窗口计数拿不到，退回一次独立 COUNT。
		total, err = s.store.CountConsoleLedgerEntries(ctx, sqlc.CountConsoleLedgerEntriesParams{
			UserID:     params.UserID,
			Currency:   walletCurrency,
			EntryTypes: entryTypes,
			FromTime:   tsNarg(params.From),
			ToTime:     tsNarg(params.To),
		})
		if err != nil {
			return nil, 0, consoleservice.RequestUnavailable("count wallet ledger entries", err)
		}
	}
	entries := make([]Entry, 0, len(rows))
	for _, row := range rows {
		entries = append(entries, Entry{
			ID:              row.ID,
			EntryType:       row.EntryType,
			Amount:          opsutil.NumericString(row.Amount),
			Currency:        row.Currency,
			BalanceAfter:    opsutil.NumericString(row.BalanceAfter),
			RequestRecordID: int8Ptr(row.RequestRecordID),
			Reason:          row.Reason,
			CreatedAt:       row.CreatedAt.Time,
		})
	}
	return entries, total, nil
}

func tsNarg(value *time.Time) pgtype.Timestamptz {
	if value == nil {
		return pgtype.Timestamptz{}
	}
	return pgtype.Timestamptz{Time: *value, Valid: true}
}

func int8Ptr(value pgtype.Int8) *int64 {
	if !value.Valid {
		return nil
	}
	v := value.Int64
	return &v
}
