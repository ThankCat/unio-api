package messages

import "encoding/json"

// 权威首字判定（Anthropic Messages）。判定是事件的纯函数，理由见
// internal/core/adapter/openai/chatcompletions/first_token.go。

// FirstTokenPayload 返回事件携带的生成负载；非空即「算首字」。
//
// 算首字：非空 text/thinking/input JSON delta、携带工具名称的 tool-use block。
// 不算首字：message_start、空 content_block_start、ping、signature-only、
// usage/message delta、block stop、message stop、error。
func FirstTokenPayload(event MessageStreamEvent) string {
	switch event.Type {
	case "content_block_start":
		var envelope struct {
			ContentBlock struct {
				Type     string `json:"type"`
				Text     string `json:"text"`
				Thinking string `json:"thinking"`
				Name     string `json:"name"`
			} `json:"content_block"`
		}
		if json.Unmarshal(event.Data, &envelope) != nil {
			return ""
		}
		switch envelope.ContentBlock.Type {
		case "text":
			return envelope.ContentBlock.Text
		case "thinking":
			return envelope.ContentBlock.Thinking
		case "tool_use", "server_tool_use":
			return envelope.ContentBlock.Name
		}
	case "content_block_delta":
		var envelope struct {
			Delta struct {
				Type        string `json:"type"`
				Text        string `json:"text"`
				Thinking    string `json:"thinking"`
				PartialJSON string `json:"partial_json"`
			} `json:"delta"`
		}
		if json.Unmarshal(event.Data, &envelope) != nil {
			return ""
		}
		// signature_delta 只是思维签名，不是生成内容，因此不在此列。
		switch envelope.Delta.Type {
		case "text_delta":
			return envelope.Delta.Text
		case "thinking_delta":
			return envelope.Delta.Thinking
		case "input_json_delta":
			return envelope.Delta.PartialJSON
		}
	}
	return ""
}
