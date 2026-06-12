package adminapi

import (
	"net/http"
	"strconv"

	"github.com/go-chi/chi/v5"

	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

// writeData 写出统一成功信封 { "data": ... }。
func writeData(w http.ResponseWriter, status int, data any) {
	_ = httpx.WriteJSON(w, status, map[string]any{"data": data})
}

// writeServiceError 把 service / 解码层的内部 failure 映射为安全的 admin 错误响应。
//
// 只回显 4xx 的安全摘要（failure.Error() 不含 cause 细节），5xx 一律返回通用文案，
// 不向客户端透传内部实现或上游原始信息。
func writeServiceError(w http.ResponseWriter, err error) {
	code := failure.CodeOf(err)
	status := adminErrorStatus(code)

	codeStr := string(code)
	if codeStr == "" {
		codeStr = "internal_error"
	}

	message := "internal error"
	if status != http.StatusInternalServerError {
		if m := err.Error(); m != "" {
			message = m
		}
	}

	_ = httpx.WriteError(w, status, codeStr, message)
}

// adminErrorStatus 把内部错误码映射为 HTTP 状态码。
func adminErrorStatus(code failure.Code) int {
	switch code {
	case failure.CodeAdminInvalidArgument:
		return http.StatusBadRequest
	case failure.CodeAdminAdapterBindingUnsupported:
		return http.StatusUnprocessableEntity
	case failure.CodeAdminPricingWindowOverlap:
		return http.StatusUnprocessableEntity
	case failure.CodeAdminNotFound:
		return http.StatusNotFound
	case failure.CodeAdminConflict:
		return http.StatusConflict
	// M7 手工调额经由 ledger：把账本业务错误映射成可读的 4xx，而非笼统 500。
	case failure.CodeLedgerInvalidAmount:
		return http.StatusBadRequest
	case failure.CodeLedgerInsufficientBalance:
		return http.StatusUnprocessableEntity
	case failure.CodeLedgerIdempotencyConflict:
		return http.StatusConflict
	// M5 能力管理：core/capability 写入校验错误映射成可读的 4xx。
	case failure.CodeCapabilityInvalidKey,
		failure.CodeCapabilityInvalidSupportLevel,
		failure.CodeCapabilityInvalidSource:
		return http.StatusBadRequest
	case failure.CodeCapabilityNotFound:
		return http.StatusNotFound
	}

	if code.Category() == failure.CategoryHTTP {
		return http.StatusBadRequest
	}

	return http.StatusInternalServerError
}

// pathID 解析路径参数 {id}，非法或非正整数时返回 admin_invalid_argument。
func pathID(r *http.Request) (int64, error) {
	raw := chi.URLParam(r, "id")
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, failure.New(
			failure.CodeAdminInvalidArgument,
			failure.WithMessage("id path parameter must be a positive integer"),
			failure.WithField("field", "id"),
		)
	}
	return id, nil
}
