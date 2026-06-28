package adminapi

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-api/internal/service/admin/query"
)

// LedgerQueryService 定义 adminapi 查询账本流水与计费异常所需的最小能力（M6 只读查询台）。
type LedgerQueryService interface {
	ListEntries(ctx context.Context, params query.EntryListParams) ([]query.LedgerEntry, int64, error)
	ListBillingExceptions(ctx context.Context, params query.ExceptionListParams) ([]query.BillingException, int64, error)
}

// ledgerEntryDTO 是账本流水响应体；金额为十进制字符串。
type ledgerEntryDTO struct {
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

// billingExceptionDTO 是计费异常响应体；金额为十进制字符串。
type billingExceptionDTO struct {
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
	userID, err := optionalInt64Query(r, "user_id")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	from, err := optionalTimeQuery(r, "from")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	to, err := optionalTimeQuery(r, "to")
	if err != nil {
		writeServiceError(w, err)
		return
	}

	page := parsePage(r)
	sort, err := parseListSort(r, map[string]struct{}{
		"created_at": {},
		"user_id":    {},
		"amount":     {},
		"entry_type": {},
	}, "created_at", true)
	if err != nil {
		writeSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	items, total, err := h.service.ListEntries(r.Context(), query.EntryListParams{
		UserID:    userID,
		EntryType: queryString(r, "entry_type"),
		Currency:  queryString(r, "currency"),
		From:      from,
		To:        to,
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]ledgerEntryDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toLedgerEntryDTO(item))
	}
	writeList(w, http.StatusOK, dtos, page, total)
}

func (h *ledgerHandler) listBillingExceptions(w http.ResponseWriter, r *http.Request) {
	userID, err := optionalInt64Query(r, "user_id")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	from, err := optionalTimeQuery(r, "from")
	if err != nil {
		writeServiceError(w, err)
		return
	}
	to, err := optionalTimeQuery(r, "to")
	if err != nil {
		writeServiceError(w, err)
		return
	}

	page := parsePage(r)
	sort, err := parseListSort(r, map[string]struct{}{
		"created_at": {},
		"user_id":    {},
		"event_type": {},
	}, "created_at", true)
	if err != nil {
		writeSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	items, total, err := h.service.ListBillingExceptions(r.Context(), query.ExceptionListParams{
		UserID:     userID,
		EventType:  queryString(r, "event_type"),
		ReasonCode: queryString(r, "reason_code"),
		From:       from,
		To:         to,
		SortField:  field,
		SortDesc:   desc,
		Limit:      page.Limit(),
		Offset:     page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}

	dtos := make([]billingExceptionDTO, 0, len(items))
	for _, item := range items {
		dtos = append(dtos, toBillingExceptionDTO(item))
	}
	writeList(w, http.StatusOK, dtos, page, total)
}

func toLedgerEntryDTO(e query.LedgerEntry) ledgerEntryDTO {
	return ledgerEntryDTO{
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
		CreatedAt:       rfc3339(e.CreatedAt),
	}
}

func toBillingExceptionDTO(e query.BillingException) billingExceptionDTO {
	return billingExceptionDTO{
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
		CreatedAt:       rfc3339(e.CreatedAt),
	}
}
