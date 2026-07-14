package user

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-api/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-api/internal/service/admin/customerops"
)

// CustomerOpsService 定义客户中心（用户/API Key §3.7）只读运维聚合所需能力。
type CustomerOpsService interface {
	UsersTable(ctx context.Context, p customerops.UsersTableParams) ([]customerops.UserRow, int64, error)
	UserDetail(ctx context.Context, userID int64, from, to time.Time) (customerops.UserDetail, error)
	UserKeys(ctx context.Context, userID int64) ([]customerops.KeyRow, error)
	ApiKeysSummary(ctx context.Context, userID int64) (customerops.ApiKeysSummary, error)
	ApiKeysTable(ctx context.Context, p customerops.ApiKeysTableParams) ([]customerops.ApiKeyRow, int64, error)
}

type customerOpsHandler struct {
	service CustomerOpsService
}

type userOpsRowDTO struct {
	ID                  int64   `json:"id"`
	Email               string  `json:"email"`
	DisplayName         string  `json:"display_name"`
	BalanceUSD          string  `json:"balance_usd"`
	ReservedUSD         string  `json:"reserved_usd"`
	AvailableUSD        string  `json:"available_usd"`
	KeyTotal            int64   `json:"key_total"`
	RequestTotal        int64   `json:"request_total"`
	Succeeded           int64   `json:"succeeded"`
	SuccessRate         float64 `json:"success_rate"`
	ConsumptionUSD      string  `json:"consumption_usd"`
	TotalConsumptionUSD string  `json:"total_consumption_usd"`
	TotalTopupUSD       string  `json:"total_topup_usd"`
	LastUsedAt          *string `json:"last_used_at"`
	CreatedAt           string  `json:"created_at"`
	LowBalance          bool    `json:"low_balance"`
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
	ID              int64   `json:"id"`
	Name            string  `json:"name"`
	KeyPrefix       string  `json:"key_prefix"`
	KeyPlaintext    *string `json:"key_plaintext"`
	UserID          int64   `json:"user_id"`
	Status          string  `json:"status"`
	RouteID         int64   `json:"route_id"`
	RouteName       string  `json:"route_name"`
	RoutePriceRatio string  `json:"route_price_ratio"`
	SpendLimit      *string `json:"spend_limit"`
	SpentTotal      string  `json:"spent_total"`
	RequestTotal    int64   `json:"request_total"`
	Succeeded       int64   `json:"succeeded"`
	SuccessRate     float64 `json:"success_rate"`
	ConsumptionUSD  string  `json:"consumption_usd"`
	LastUsedAt      *string `json:"last_used_at"`
	ExpiresAt       *string `json:"expires_at"`
}

func (h *customerOpsHandler) usersTable(w http.ResponseWriter, r *http.Request) {
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	page := adminhttp.ParsePage(r)
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"email":             {},
		"balance":           {},
		"keys":              {},
		"consumption":       {},
		"total_consumption": {},
		"total_topup":       {},
		"requests":          {},
		"last_used":         {},
		"created_at":        {},
		"display_name":      {},
	}, "email", false)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.UsersTable(r.Context(), customerops.UsersTableParams{
		From:      from,
		To:        to,
		Search:    adminhttp.QueryString(r, "search"),
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]userOpsRowDTO, 0, len(rows))
	for _, u := range rows {
		out = append(out, userOpsRowDTO{
			ID: u.ID, Email: u.Email, DisplayName: u.DisplayName, BalanceUSD: u.BalanceUSD, ReservedUSD: u.ReservedUSD,
			AvailableUSD: u.AvailableUSD, KeyTotal: u.KeyTotal, RequestTotal: u.RequestTotal,
			Succeeded: u.Succeeded, SuccessRate: u.SuccessRate, ConsumptionUSD: u.ConsumptionUSD,
			TotalConsumptionUSD: u.TotalConsumptionUSD, TotalTopupUSD: u.TotalTopupUSD,
			LastUsedAt: adminhttp.RFC3339Ptr(u.LastUsedAt), CreatedAt: adminhttp.RFC3339(u.CreatedAt), LowBalance: u.LowBalance,
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *customerOpsHandler) userDetail(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	d, err := h.service.UserDetail(r.Context(), id, from, to)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, userOpsDetailDTO{
		BalanceUSD: d.BalanceUSD, ReservedUSD: d.ReservedUSD, AvailableUSD: d.AvailableUSD,
		RequestTotal: d.RequestTotal, Succeeded: d.Succeeded, SuccessRate: d.SuccessRate, ConsumptionUSD: d.ConsumptionUSD,
	})
}

func (h *customerOpsHandler) apiKeysSummary(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	s, err := h.service.ApiKeysSummary(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, apiKeysOpsSummaryDTO{KeyTotal: s.KeyTotal, KeyEnabled: s.KeyEnabled, SpendCapped: s.SpendCapped})
}

func (h *customerOpsHandler) apiKeysTable(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	from, to, _, err := adminhttp.RangeWindow(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	page := adminhttp.ParsePage(r)
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"name":        {},
		"requests":    {},
		"spent":       {},
		"consumption": {},
		"last_used":   {},
	}, "requests", true)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	field, desc := sort.SQLParams()
	rows, total, err := h.service.ApiKeysTable(r.Context(), customerops.ApiKeysTableParams{
		UserID:    id,
		From:      from,
		To:        to,
		Search:    adminhttp.QueryString(r, "search"),
		SortField: field,
		SortDesc:  desc,
		Limit:     page.Limit(),
		Offset:    page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]apiKeyOpsRowDTO, 0, len(rows))
	for _, k := range rows {
		out = append(out, apiKeyOpsRowDTO{
			ID: k.ID, Name: k.Name, KeyPrefix: k.KeyPrefix, KeyPlaintext: k.KeyPlaintext, UserID: k.UserID, Status: k.Status,
			RouteID: int64Value(k.RouteID), RouteName: k.RouteName, RoutePriceRatio: k.RoutePriceRatio, SpendLimit: k.SpendLimit, SpentTotal: k.SpentTotal,
			RequestTotal: k.RequestTotal, Succeeded: k.Succeeded, SuccessRate: k.SuccessRate,
			ConsumptionUSD: k.ConsumptionUSD, LastUsedAt: adminhttp.RFC3339Ptr(k.LastUsedAt), ExpiresAt: adminhttp.RFC3339Ptr(k.ExpiresAt),
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}
