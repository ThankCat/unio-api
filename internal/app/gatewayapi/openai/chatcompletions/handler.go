package chatcompletions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/ThankCat/unio-api/internal/core/adapter"
	"github.com/ThankCat/unio-api/internal/core/routing"
	"github.com/ThankCat/unio-api/internal/platform/failure"
	"github.com/ThankCat/unio-api/internal/platform/httpx"
)

const (
	// maxStopSequences 是 OpenAI-compatible stop 序列数量上限。
	maxStopSequences = 4

	// maxUserLength 是 user 字段的保守长度上限，避免审计/风控标识无限膨胀。
	maxUserLength = 512
)

// ChatCompletionService 定义 chat completions handler 依赖的业务能力。
type ChatCompletionService interface {
	CreateChatCompletion(ctx context.Context, req ChatCompletionRequest) (*ChatCompletionResponse, error)
	StreamChatCompletion(ctx context.Context, req ChatCompletionRequest, emit func(ChatCompletionStreamResponse) error) error
}

// chatCompletionsHandler 处理 OpenAI-compatible chat completions 请求。
type chatCompletionsHandler struct {
	service ChatCompletionService
}

// NewChatCompletionsHandler 构造 OpenAI chat completions HTTP handler，供 gatewayapi router 挂载。
func NewChatCompletionsHandler(service ChatCompletionService) http.Handler {
	return &chatCompletionsHandler{service: service}
}

// ServeHTTP 解析请求、调用 service，并写出 HTTP 响应。
func (h *chatCompletionsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req ChatCompletionRequest

	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		writeJSONDecodeError(w, err)
		return
	}

	if validationErr := validateChatCompletionRequest(req); validationErr != nil {
		writeChatValidationError(w, validationErr)
		return
	}

	if req.Stream != nil && *req.Stream {
		sw, err := httpx.NewSSEWriter(r.Context(), w, httpx.SSEWriterConfig{})
		if err != nil {
			// 唯一可能的构造错误是底层 writer 不支持 flush，此时还没写任何 SSE，可退回 JSON error。
			_ = httpx.WriteError(w, http.StatusInternalServerError, "streaming_unsupported", "streaming unsupported")
			return
		}

		err = h.service.StreamChatCompletion(r.Context(), req, func(chunk ChatCompletionStreamResponse) error {
			payload, marshalErr := json.Marshal(chunk)
			if marshalErr != nil {
				return marshalErr
			}

			return sw.WriteData(payload)
		})
		if err != nil {
			if sw.Started() {
				// SSE 已经开始后不能再返回普通 JSON error，只能尽力写出 data-only error chunk。
				writeChatStreamError(sw, req, err)
				return
			}

			writeChatServiceError(
				w,
				req,
				err,
				"stream_chat_completion_error",
				"stream chat completion failed",
			)
			return
		}

		_ = sw.WriteData([]byte("[DONE]"))
		return
	}

	resp, err := h.service.CreateChatCompletion(r.Context(), req)
	if err != nil {
		writeChatServiceError(
			w,
			req,
			err,
			"internal_error",
			"chat completion failed",
		)
		return
	}

	_ = httpx.WriteJSON(w, http.StatusOK, resp)
}

// stringPtr 返回字符串指针，用于构造 optional 字段。
func stringPtr(v string) *string {
	return &v
}

// chatServiceErrorResponse 表示 chat service 错误对应的 OpenAI-compatible HTTP 响应。
type chatServiceErrorResponse struct {
	status    int
	code      string
	message   string
	errorType string
	param     *string
}

// writeChatServiceError 将 chat service 错误写成 OpenAI-compatible JSON error。
func writeChatServiceError(w http.ResponseWriter, req ChatCompletionRequest, err error, fallbackCode string, fallbackMessage string) {
	resp := mapChatServiceError(req, err, fallbackCode, fallbackMessage)

	_ = httpx.WriteOpenAIError(
		w,
		resp.status,
		resp.code,
		resp.message,
		resp.errorType,
		resp.param,
	)
}

