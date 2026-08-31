package cdkey

import (
	"encoding/csv"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	admincdkey "github.com/ThankCat/unio-gateway/internal/service/admin/cdkey"
)

type cdkeyDTO struct {
	ID                    int64   `json:"id"`
	BatchID               string  `json:"batch_id"`
	MaskedCode            string  `json:"masked_code"`
	CodePrefix            string  `json:"code_prefix"`
	CodeSuffix            string  `json:"code_suffix"`
	Amount                string  `json:"amount"`
	Currency              string  `json:"currency"`
	Status                string  `json:"status"`
	CreatedAt             string  `json:"created_at"`
	RedeemedAt            *string `json:"redeemed_at,omitempty"`
	RevokedAt             *string `json:"revoked_at,omitempty"`
	RedemptionID          *int64  `json:"redemption_id,omitempty"`
	RedemptionUserID      *int64  `json:"redemption_user_id,omitempty"`
	RedemptionUserEmail   *string `json:"redemption_user_email,omitempty"`
	RedemptionDisplayName *string `json:"redemption_user_display_name,omitempty"`
	RedemptionLedgerID    *int64  `json:"redemption_ledger_entry_id,omitempty"`
	RedemptionAt          *string `json:"redemption_redeemed_at,omitempty"`
}

type listQuery struct {
	admincdkey.ListParams
	Page     int
	PageSize int
}

const cdkeyInventoryMaxPageSize = 500

type generateRequest struct {
	Items []generateItemRequest `json:"items"`
}

type generateItemRequest struct {
	Amount   string `json:"amount"`
	Quantity int    `json:"quantity"`
}

type bulkRequest struct {
	Scope      string       `json:"scope"`
	IDs        []int64      `json:"ids"`
	Filter     exportFilter `json:"filter"`
	ExcludeIDs []int64      `json:"exclude_ids"`
}

type exportRequest struct {
	Statuses []string `json:"statuses"`
	Scope    string   `json:"scope"`
	IDs      []int64  `json:"ids"`
}

type exportFilter struct {
	// Status is the single-value filter used by the list UI. Statuses is kept
	// for export callers that explicitly select more than one state.
	Status   string   `json:"status"`
	Statuses []string `json:"statuses"`
	Amount   string   `json:"amount"`
	BatchID  string   `json:"batch_id"`
	Search   string   `json:"search"`
	From     string   `json:"from"`
	To       string   `json:"to"`
}

type summaryDTO struct {
	Value                   summaryTotalsDTO           `json:"value"`
	Quantity                summaryTotalsDTO           `json:"quantity"`
	RedemptionRate          float64                    `json:"redemption_rate"`
	BatchCount              int64                      `json:"batch_count"`
	BatchesWithUnused       int64                      `json:"batches_with_unused"`
	FullyRedeemedBatchCount int64                      `json:"fully_redeemed_batch_count"`
	BatchByAmount           map[string]batchSummaryDTO `json:"batch_by_amount"`
	BatchSummary            batchSummaryCardDTO        `json:"batch_summary"`
	RevokedCount            int64                      `json:"revoked_count"`
}

type batchSummaryDTO struct {
	BatchCount              int64 `json:"batch_count"`
	BatchesWithUnused       int64 `json:"batches_with_unused"`
	FullyRedeemedBatchCount int64 `json:"fully_redeemed_batch_count"`
}

// batchSummaryCardDTO is the compact shape consumed by the Admin metric
// tooltip. The flat fields above remain for clients that already parse them.
type batchSummaryCardDTO struct {
	Total         int64                                `json:"total"`
	WithUnused    int64                                `json:"with_unused"`
	FullyRedeemed int64                                `json:"fully_redeemed"`
	ByAmount      map[string]batchSummaryCardAmountDTO `json:"by_amount"`
}

type batchSummaryCardAmountDTO struct {
	Total         int64 `json:"total"`
	WithUnused    int64 `json:"with_unused"`
	FullyRedeemed int64 `json:"fully_redeemed"`
}

type summaryTotalsDTO struct {
	Total    string                      `json:"total"`
	Redeemed string                      `json:"redeemed"`
	Unused   string                      `json:"unused"`
	ByAmount map[string]summaryAmountDTO `json:"by_amount"`
}

type summaryAmountDTO struct {
	Redeemed      string `json:"redeemed"`
	Unused        string `json:"unused"`
	RedeemedCount int64  `json:"redeemed_count"`
	UnusedCount   int64  `json:"unused_count"`
}

