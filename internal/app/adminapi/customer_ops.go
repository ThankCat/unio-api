package adminapi

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/service/admin/customerops"
)

// CustomerOpsService 定义客户中心（用户/API Key §3.7）只读运维聚合所需能力。
type CustomerOpsService interface {
	UsersSummary(ctx context.Context, from, to time.Time) (customerops.UsersSummary, error)
	UsersTable(ctx context.Context, p customerops.UsersTableParams) ([]customerops.UserRow, int64, error)
	UserDetail(ctx context.Context, userID int64, from, to time.Time) (customerops.UserDetail, error)
	UserKeys(ctx context.Context, userID int64) ([]customerops.KeyRow, error)
	ApiKeysSummary(ctx context.Context, userID int64) (customerops.ApiKeysSummary, error)
	ApiKeysTable(ctx context.Context, p customerops.ApiKeysTableParams) ([]customerops.ApiKeyRow, int64, error)
}

type customerOpsHandler struct {
	service CustomerOpsService
}

type usersOpsSummaryDTO struct {
	UserTotal       int64   `json:"user_total"`
	BalanceUSD      string  `json:"balance_usd"`
	ReservedUSD     string  `json:"reserved_usd"`
	AvailableUSD    string  `json:"available_usd"`
	LowBalanceTotal int64   `json:"low_balance_total"`
	RequestTotal    int64   `json:"request_total"`
	Succeeded       int64   `json:"succeeded"`
	SuccessRate     float64 `json:"success_rate"`
	ConsumptionUSD  string  `json:"consumption_usd"`
}

type userOpsRowDTO struct {
	ID             int64   `json:"id"`
	Email          string  `json:"email"`
	DisplayName    string  `json:"display_name"`
	BalanceUSD     string  `json:"balance_usd"`
	ReservedUSD    string  `json:"reserved_usd"`
	AvailableUSD   string  `json:"available_usd"`
	KeyTotal       int64   `json:"key_total"`
	RequestTotal   int64   `json:"request_total"`
	Succeeded      int64   `json:"succeeded"`
	SuccessRate    float64 `json:"success_rate"`
	ConsumptionUSD string  `json:"consumption_usd"`
	LastUsedAt     *string `json:"last_used_at"`
	LowBalance     bool    `json:"low_balance"`
}

type userOpsDetailDTO struct {
	BalanceUSD     string  `json:"balance_usd"`
	ReservedUSD    string  `json:"reserved_usd"`
	AvailableUSD   string  `json:"available_usd"`
	RequestTotal   int64   `json:"request_total"`
	Succeeded      int64   `json:"succeeded"`
	SuccessRate    float64 `json:"success_rate"`
	ConsumptionUSD string  `json:"consumption_usd"`
}

type customerKeyDTO struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Status     string  `json:"status"`
	SpendLimit *string `json:"spend_limit"`
	SpentTotal string  `json:"spent_total"`
	LastUsedAt *string `json:"last_used_at"`
}

type apiKeysOpsSummaryDTO struct {
	KeyTotal    int64 `json:"key_total"`
	KeyEnabled  int64 `json:"key_enabled"`
	SpendCapped int64 `json:"spend_capped"`
}

type apiKeyOpsRowDTO struct {
	ID             int64   `json:"id"`
	Name           string  `json:"name"`
	KeyPrefix      string  `json:"key_prefix"`
	KeyPlaintext   *string `json:"key_plaintext"`
	UserID         int64   `json:"user_id"`
	Status         string  `json:"status"`
	RouteID        *int64  `json:"route_id"`
	RouteName      string  `json:"route_name"`
	SpendLimit     *string `json:"spend_limit"`
	SpentTotal     string  `json:"spent_total"`
	RequestTotal   int64   `json:"request_total"`
	Succeeded      int64   `json:"succeeded"`
	SuccessRate    float64 `json:"success_rate"`
	ConsumptionUSD string  `json:"consumption_usd"`
	LastUsedAt     *string `json:"last_used_at"`
	ExpiresAt      *string `json:"expires_at"`
}

func (h *customerOpsHandler) usersSummary(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	s, err := h.service.UsersSummary(r.Context(), from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, usersOpsSummaryDTO{
		UserTotal: s.UserTotal, BalanceUSD: s.BalanceUSD, ReservedUSD: s.ReservedUSD, AvailableUSD: s.AvailableUSD,
		LowBalanceTotal: s.LowBalanceTotal, RequestTotal: s.RequestTotal, Succeeded: s.Succeeded,
		SuccessRate: s.SuccessRate, ConsumptionUSD: s.ConsumptionUSD,
	})
}

