package chatcompletions

import (
	"encoding/json"

	gatewayapi "github.com/ThankCat/unio-api/internal/app/gatewayapi/openai/chatcompletions"
	"github.com/ThankCat/unio-api/internal/core/adapter"
	chatcompletionsadapter "github.com/ThankCat/unio-api/internal/core/adapter/openai/chatcompletions"
)

func mapGatewayMessagesToAdapter(messages []gatewayapi.ChatMessage) []chatcompletionsadapter.ChatMessage {
	out := make([]chatcompletionsadapter.ChatMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, chatcompletionsadapter.ChatMessage{
			Role:             msg.Role,
			Content:          append(json.RawMessage(nil), msg.Content...),
			ReasoningContent: msg.ReasoningContent,
			ToolCallID:       msg.ToolCallID,
			ToolCalls:        mapGatewayToolCallsToAdapter(msg.ToolCalls),
		})
	}
	return out
}

func mapGatewayToolCallsToAdapter(calls []gatewayapi.ChatCompletionToolCall) []chatcompletionsadapter.ChatToolCall {
	if len(calls) == 0 {
		return nil
	}

	out := make([]chatcompletionsadapter.ChatToolCall, 0, len(calls))
	for _, call := range calls {
		out = append(out, chatcompletionsadapter.ChatToolCall{
			ID:   call.ID,
			Type: call.Type,
			Function: chatcompletionsadapter.ChatToolCallFunction{
				Name:      call.Function.Name,
				Arguments: call.Function.Arguments,
			},
		})
	}

	return out
}

func mapGatewayToolsToAdapter(tools []gatewayapi.ChatCompletionTool) []chatcompletionsadapter.ChatTool {
	if len(tools) == 0 {
		return nil
	}

	out := make([]chatcompletionsadapter.ChatTool, 0, len(tools))
	for _, tool := range tools {
		out = append(out, chatcompletionsadapter.ChatTool{
			Type: tool.Type,
			Function: chatcompletionsadapter.ChatFunctionTool{
				Name:        tool.Function.Name,
				Description: tool.Function.Description,
				Parameters:  append(json.RawMessage(nil), tool.Function.Parameters...),
				Strict:      tool.Function.Strict,
			},
		})
	}

	return out
}

func mapGatewayResponseFormatToAdapter(format *gatewayapi.ChatCompletionResponseFormat) *chatcompletionsadapter.ChatResponseFormat {
	if format == nil {
		return nil
	}

	return &chatcompletionsadapter.ChatResponseFormat{
		Type:       format.Type,
		JSONSchema: append(json.RawMessage(nil), format.JSONSchema...),
	}
}

func mapGatewayRequestToAdapter(req gatewayapi.ChatCompletionRequest, upstreamModel string) chatcompletionsadapter.ChatRequest {
	extensions := make(map[string]json.RawMessage, len(req.Extensions))
	for k, v := range req.Extensions {
		extensions[k] = append(json.RawMessage(nil), v...)
	}
	return chatcompletionsadapter.ChatRequest{
		Model:                upstreamModel,
		Messages:             mapGatewayMessagesToAdapter(req.Messages),
		Temperature:          req.Temperature,
		TopP:                 req.TopP,
		MaxTokens:            req.MaxTokens,
		MaxCompletionTokens:  req.MaxCompletionTokens,
		PresencePenalty:      req.PresencePenalty,
		FrequencyPenalty:     req.FrequencyPenalty,
		Stop:                 req.Stop,
		User:                 req.User,
		ReasoningEffort:      req.ReasoningEffort,
		Tools:                mapGatewayToolsToAdapter(req.Tools),
		ToolChoice:           cloneRawMessage(req.ToolChoice),
		ParallelToolCalls:    req.ParallelToolCalls,
		ResponseFormat:       mapGatewayResponseFormatToAdapter(req.ResponseFormat),
		N:                    req.N,
		Seed:                 req.Seed,
		Logprobs:             req.Logprobs,
		TopLogprobs:          req.TopLogprobs,
		LogitBias:            cloneRawMessage(req.LogitBias),
		Modalities:           req.Modalities,
		Audio:                cloneRawMessage(req.Audio),
		Prediction:           cloneRawMessage(req.Prediction),
		Metadata:             cloneRawMessage(req.Metadata),
		WebSearchOptions:     cloneRawMessage(req.WebSearchOptions),
		Store:                req.Store,
		ServiceTier:          req.ServiceTier,
		Verbosity:            req.Verbosity,
		PromptCacheKey:       req.PromptCacheKey,
		PromptCacheRetention: req.PromptCacheRetention,
		SafetyIdentifier:     req.SafetyIdentifier,
		FunctionCall:         cloneRawMessage(req.FunctionCall),
		Functions:            cloneRawMessage(req.Functions),
		Extensions:           extensions,
	}
}

