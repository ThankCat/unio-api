package cdkey

import (
	"net/http"
	"time"

	consoleauth "github.com/ThankCat/unio-gateway/internal/app/consoleapi/auth"
	consolemiddleware "github.com/ThankCat/unio-gateway/internal/app/consoleapi/middleware"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
	consolecdkey "github.com/ThankCat/unio-gateway/internal/service/console/cdkey"
)

type redeemRequest struct {
	Code string `json:"code"`
}

type redemptionDTO struct {
	RedemptionID  int64  `json:"redemption_id"`
	Amount        string `json:"amount"`
	Currency      string `json:"currency"`
	LedgerEntryID int64  `json:"ledger_entry_id"`
	BalanceAfter  string `json:"balance_after"`
	RedeemedAt    string `json:"redeemed_at"`
}

func (h *handler) redeem(w http.ResponseWriter, r *http.Request) {
	principal, ok := consoleauth.PrincipalFromContext(r.Context())
	if !ok {
		h.errorWriter.Write(w, r, &consoleservice.Error{Code: serviceauth.CodeSessionInvalid, Message: "The current session is invalid.", Status: http.StatusUnauthorized})
		return
	}
	var body redeemRequest
	if err := transport.DecodeJSON(w, r, &body); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	var redemption consolecdkey.Redemption
	var serviceErr *consoleservice.Error
	if ipService, ok := h.service.(IPService); ok {
		redemption, serviceErr = ipService.RedeemWithIP(r.Context(), principal.UserID, body.Code, consolemiddleware.ClientIPFromContext(r.Context()))
	} else {
		redemption, serviceErr = h.service.Redeem(r.Context(), principal.UserID, body.Code)
	}
	if serviceErr != nil {
		h.errorWriter.Write(w, r, serviceErr)
		return
	}
	transport.WriteData(w, http.StatusOK, redemptionDTO{RedemptionID: redemption.ID, Amount: redemption.Amount, Currency: redemption.Currency, LedgerEntryID: redemption.LedgerEntryID, BalanceAfter: redemption.BalanceAfter, RedeemedAt: redemption.RedeemedAt.UTC().Format(time.RFC3339Nano)})
}
