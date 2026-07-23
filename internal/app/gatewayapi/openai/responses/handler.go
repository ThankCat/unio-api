package responses

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/core/adapter"
	"github.com/ThankCat/unio-gateway/internal/core/routing"
	"github.com/ThankCat/unio-gateway/internal/core/sessionhint"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/httpx"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

// ResponsesService 定义 Responses handler 依赖的业务能力。
type ResponsesService interface {
	CreateResponse(ctx context.Context, req ResponsesRequest) (*lifecycle.NonStreamResult[*ResponsesResponse], error)
	StreamResponse(ctx context.Context, req ResponsesRequest, emit func(ResponsesStreamEvent) error) error
	// CompactHistory 无状态降级压缩会话历史（POST /v1/responses/compact）。
	CompactHistory(ctx context.Context, req ResponsesRequest) (*lifecycle.NonStreamResult[*CompactHistoryResponse], error)
	// CountInputTokens 本地估算请求 input token（POST /v1/responses/input_tokens），不调上游、不计费。
	CountInputTokens(ctx context.Context, req ResponsesRequest) (*InputTokenCountResponse, error)
}

// responsesHandler 处理 OpenAI Responses API（POST /v1/responses）请求。
type responsesHandler struct {
	service ResponsesService
}

// NewResponsesHandler 构造 Responses HTTP handler，供 gatewayapi router 挂载。
func NewResponsesHandler(service ResponsesService) http.Handler {
	return &responsesHandler{service: service}
}

// ServeHTTP 解析请求、调用 service，并写出 Responses 协议响应。
func (h *responsesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req ResponsesRequest

	// session-id 头是会话粘性路由的回退会话键（body prompt_cache_key 优先，大 uncache 缺口 P0）；
	// 只捕获进 ctx，是否可用由 service 提取器判定。
	r = r.WithContext(sessionhint.WithClientSessionID(r.Context(), r.Header.Get("session-id")))

	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeResponsesDecodeError(w, err)
		return
	}

	// OpenAI-Beta 头(如 responses_multi_agent=v1)来自 header 而非 body：捕获后随请求透传，
	// 由直传路径转发给上游(DEC-013 宽进;桥接路径不适用 multi-agent)。多值按逗号合并保真转发。
	req.OpenAIBeta = openAIBetaHeader(r)

	if validationErr := validateResponsesRequest(req); validationErr != nil {
		writeResponsesValidationError(w, validationErr)
		return
	}

	if req.StreamEnabled() {
		// 流式是 Codex 主路径：以 Responses 命名事件（event: <type> + data: <json>）写出 SSE。
		// Responses 流以 response.completed/incomplete/failed 收口，不发 chat 的 [DONE] 哨兵。
		sw, err := httpx.NewSSEWriter(r.Context(), w, httpx.SSEWriterConfig{})
		if err != nil {
			// 唯一可能的构造错误是底层 writer 不支持 flush，此时尚未写任何 SSE，可退回 JSON error。
			_ = httpx.WriteError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming unsupported")
			return
		}

		err = h.service.StreamResponse(r.Context(), req, func(ev ResponsesStreamEvent) error {
			payload, marshalErr := json.Marshal(ev)
			if marshalErr != nil {
				return marshalErr
			}
			return sw.WriteEvent(httpx.SSEEvent{Type: ev.Type, Data: payload})
		})
		if err != nil {
			if sw.Started() {
				// SSE 已开始后不能再返回 JSON error，只能尽力写出一条 error 事件后中断连接。
				writeResponsesStreamError(sw, req, err)
				return
			}

			writeResponsesServiceError(w, req, err, "stream_responses_error", "stream responses failed")
			return
		}
		return
	}

	result, err := h.service.CreateResponse(r.Context(), req)
	if err != nil {
		writeResponsesServiceError(w, req, err, "internal_error", "responses request failed")
		return
	}

	_ = result.FinalizeDelivery(func(resp *ResponsesResponse) error {
		return httpx.WriteJSON(w, http.StatusOK, resp)
	})
}

