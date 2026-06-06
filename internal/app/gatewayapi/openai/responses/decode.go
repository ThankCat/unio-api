package responses

import (
	"bytes"
	"encoding/json"
)

// knownResponsesFields 是当前 ResponsesRequest 已建模的顶层 JSON 字段。
// 新增 typed 字段时必须同步更新，否则会被误收进 Extensions。
var knownResponsesFields = map[string]struct{}{
	"model":                  {},
	"input":                  {},
	"instructions":           {},
	"max_output_tokens":      {},
	"temperature":            {},
	"top_p":                  {},
	"stream":                 {},
	"store":                  {},
	"parallel_tool_calls":    {},
	"tools":                  {},
	"tool_choice":            {},
	"reasoning":              {},
	"text":                   {},
	"include":                {},
	"metadata":               {},
	"user":                   {},
	"safety_identifier":      {},
	"previous_response_id":   {},
	"truncation":             {},
	"service_tier":           {},
	"prompt_cache_key":       {},
	"prompt_cache_retention": {},
	"background":             {},
}

// UnmarshalJSON 实现 decode 双轨：typed 字段 + Extensions。
//
// 按 DEC-012「协议为先」，ingress 只校验协议合法性，不因 provider 能力 Reject 合法字段。
// 未显式建模的合法顶层字段（如 Codex 专属 client_metadata）保留进 Extensions，
// 由 translation 决定 Drop/Reject，而不是在此返回 400。
func (req *ResponsesRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// alias 技巧：避免 UnmarshalJSON 递归调用自身。
	type responsesRequestAlias ResponsesRequest
	aux := responsesRequestAlias{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	*req = ResponsesRequest(aux)
	req.Extensions = make(map[string]json.RawMessage, len(raw))

	for key, value := range raw {
		if _, known := knownResponsesFields[key]; known {
			continue
		}
		req.Extensions[key] = value
	}

	return nil
}

// UnmarshalJSON 解析 Responses `input` union：单条字符串或 input item 数组。
//
// 其它形态（object/number 等）不在此 hard fail；保留 Raw 交给 validation 报精确的 "input" 错误。
func (in *ResponsesInput) UnmarshalJSON(data []byte) error {
	in.Raw = append(in.Raw[:0], data...)

	trimmed := bytes.TrimSpace(data)
	if len(trimmed) == 0 || bytes.Equal(trimmed, []byte("null")) {
		return nil
	}

	switch trimmed[0] {
	case '"':
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		in.Text = &s
	case '[':
		return json.Unmarshal(data, &in.Items)
	}

	return nil
}
