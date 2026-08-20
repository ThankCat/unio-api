package requests

import (
	"net/http"

	consoleauth "github.com/ThankCat/unio-gateway/internal/app/consoleapi/auth"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
	consolerequests "github.com/ThankCat/unio-gateway/internal/service/console/requests"
)

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	query, err := parseListQuery(r)
	if err != nil {
		h.errorWriter.Write(w, err)
		return
	}
	query.params.UserID = principal.UserID
	items, total, listErr := h.service.List(r.Context(), query.params)
	if listErr != nil {
		h.errorWriter.Write(w, listErr)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, listData{
		Items:    toItemDTOs(items),
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
	// from/to 可空；缺省时统计账户全部实际扣费历史。
	from, fromErr := parseTimeQuery(r, "from")
	if fromErr != nil {
		h.errorWriter.Write(w, fromErr)
		return
	}
	to, toErr := parseTimeQuery(r, "to")
	if toErr != nil {
		h.errorWriter.Write(w, toErr)
		return
	}
	summary, err := h.service.Summary(r.Context(), consolerequests.SummaryParams{
		UserID: principal.UserID,
		From:   from,
		To:     to,
	})
	if err != nil {
		h.errorWriter.Write(w, err)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, summaryData{
		RequestCount:     summary.RequestCount,
		TokenCount:       summary.TokenCount,
		InputTokenCount:  summary.InputTokenCount,
		OutputTokenCount: summary.OutputTokenCount,
		ChargeUSD:        summary.ChargeUSD,
		AverageLatencyMs: summary.AverageLatencyMs,
	})
}

func (h *handler) filters(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	filters, err := h.service.Filters(r.Context(), principal.UserID)
	if err != nil {
		h.errorWriter.Write(w, err)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, filtersData{
		Routes:    emptyOptions(filters.Routes),
		APIKeys:   emptyOptions(filters.APIKeys),
		Endpoints: emptyStrings(filters.Endpoints),
	})
}

func requirePrincipal(w http.ResponseWriter, errorWriter transport.ErrorWriter, r *http.Request) (serviceauth.Principal, bool) {
	principal, ok := consoleauth.PrincipalFromContext(r.Context())
	if ok {
		return principal, true
	}
	errorWriter.Write(w, &consoleservice.Error{
		Code:    serviceauth.CodeSessionInvalid,
		Message: "The current session is invalid.",
		Status:  http.StatusUnauthorized,
	})
	return serviceauth.Principal{}, false
}

func emptyOptions(values []consolerequests.FilterOption) []consolerequests.FilterOption {
	if values == nil {
		return []consolerequests.FilterOption{}
	}
	return values
}

func emptyStrings(values []string) []string {
	if values == nil {
		return []string{}
	}
	return values
}
