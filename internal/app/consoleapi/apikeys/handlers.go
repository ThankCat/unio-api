package apikeys

import (
	"net/http"

	"github.com/go-chi/chi/v5"

	consoleauth "github.com/ThankCat/unio-gateway/internal/app/consoleapi/auth"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	consoleapikeys "github.com/ThankCat/unio-gateway/internal/service/console/apikeys"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
)

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	query, err := parseListQuery(r)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	query.params.UserID = principal.UserID
	keys, total, listErr := h.service.List(r.Context(), query.params)
	if listErr != nil {
		h.errorWriter.Write(w, r, listErr)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, listData{
		Items:    toKeyDTOs(keys),
		Page:     query.page,
		PageSize: query.pageSize,
		Total:    total,
	})
}

func (h *handler) summary(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	window, err := parseWindow(r)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	summary, summaryErr := h.service.Summary(r.Context(), principal.UserID, window)
	if summaryErr != nil {
		h.errorWriter.Write(w, r, summaryErr)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, summaryData{
		KeyTotal:     summary.KeyTotal,
		KeyActive:    summary.KeyActive,
		NearLimit:    summary.NearLimit,
		RequestCount: summary.RequestCount,
		ChargeUSD:    summary.ChargeUSD,
	})
}

func (h *handler) get(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	keyID, idErr := parsePathID(r, chi.URLParam(r, "id"))
	if idErr != nil {
		h.errorWriter.Write(w, r, idErr)
		return
	}
	window, err := parseWindow(r)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	detail, getErr := h.service.Get(r.Context(), consoleapikeys.GetParams{
		UserID: principal.UserID,
		KeyID:  keyID,
		Window: window,
	})
	if getErr != nil {
		h.errorWriter.Write(w, r, getErr)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, toDetailDTO(detail))
}

func (h *handler) create(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	var req createRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.errorWriter.Write(w, r, consoleservice.InvalidArgument("body", "The request body is not valid JSON."))
		return
	}
	params, paramErr := req.toParams()
	if paramErr != nil {
		h.errorWriter.Write(w, r, paramErr)
		return
	}
	params.UserID = principal.UserID

	created, createErr := h.service.Create(r.Context(), params)
	if createErr != nil {
		h.errorWriter.Write(w, r, createErr)
		return
	}
	// 明文不落库，这个响应是客户端唯一一次拿到它的机会。
	_ = transport.WriteData(w, http.StatusCreated, createdKeyDTO{
		keyDTO:    toKeyDTO(created.Key),
		Plaintext: created.Plaintext,
	})
}

func (h *handler) update(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	keyID, idErr := parsePathID(r, chi.URLParam(r, "id"))
	if idErr != nil {
		h.errorWriter.Write(w, r, idErr)
		return
	}
	var req updateRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		h.errorWriter.Write(w, r, consoleservice.InvalidArgument("body", "The request body is not valid JSON."))
		return
	}
	params, paramErr := req.toParams()
	if paramErr != nil {
		h.errorWriter.Write(w, r, paramErr)
		return
	}
	params.UserID = principal.UserID
	params.KeyID = keyID

	key, updateErr := h.service.Update(r.Context(), params)
	if updateErr != nil {
		h.errorWriter.Write(w, r, updateErr)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, toKeyDTO(key))
}

func (h *handler) revoke(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	keyID, idErr := parsePathID(r, chi.URLParam(r, "id"))
	if idErr != nil {
		h.errorWriter.Write(w, r, idErr)
		return
	}
	key, revokeErr := h.service.Revoke(r.Context(), principal.UserID, keyID)
	if revokeErr != nil {
		h.errorWriter.Write(w, r, revokeErr)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, toKeyDTO(key))
}

func (h *handler) remove(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	keyID, idErr := parsePathID(r, chi.URLParam(r, "id"))
	if idErr != nil {
		h.errorWriter.Write(w, r, idErr)
		return
	}
	if err := h.service.Delete(r.Context(), principal.UserID, keyID); err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func requirePrincipal(
	w http.ResponseWriter,
	errorWriter transport.ErrorWriter,
	r *http.Request,
) (serviceauth.Principal, bool) {
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