// openAIBetaHeader 读取并合并 OpenAI-Beta 头（可能多次出现），去空白后以逗号连接;无则返回空串。
func openAIBetaHeader(r *http.Request) string {
	var tokens []string
	for _, headerValue := range r.Header.Values("OpenAI-Beta") {
		for _, token := range strings.Split(headerValue, ",") {
			if beta := strings.TrimSpace(token); beta != "" {
				tokens = append(tokens, beta)
			}
		}
	}
	return strings.Join(tokens, ", ")
}

// responsesServiceErrorResponse 表示 Responses service 错误对应的协议原生 HTTP 响应。
type responsesServiceErrorResponse struct {
	status    int
	code      string
	message   string
	errorType string
	param     *string
}

// writeResponsesServiceError 将内部业务错误写成 Responses 原生 JSON error（BRIDGE §7）。
func writeResponsesServiceError(w http.ResponseWriter, req ResponsesRequest, err error, fallbackCode string, fallbackMessage string) {
	resp := mapResponsesServiceError(req, err, fallbackCode, fallbackMessage)
	httpx.SetRetryAfter(w, lifecycle.ProvableRetryAfter(err))

	_ = httpx.WriteOpenAIError(
		w,
		resp.status,
		resp.code,
		resp.message,
		resp.errorType,
		resp.param,
	)
}

// mapResponsesServiceError 将内部业务错误映射成用户可见错误。
//
// 与 chat/messages 一致：上游凭据/服务端错误统一归 502 api_error，绝不渲染成客户 401；
// 不透传上游原始 body。
func mapResponsesServiceError(req ResponsesRequest, err error, fallbackCode string, fallbackMessage string) responsesServiceErrorResponse {
	modelParam := stringPtr("model")

	switch {
	case failure.CodeOf(err) == failure.CodeLedgerInsufficientBalance:
		// 402：与限流 429 区分，避免客户端把余额不足当成限速重试。
		// body 仍用 insufficient_quota，兼容按 code/type 判断的 OpenAI 客户端。
		return responsesServiceErrorResponse{
			status:    http.StatusPaymentRequired,
			code:      "insufficient_quota",
			message:   "You exceeded your current quota. Please check your balance or billing details.",
			errorType: "insufficient_quota",
			param:     nil,
		}
	case failure.CodeOf(err) == failure.CodeRateLimitExceeded, failure.CodeOf(err) == failure.CodeGatewayChannelRateLimited, failure.CodeOf(err) == failure.CodeGatewayChannelConcurrencyLimited:
		// Key 级 TPM 或渠道级 RPM/TPM/RPD 限流命中（P2-8）：统一 429，不泄露具体维度阈值。
		return responsesServiceErrorResponse{
			status:    http.StatusTooManyRequests,
			code:      "rate_limit_exceeded",
			message:   "You have exceeded the rate limit. Please slow down and retry later.",
			errorType: "rate_limit_error",
			param:     nil,
		}
	case isResponsesRequestAdmissionUnavailable(err):
		return responsesServiceErrorResponse{
			status:    http.StatusServiceUnavailable,
			code:      "service_unavailable",
			message:   "The service is temporarily unavailable.",
			errorType: "api_error",
			param:     nil,
		}
	case failure.CodeOf(err) == failure.CodeAdapterRequestUnsupported:
		param := responsesErrorFieldParam(err)
		message := "This model does not support one of the request parameters."
		if param != nil {
			message = fmt.Sprintf("This model does not support the parameter: %s.", *param)
		}
		return responsesServiceErrorResponse{
			status:    http.StatusBadRequest,
			code:      "unsupported_parameter",
			message:   message,
			errorType: "invalid_request_error",
			param:     param,
		}
	case errors.Is(err, routing.ErrModelNotFound), errors.Is(err, routing.ErrModelNotAvailable):
		return responsesServiceErrorResponse{
			status:    http.StatusNotFound,
			code:      "model_not_found",
			message:   fmt.Sprintf("The model %q does not exist or you do not have access to it.", req.Model),
			errorType: "invalid_request_error",
			param:     modelParam,
		}
	case errors.Is(err, routing.ErrNoAvailableChannel):
		return responsesServiceErrorResponse{
			status:    http.StatusServiceUnavailable,
			code:      "model_unavailable",
			message:   fmt.Sprintf("The model %q is temporarily unavailable.", req.Model),
			errorType: "api_error",
			param:     modelParam,
		}
	}

	if category, ok := adapter.UpstreamCategoryOf(err); ok {
		return mapUpstreamResponsesError(category)
	}

	return responsesServiceErrorResponse{
		status:    http.StatusInternalServerError,
		code:      fallbackCode,
		message:   fallbackMessage,
		errorType: "api_error",
		param:     nil,
	}
}

