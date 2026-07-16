package query

import (
	"context"
	"time"

	"github.com/ThankCat/unio-api/internal/platform/store/sqlc"
)

// LedgerStore 定义账本只读查询所需的存储能力。
type LedgerStore interface {
	ListLedgerEntriesPage(ctx context.Context, arg sqlc.ListLedgerEntriesPageParams) ([]sqlc.ListLedgerEntriesPageRow, error)
	CountLedgerEntries(ctx context.Context, arg sqlc.CountLedgerEntriesParams) (int64, error)
	ListLedgerBillingExceptionsPage(ctx context.Context, arg sqlc.ListLedgerBillingExceptionsPageParams) ([]sqlc.ListLedgerBillingExceptionsPageRow, error)
	CountLedgerBillingExceptions(ctx context.Context, arg sqlc.CountLedgerBillingExceptionsParams) (int64, error)
}

// EntryListParams 是分页/过滤列出账本流水的入参；指针/空串/nil 表示该维度不过滤。
type EntryListParams struct {
	UserID    *int64
	EntryType string
	Currency  string
	From      *time.Time
	To        *time.Time
	SortField string
	SortDesc  bool
	Limit     int32
	Offset    int32
}

// ExceptionListParams 是分页/过滤列出计费异常的入参；指针/空串/nil 表示该维度不过滤。
type ExceptionListParams struct {
	UserID    *int64
	EventType string
	// ReasonCode 按异常原因码过滤（如 orphan_reservation_swept 用于孤儿清扫观测视图）；空串不过滤。
	ReasonCode string
	From       *time.Time
	To         *time.Time
	SortField  string
	SortDesc   bool
	Limit      int32
	Offset     int32
}

// LedgerEntry 是一条用户余额变化账本流水（金额为十进制字符串）。
type LedgerEntry struct {
	ID              int64
	UserID          int64
	UserDisplayName string // 列表联表带出；详情嵌入路径可为空。
	UserEmail       string
	RequestRecordID *int64
	EntryType       string
	Amount          string
	Currency        string
	BalanceBefore   string
	BalanceAfter    string
	IdempotencyKey  string
	Reason          string
	CreatedAt       time.Time
}

// BillingException 是结算中的平台核销 / 风险敞口审计事实（金额为十进制字符串）。
type BillingException struct {
	ID              int64
	UserID          int64
	UserDisplayName string // 列表联表带出；详情路径可为空。
	UserEmail       string
	RequestRecordID int64
	RequestID       string // 对外 request_id；列表联表带出，详情路径可能由调用方补齐。
	ReservationID   int64
	EventType       string
	ActualAmount    *string
	CapturedAmount  string
	PlatformAmount  string
	Currency        string
	ReasonCode      string
	Reason          string
	CreatedAt       time.Time
}

// LedgerService 提供账本流水与计费异常只读查询。
type LedgerService struct {
	store LedgerStore
}

// NewLedgerService 创建账本只读查询服务。
func NewLedgerService(store LedgerStore) *LedgerService {
	return &LedgerService{store: store}
}

