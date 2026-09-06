package messages

import (
	"github.com/ThankCat/unio-gateway/internal/service/gateway/anthropic/messages/dto"
)

// 协议 DTO 已下沉到 internal/service/gateway/anthropic/messages/dto（service 层契约）；此处以类型别名重新导出，HTTP 层调用方不变。
type (
	MessageRequest      = dto.MessageRequest
	Message             = dto.Message
	MessageResponse     = dto.MessageResponse
	MessageUsage        = dto.MessageUsage
	CacheCreation       = dto.CacheCreation
	OutputTokensDetails = dto.OutputTokensDetails
	ServerToolUse       = dto.ServerToolUse
	StreamFrame         = dto.StreamFrame
	StreamMessageStop   = dto.StreamMessageStop
)
