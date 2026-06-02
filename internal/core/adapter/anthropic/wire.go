package anthropic

import (
	"bytes"
	"encoding/json"
)

// messagesRequest 是发送给 Anthropic Messages 上游的 wire 请求 DTO。
//
// 复杂 union（system、content、thinking、tool_choice、tools、metadata）以 json.RawMessage 透传，
// 由 provider adapter 在 reject/request map 阶段保证只送上游能保持语义的字段。
type messagesRequest struct {
	Model         string          `json:"model"`
	System        json.RawMessage `json:"system,omitempty"`
	Messages      []messageWire   `json:"messages"`
	MaxTokens     *int            `json:"max_tokens,omitempty"`
	StopSequences []string        `json:"stop_sequences,omitempty"`
	Temperature   *float64        `json:"temperature,omitempty"`
	TopP          *float64        `json:"top_p,omitempty"`
	TopK          *int            `json:"top_k,omitempty"`
	Thinking      json.RawMessage `json:"thinking,omitempty"`
	ToolChoice    json.RawMessage `json:"tool_choice,omitempty"`
	Tools         json.RawMessage `json:"tools,omitempty"`
	Metadata      json.RawMessage `json:"metadata,omitempty"`
	Stream        bool            `json:"stream,omitempty"`
}

// messageWire 是 wire 层单条消息。
type messageWire struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

// messagesResponse 是 Anthropic Messages 上游非流式响应 wire DTO。
type messagesResponse struct {
	ID           string            `json:"id"`
	Type         string            `json:"type"`
	Role         string            `json:"role"`
	Model        string            `json:"model"`
	Content      []json.RawMessage `json:"content"`
	StopReason   *string           `json:"stop_reason"`
	StopSequence *string           `json:"stop_sequence"`
	Usage        usageWire         `json:"usage"`
}

// usageWire 是 Anthropic usage 的 wire DTO；可选维度用指针区分上游"未提供"与"为 0"。
type usageWire struct {
	InputTokens              *int               `json:"input_tokens"`
	CacheCreationInputTokens *int               `json:"cache_creation_input_tokens"`
	CacheReadInputTokens     *int               `json:"cache_read_input_tokens"`
	CacheCreation            *cacheCreationWire `json:"cache_creation"`
	OutputTokens             *int               `json:"output_tokens"`
	OutputTokensDetails      *outputDetailsWire `json:"output_tokens_details"`
	ServerToolUse            *serverToolWire    `json:"server_tool_use"`
	ServiceTier              *string            `json:"service_tier"`
}

type cacheCreationWire struct {
	Ephemeral5mInputTokens *int `json:"ephemeral_5m_input_tokens"`
	Ephemeral1hInputTokens *int `json:"ephemeral_1h_input_tokens"`
}

type outputDetailsWire struct {
	ThinkingTokens *int `json:"thinking_tokens"`
}

type serverToolWire struct {
	WebSearchRequests *int `json:"web_search_requests"`
	WebFetchRequests  *int `json:"web_fetch_requests"`
}

// streamUsageEnvelope 用于从 message_start / message_delta 事件中提取 usage 与 stop 信息。
type streamUsageEnvelope struct {
	Delta *struct {
		StopReason   *string `json:"stop_reason"`
		StopSequence *string `json:"stop_sequence"`
	} `json:"delta"`
	Message *struct {
		ID    string     `json:"id"`
		Model string     `json:"model"`
		Usage *usageWire `json:"usage"`
	} `json:"message"`
	Usage *usageWire `json:"usage"`
}