func (h *customerOpsHandler) usersTable(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	page := parsePage(r)
	sort, err := parseListSort(r, map[string]struct{}{
		"email":       {},
		"balance":     {},
		"consumption": {},
		"requests":    {},
		"last_used":   {},
	}, "consumption", true)
	if err != nil {
		writeSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.UsersTable(r.Context(), customerops.UsersTableParams{
		From:      from,
		To:        to,
		Search:    queryString(r, "search"),
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]userOpsRowDTO, 0, len(rows))
	for _, u := range rows {
		out = append(out, userOpsRowDTO{
			ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, BalanceUSD: u.BalanceUSD, ReservedUSD: u.ReservedUSD,
			AvailableUSD: u.AvailableUSD, KeyTotal: u.KeyTotal, RequestTotal: u.RequestTotal,
			Succeeded: u.Succeeded, SuccessRate: u.SuccessRate, ConsumptionUSD: u.ConsumptionUSD,
			LastUsedAt: rfc3339Ptr(u.LastUsedAt), LowBalance: u.LowBalance,
		})
	}
	writeList(w, http.StatusOK, out, page, total)
}

func (h *customerOpsHandler) userDetail(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	from, to, _, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	d, err := h.service.UserDetail(r.Context(), id, from, to)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, userOpsDetailDTO{
		BalanceUSD: d.BalanceUSD, ReservedUSD: d.ReservedUSD, AvailableUSD: d.AvailableUSD,
		RequestTotal: d.RequestTotal, Succeeded: d.Succeeded, SuccessRate: d.SuccessRate, ConsumptionUSD: d.ConsumptionUSD,
	})
}

func (h *customerOpsHandler) userKeys(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	rows, err := h.service.UserKeys(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, toCustomerKeyDTOs(rows))
}

func toCustomerKeyDTOs(rows []customerops.KeyRow) []customerKeyDTO {
	out := make([]customerKeyDTO, 0, len(rows))
	for _, k := range rows {
		out = append(out, customerKeyDTO{
			ID: k.ID, Name: k.Name, Status: k.Status,
			SpendLimit: k.SpendLimit, SpentTotal: k.SpentTotal, LastUsedAt: rfc3339Ptr(k.LastUsedAt),
		})
	}
	return out
}

func (h *customerOpsHandler) apiKeysSummary(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	s, err := h.service.ApiKeysSummary(r.Context(), id)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	writeData(w, http.StatusOK, apiKeysOpsSummaryDTO{KeyTotal: s.KeyTotal, KeyEnabled: s.KeyEnabled, SpendCapped: s.SpendCapped})
}

func (h *customerOpsHandler) apiKeysTable(w http.ResponseWriter, r *http.Request) {
	id, err := pathID(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	from, to, _, err := rangeWindow(r)
	if err != nil {
		writeServiceError(w, err)
		return
	}
	page := parsePage(r)
	sort, err := parseListSort(r, map[string]struct{}{
		"name":        {},
		"requests":    {},
		"spent":       {},
		"consumption": {},
		"last_used":   {},
	}, "requests", true)
	if err != nil {
		writeSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.ApiKeysTable(r.Context(), customerops.ApiKeysTableParams{
		UserID:    id,
		From:      from,
		To:        to,
		Search:    queryString(r, "search"),
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		writeServiceError(w, err)
		return
	}
	out := make([]apiKeyOpsRowDTO, 0, len(rows))
	for _, k := range rows {
		out = append(out, apiKeyOpsRowDTO{
			ID: k.ID, Name: k.Name, KeyPrefix: k.KeyPrefix, KeyPlaintext: k.KeyPlaintext, UserID: k.UserID, Status: k.Status,
			RouteID: k.RouteID, RouteName: k.RouteName, SpendLimit: k.SpendLimit, SpentTotal: k.SpentTotal,
			RequestTotal: k.RequestTotal, Succeeded: k.Succeeded, SuccessRate: k.SuccessRate,
			ConsumptionUSD: k.ConsumptionUSD, LastUsedAt: rfc3339Ptr(k.LastUsedAt), ExpiresAt: rfc3339Ptr(k.ExpiresAt),
		})
	}
	writeList(w, http.StatusOK, out, page, total)
}
