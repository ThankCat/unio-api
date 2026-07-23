package responses

import (
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
)

// endpoints_handler.go 提供 /v1/responses 主创建端点之外的其余 endpoint handler：
//   - POST /responses/compact     无状态降级压缩（TASK-11.11 / GAP-11-007）
//   - POST /responses/input_tokens 本地估算 input token（TASK-11.12 / GAP-11-008）
//   - 有状态 endpoint（retrieve/delete/cancel/input_items）统一 501（TASK-11.13 / GAP-11-009）
//
// compact 与 input_tokens 复用 ResponsesRequest 的 decode/validation：compact 请求体是
// CompactionInput（/responses 的子集），input_tokens 请求体也是 /responses 的子集，结构兼容。

// compactHandler 处理 POST /v1/responses/compact。
type compactHandler struct {
	service ResponsesService
}

// NewResponsesCompactHandler 构造 compact endpoint handler。
func NewResponsesCompactHandler(service ResponsesService) http.Handler {
	return &compactHandler{service: service}
}

// ServeHTTP 解析 CompactionInput、调用 service 压缩，并写出 {"output":[...]}。
func (h *compactHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req ResponsesRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeResponsesDecodeError(w, err)
		return
	}
	if validationErr := validateResponsesRequest(req); validationErr != nil {
		writeResponsesValidationError(w, validationErr)
		return
	}

	result, err := h.service.CompactHistory(r.Context(), req)
	if err != nil {
		writeResponsesServiceError(w, req, err, "internal_error", "responses compact failed")
		return
	}

	_ = result.FinalizeDelivery(func(resp *CompactHistoryResponse) error {
		return httpx.WriteJSON(w, http.StatusOK, resp)
	})
}

// inputTokensHandler 处理 POST /v1/responses/input_tokens。
type inputTokensHandler struct {
	service ResponsesService
}

// NewResponsesInputTokensHandler 构造 input_tokens endpoint handler。
func NewResponsesInputTokensHandler(service ResponsesService) http.Handler {
	return &inputTokensHandler{service: service}
}

// ServeHTTP 解析请求、本地估算 input token，并写出 {"input_tokens":N,"object":"response.input_tokens"}。
func (h *inputTokensHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req ResponsesRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeResponsesDecodeError(w, err)
		return
	}
	if validationErr := validateResponsesRequest(req); validationErr != nil {
		writeResponsesValidationError(w, validationErr)
		return
	}

	resp, err := h.service.CountInputTokens(r.Context(), req)
	if err != nil {
		writeResponsesServiceError(w, req, err, "internal_error", "responses input_tokens failed")
		return
	}

	_ = httpx.WriteJSON(w, http.StatusOK, resp)
}

// NewResponsesStatelessUnsupportedHandler 构造有状态 endpoint 的统一 501 handler。
//
// retrieve/delete/cancel/input_items 共用同一处理：Unio 无服务端响应存储，统一返回
// 501 unsupported_endpoint_stateless（无需 service，纯静态响应）。
func NewResponsesStatelessUnsupportedHandler() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeResponsesStatelessUnsupported(
			w,
			"This endpoint is not supported. Unio does not store responses; resend the full input on each request.",
		)
	})
}