// ListEntries 按 params 过滤分页倒序列出账本流水，并返回过滤后的总数。
func (s *LedgerService) ListEntries(ctx context.Context, params EntryListParams) ([]LedgerEntry, int64, error) {
	rows, err := s.store.ListLedgerEntriesPage(ctx, sqlc.ListLedgerEntriesPageParams{
		UserID:     int8Narg(params.UserID),
		EntryType:  textNarg(params.EntryType),
		Currency:   textNarg(params.Currency),
		FromTime:   tsNarg(params.From),
		ToTime:     tsNarg(params.To),
		SortField:  textNarg(params.SortField),
		SortDesc:   boolNarg(params.SortDesc),
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list ledger entries")
	}

	total, err := s.store.CountLedgerEntries(ctx, sqlc.CountLedgerEntriesParams{
		UserID:    int8Narg(params.UserID),
		EntryType: textNarg(params.EntryType),
		Currency:  textNarg(params.Currency),
		FromTime:  tsNarg(params.From),
		ToTime:    tsNarg(params.To),
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count ledger entries")
	}

	items := make([]LedgerEntry, 0, len(rows))
	for _, row := range rows {
		items = append(items, toLedgerEntryListRow(row))
	}
	return items, total, nil
}

// ListBillingExceptions 按 params 过滤分页倒序列出计费异常，并返回过滤后的总数。
func (s *LedgerService) ListBillingExceptions(ctx context.Context, params ExceptionListParams) ([]BillingException, int64, error) {
	rows, err := s.store.ListLedgerBillingExceptionsPage(ctx, sqlc.ListLedgerBillingExceptionsPageParams{
		UserID:     int8Narg(params.UserID),
		EventType:  textNarg(params.EventType),
		ReasonCode: textNarg(params.ReasonCode),
		FromTime:   tsNarg(params.From),
		ToTime:     tsNarg(params.To),
		SortField:  textNarg(params.SortField),
		SortDesc:   boolNarg(params.SortDesc),
		PageLimit:  params.Limit,
		PageOffset: params.Offset,
	})
	if err != nil {
		return nil, 0, storeFailed(err, "list billing exceptions")
	}

	total, err := s.store.CountLedgerBillingExceptions(ctx, sqlc.CountLedgerBillingExceptionsParams{
		UserID:     int8Narg(params.UserID),
		EventType:  textNarg(params.EventType),
		ReasonCode: textNarg(params.ReasonCode),
		FromTime:   tsNarg(params.From),
		ToTime:     tsNarg(params.To),
	})
	if err != nil {
		return nil, 0, storeFailed(err, "count billing exceptions")
	}

	items := make([]BillingException, 0, len(rows))
	for _, row := range rows {
		items = append(items, toBillingExceptionListRow(row))
	}
	return items, total, nil
}

func toLedgerEntry(e sqlc.LedgerEntry) LedgerEntry {
	return LedgerEntry{
		ID:              e.ID,
		UserID:          e.UserID,
		RequestRecordID: int8Ptr(e.RequestRecordID),
		EntryType:       e.EntryType,
		Amount:          numericString(e.Amount),
		Currency:        e.Currency,
		BalanceBefore:   numericString(e.BalanceBefore),
		BalanceAfter:    numericString(e.BalanceAfter),
		IdempotencyKey:  e.IdempotencyKey,
		Reason:          e.Reason,
		CreatedAt:       e.CreatedAt.Time,
	}
}

func toLedgerEntryListRow(e sqlc.ListLedgerEntriesPageRow) LedgerEntry {
	return LedgerEntry{
		ID:              e.ID,
		UserID:          e.UserID,
		UserDisplayName: e.UserDisplayName,
		UserEmail:       e.UserEmail,
		RequestRecordID: int8Ptr(e.RequestRecordID),
		EntryType:       e.EntryType,
		Amount:          numericString(e.Amount),
		Currency:        e.Currency,
		BalanceBefore:   numericString(e.BalanceBefore),
		BalanceAfter:    numericString(e.BalanceAfter),
		IdempotencyKey:  e.IdempotencyKey,
		Reason:          e.Reason,
		CreatedAt:       e.CreatedAt.Time,
	}
}

func toBillingException(e sqlc.LedgerBillingException) BillingException {
	return BillingException{
		ID:              e.ID,
		UserID:          e.UserID,
		RequestRecordID: e.RequestRecordID,
		ReservationID:   e.ReservationID,
		EventType:       e.EventType,
		ActualAmount:    numericPtr(e.ActualAmount),
		CapturedAmount:  numericString(e.CapturedAmount),
		PlatformAmount:  numericString(e.PlatformAmount),
		Currency:        e.Currency,
		ReasonCode:      e.ReasonCode,
		Reason:          e.Reason,
		CreatedAt:       e.CreatedAt.Time,
	}
}

func toBillingExceptionListRow(e sqlc.ListLedgerBillingExceptionsPageRow) BillingException {
	return BillingException{
		ID:              e.ID,
		UserID:          e.UserID,
		UserDisplayName: e.UserDisplayName,
		UserEmail:       e.UserEmail,
		RequestRecordID: e.RequestRecordID,
		RequestID:       e.RequestID,
		ReservationID:   e.ReservationID,
		EventType:       e.EventType,
		ActualAmount:    numericPtr(e.ActualAmount),
		CapturedAmount:  numericString(e.CapturedAmount),
		PlatformAmount:  numericString(e.PlatformAmount),
		Currency:        e.Currency,
		ReasonCode:      e.ReasonCode,
		Reason:          e.Reason,
		CreatedAt:       e.CreatedAt.Time,
	}
}
