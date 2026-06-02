package openai

import (
	"bytes"
	"encoding/json"
)

// buildChatCompletionRequestBody 将 adapter 请求编码为 upstream wire JSON，并 merge Extensions。
func buildChatCompletionRequestBody(req ChatRequest, stream bool) ([]byte, error) {
	wire := chatCompletionRequest{
		Model:                req.Model,
		Messages:             adapterMessagesToWire(req.Messages),
		Temperature:          req.Temperature,
		TopP:                 req.TopP,
		MaxTokens:            resolveWireMaxTokens(req),
		PresencePenalty:      req.PresencePenalty,
		FrequencyPenalty:     req.FrequencyPenalty,
		Stop:                 req.Stop,
		User:                 req.User,
		ReasoningEffort:      req.ReasoningEffort,
		Tools:                marshalJSONValue(req.Tools),
		ToolChoice:           cloneRawMessage(req.ToolChoice),
		ParallelToolCalls:    req.ParallelToolCalls,
		ResponseFormat:       adapterResponseFormatToWire(req.ResponseFormat),
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
	}

	if stream {
		wire.Stream = true
		wire.StreamOptions = &chatStreamOptions{IncludeUsage: true}
	}

	base, err := json.Marshal(wire)
	if err != nil {
		return nil, err
	}

	if len(req.Extensions) == 0 {
		return base, nil
	}

	return mergeJSONObjects(base, req.Extensions)
}

func resolveWireMaxTokens(req ChatRequest) *int {
	if req.MaxCompletionTokens != nil {
		return req.MaxCompletionTokens
	}

	return req.MaxTokens
}

func adapterMessagesToWire(messages []ChatMessage) []chatMessage {
	out := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		wire := chatMessage{
			Role:             mapWireMessageRole(msg.Role),
			Content:          cloneRawMessage(msg.Content),
			ReasoningContent: msg.ReasoningContent,
			ToolCallID:       msg.ToolCallID,
			ToolCalls:        marshalJSONValue(msg.ToolCalls),
		}
		out = append(out, wire)
	}

	return out
}

// mapWireMessageRole 将 OpenAI developer role 映射为上游可接受的 system。
func mapWireMessageRole(role string) string {
	if role == "developer" {
		return "system"
	}

	return role
}

func mergeJSONObjects(base []byte, extensions map[string]json.RawMessage) ([]byte, error) {
	merged := make(map[string]json.RawMessage)
	if err := json.Unmarshal(base, &merged); err != nil {
		return nil, err
	}

	for key, value := range extensions {
		if _, exists := merged[key]; exists {
			continue
		}
		merged[key] = value
	}

	return json.Marshal(merged)
}

func encodeRequestBody(req ChatRequest, stream bool) (*bytes.Buffer, error) {
	body, err := buildChatCompletionRequestBody(req, stream)
	if err != nil {
		return nil, err
	}

	return bytes.NewBuffer(body), nil
}