func cloneRawMessage(src json.RawMessage) json.RawMessage {
	if len(src) == 0 {
		return nil
	}

	return append(json.RawMessage(nil), src...)
}

func mapAdapterToolCallsToGateway(calls []chatcompletionsadapter.ChatToolCall) []gatewayapi.ChatCompletionToolCall {
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

func mapAdapterResponseToGateway(reqModel string, resp chatcompletionsadapter.ChatResponse) gatewayapi.ChatCompletionResponse {
	finishReason := resp.FinishReason
	if finishReason == "" {
		finishReason = "stop"
	}
	msg := gatewayapi.ChatMessage{
		Role:             "assistant",
		ReasoningContent: resp.ReasoningContent,
		ToolCalls:        mapAdapterToolCallsToGateway(resp.ToolCalls),
		Refusal:          resp.Refusal,
		Annotations:      cloneRawMessage(resp.Annotations),
		Audio:            cloneRawMessage(resp.Audio),
	}
	if resp.Content != "" {
		msg.Content = jsonStringContent(resp.Content)
	}
	return gatewayapi.ChatCompletionResponse{
		ID:      resp.ID,
		Object:  "chat.completion",
		Created: resp.Created,
		Model:   reqModel,
		Choices: []gatewayapi.ChatCompletionChoice{{
			Index:        0,
			Message:      msg,
			FinishReason: finishReason,
			Logprobs:     cloneRawMessage(resp.Logprobs),
		}},
		Usage:             mapAdapterUsageToGateway(resp.Usage),
		ServiceTier:       resp.ServiceTier,
		SystemFingerprint: resp.SystemFingerprint,
	}
}

func mapAdapterStreamChunkToGateway(reqModel string, chunk chatcompletionsadapter.ChatStreamChunk, emitUsageNull bool) gatewayapi.ChatCompletionStreamResponse {
	delta := gatewayapi.ChatCompletionStreamDelta{
		Role:             chunk.Role,
		Content:          chunk.Content,
		ReasoningContent: chunk.ReasoningContent,
		ToolCalls:        cloneRawMessage(chunk.ToolCalls),
		Refusal:          chunk.Refusal,
		FunctionCall:     cloneRawMessage(chunk.FunctionCall),
	}

	return gatewayapi.ChatCompletionStreamResponse{
		ID:              chunk.ID,
		Object:          "chat.completion.chunk",
		Created:         chunk.Created,
		Model:           reqModel,
		EmitUsageAsNull: emitUsageNull,
		Choices: []gatewayapi.ChatCompletionStreamChoice{{
			Index:        chunk.Index,
			Delta:        delta,
			FinishReason: chunk.FinishReason,
			Logprobs:     cloneRawMessage(chunk.Logprobs),
		}},
		ServiceTier:       chunk.ServiceTier,
		SystemFingerprint: chunk.SystemFingerprint,
	}
}

func jsonStringContent(s string) json.RawMessage {
	b, _ := json.Marshal(s)
	return b
}
