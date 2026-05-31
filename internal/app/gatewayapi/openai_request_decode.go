package gatewayapi

import (
	"encoding/json"
	"fmt"
)

// chatRequestRejectError 表示 Compatibility Matrix 明确 Reject 的顶层请求字段。
type chatRequestRejectError struct {
	param   string
	message string
}

func (e *chatRequestRejectError) Error() string {
	return e.message
}

// knownChatCompletionFields 是当前 ChatCompletionRequest 已建模的顶层 JSON 字段。
// 新增 typed 字段时，必须同步更新这里，否则会被误收进 Extensions。
var knownChatCompletionFields = map[string]struct{}{
	"model":                 {},
	"messages":              {},
	"stream":                {},
	"stream_options":        {},
	"temperature":           {},
	"top_p":                 {},
	"max_tokens":            {},
	"presence_penalty":      {},
	"frequency_penalty":     {},
	"stop":                  {},
	"user":                  {},
	"max_completion_tokens": {},
	"reasoning_effort":      {},
	"tools":                 {},
	"tool_choice":           {},
	"parallel_tool_calls":   {},
	"response_format":       {},
}

// rejectedChatCompletionFields 是 Compatibility Matrix 标记为 Reject 的顶层字段。
var rejectedChatCompletionFields = map[string]struct{}{
	"service_tier":       {},
	"store":              {},
	"web_search_options": {},
}

// UnmarshalJSON 实现 decode 双轨：typed 字段 + Extensions，禁止 silent drop。
func (req *ChatCompletionRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	for key := range raw {
		if _, rejected := rejectedChatCompletionFields[key]; rejected {
			return &chatRequestRejectError{
				param:   key,
				message: fmt.Sprintf("unsupported parameter: %s", key),
			}
		}
	}

	// alias 技巧：避免 UnmarshalJSON 递归调用自身。
	type chatCompletionRequestAlias ChatCompletionRequest
	aux := chatCompletionRequestAlias{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	*req = ChatCompletionRequest(aux)
	req.Extensions = make(map[string]json.RawMessage, len(raw))

	for key, value := range raw {
		if _, known := knownChatCompletionFields[key]; known {
			continue
		}

		req.Extensions[key] = value
	}

	return nil
}

// HasExtension 判断请求是否保留了指定扩展字段。
func (req *ChatCompletionRequest) HasExtension(name string) bool {
	_, ok := req.Extensions[name]
	return ok
}

// Extension 返回指定扩展字段的原始 JSON；不存在时返回 nil。
func (req *ChatCompletionRequest) Extension(name string) json.RawMessage {
	if req == nil || req.Extensions == nil {
		return nil
	}

	return req.Extensions[name]
}
