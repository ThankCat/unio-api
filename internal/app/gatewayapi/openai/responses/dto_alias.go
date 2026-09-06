package responses

import "github.com/ThankCat/unio-gateway/internal/service/gateway/openai/responses/dto"

// 协议 DTO 已下沉到 internal/service/gateway/openai/responses/dto（service 层契约）；此处以类型别名重新导出，HTTP 层调用方不变。
type (
	ResponsesRequest             = dto.ResponsesRequest
	ResponsesInput               = dto.ResponsesInput
	ResponsesInt                 = dto.ResponsesInt
	ResponseInputItem            = dto.ResponseInputItem
	ResponsesReasoning           = dto.ResponsesReasoning
	ResponsesTextControls        = dto.ResponsesTextControls
	ResponsesResponse            = dto.ResponsesResponse
	ResponseOutputItem           = dto.ResponseOutputItem
	ResponseOutputContent        = dto.ResponseOutputContent
	ResponsesUsage               = dto.ResponsesUsage
	ResponsesInputTokensDetails  = dto.ResponsesInputTokensDetails
	ResponsesOutputTokensDetails = dto.ResponsesOutputTokensDetails
	ResponsesIncompleteDetails   = dto.ResponsesIncompleteDetails
	ResponsesErrorObject         = dto.ResponsesErrorObject
	InputTokenCountResponse      = dto.InputTokenCountResponse
	CompactHistoryResponse       = dto.CompactHistoryResponse
	ResponsesStreamEvent         = dto.ResponsesStreamEvent
	ResponsesStreamErrorEvent    = dto.ResponsesStreamErrorEvent
	ResponsesTool                = dto.ResponsesTool
)

const (
	EventResponseCreated           = dto.EventResponseCreated
	EventResponseInProgress        = dto.EventResponseInProgress
	EventOutputItemAdded           = dto.EventOutputItemAdded
	EventOutputItemDone            = dto.EventOutputItemDone
	EventContentPartAdded          = dto.EventContentPartAdded
	EventContentPartDone           = dto.EventContentPartDone
	EventOutputTextDelta           = dto.EventOutputTextDelta
	EventOutputTextDone            = dto.EventOutputTextDone
	EventReasoningTextDelta        = dto.EventReasoningTextDelta
	EventReasoningTextDone         = dto.EventReasoningTextDone
	EventReasoningSummaryTextDelta = dto.EventReasoningSummaryTextDelta
	EventReasoningSummaryTextDone  = dto.EventReasoningSummaryTextDone
	EventReasoningSummaryPartAdded = dto.EventReasoningSummaryPartAdded
	EventReasoningSummaryPartDone  = dto.EventReasoningSummaryPartDone
	EventRefusalDelta              = dto.EventRefusalDelta
	EventRefusalDone               = dto.EventRefusalDone
	EventFunctionCallArgsDelta     = dto.EventFunctionCallArgsDelta
	EventFunctionCallArgsDone      = dto.EventFunctionCallArgsDone
	EventCustomToolCallInputDelta  = dto.EventCustomToolCallInputDelta
	EventCustomToolCallInputDone   = dto.EventCustomToolCallInputDone
	EventResponseCompleted         = dto.EventResponseCompleted
	EventResponseIncomplete        = dto.EventResponseIncomplete
	EventResponseFailed            = dto.EventResponseFailed
	toolTypeFunction               = dto.ToolTypeFunction
	toolTypeNamespace              = dto.ToolTypeNamespace
	toolTypeCustom                 = dto.ToolTypeCustom
)