func (h *handler) list(w http.ResponseWriter, r *http.Request) {
	query, err := parseListQuery(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.List(r.Context(), query.ListParams)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	items := make([]cdkeyDTO, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, cdkeyDTOFrom(item))
	}
	adminhttp.WriteList(w, http.StatusOK, items, adminhttp.PageParams{Page: query.Page, PageSize: query.PageSize}, result.Total)
}

func (h *handler) summary(w http.ResponseWriter, r *http.Request) {
	query, err := parseListQuery(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.Summary(r.Context(), query.ListParams)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, summaryDTOFrom(result))
}

func (h *handler) redemptions(w http.ResponseWriter, r *http.Request) {
	query, err := parseRedemptionsQuery(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.ListRedemptions(r.Context(), query.ListParams)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	items := make([]map[string]any, 0, len(result.Items))
	for _, item := range result.Items {
		items = append(items, map[string]any{
			"id": item.ID, "cdkey_id": item.CDKeyID, "batch_id": item.BatchID,
			"masked_code": item.MaskedCode, "user_id": item.UserID, "user_display_name": item.UserDisplayName, "user_email": item.UserEmail,
			"amount": item.Amount, "currency": item.Currency, "ledger_entry_id": item.LedgerEntryID,
			"redeemed_at": item.RedeemedAt.UTC().Format(time.RFC3339Nano),
		})
	}
	adminhttp.WriteList(w, http.StatusOK, items, adminhttp.PageParams{Page: query.Page, PageSize: query.PageSize}, result.Total)
}

// parseRedemptionsQuery keeps the redemption table's sort contract separate
// from the CDKEY inventory table. The shared list hook sends -redeemed_at on
// its first request, so accepting only inventory fields here would make the
// redemption tab fail before it can render any rows.
func parseRedemptionsQuery(r *http.Request) (listQuery, error) {
	page := adminhttp.ParsePage(r)
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"redeemed_at": {},
		"amount":      {},
		"user_id":     {},
	}, "redeemed_at", true)
	if err != nil {
		return listQuery{}, err
	}
	from, err := adminhttp.OptionalTimeQuery(r, "from")
	if err != nil {
		return listQuery{}, err
	}
	to, err := adminhttp.OptionalTimeQuery(r, "to")
	if err != nil {
		return listQuery{}, err
	}
	field, desc := sort.SQLParams()
	params := admincdkey.ListParams{
		Amount: adminhttp.QueryString(r, "amount"),
		Search: adminhttp.QueryString(r, "search"),
		Sort:   field,
		Desc:   desc,
		Limit:  page.Limit(),
		Offset: page.Offset(),
	}
	if from != nil {
		params.From = *from
	}
	if to != nil {
		params.To = *to
	}
	return listQuery{ListParams: params, Page: page.Page, PageSize: page.PageSize}, nil
}

func (h *handler) generate(w http.ResponseWriter, r *http.Request) {
	var body generateRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	items := make([]admincdkey.GenerateItem, 0, len(body.Items))
	for _, item := range body.Items {
		items = append(items, admincdkey.GenerateItem{Amount: item.Amount, Quantity: item.Quantity})
	}
	result, err := h.service.Generate(r.Context(), admincdkey.GenerateParams{Items: items})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, map[string]any{
		"batch_id": result.BatchID, "currency": result.Currency,
		"total_quantity": result.TotalQuantity, "total_value": result.TotalValue,
		"lines": result.Lines, "masked_codes": result.MaskedCodes,
	})
}

func (h *handler) revoke(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Revoke(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, map[string]any{"id": id, "status": "revoked"})
}

func (h *handler) delete(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if err := h.service.Delete(r.Context(), id); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *handler) bulkRevoke(w http.ResponseWriter, r *http.Request) {
	var body bulkRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	selection, err := selectionFromRequest(body)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.BulkRevokeSelection(r.Context(), selection)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, result)
}

func (h *handler) bulkDelete(w http.ResponseWriter, r *http.Request) {
	var body bulkRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	selection, err := selectionFromRequest(body)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.BulkDeleteSelection(r.Context(), selection)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, result)
}