// mapChatServiceError 将内部业务错误映射成用户可见错误。
func mapChatServiceError(req ChatCompletionRequest, err error, fallbackCode string, fallbackMessage string) chatServiceErrorResponse {
	modelParam := stringPtr("model")

	switch {
	case failure.CodeOf(err) == failure.CodeLedgerInsufficientBalance:
		return chatServiceErrorResponse{
			status:    http.StatusTooManyRequests,
			code:      "insufficient_quota",
			message:   "You exceeded your current quota. Please check your balance or billing details.",
			errorType: "insufficient_quota",
			param:     nil,
		}
	case failure.CodeOf(err) == failure.CodeAdapterRequestUnsupported:
		// adapter 在调用上游前明确拒绝当前 provider 无法保持语义的字段。
		param := chatErrorFieldParam(err)
		message := "This model does not support one of the request parameters."
		if param != nil {
			message = fmt.Sprintf("This model does not support the parameter: %s.", *param)
		}
		return chatServiceErrorResponse{
			status:    http.StatusBadRequest,
			code:      "unsupported_parameter",
			message:   message,
			errorType: "invalid_request_error",
			param:     param,
		}
	case errors.Is(err, routing.ErrModelNotFound), errors.Is(err, routing.ErrModelNotAvailable):
		return chatServiceErrorResponse{
			status:    http.StatusNotFound,
			code:      "model_not_found",
			message:   fmt.Sprintf("The model %q does not exist or you do not have access to it.", req.Model),
			errorType: "invalid_request_error",
			param:     modelParam,
		}
	case errors.Is(err, routing.ErrNoAvailableChannel):
		return chatServiceErrorResponse{
			status:    http.StatusServiceUnavailable,
			code:      "model_unavailable",
			message:   fmt.Sprintf("The model %q is temporarily unavailable.", req.Model),
			errorType: "api_error",
			param:     modelParam,
		}
	case errors.Is(err, routing.ErrModelCapabilityUnavailable), errors.Is(err, routing.ErrChannelCapabilityUnavailable):
		// 对客户统一渲染为「模型不支持该能力」：model/channel 内部分层只进审计，不向客户暴露 channel 拓扑。
		return chatServiceErrorResponse{
			status:    http.StatusBadRequest,
			code:      "model_capability_unavailable",
			message:   capabilityUnavailableMessage(req.Model, err),
			errorType: "invalid_request_error",
			param:     modelParam,
		}
	}

	// 上游 provider 调用失败：只消费 adapter 给出的稳定 category，不解析 provider 原始 body。
	// HTTP status 策略与 Anthropic handler 保持一致（见 ACCEPTANCE.md 安全验收）。
	if category, ok := adapter.UpstreamCategoryOf(err); ok {
		return mapUpstreamChatError(category)
	}

	return chatServiceErrorResponse{
		status:    http.StatusInternalServerError,
		code:      fallbackCode,
		message:   fallbackMessage,
		errorType: "api_error",
		param:     nil,
	}
}

// mapUpstreamChatError 把上游错误分类映射成 OpenAI-compatible 错误响应。
//
// upstream auth/permission 是平台 channel 凭据问题，绝不渲染成 401/authentication_error，
// 以免客户误以为自己的 API key 失效；统一归为 502 api_error。
func mapUpstreamChatError(category adapter.UpstreamErrorCategory) chatServiceErrorResponse {
	switch category {
	case adapter.UpstreamErrorRateLimit:
		return chatServiceErrorResponse{
			status:    http.StatusTooManyRequests,
			code:      "rate_limit_exceeded",
			message:   "The upstream provider is rate limiting requests. Please retry later.",
			errorType: "rate_limit_error",
		}
	case adapter.UpstreamErrorTimeout:
		return chatServiceErrorResponse{
			status:    http.StatusGatewayTimeout,
			code:      "upstream_timeout",
			message:   "The upstream provider timed out. Please retry later.",
			errorType: "api_error",
		}
	case adapter.UpstreamErrorBadRequest:
		return chatServiceErrorResponse{
			status:    http.StatusBadRequest,
			code:      "invalid_request",
			message:   "The upstream provider rejected the request.",
			errorType: "invalid_request_error",
		}
	default:
		// auth / permission / server / timeout 之外的 5xx / unknown 统一归为上游网关错误。
		return chatServiceErrorResponse{
			status:    http.StatusBadGateway,
			code:      "upstream_error",
			message:   "The upstream provider returned an error. Please retry later.",
			errorType: "api_error",
		}
	}
}

