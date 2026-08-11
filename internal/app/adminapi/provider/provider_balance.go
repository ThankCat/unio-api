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
	UsageSource           *string `json:"usage_source"`
	EntryType             string  `json:"entry_type"`
	Amount                string  `json:"amount"`
	Currency              string  `json:"currency"`
	BalanceBefore         string  `json:"balance_before"`
	BalanceAfter          string  `json:"balance_after"`
	IdempotencyKey        string  `json:"idempotency_key"`
	Reason                string  `json:"reason"`
	CreatedAt             string  `json:"created_at"`
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

func providerLedgerEntryDTOFrom(entry providerbalance.Entry) providerLedgerEntryDTO {
	return providerLedgerEntryDTO{
		ID: entry.ID, ProviderID: entry.ProviderID,
		RequestRecordID: entry.RequestRecordID, RequestAttemptID: entry.RequestAttemptID,
		CostSnapshotID: entry.CostSnapshotID, ChannelID: entry.ChannelID,
		RequestID: entry.RequestID, ChannelName: entry.ChannelName, UpstreamModel: entry.UpstreamModel,
		ProviderProbeRecordID: entry.ProviderProbeRecordID,
		UsageSource:           entry.UsageSource,
		EntryType:             entry.EntryType, Amount: entry.Amount, Currency: entry.Currency,
		BalanceBefore: entry.BalanceBefore, BalanceAfter: entry.BalanceAfter,
		IdempotencyKey: entry.IdempotencyKey, Reason: entry.Reason,
		CreatedAt: entry.CreatedAt.UTC().Format(time.RFC3339),
	}
}