func (h *handler) export(w http.ResponseWriter, r *http.Request) {
	var body exportRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	statuses := body.Statuses
	rows, err := h.service.Export(r.Context(), admincdkey.ExportParams{Statuses: statuses, Scope: body.Scope, IDs: body.IDs})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	if h.logger != nil {
		h.logger.Info(
			"cdkey export",
			zap.String("event", "cdkey_export"),
			zap.String("actor", adminhttp.AdminActor(r)),
			zap.String("scope", strings.TrimSpace(body.Scope)),
			zap.Strings("statuses", statuses),
			zap.Int("row_count", len(rows)),
			zap.String("request_id", httpx.RequestID(r.Context())),
		)
	}
	w.Header().Set("Content-Type", "text/csv; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store, no-cache, must-revalidate, private")
	w.Header().Set("Pragma", "no-cache")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Content-Disposition", `attachment; filename="cdkeys.csv"`)
	w.WriteHeader(http.StatusOK)
	writer := csv.NewWriter(w)
	_ = writer.Write([]string{"id", "batch_id", "cdkey", "amount", "currency", "status", "created_at", "redeemed_at", "revoked_at", "redemption_user_id", "redemption_user_email", "ledger_entry_id", "redemption_at"})
	for _, row := range rows {
		redeemedAt, revokedAt, redemptionAt := "", "", ""
		if row.RedeemedAt != nil {
			redeemedAt = row.RedeemedAt.UTC().Format(time.RFC3339Nano)
		}
		if row.RevokedAt != nil {
			revokedAt = row.RevokedAt.UTC().Format(time.RFC3339Nano)
		}
		if row.RedemptionAt != nil {
			redemptionAt = row.RedemptionAt.UTC().Format(time.RFC3339Nano)
		}
		userID, ledgerID, userEmail := "", "", ""
		if row.RedemptionUserID != nil {
			userID = strconv.FormatInt(*row.RedemptionUserID, 10)
		}
		if row.RedemptionLedgerID != nil {
			ledgerID = strconv.FormatInt(*row.RedemptionLedgerID, 10)
		}
		if row.RedemptionUserEmail != nil {
			userEmail = *row.RedemptionUserEmail
		}
		_ = writer.Write([]string{strconv.FormatInt(row.ID, 10), row.BatchID, row.CodePlaintext, row.Amount, row.Currency, row.Status, row.CreatedAt.UTC().Format(time.RFC3339Nano), redeemedAt, revokedAt, userID, userEmail, ledgerID, redemptionAt})
	}
	writer.Flush()
}

func parseListQuery(r *http.Request) (listQuery, error) {
	page := adminhttp.ParsePageWithMax(r, cdkeyInventoryMaxPageSize)
	sort, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"created_at": {},
		"amount":     {},
		"status":     {},
	}, "created_at", true)
	if err != nil {
		return listQuery{}, err
	}
	from, err := adminhttp.OptionalTimeQuery(r, "from")
	if err != nil {
		return listQuery{}, err
	}
	to, err := adminhttp.OptionalTimeQuery(r, "to")
	if err != nil {
		return listQuery{}, err
	}
	amount := adminhttp.QueryString(r, "amount")
	statuses := queryValues(r, "status")
	for _, status := range statuses {
		if status != "unused" && status != "redeemed" && status != "revoked" {
			return listQuery{}, adminhttp.InvalidRequestField("status", "status contains an unsupported value")
		}
	}
	field, desc := sort.SQLParams()
	params := admincdkey.ListParams{Statuses: statuses, Amount: amount, BatchID: adminhttp.QueryString(r, "batch_id"), Search: adminhttp.QueryString(r, "search"), Sort: field, Desc: desc, Limit: page.Limit(), Offset: page.Offset()}
	if from != nil {
		params.From = *from
	}
	if to != nil {
		params.To = *to
	}
	return listQuery{ListParams: params, Page: page.Page, PageSize: page.PageSize}, nil
}

func queryValues(r *http.Request, key string) []string {
	values := r.URL.Query()[key]
	out := make([]string, 0, len(values))
	for _, value := range values {
		for _, part := range strings.Split(value, ",") {
			if part = strings.TrimSpace(part); part != "" {
				out = append(out, part)
			}
		}
	}
	return out
}

func filterFromRequest(in exportFilter) (admincdkey.ListParams, error) {
	statuses := append([]string(nil), in.Statuses...)
	if strings.TrimSpace(in.Status) != "" {
		statuses = append(statuses, in.Status)
	}
	params := admincdkey.ListParams{Statuses: statuses, Amount: in.Amount, BatchID: in.BatchID, Search: in.Search}
	if in.From != "" {
		value, err := time.Parse(time.RFC3339, in.From)
		if err != nil {
			return admincdkey.ListParams{}, adminhttp.InvalidRequestField("filter.from", "filter.from must be RFC3339")
		}
		params.From = value
	}
	if in.To != "" {
		value, err := time.Parse(time.RFC3339, in.To)
		if err != nil {
			return admincdkey.ListParams{}, adminhttp.InvalidRequestField("filter.to", "filter.to must be RFC3339")
		}
		params.To = value
	}
	return params, nil
}

