package chatcompletions

import (
	"github.com/ThankCat/unio-gateway/internal/service/gateway/openai/chatcompletions/dto"
)

// 协议 DTO 已下沉到 internal/service/gateway/openai/chatcompletions/dto（service 层契约）；此处以类型别名重新导出，HTTP 层调用方不变。
type (
	ChatCompletionRequest           = dto.ChatCompletionRequest
	ChatCompletionStreamOptions     = dto.ChatCompletionStreamOptions
	ChatMessage                     = dto.ChatMessage
	ChatCompletionResponse          = dto.ChatCompletionResponse
	ChatCompletionChoice            = dto.ChatCompletionChoice
	ChatCompletionUsage             = dto.ChatCompletionUsage
	ChatCompletionPromptDetails     = dto.ChatCompletionPromptDetails
	ChatCompletionCompletionDetails = dto.ChatCompletionCompletionDetails
	ChatCompletionStreamResponse    = dto.ChatCompletionStreamResponse
	ChatCompletionStreamChoice      = dto.ChatCompletionStreamChoice
	ChatCompletionStreamDelta       = dto.ChatCompletionStreamDelta
	ChatCompletionTool              = dto.ChatCompletionTool
	ChatCompletionFunctionTool      = dto.ChatCompletionFunctionTool
	ChatCompletionToolCall          = dto.ChatCompletionToolCall
	ChatCompletionToolCallFunction  = dto.ChatCompletionToolCallFunction
	ChatCompletionResponseFormat    = dto.ChatCompletionResponseFormat
)
