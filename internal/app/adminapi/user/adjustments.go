package user

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/admin/customer"
)

// AdjustmentService 定义 adminapi 手工调额所需的最小能力（M7 客户管理）。
type AdjustmentService interface {
	Adjust(ctx context.Context, params customer.AdjustParams) (customer.Adjustment, error)
}

// createAdjustmentRequest 是手工调额请求体。
// direction: credit（充值）/ debit（扣款）；amount 为正向绝对值十进制字符串。
// idempotency_key 可选：传入可保证重试不重复入账，缺省由服务端生成。
type createAdjustmentRequest struct {
	Direction      string `json:"direction"`
	Amount         string `json:"amount"`
	Currency       string `json:"currency"`
	Reason         string `json:"reason"`
	IdempotencyKey string `json:"idempotency_key"`
}

// adjustmentDTO 是手工调额结果响应体。
type adjustmentDTO struct {
	EntryID      int64  `json:"entry_id"`
	UserID       int64  `json:"user_id"`
	EntryType    string `json:"entry_type"`
	Amount       string `json:"amount"`
	Currency     string `json:"currency"`
	BalanceAfter string `json:"balance_after"`
	Reason       string `json:"reason"`
}

type adjustmentsHandler struct {
	service AdjustmentService
}

func (h *adjustmentsHandler) create(w http.ResponseWriter, r *http.Request) {
	id, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	var req createAdjustmentRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	result, err := h.service.Adjust(r.Context(), customer.AdjustParams{
		UserID:         id,
		Direction:      req.Direction,
		Amount:         req.Amount,
		Currency:       req.Currency,
		Reason:         req.Reason,
		IdempotencyKey: req.IdempotencyKey,
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}

	adminhttp.WriteData(w, http.StatusCreated, adjustmentDTO{
		EntryID:      result.EntryID,
		UserID:       result.UserID,
		EntryType:    result.EntryType,
		Amount:       result.Amount,
		Currency:     result.Currency,
		BalanceAfter: result.BalanceAfter,
		Reason:       result.Reason,
	})
}