// selectionFromRequest converts the wire representation used by the Admin
// table into a service selection. Filter selection is resolved server-side so
// it can cover rows beyond the currently loaded page; exclude_ids supports the
// UI's "select all filtered, then uncheck" behavior.
func selectionFromRequest(in bulkRequest) (admincdkey.Selection, error) {
	filter, err := filterFromRequest(in.Filter)
	if err != nil {
		return admincdkey.Selection{}, err
	}
	scope := strings.ToLower(strings.TrimSpace(in.Scope))
	if scope == "" {
		// Preserve the original ids-only contract for older Admin clients.
		scope = "selected"
	}
	if scope != "selected" && scope != "page" && scope != "filter" {
		return admincdkey.Selection{}, adminhttp.InvalidRequestField("scope", "scope must be selected, page, or filter")
	}
	return admincdkey.Selection{
		Scope:      scope,
		IDs:        in.IDs,
		Filter:     filter,
		ExcludeIDs: in.ExcludeIDs,
	}, nil
}

func summaryDTOFrom(summary admincdkey.Summary) summaryDTO {
	batchByAmount := make(map[string]batchSummaryDTO, len(summary.BatchByAmount))
	batchCardByAmount := make(map[string]batchSummaryCardAmountDTO, len(summary.BatchByAmount))
	for amount, value := range summary.BatchByAmount {
		batchByAmount[amount] = batchSummaryDTO{
			BatchCount:              value.BatchCount,
			BatchesWithUnused:       value.BatchesWithUnused,
			FullyRedeemedBatchCount: value.FullyRedeemedBatchCount,
		}
		batchCardByAmount[amount] = batchSummaryCardAmountDTO{
			Total: value.BatchCount, WithUnused: value.BatchesWithUnused,
			FullyRedeemed: value.FullyRedeemedBatchCount,
		}
	}
	return summaryDTO{
		Value:                   summaryTotalsDTOFrom(summary.Value),
		Quantity:                summaryTotalsDTOFrom(summary.Quantity),
		RedemptionRate:          summary.RedemptionRate,
		BatchCount:              summary.BatchCount,
		BatchesWithUnused:       summary.BatchesWithUnused,
		FullyRedeemedBatchCount: summary.FullyRedeemedBatchCount,
		BatchByAmount:           batchByAmount,
		BatchSummary: batchSummaryCardDTO{
			Total: summary.BatchCount, WithUnused: summary.BatchesWithUnused,
			FullyRedeemed: summary.FullyRedeemedBatchCount, ByAmount: batchCardByAmount,
		},
		RevokedCount: summary.RevokedCount,
	}
}

func summaryTotalsDTOFrom(totals admincdkey.SummaryTotals) summaryTotalsDTO {
	byAmount := make(map[string]summaryAmountDTO, len(totals.ByAmount))
	for amount, value := range totals.ByAmount {
		byAmount[amount] = summaryAmountDTO{
			Redeemed:      value.Redeemed,
			Unused:        value.Unused,
			RedeemedCount: value.RedeemedCount,
			UnusedCount:   value.UnusedCount,
		}
	}
	return summaryTotalsDTO{
		Total:    totals.Total,
		Redeemed: totals.Redeemed,
		Unused:   totals.Unused,
		ByAmount: byAmount,
	}
}

func cdkeyDTOFrom(item admincdkey.CDKey) cdkeyDTO {
	return cdkeyDTO{
		ID:                    item.ID,
		BatchID:               item.BatchID,
		MaskedCode:            item.MaskedCode,
		CodePrefix:            item.CodePrefix,
		CodeSuffix:            item.CodeSuffix,
		Amount:                item.Amount,
		Currency:              item.Currency,
		Status:                item.Status,
		CreatedAt:             item.CreatedAt.UTC().Format(time.RFC3339Nano),
		RedeemedAt:            formatTimePtr(item.RedeemedAt),
		RevokedAt:             formatTimePtr(item.RevokedAt),
		RedemptionID:          item.RedemptionID,
		RedemptionUserID:      item.RedemptionUserID,
		RedemptionUserEmail:   item.RedemptionUserEmail,
		RedemptionDisplayName: item.RedemptionUserDisplayName,
		RedemptionLedgerID:    item.RedemptionLedgerID,
		RedemptionAt:          formatTimePtr(item.RedemptionAt),
	}
}

func formatTimePtr(value *time.Time) *string {
	if value == nil {
		return nil
	}
	formatted := value.UTC().Format(time.RFC3339Nano)
	return &formatted
}

// Keep chi imported in generated documentation examples and ensure route path
// parameters remain available to static analysis.
var _ = chi.URLParam
