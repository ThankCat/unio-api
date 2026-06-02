package messages

import "encoding/json"

// knownMessageFields 是当前 MessageRequest 已建模的 typed 顶层 JSON 字段。
//
// 只列出 struct 已建模的字段；新增 typed 字段时必须同步更新。未列出的合法顶层字段
// （如 mcp_servers / service_tier / container / inference_geo / output_config）按 DEC-012
// 进入 Extensions，由 adapter 出站 Drop，而不是在 ingress silent drop 或 Reject。
var knownMessageFields = map[string]struct{}{
	"model":          {},
	"messages":       {},
	"max_tokens":     {},
	"system":         {},
	"metadata":       {},
	"stop_sequences": {},
	"stream":         {},
	"temperature":    {},
	"top_k":          {},
	"top_p":          {},
	"thinking":       {},
	"tool_choice":    {},
	"tools":          {},
}

// UnmarshalJSON 实现 decode 双轨：typed 字段 + Extensions。
//
// 按 DEC-012「协议为先」，ingress 只校验协议合法性，不因 provider 能力 Reject 合法 Anthropic
// 字段。未显式建模的合法顶层字段保留进 Extensions，provider 无法转换时由 adapter 出站 Drop。
func (req *MessageRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	// alias 技巧：避免 UnmarshalJSON 递归调用自身。
	type messageRequestAlias MessageRequest
	aux := messageRequestAlias{}
	if err := json.Unmarshal(data, &aux); err != nil {
		return err
	}

	*req = MessageRequest(aux)
	req.Extensions = make(map[string]json.RawMessage, len(raw))

	for key, value := range raw {
		if _, known := knownMessageFields[key]; known {
			continue
		}

		req.Extensions[key] = value
	}

	return nil
}

// HasExtension 判断请求是否保留了指定扩展字段。
func (req *MessageRequest) HasExtension(name string) bool {
	_, ok := req.Extensions[name]
	return ok
}

// Extension 返回指定扩展字段的原始 JSON；不存在时返回 nil。
func (req *MessageRequest) Extension(name string) json.RawMessage {
	if req == nil || req.Extensions == nil {
		return nil
	}

	return req.Extensions[name]
}
