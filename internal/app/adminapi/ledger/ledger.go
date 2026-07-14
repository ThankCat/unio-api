package ledger

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

// LedgerQueryService 定义 adminapi 查询账本流水与计费异常所需的最小能力（M6 只读查询台）。
type LedgerQueryService interface {
	ListEntries(ctx context.Context, params query.EntryListParams) ([]query.LedgerEntry, int64, error)
	ListBillingExceptions(ctx context.Context, params query.ExceptionListParams) ([]query.BillingException, int64, error)
}

// LedgerEntryDTO 是账本流水响应体；金额为十进制字符串。
type LedgerEntryDTO struct {
	ID              int64  `json:"id"`
	UserID          int64  `json:"user_id"`
	RequestRecordID *int64 `json:"request_record_id"`
	EntryType       string `json:"entry_type"`
	Amount          string `json:"amount"`
	Currency        string `json:"currency"`
	BalanceBefore   string `json:"balance_before"`
	BalanceAfter    string `json:"balance_after"`
	IdempotencyKey  string `json:"idempotency_key"`
	Reason          string `json:"reason"`
	CreatedAt       string `json:"created_at"`
}

// BillingExceptionDTO 是计费异常响应体；金额为十进制字符串。
type BillingExceptionDTO struct {
	ID              int64   `json:"id"`
	UserID          int64   `json:"user_id"`
	RequestRecordID int64   `json:"request_record_id"`
	ReservationID   int64   `json:"reservation_id"`
	EventType       string  `json:"event_type"`
	ActualAmount    *string `json:"actual_amount"`
	CapturedAmount  string  `json:"captured_amount"`
	PlatformAmount  string  `json:"platform_amount"`
	Currency        string  `json:"currency"`
	ReasonCode      string  `json:"reason_code"`
	Reason          string  `json:"reason"`
	CreatedAt       string  `json:"created_at"`
}

type ledgerHandler struct {
	service LedgerQueryService
}

func (h *ledgerHandler) listEntries(w http.ResponseWriter, r *http.Request) {
	userID, err := adminhttp.OptionalInt64Query(r, "user_id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, err := adminhttp.OptionalTimeQuery(r, "from")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	to, err := adminhttp.OptionalTimeQuery(r, "to")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	page := adminhttp.ParsePage(r)
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"created_at": {},
		"user_id":    {},
		"amount":     {},
		"entry_type": {},
	}, "created_at", true)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	items, total, err := h.service.ListEntries(r.Context(), query.EntryListParams{
		UserID:    userID,
		EntryType: adminhttp.QueryString(r, "entry_type"),
		Currency:  adminhttp.QueryString(r, "currency"),
		From:      from,
		To:        to,
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]LedgerEntryDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, ToLedgerEntryDTO(item))
	}
	adminhttp.WriteList(w, http.StatusOK, dtos, page, total)
}

func (h *ledgerHandler) listBillingExceptions(w http.ResponseWriter, r *http.Request) {
	userID, err := adminhttp.OptionalInt64Query(r, "user_id")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, err := adminhttp.OptionalTimeQuery(r, "from")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	to, err := adminhttp.OptionalTimeQuery(r, "to")
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	page := adminhttp.ParsePage(r)
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"created_at": {},
		"user_id":    {},
		"event_type": {},
	}, "created_at", true)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	items, total, err := h.service.ListBillingExceptions(r.Context(), query.ExceptionListParams{
		UserID:     userID,
		EventType:  adminhttp.QueryString(r, "event_type"),
		ReasonCode: adminhttp.QueryString(r, "reason_code"),
		From:       from,
		To:         to,
		SortField:  field,
		SortDesc:   desc,
		Limit:      page.Limit(),
		Offset:     page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	dtos := make([]BillingExceptionDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, ToBillingExceptionDTO(item))
	}
	adminhttp.WriteList(w, http.StatusOK, dtos, page, total)
}

func ToLedgerEntryDTO(e query.LedgerEntry) LedgerEntryDTO {
	return LedgerEntryDTO{
		ID:              e.ID,
		UserID:          e.UserID,
		RequestRecordID: e.RequestRecordID,
		EntryType:       e.EntryType,
		Amount:          e.Amount,
		Currency:        e.Currency,
		BalanceBefore:   e.BalanceBefore,
		BalanceAfter:    e.BalanceAfter,
		IdempotencyKey:  e.IdempotencyKey,
		Reason:          e.Reason,
		CreatedAt:       adminhttp.RFC3339(e.CreatedAt),
	}
}

func ToBillingExceptionDTO(e query.BillingException) BillingExceptionDTO {
	return BillingExceptionDTO{
		ID:              e.ID,
		UserID:          e.UserID,
		RequestRecordID: e.RequestRecordID,
		ReservationID:   e.ReservationID,
		EventType:       e.EventType,
		ActualAmount:    e.ActualAmount,
		CapturedAmount:  e.CapturedAmount,
		PlatformAmount:  e.PlatformAmount,
		Currency:        e.Currency,
		ReasonCode:      e.ReasonCode,
		Reason:          e.Reason,
		CreatedAt:       adminhttp.RFC3339(e.CreatedAt),
	}
}
