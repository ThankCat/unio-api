package provider

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/providerbalance"
)

// ProviderBalanceService 定义 Provider 调额和账本查询所需能力。
type ProviderBalanceService interface {
	Adjust(context.Context, providerbalance.AdjustParams) (providerbalance.Adjustment, error)
	List(context.Context, providerbalance.ListParams) ([]providerbalance.Entry, int64, error)
	ListRisks(context.Context, providerbalance.RiskListParams) ([]providerbalance.CostRisk, int64, error)
	RiskSummary(context.Context, int64) (providerbalance.CostRiskSummary, error)
}

type createProviderBalanceAdjustmentRequest struct {
	Direction      string `json:"direction"`
	Amount         string `json:"amount"`
	TargetBalance  string `json:"target_balance"`
	Currency       string `json:"currency"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

type providerBalanceAdjustmentDTO struct {
	EntryID      int64  `json:"entry_id"`
	ProviderID   int64  `json:"provider_id"`
	EntryType    string `json:"entry_type"`
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	BalanceAfter string `json:"balance_after"`
	Reason       string `json:"reason"`
}

type providerLedgerEntryDTO struct {
	ID                    int64   `json:"id"`
	ProviderID            int64   `json:"provider_id"`
	RequestRecordID       *int64  `json:"request_record_id"`
	RequestAttemptID      *int64  `json:"request_attempt_id"`
	CostSnapshotID        *int64  `json:"cost_snapshot_id"`
	ChannelID             *int64  `json:"channel_id"`
	RequestID             *string `json:"request_id"`
	ChannelName           *string `json:"channel_name"`
	UpstreamModel         *string `json:"upstream_model"`
	ProviderProbeRecordID *int64  `json:"provider_probe_record_id"`
	EntryType             string  `json:"entry_type"`
	Amount                string  `json:"amount"`
	Currency              string  `json:"currency"`
	BalanceBefore         string  `json:"balance_before"`
	BalanceAfter          string  `json:"balance_after"`
	IdempotencyKey        string  `json:"idempotency_key"`
	Reason                string  `json:"reason"`
	CreatedAt             string  `json:"created_at"`
}

type providerCostRiskDTO struct {
	ID                    int64   `json:"id"`
	ProviderID            int64   `json:"provider_id"`
	RequestRecordID       *int64  `json:"request_record_id"`
	RequestAttemptID      *int64  `json:"request_attempt_id"`
	ProviderProbeRecordID *int64  `json:"provider_probe_record_id"`
	SourceType            string  `json:"source_type"`
	EstimatedAmount       *string `json:"estimated_amount"`
	Currency              *string `json:"currency"`
	ReasonCode            string  `json:"reason_code"`
	Reason                string  `json:"reason"`
	Status                string  `json:"status"`
	ReconciliationEntryID *int64  `json:"reconciliation_ledger_entry_id"`
	RequestID             *string `json:"request_id"`
	UpstreamModel         *string `json:"upstream_model"`
	ChannelName           *string `json:"channel_name"`
	CreatedAt             string  `json:"created_at"`
	ReconciledAt          *string `json:"reconciled_at"`
}

type providerCostRiskSummaryDTO struct {
	UnresolvedCount    int64  `json:"unresolved_count"`
	EstimatedAmountUSD string `json:"estimated_amount_usd"`
	UnknownAmountCount int64  `json:"unknown_amount_count"`
}

type providerBalanceHandler struct {
	service ProviderBalanceService
}

func (h *providerBalanceHandler) adjust(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	var body createProviderBalanceAdjustmentRequest
	if err := httpx.DecodeJSON(w, r, &body); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	result, err := h.service.Adjust(r.Context(), providerbalance.AdjustParams{
		ProviderID: id, Direction: body.Direction, Amount: body.Amount,
		TargetBalance: body.TargetBalance,
		Currency:      body.Currency, Reason: body.Reason, IdempotencyKey: body.IdempotencyKey,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusCreated, providerBalanceAdjustmentDTO{
		EntryID: result.EntryID, ProviderID: result.ProviderID, EntryType: result.EntryType,
		Amount: result.Amount, Currency: result.Currency, BalanceAfter: result.BalanceAfter, Reason: result.Reason,
	})
}

func (h *providerBalanceHandler) ledgerEntries(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
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
	params := providerbalance.ListParams{
		ProviderID: id,
		EntryType:  adminhttp.QueryString(r, "entry_type"),
		RequestID:  adminhttp.QueryString(r, "request_id"),
		Limit:      page.Limit(),
		Offset:     page.Offset(),
	}
	if from != nil {
		params.From = *from
	}
	if to != nil {
		params.To = *to
	}
	entries, total, err := h.service.List(r.Context(), params)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]providerLedgerEntryDTO, 0, len(entries))
	for _, entry := range entries {
		out = append(out, providerLedgerEntryDTOFrom(entry))
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *providerBalanceHandler) costRisks(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	page := adminhttp.ParsePage(r)
	items, total, err := h.service.ListRisks(r.Context(), providerbalance.RiskListParams{
		ProviderID: id, Status: adminhttp.QueryString(r, "status"), Limit: page.Limit(), Offset: page.Offset(),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := make([]providerCostRiskDTO, 0, len(items))
	for _, item := range items {
		var reconciledAt *string
		if item.ReconciledAt != nil {
			value := item.ReconciledAt.UTC().Format(time.RFC3339)
			reconciledAt = &value
		}
		out = append(out, providerCostRiskDTO{
			ID: item.ID, ProviderID: item.ProviderID, RequestRecordID: item.RequestRecordID,
			RequestAttemptID: item.RequestAttemptID, ProviderProbeRecordID: item.ProviderProbeRecordID,
			SourceType: item.SourceType, EstimatedAmount: item.EstimatedAmount, Currency: item.Currency,
			ReasonCode: item.ReasonCode, Reason: item.Reason, Status: item.Status,
			ReconciliationEntryID: item.ReconciliationEntryID, RequestID: item.RequestID,
			UpstreamModel: item.UpstreamModel, ChannelName: item.ChannelName,
			CreatedAt: item.CreatedAt.UTC().Format(time.RFC3339), ReconciledAt: reconciledAt,
		})
	}
	adminhttp.WriteList(w, http.StatusOK, out, page, total)
}

func (h *providerBalanceHandler) costRiskSummary(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	summary, err := h.service.RiskSummary(r.Context(), id)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	adminhttp.WriteData(w, http.StatusOK, providerCostRiskSummaryDTO{
		UnresolvedCount: summary.UnresolvedCount, EstimatedAmountUSD: summary.EstimatedAmountUSD,
		UnknownAmountCount: summary.UnknownAmountCount,
	})
}

func providerLedgerEntryDTOFrom(entry providerbalance.Entry) providerLedgerEntryDTO {
	return providerLedgerEntryDTO{
		ID: entry.ID, ProviderID: entry.ProviderID,
		RequestRecordID: entry.RequestRecordID, RequestAttemptID: entry.RequestAttemptID,
		CostSnapshotID: entry.CostSnapshotID, ChannelID: entry.ChannelID,
		RequestID: entry.RequestID, ChannelName: entry.ChannelName, UpstreamModel: entry.UpstreamModel,
		ProviderProbeRecordID: entry.ProviderProbeRecordID,
		EntryType:             entry.EntryType, Amount: entry.Amount, Currency: entry.Currency,
		BalanceBefore: entry.BalanceBefore, BalanceAfter: entry.BalanceAfter,
		IdempotencyKey: entry.IdempotencyKey, Reason: entry.Reason,
		CreatedAt: entry.CreatedAt.UTC().Format(time.RFC3339),
	}
}
