package chatcompletions

import "encoding/json"

// knownChatCompletionFields 是当前 ChatCompletionRequest 已建模的顶层 JSON 字段。
// 新增 typed 字段时，必须同步更新这里，否则会被误收进 Extensions。
var knownChatCompletionFields = map[string]struct{}{
	"model":                  {},
	"messages":               {},
	"stream":                 {},
	"stream_options":         {},
	"temperature":            {},
	"top_p":                  {},
	"max_tokens":             {},
	"presence_penalty":       {},
	"frequency_penalty":      {},
	"stop":                   {},
	"user":                   {},
	"max_completion_tokens":  {},
	"reasoning_effort":       {},
	"tools":                  {},
	"tool_choice":            {},
	"parallel_tool_calls":    {},
	"response_format":        {},
	"n":                      {},
	"seed":                   {},
	"logprobs":               {},
	"top_logprobs":           {},
	"logit_bias":             {},
	"modalities":             {},
	"audio":                  {},
	"prediction":             {},
	"metadata":               {},
	"store":                  {},
	"service_tier":           {},
	"verbosity":              {},
	"prompt_cache_key":       {},
	"prompt_cache_retention": {},
	"safety_identifier":      {},
	"web_search_options":     {},
	"function_call":          {},
	"functions":              {},
}

// UnmarshalJSON 实现 decode 双轨：typed 字段 + Extensions。
//
// 按 DEC-012「协议为先」，ingress 只校验协议合法性，不因 provider 能力 Reject 合法 OpenAI
// 字段。未显式建模的合法顶层字段（如 service_tier / store / web_search_options）保留进
// Extensions，provider 无法转换时由 adapter 出站 Drop，而不是在此返回 400。
func (req *ChatCompletionRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
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
