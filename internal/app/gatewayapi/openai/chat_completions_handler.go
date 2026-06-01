package gatewayapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

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

// ServeHTTP 解析请求、调用 service，并写出 HTTP 响应。
func (h *chatCompletionsHandler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	var req ChatCompletionRequest

	if err := httpx.DecodeJSON(w, r, &req); err != nil {
		var rejectErr *chatRequestRejectError
		if errors.As(err, &rejectErr) {
			writeChatValidationError(w, &chatValidationError{
				param:   rejectErr.param,
				message: rejectErr.message,
			})
			return
		}
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
	default:
		return chatServiceErrorResponse{
			status:    http.StatusInternalServerError,
			code:      fallbackCode,
			message:   fallbackMessage,
			errorType: "api_error",
			param:     nil,
		}
	}
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
