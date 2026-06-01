package gateway

import (
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/app/gatewayapi"
	"github.com/ThankCat/unio-api/internal/core/adapter"
)

func mapGatewayMessagesToAdapter(messages []gatewayapi.ChatMessage) []adapter.ChatMessage {
	out := make([]adapter.ChatMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, adapter.ChatMessage{
			Role:             msg.Role,
			Content:          append(json.RawMessage(nil), msg.Content...),
			ReasoningContent: msg.ReasoningContent,
			ToolCallID:       msg.ToolCallID,
			ToolCalls:        mapGatewayToolCallsToAdapter(msg.ToolCalls),
		})
	}
	return out
}

func mapGatewayToolCallsToAdapter(calls []gatewayapi.ChatCompletionToolCall) []adapter.ChatToolCall {
	if len(calls) == 0 {
		return nil
	}

	out := make([]adapter.ChatToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, adapter.ChatToolCall{
			ID:   call.ID,
			Type: call.Type,
			Function: adapter.ChatToolCallFunction{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}

	return out
}

func mapGatewayToolsToAdapter(tools []gatewayapi.ChatCompletionTool) []adapter.ChatTool {
	if len(tools) == 0 {
		return nil
	}

	out := make([]adapter.ChatTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, adapter.ChatTool{
			Type: tool.Type,
			Function: adapter.ChatFunctionTool{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  append(json.RawMessage(nil), tool.Function.Parameters...),
				Strict:      tool.Function.Strict,
			},
		})
	}

	return out
}

func mapGatewayResponseFormatToAdapter(format *gatewayapi.ChatCompletionResponseFormat) *adapter.ChatResponseFormat {
	if format == nil {
		return nil
	}

	return &adapter.ChatResponseFormat{
		Type:       format.Type,
		JSONSchema: append(json.RawMessage(nil), format.JSONSchema...),
	}
}

func mapGatewayRequestToAdapter(req gatewayapi.ChatCompletionRequest, upstreamModel string) adapter.ChatRequest {
	extensions := make(map[string]json.RawMessage, len(req.Extensions))
	for k, v := range req.Extensions {
		extensions[k] = append(json.RawMessage(nil), v...)
	}
	return adapter.ChatRequest{
		Model:               upstreamModel,
		Messages:            mapGatewayMessagesToAdapter(req.Messages),
		Temperature:         req.Temperature,
		TopP:                req.TopP,
		MaxTokens:           req.MaxTokens,
		MaxCompletionTokens: req.MaxCompletionTokens,
		PresencePenalty:     req.PresencePenalty,
		FrequencyPenalty:    req.FrequencyPenalty,
		Stop:                req.Stop,
		User:                req.User,
		ReasoningEffort:     req.ReasoningEffort,
		Tools:               mapGatewayToolsToAdapter(req.Tools),
		ToolChoice:          cloneRawMessage(req.ToolChoice),
		ParallelToolCalls:   req.ParallelToolCalls,
		ResponseFormat:      mapGatewayResponseFormatToAdapter(req.ResponseFormat),
		Extensions:          extensions,
	}
}

func cloneRawMessage(src json.RawMessage) json.RawMessage {
	if len(src) == 0 {
		return nil
	}

	return append(json.RawMessage(nil), src...)
}

func mapAdapterToolCallsToGateway(calls []adapter.ChatToolCall) []gatewayapi.ChatCompletionToolCall {
	if len(calls) == 0 {
		return nil
	}

	out := make([]gatewayapi.ChatCompletionToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, gatewayapi.ChatCompletionToolCall{
			ID:   call.ID,
			Type: call.Type,
			Function: gatewayapi.ChatCompletionToolCallFunction{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}

	return out
}

func mapAdapterUsageToGateway(usage adapter.ChatUsage) gatewayapi.ChatCompletionUsage {
	out := gatewayapi.ChatCompletionUsage{
		PromptTokens:     usage.PromptTokens,
		CompletionTokens: usage.CompletionTokens,
		TotalTokens:      usage.TotalTokens,
	}
	if usage.CachedTokens > 0 {
		out.PromptTokensDetails = &gatewayapi.ChatCompletionPromptDetails{
			CachedTokens: usage.CachedTokens,
		}
	}
	if usage.ReasoningTokens > 0 {
		out.CompletionTokensDetails = &gatewayapi.ChatCompletionCompletionDetails{
			ReasoningTokens: usage.ReasoningTokens,
		}
	}
	return out
}

func mapAdapterResponseToGateway(reqModel string, resp adapter.ChatResponse) gatewayapi.ChatCompletionResponse {
	finishReason := resp.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	msg := gatewayapi.ChatMessage{
		Role:             "assistant",
		ReasoningContent: resp.ReasoningContent,
		ToolCalls:        mapAdapterToolCallsToGateway(resp.ToolCalls),
	}
	if resp.Content != "" {
		msg.Content = jsonStringContent(resp.Content)
	}
	return gatewayapi.ChatCompletionResponse{
		ID:     resp.ID,
		Object: "chat.completion",
		Model:  reqModel,
		Choices: []gatewayapi.ChatCompletionChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: finishReason,
		}},
		Usage: mapAdapterUsageToGateway(resp.Usage),
	}
}

func mapAdapterStreamChunkToGateway(reqModel string, chunk adapter.ChatStreamChunk, emitUsageNull bool) gatewayapi.ChatCompletionStreamResponse {
	delta := gatewayapi.ChatCompletionStreamDelta{
		Role:             chunk.Role,
		Content:          chunk.Content,
		ReasoningContent: chunk.ReasoningContent,
		ToolCalls:        cloneRawMessage(chunk.ToolCalls),
	}

	return gatewayapi.ChatCompletionStreamResponse{
		ID:              chunk.ID,
		Object:          "chat.completion.chunk",
		Model:           reqModel,
		EmitUsageAsNull: emitUsageNull,
		Choices: []gatewayapi.ChatCompletionStreamChoice{{
			Index:        0,
			Delta:        delta,
			FinishReason: chunk.FinishReason,
		}},
	}
}

func jsonStringContent(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
