package messages

import (
	"encoding/json"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/anthropic/messages"
	messagesadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic/messages"
)

func mapGatewayMessagesToAdapter(messages []gatewayapi.Message) []messagesadapter.Message {
	out := make([]messagesadapter.Message, 0, len(messages))
	for _, msg := range messages {
		out = append(out, messagesadapter.Message{
			Role:    msg.Role,
			Content: append(json.RawMessage(nil), msg.Content...),
		})
	}
	return out
}

func mapGatewayRequestToAdapter(req gatewayapi.MessageRequest, upstreamModel string) messagesadapter.MessageRequest {
	extensions := make(map[string]json.RawMessage, len(req.Extensions))
	for k, v := range req.Extensions {
		extensions[k] = append(json.RawMessage(nil), v...)
	}

	stream := false
	if req.Stream != nil {
		stream = *req.Stream
	}

	return messagesadapter.MessageRequest{
		Model:         upstreamModel,
		System:        append(json.RawMessage(nil), req.System...),
		Messages:      mapGatewayMessagesToAdapter(req.Messages),
		MaxTokens:     req.MaxTokens,
		StopSequences: append([]string(nil), req.StopSequences...),
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		TopK:          req.TopK,
		Thinking:      append(json.RawMessage(nil), req.Thinking...),
		ToolChoice:    append(json.RawMessage(nil), req.ToolChoice...),
		Tools:         append(json.RawMessage(nil), req.Tools...),
		Metadata:      append(json.RawMessage(nil), req.Metadata...),
		Stream:        stream,
		Extensions:    extensions,
		AnthropicBeta: append([]string(nil), req.AnthropicBeta...),
	}
}

func mapAdapterUsageToGateway(usage messagesadapter.MessageUsage) gatewayapi.MessageUsage {
	out := gatewayapi.MessageUsage{
		InputTokens:  usage.InputTokens,
		OutputTokens: usage.OutputTokens,
	}
	if usage.CacheCreationInputTokens != nil {
		out.CacheCreationInputTokens = usage.CacheCreationInputTokens
	}
	if usage.CacheReadInputTokens != nil {
		out.CacheReadInputTokens = usage.CacheReadInputTokens
	}
	if usage.CacheCreation != nil {
		out.CacheCreation = &gatewayapi.CacheCreation{
			Ephemeral5mInputTokens: usage.CacheCreation.Ephemeral5mInputTokens,
			Ephemeral1hInputTokens: usage.CacheCreation.Ephemeral1hInputTokens,
		}
	}
	if usage.ThinkingOutputTokens != nil {
		out.OutputTokensDetails = &gatewayapi.OutputTokensDetails{
			ThinkingTokens: usage.ThinkingOutputTokens,
		}
	}
	if usage.ServerToolUse != nil {
		out.ServerToolUse = &gatewayapi.ServerToolUse{
			WebSearchRequests: usage.ServerToolUse.WebSearchRequests,
			WebFetchRequests:  usage.ServerToolUse.WebFetchRequests,
		}
	}
	if usage.ServiceTier != nil {
		out.ServiceTier = usage.ServiceTier
	}
	return out
}

func mapAdapterResponseToGateway(catalogModel string, resp messagesadapter.MessageResponse) gatewayapi.MessageResponse {
	content := make([]json.RawMessage, len(resp.Content))
	for i, block := range resp.Content {
		content[i] = append(json.RawMessage(nil), block...)
	}

	role := resp.Role
	if role == "" {
		role = "assistant"
	}

	return gatewayapi.MessageResponse{
		ID:           resp.ID,
		Type:         "message",
		Role:         role,
		Model:        catalogModel,
		Content:      content,
		StopReason:   resp.StopReason,
		StopSequence: resp.StopSequence,
		Usage:        mapAdapterUsageToGateway(resp.Usage),
	}
}

// patchStreamEventCatalogModel 把 message_start 事件中的 model 字段恢复为客户 catalog model。
func patchStreamEventCatalogModel(catalogModel string, ev messagesadapter.MessageStreamEvent) json.RawMessage {
	if ev.Type != "message_start" || len(ev.Data) == 0 {
		return append(json.RawMessage(nil), ev.Data...)
	}

	var payload map[string]json.RawMessage
	if err := json.Unmarshal(ev.Data, &payload); err != nil {
		return append(json.RawMessage(nil), ev.Data...)
	}

	msgRaw, ok := payload["message"]
	if !ok {
		return append(json.RawMessage(nil), ev.Data...)
	}

	var msg map[string]json.RawMessage
	if err := json.Unmarshal(msgRaw, &msg); err != nil {
		return append(json.RawMessage(nil), ev.Data...)
	}

	modelBytes, err := json.Marshal(catalogModel)
	if err != nil {
		return append(json.RawMessage(nil), ev.Data...)
	}
	msg["model"] = modelBytes

	patchedMsg, err := json.Marshal(msg)
	if err != nil {
		return append(json.RawMessage(nil), ev.Data...)
	}
	payload["message"] = patchedMsg

	out, err := json.Marshal(payload)
	if err != nil {
		return append(json.RawMessage(nil), ev.Data...)
	}
	return out
}
