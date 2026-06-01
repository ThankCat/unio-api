package messages

import (
	"encoding/json"
	"fmt"
)

// messageRequestRejectError 表示 ingress 明确 Reject 的顶层请求字段（未登记扩展）。
type messageRequestRejectError struct {
	param   string
	message string
}

func (e *messageRequestRejectError) Error() string {
	return e.message
}

// knownMessageFields 是当前 MessageRequest 已建模的顶层 JSON 字段。
// 新增 typed 字段时必须同步更新，否则会被误收进 Extensions。
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
	"output_config":  {},
	"service_tier":   {},
	"container":      {},
	"inference_geo":  {},
}

// rejectedMessageFields 是 ingress 明确 Reject 的顶层字段。
// mcp_servers 在 DeepSeek 兼容文档中登记但被忽略；未作为登记扩展开放前明确 Reject，
// 避免客户误以为 MCP server 生效。
var rejectedMessageFields = map[string]struct{}{
	"mcp_servers": {},
}

// UnmarshalJSON 实现 decode 双轨：typed 字段 + Extensions，禁止 silent drop。
func (req *MessageRequest) UnmarshalJSON(data []byte) error {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}

	for key := range raw {
		if _, rejected := rejectedMessageFields[key]; rejected {
			return &messageRequestRejectError{
				param:   key,
				message: fmt.Sprintf("unsupported parameter: %s", key),
			}
		}
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
