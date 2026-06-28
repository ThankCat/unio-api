package messages

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

const (
	anthropicVersionHeader = "anthropic-version"
	anthropicBetaHeader    = "anthropic-beta"
)

// supportedAnthropicVersions 是 ingress 当前接受的 Anthropic API 版本。
var supportedAnthropicVersions = map[string]struct{}{
	"2023-06-01": {},
}

// MessagesService 定义 Anthropic Messages handler 依赖的业务能力。
type MessagesService interface {
	CreateMessage(ctx context.Context, req MessageRequest) (*MessageResponse, error)
	StreamMessage(ctx context.Context, req MessageRequest, emit func(StreamFrame) error) error
}

type messagesHandler struct {
	service MessagesService
	logger  *slog.Logger
}

// NewMessagesHandler 构造 Anthropic Messages HTTP handler。
//
// logger 用于脱敏审计被忽略的 anthropic-beta（仅记录非敏感的 beta 能力名）；
// 传 nil 时回退 slog.Default()，保证测试和异常装配下也不会 panic。
func NewMessagesHandler(service MessagesService, logger *slog.Logger) http.Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &messagesHandler{service: service, logger: logger}
}

func (h *messagesHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if verr := validateAnthropicIngressHeaders(r); verr != nil {
		writeMessageValidationError(w, verr)
		return
	}

	h.auditIgnoredBetaHeaders(r)

	var req MessageRequest
	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeJSONDecodeError(w, err)
		return
	}
	req.AnthropicBeta = anthropicBetaTokens(r)

	if validationErr := validateMessageRequest(req); validationErr != nil {
		writeMessageValidationError(w, validationErr)
		return
	}

	if req.IsStream() {
		sw, err := httpx.NewSSEWriter(r.Context(), w, httpx.SSEWriterConfig{})
		if err != nil {
			_ = writeAnthropicError(w, http.StatusInternalServerError, "api_error", "streaming unsupported", requestIDFromContext(r))
			return
		}

		err = h.service.StreamMessage(r.Context(), req, func(frame StreamFrame) error {
			return sw.WriteEvent(httpx.SSEEvent{
				Type: frame.EventType,
				Data: frame.Data,
			})
		})
		if err != nil {
			if sw.Started() {
				writeStreamServiceError(sw, req, err)
				return
			}

			writeMessageServiceError(w, req, err)
			return
		}
		return
	}

	resp, err := h.service.CreateMessage(r.Context(), req)
	if err != nil {
		writeMessageServiceError(w, req, err)
		return
	}

	_ = httpx.WriteJSON(w, http.StatusOK, resp)
}

func validateAnthropicIngressHeaders(r *http.Request) *messageValidationError {
	version := strings.TrimSpace(r.Header.Get(anthropicVersionHeader))
	if version == "" {
		return &messageValidationError{
			param:   anthropicVersionHeader,
			message: "anthropic-version header is required",
		}
	}
	if _, ok := supportedAnthropicVersions[version]; !ok {
		return &messageValidationError{
			param:   anthropicVersionHeader,
			message: fmt.Sprintf("unsupported anthropic-version %q", version),
		}
	}

	return nil
}

// anthropicBetaTokens 解析 anthropic-beta header 为去空白的非空 token 列表。
//
// Anthropic 允许 beta header 出现多次，也允许单个 header 内用逗号分隔多个 beta。
func anthropicBetaTokens(r *http.Request) []string {
	var tokens []string
	for _, headerValue := range r.Header.Values(anthropicBetaHeader) {
		for _, token := range strings.Split(headerValue, ",") {
			if beta := strings.TrimSpace(token); beta != "" {
				tokens = append(tokens, beta)
			}
		}
	}
	return tokens
}

// auditIgnoredBetaHeaders 脱敏审计被忽略的 anthropic-beta（DEC-013）。
//
// 按 DEC-012「协议为先」：ingress 不因 provider 能力拒绝 beta（不再 400），而是接受后在
// provider 映射层 Drop。当前 DeepSeek Anthropic endpoint 忽略所有 beta 且出站不发送，
// 因此 beta 对行为无影响——这里只做脱敏审计（beta 是非敏感能力名），既不静默吞掉，也不在
// 响应里假装某 beta 已生效。未来接入真实 Anthropic 1P adapter 时，应改为按登记表 Pass 转发。
func (h *messagesHandler) auditIgnoredBetaHeaders(r *http.Request) {
	tokens := anthropicBetaTokens(r)
	if len(tokens) == 0 {
		return
	}

	h.logger.LogAttrs(
		r.Context(),
		slog.LevelDebug,
		"anthropic-beta ignored (no-op for current provider)",
		slog.Any("dropped_beta_headers", tokens),
	)
}

func writeMessageValidationError(w http.ResponseWriter, verr *messageValidationError) {
	_ = writeAnthropicError(
		w,
		http.StatusBadRequest,
		"invalid_request_error",
		verr.message,
		"",
	)
}

func writeJSONDecodeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	errorType := "invalid_request_error"
	message := "invalid json body"

	switch {
	case errors.Is(err, httpx.ErrUnsupportedContentType):
		status = http.StatusUnsupportedMediaType
		message = "content type must be application/json"
	case errors.Is(err, httpx.ErrRequestBodyTooLarge):
		status = http.StatusRequestEntityTooLarge
		errorType = "request_too_large"
		message = "request body too large"
	case errors.Is(err, httpx.ErrEmptyJSONBody):
		message = "request body is required"
	case errors.Is(err, httpx.ErrTrailingJSONToken):
		message = "request body must contain a single JSON object"
	}

	_ = writeAnthropicError(w, status, errorType, message, "")
}

func writeMessageServiceError(w http.ResponseWriter, req MessageRequest, err error) {
	status, errorType, message := mapMessageServiceError(req, err)
	_ = writeAnthropicError(w, status, errorType, message, "")
}

func mapMessageServiceError(req MessageRequest, err error) (status int, errorType string, message string) {
	switch {
	case failure.CodeOf(err) == failure.CodeLedgerInsufficientBalance:
		return http.StatusTooManyRequests, "rate_limit_error", "Your credit balance is too low to access the API. Please go to Plans & Billing to upgrade or purchase credits."
	case failure.CodeOf(err) == failure.CodeRateLimitExceeded, failure.CodeOf(err) == failure.CodeGatewayChannelRateLimited:
		// Key 级 TPM 或渠道级 RPM/TPM/RPD 限流命中（P2-8）：统一 429，不泄露具体维度阈值。
		return http.StatusTooManyRequests, "rate_limit_error", "You have exceeded the rate limit. Please slow down and retry later."
	case failure.CodeOf(err) == failure.CodeAdapterRequestUnsupported:
		return http.StatusBadRequest, "invalid_request_error", "This model does not support one of the request parameters."
	case errors.Is(err, routing.ErrModelNotFound), errors.Is(err, routing.ErrModelNotAvailable):
		return http.StatusNotFound, "not_found_error", fmt.Sprintf("model: %s", req.Model)
	case errors.Is(err, routing.ErrNoAvailableChannel):
		return http.StatusServiceUnavailable, "api_error", fmt.Sprintf("model %q is temporarily unavailable", req.Model)
	}

	// 上游 provider 调用失败：只消费 adapter 给出的稳定 category，不解析 provider 原始 body。
	// HTTP status 策略与 OpenAI handler 保持一致（见 ACCEPTANCE.md 安全验收）。
	if category, ok := adapter.UpstreamCategoryOf(err); ok {
		return mapUpstreamMessageError(category)
	}

	return http.StatusInternalServerError, "api_error", "request failed"
}

// mapUpstreamMessageError 把上游错误分类映射成 Anthropic 原生错误响应。
//
// upstream auth/permission 是平台 channel 凭据问题，绝不渲染成 401/authentication_error，
// 以免客户误以为自己的 API key 失效；统一归为 502 api_error。
func mapUpstreamMessageError(category adapter.UpstreamErrorCategory) (status int, errorType string, message string) {
	switch category {
	case adapter.UpstreamErrorRateLimit:
		return http.StatusTooManyRequests, "rate_limit_error", "The upstream provider is rate limiting requests. Please retry later."
	case adapter.UpstreamErrorTimeout:
		return http.StatusGatewayTimeout, "api_error", "The upstream provider timed out. Please retry later."
	case adapter.UpstreamErrorBadRequest:
		return http.StatusBadRequest, "invalid_request_error", "The upstream provider rejected the request."
	default:
		// auth / permission / server / unknown 统一归为上游网关错误。
		return http.StatusBadGateway, "api_error", "The upstream provider returned an error. Please retry later."
	}
}

// writeStreamServiceError 在 SSE 已开始后写出 Anthropic 原生 error event。
//
// SSE 一旦开始就不能回退普通 JSON error，因此复用与非流式相同的映射策略，
// 按真实错误分类渲染原生 error type（限流、上游错误等），不写死 api_error。
func writeStreamServiceError(sw *httpx.SSEWriter, req MessageRequest, err error) {
	_, errorType, message := mapMessageServiceError(req, err)

	payload, marshalErr := json.Marshal(StreamError{
		Type: "error",
		Error: anthropic.ErrorBody{
			Type:    errorType,
			Message: message,
		},
	})
	if marshalErr != nil {
		return
	}

	_ = sw.WriteEvent(httpx.SSEEvent{Type: "error", Data: payload})
}

func writeAnthropicError(w http.ResponseWriter, status int, errorType string, message string, requestID string) error {
	return httpx.WriteJSON(w, status, anthropic.NewErrorResponse(errorType, message, requestID))
}

func requestIDFromContext(r *http.Request) string {
	if r == nil {
		return ""
	}
	return r.Header.Get("X-Request-ID")
}