// chatErrorFieldParam 从内部 failure 中提取安全的 "param" 字段，用于在 OpenAI 错误响应中定位字段。
func chatErrorFieldParam(err error) *string {
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

// capabilityUnavailableMessage 构造 capability 不可用的客户可见文案，列出缺失能力 key（不泄漏 channel 拓扑）。
func capabilityUnavailableMessage(model string, err error) string {
	if missing := routing.MissingCapabilities(err); missing != "" {
		return fmt.Sprintf("The model %q does not support the required capabilities: %s.", model, missing)
	}
	return fmt.Sprintf("The model %q does not support a capability required by this request.", model)
}

// writeJSONDecodeError 将 JSON decode 错误转换成 OpenAI-compatible 错误响应。
func writeJSONDecodeError(w http.ResponseWriter, err error) {
	status := http.StatusBadRequest
	message := "invalid json body"

	switch {
	case errors.Is(err, httpx.ErrUnsupportedContentType):
		status = http.StatusUnsupportedMediaType
		message = "content type must be application/json"
	case errors.Is(err, httpx.ErrRequestBodyTooLarge):
		status = http.StatusRequestEntityTooLarge
		message = "request body too large"
	case errors.Is(err, httpx.ErrEmptyJSONBody):
		message = "request body is required"
	case errors.Is(err, httpx.ErrTrailingJSONToken):
		message = "request body must contain a single JSON object"
	}

	_ = httpx.WriteOpenAIError(
		w,
		status,
		"invalid_request",
		message,
		"invalid_request_error",
		nil,
	)
}

// chatValidationError 表示 chat request validation 失败后的用户可见错误。
type chatValidationError struct {
	param   string
	message string
}

// validateChatCompletionRequest 校验 OpenAI-compatible chat request 的 HTTP DTO 边界。
func validateChatCompletionRequest(req ChatCompletionRequest) *chatValidationError {
	if strings.TrimSpace(req.Model) == "" {
		return &chatValidationError{param: "model", message: "model is required"}
	}

	if len(req.Messages) == 0 {
		return &chatValidationError{param: "messages", message: "messages is required"}
	}

	for i, msg := range req.Messages {
		roleParam := fmt.Sprintf("messages.%d.role", i)
		if strings.TrimSpace(msg.Role) == "" {
			return &chatValidationError{param: roleParam, message: "message role is required"}
		}

		if !isSupportedChatRole(msg.Role) {
			return &chatValidationError{
				param:   roleParam,
				message: "message role must be one of system, user, assistant, developer, tool",
			}
		}

		if validationErr := validateChatMessageContent(msg, i); validationErr != nil {
			return validationErr
		}
	}

	if req.MaxCompletionTokens != nil && *req.MaxCompletionTokens <= 0 {
		return &chatValidationError{param: "max_completion_tokens", message: "max_completion_tokens must be greater than 0"}
	}

	if req.Temperature != nil && (*req.Temperature < 0 || *req.Temperature > 2) {
		return &chatValidationError{param: "temperature", message: "temperature must be between 0 and 2"}
	}

	if req.TopP != nil && (*req.TopP < 0 || *req.TopP > 1) {
		return &chatValidationError{param: "top_p", message: "top_p must be between 0 and 1"}
	}

	if req.MaxTokens != nil && *req.MaxTokens <= 0 {
		return &chatValidationError{param: "max_tokens", message: "max_tokens must be greater than 0"}
	}

	if req.PresencePenalty != nil && (*req.PresencePenalty < -2 || *req.PresencePenalty > 2) {
		return &chatValidationError{param: "presence_penalty", message: "presence_penalty must be between -2 and 2"}
	}

	if req.FrequencyPenalty != nil && (*req.FrequencyPenalty < -2 || *req.FrequencyPenalty > 2) {
		return &chatValidationError{param: "frequency_penalty", message: "frequency_penalty must be between -2 and 2"}
	}

	if len(req.Stop) > maxStopSequences {
		return &chatValidationError{param: "stop", message: "stop must contain at most 4 sequences"}
	}

	for i, stop := range req.Stop {
		if strings.TrimSpace(stop) == "" {
			return &chatValidationError{
				param:   fmt.Sprintf("stop.%d", i),
				message: "stop sequence must not be empty",
			}
		}
	}

	if req.User != nil {
		if strings.TrimSpace(*req.User) == "" {
			return &chatValidationError{param: "user", message: "user must not be empty"}
		}

		if len(*req.User) > maxUserLength {
			return &chatValidationError{param: "user", message: "user must be at most 512 characters"}
		}
	}

	if req.N != nil && *req.N < 1 {
		return &chatValidationError{param: "n", message: "n must be greater than or equal to 1"}
	}

	// top_logprobs 是 0~20 的整数，且 OpenAI 要求同时开启 logprobs；这是协议硬约束，
	// 不属于 provider 能力差异，因此在 ingress 校验而不是交给 adapter Drop。
	if req.TopLogprobs != nil {
		if *req.TopLogprobs < 0 || *req.TopLogprobs > 20 {
			return &chatValidationError{param: "top_logprobs", message: "top_logprobs must be between 0 and 20"}
		}
		if req.Logprobs == nil || !*req.Logprobs {
			return &chatValidationError{param: "top_logprobs", message: "top_logprobs requires logprobs to be set to true"}
		}
	}

	return nil
}

// writeChatValidationError 将 chat validation 错误写成 OpenAI-compatible error。
func writeChatValidationError(w http.ResponseWriter, validationErr *chatValidationError) {
	_ = httpx.WriteOpenAIError(
		w,
		http.StatusBadRequest,
		"invalid_request",
		validationErr.message,
		"invalid_request_error",
		stringPtr(validationErr.param),
	)
}

// writeChatStreamError 在 SSE 已开始后写出 OpenAI-compatible data-only error chunk。
func writeChatStreamError(sw *httpx.SSEWriter, req ChatCompletionRequest, err error) {
	resp := mapChatServiceError(
		req,
		err,
		"stream_error",
		"stream chat completion failed",
	)

	body := httpx.ErrorResponse{
		Error: httpx.ErrorBody{
			Message: resp.message,
			Type:    resp.errorType,
			Param:   resp.param,
			Code:    resp.code,
		},
	}

	payload, marshalErr := json.Marshal(body)
	if marshalErr != nil {
		return
	}

	_ = sw.WriteData(payload)
}
