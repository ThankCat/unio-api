package chatcompletions

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
		MaxTokens:            req.MaxTokens,
		MaxCompletionTokens:  req.MaxCompletionTokens,
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

// adapterMessagesToWire 忠实编码 messages：role（含 developer）原样透传，不做任何 provider 归一。
// provider 专属的 role 塌缩（如 DeepSeek developer→system）由对应 provider adapter 在调 base 前完成。
func adapterMessagesToWire(messages []ChatMessage) []chatMessage {
	out := make([]chatMessage, 0, len(messages))
	for _, msg := range messages {
		out = append(out, adapterChatMessageToWire(msg))
	}

	return out
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