// buildMessagesRequestBody 把内部请求编码为上游 wire JSON，并 merge 未显式建模的 Extensions。
func buildMessagesRequestBody(req MessageRequest) ([]byte, error) {
	wire := messagesRequest{
		Model:         req.Model,
		System:        cloneRaw(req.System),
		Messages:      adapterMessagesToWire(req.Messages),
		MaxTokens:     req.MaxTokens,
		StopSequences: req.StopSequences,
		Temperature:   req.Temperature,
		TopP:          req.TopP,
		TopK:          req.TopK,
		Thinking:      cloneRaw(req.Thinking),
		ToolChoice:    cloneRaw(req.ToolChoice),
		Tools:         cloneRaw(req.Tools),
		Metadata:      cloneRaw(req.Metadata),
		Stream:        req.Stream,
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

func adapterMessagesToWire(messages []Message) []messageWire {
	out := make([]messageWire, 0, len(messages))
	for _, msg := range messages {
		out = append(out, messageWire{
			Role:    msg.Role,
			Content: cloneRaw(msg.Content),
		})
	}
	return out
}

// mergeJSONObjects 把 base JSON 对象与 extensions 合并；已存在的键不被覆盖，避免扩展改写显式字段。
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

func encodeMessagesRequestBody(req MessageRequest) (*bytes.Buffer, error) {
	body, err := buildMessagesRequestBody(req)
	if err != nil {
		return nil, err
	}
	return bytes.NewBuffer(body), nil
}

// cloneRaw 复制一段 RawMessage，避免共享底层切片被后续修改。
func cloneRaw(raw json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return nil
	}
	out := make(json.RawMessage, len(raw))
	copy(out, raw)
	return out
}

// messageUsageFromWire 把 wire usage 映射为协议族内部 MessageUsage。
func messageUsageFromWire(w usageWire) MessageUsage {
	usage := MessageUsage{
		InputTokens:              intValue(w.InputTokens),
		CacheCreationInputTokens: w.CacheCreationInputTokens,
		CacheReadInputTokens:     w.CacheReadInputTokens,
		OutputTokens:             intValue(w.OutputTokens),
		ServiceTier:              w.ServiceTier,
	}

	if w.CacheCreation != nil {
		usage.CacheCreation = &CacheCreationUsage{
			Ephemeral5mInputTokens: w.CacheCreation.Ephemeral5mInputTokens,
			Ephemeral1hInputTokens: w.CacheCreation.Ephemeral1hInputTokens,
		}
	}
	if w.OutputTokensDetails != nil {
		usage.ThinkingOutputTokens = w.OutputTokensDetails.ThinkingTokens
	}
	if w.ServerToolUse != nil {
		usage.ServerToolUse = &ServerToolUsage{
			WebSearchRequests: w.ServerToolUse.WebSearchRequests,
			WebFetchRequests:  w.ServerToolUse.WebFetchRequests,
		}
	}

	return usage
}

func intValue(v *int) int {
	if v == nil {
		return 0
	}
	return *v
}

// mergeUsageWire 把 Anthropic stream 分散在 message_start 与 message_delta 的 usage 合并。
//
// wire 基础 token 使用指针保留“本事件未携带”语义：官方 Anthropic stream 的
// message_start 主要给输入计量，message_delta 再给最终输出计量；DeepSeek 当前会在
// message_delta 返回完整五字段。两种形状都必须生成同一份完整最终 facts。
func mergeUsageWire(dst *usageWire, src usageWire) {
	if src.InputTokens != nil {
		dst.InputTokens = src.InputTokens
	}
	if src.CacheCreationInputTokens != nil {
		dst.CacheCreationInputTokens = src.CacheCreationInputTokens
	}
	if src.CacheReadInputTokens != nil {
		dst.CacheReadInputTokens = src.CacheReadInputTokens
	}
	if src.OutputTokens != nil {
		dst.OutputTokens = src.OutputTokens
	}
	if src.ServiceTier != nil {
		dst.ServiceTier = src.ServiceTier
	}

	if src.CacheCreation != nil {
		if dst.CacheCreation == nil {
			dst.CacheCreation = &cacheCreationWire{}
		}
		if src.CacheCreation.Ephemeral5mInputTokens != nil {
			dst.CacheCreation.Ephemeral5mInputTokens = src.CacheCreation.Ephemeral5mInputTokens
		}
		if src.CacheCreation.Ephemeral1hInputTokens != nil {
			dst.CacheCreation.Ephemeral1hInputTokens = src.CacheCreation.Ephemeral1hInputTokens
		}
	}

	if src.OutputTokensDetails != nil {
		if dst.OutputTokensDetails == nil {
			dst.OutputTokensDetails = &outputDetailsWire{}
		}
		if src.OutputTokensDetails.ThinkingTokens != nil {
			dst.OutputTokensDetails.ThinkingTokens = src.OutputTokensDetails.ThinkingTokens
		}
	}

	if src.ServerToolUse != nil {
		if dst.ServerToolUse == nil {
			dst.ServerToolUse = &serverToolWire{}
		}
		if src.ServerToolUse.WebSearchRequests != nil {
			dst.ServerToolUse.WebSearchRequests = src.ServerToolUse.WebSearchRequests
		}
		if src.ServerToolUse.WebFetchRequests != nil {
			dst.ServerToolUse.WebFetchRequests = src.ServerToolUse.WebFetchRequests
		}
	}
}
