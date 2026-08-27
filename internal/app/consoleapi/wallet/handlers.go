package wallet

import (
	"net/http"
	"time"

	consoleauth "github.com/ThankCat/unio-gateway/internal/app/consoleapi/auth"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
)

type entryDTO struct {
	ID              int64  `json:"id"`
	EntryType       string `json:"entry_type"`
	Amount          string `json:"amount"`
	Currency        string `json:"currency"`
	BalanceAfter    string `json:"balance_after"`
	RequestRecordID *int64 `json:"request_record_id,omitempty"`
	Reason          string `json:"reason"`
	CreatedAt       string `json:"created_at"`
}

type listData struct {
	Items    []entryDTO `json:"items"`
	Page     int        `json:"page"`
	PageSize int        `json:"page_size"`
	Total    int64      `json:"total"`
}

func (h *handler) transactions(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	query, parseErr := parseListQuery(r)
	if parseErr != nil {
		h.errorWriter.Write(w, r, parseErr)
		return
	}
	query.params.UserID = principal.UserID
	entries, total, listErr := h.service.List(r.Context(), query.params)
	if listErr != nil {
		h.errorWriter.Write(w, r, listErr)
		return
	}
	items := make([]entryDTO, 0, len(entries))
	for _, entry := range entries {
		items = append(items, entryDTO{
			ID:              entry.ID,
			EntryType:       entry.EntryType,
			Amount:          entry.Amount,
			Currency:        entry.Currency,
			BalanceAfter:    entry.BalanceAfter,
			RequestRecordID: entry.RequestRecordID,
			Reason:          entry.Reason,
			CreatedAt:       entry.CreatedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	_ = transport.WriteData(w, http.StatusOK, listData{
		Items:    items,
		Page:     query.page,
		PageSize: query.pageSize,
		Total:    total,
	})
}

func requirePrincipal(w http.ResponseWriter, errorWriter transport.ErrorWriter, r *http.Request) (serviceauth.Principal, bool) {
	principal, ok := consoleauth.PrincipalFromContext(r.Context())
	if ok {
		return principal, true
	}
	errorWriter.Write(w, r, &consoleservice.Error{
		Code:    serviceauth.CodeSessionInvalid,
		Message: "The current session is invalid.",
		Status:  http.StatusUnauthorized,
	})
	return serviceauth.Principal{}, false
}