func isResponsesRequestAdmissionUnavailable(err error) bool {
	switch failure.CodeOf(err) {
	case failure.CodeGatewayBreakerStoreUnavailable,
		failure.CodeGatewayRuntimeSyncRequired,
		failure.CodeGatewayRuntimeStateLost,
		failure.CodeGatewayBreakerPermitConflict,
		failure.CodeDependencyRedisUnavailable,
		failure.CodeDependencyPostgresUnavailable:
		return true
	default:
		return false
	}
}

// mapUpstreamResponsesError 把上游错误分类映射成 Responses 协议错误响应（BRIDGE §7）。
func mapUpstreamResponsesError(category adapter.UpstreamErrorCategory) responsesServiceErrorResponse {
	switch category {
	case adapter.UpstreamErrorRateLimit:
		return responsesServiceErrorResponse{
			status:    http.StatusTooManyRequests,
			code:      "rate_limit_exceeded",
			message:   "The upstream provider is rate limiting requests. Please retry later.",
			errorType: "rate_limit_error",
		}
	case adapter.UpstreamErrorTimeout:
		return responsesServiceErrorResponse{
			status:    http.StatusGatewayTimeout,
			code:      "upstream_timeout",
			message:   "The upstream provider timed out. Please retry later.",
			errorType: "api_error",
		}
	case adapter.UpstreamErrorBadRequest:
		return responsesServiceErrorResponse{
			status:    http.StatusBadRequest,
			code:      "invalid_request",
			message:   "The upstream provider rejected the request.",
			errorType: "invalid_request_error",
		}
	default:
		return responsesServiceErrorResponse{
			status:    http.StatusBadGateway,
			code:      "upstream_error",
			message:   "The upstream provider returned an error. Please retry later.",
			errorType: "api_error",
		}
	}
}

// writeResponsesStreamError 在 SSE 已开始后写出一条 Responses error 事件（best-effort）。
//
// 首帧前的错误仍走 JSON error（可达性最佳）；本函数只覆盖流尾错误：此时无法再改写 HTTP 状态，
// 只能附加 event:error 让 Codex/SDK 感知失败后中断。不透传上游原始 body，只渲染安全 message/code。
func writeResponsesStreamError(sw *httpx.SSEWriter, req ResponsesRequest, err error) {
	resp := mapResponsesServiceError(req, err, "stream_error", "stream responses failed")

	body := ResponsesStreamErrorEvent{
		Type:    "error",
		Code:    resp.code,
		Message: resp.message,
		Param:   resp.param,
	}

	payload, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return
	}
	_ = sw.WriteEvent(httpx.SSEEvent{Type: "error", Data: payload})
}

// responsesErrorFieldParam 从内部 failure 中提取安全的 "param" 字段。
func responsesErrorFieldParam(err error) *string {
	for _, field := range failure.FieldsOf(err) {
		if field.Key != "param" {
			continue
		}
		if value, ok := field.Value.(string); ok && value != "" {
			return stringPtr(value)
		}
	}

	return nil
}
