package usage

import (
	"net/http"

	consoleauth "github.com/ThankCat/unio-gateway/internal/app/consoleapi/auth"
	"github.com/ThankCat/unio-gateway/internal/app/consoleapi/transport"
	consoleservice "github.com/ThankCat/unio-gateway/internal/service/console"
	serviceauth "github.com/ThankCat/unio-gateway/internal/service/console/auth"
)

func (h *handler) overview(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	params, parseErr := parseOverviewQuery(r)
	if parseErr != nil {
		h.errorWriter.Write(w, r, parseErr)
		return
	}
	params.UserID = principal.UserID
	overview, err := h.service.Overview(r.Context(), params)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, overviewData{
		Bucket:         overview.Bucket,
		From:           formatTime(overview.From),
		To:             formatTime(overview.To),
		PreviousFrom:   formatTime(overview.PreviousFrom),
		PreviousTo:     formatTime(overview.PreviousTo),
		Current:        toWindowDTO(overview.Current),
		Previous:       toWindowDTO(overview.Previous),
		Series:         toPointDTOs(overview.Series),
		PreviousSeries: toPointDTOs(overview.PreviousSeries),
	})
}

func (h *handler) trend(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	params, parseErr := parseTrendQuery(r)
	if parseErr != nil {
		h.errorWriter.Write(w, r, parseErr)
		return
	}
	params.UserID = principal.UserID
	trend, err := h.service.Trend(r.Context(), params)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, toTrendData(trend))
}

func (h *handler) groups(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	params, parseErr := parseGroupsQuery(r)
	if parseErr != nil {
		h.errorWriter.Write(w, r, parseErr)
		return
	}
	params.UserID = principal.UserID
	items, err := h.service.Groups(r.Context(), params)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, groupsData{
		By:    params.By,
		Items: toGroupItemDTOs(items),
	})
}

func (h *handler) filters(w http.ResponseWriter, r *http.Request) {
	principal, ok := requirePrincipal(w, h.errorWriter, r)
	if !ok {
		return
	}
	filters, err := h.service.Filters(r.Context(), principal.UserID)
	if err != nil {
		h.errorWriter.Write(w, r, err)
		return
	}
	_ = transport.WriteData(w, http.StatusOK, filtersData{
		APIKeys: filters.APIKeys,
		Models:  filters.Models,
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
