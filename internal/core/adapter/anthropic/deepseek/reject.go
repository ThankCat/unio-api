package deepseek

import (
	"bytes"
	"encoding/json"
	"fmt"

	anthropicadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic"
	"github.com/ThankCat/unio-api/internal/platform/failure"
)

// deepseekSupportedContentBlocks 是 DeepSeek Anthropic endpoint 支持的 content block 类型。
// 见 DEEPSEEK_ANTHROPIC_MAPPING.md §5；其余 block 必须前置 Reject。
var deepseekSupportedContentBlocks = map[string]bool{
	"text":                   true,
	"thinking":               true,
	"tool_use":               true,
	"tool_result":            true,
	"server_tool_use":        true,
	"web_search_tool_result": true,
}

// rejectUnsupportedRequest 在调用 DeepSeek Anthropic 上游前，拒绝 DeepSeek 无法保持语义的字段。
//
// 关键依据是 2026-06-01 黑盒冻结：DeepSeek 对 image 等不支持的 content block 返回 HTTP 200 并
// 静默丢弃（正文假装"看不到图片"），这种 silent-ignore 会让客户误以为内容已处理，因此必须前置
// Reject 而不是透传给上游。container / service_tier / inference_geo / output_config.format 等
// DeepSeek 忽略且可能误导客户预期的字段，按 mapping §4/§10 默认 Reject。
func rejectUnsupportedRequest(req anthropicadapter.MessageRequest) error {
	for i, msg := range req.Messages {
		if err := rejectMessageContent(i, msg.Content); err != nil {
			return err
		}
	}

	if err := rejectTools(req.Tools); err != nil {
		return err
	}

	if err := rejectMetadata(req.Metadata); err != nil {
		return err
	}

	return rejectIgnoredExtensions(req.Extensions)
}

// rejectMessageContent 校验单条消息的 content：string shorthand 放行，数组中每个 block 类型
// 必须是 DeepSeek 支持的类型。
func rejectMessageContent(msgIndex int, raw json.RawMessage) error {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 || data[0] != '[' {
		// string shorthand 或空（ingress 已校验非空）；非数组无需逐 block 检查。
		return nil
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(data, &blocks); err != nil {
		return unsupportedRequest(fmt.Sprintf("messages.%d.content", msgIndex), "message content array is malformed")
	}

	for j, block := range blocks {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(block, &head); err != nil {
			return unsupportedRequest(fmt.Sprintf("messages.%d.content.%d", msgIndex, j), "content block must be an object")
		}
		if !deepseekSupportedContentBlocks[head.Type] {
			return unsupportedRequest(
				fmt.Sprintf("messages.%d.content.%d.type", msgIndex, j),
				fmt.Sprintf("content block type %q is not supported by this model", head.Type),
			)
		}
	}

	return nil
}

// rejectTools 拒绝 DeepSeek 不支持的内置 server tool；客户 custom tool 放行。
func rejectTools(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var tools []json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return unsupportedRequest("tools", "tools must be an array")
	}

	for i, tool := range tools {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(tool, &head); err != nil {
			return unsupportedRequest(fmt.Sprintf("tools.%d", i), "tool must be an object")
		}
		// 客户 custom tool 不带 type 或 type=custom；其余内置 server tool 全部 Reject。
		if head.Type != "" && head.Type != "custom" {
			return unsupportedRequest(
				fmt.Sprintf("tools.%d.type", i),
				fmt.Sprintf("tool type %q is not supported by this model", head.Type),
			)
		}
	}

	return nil
}

// rejectMetadata 拒绝 user_id 之外的 metadata 字段（DeepSeek 忽略，默认 Reject）。
func rejectMetadata(raw json.RawMessage) error {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return unsupportedRequest("metadata", "metadata must be an object")
	}

	for key := range fields {
		if key != "user_id" {
			return unsupportedRequest("metadata."+key, "only metadata.user_id is supported by this model")
		}
	}

	return nil
}

// rejectIgnoredExtensions 拒绝 DeepSeek 忽略且会误导客户预期的顶层字段。
func rejectIgnoredExtensions(extensions map[string]json.RawMessage) error {
	for _, key := range []string{"container", "service_tier", "inference_geo"} {
		if _, ok := extensions[key]; ok {
			return unsupportedRequest(key, fmt.Sprintf("%s is not supported by this model", key))
		}
	}

	if raw, ok := extensions["output_config"]; ok {
		var cfg map[string]json.RawMessage
		if err := json.Unmarshal(raw, &cfg); err != nil {
			return unsupportedRequest("output_config", "output_config must be an object")
		}
		if _, ok := cfg["format"]; ok {
			return unsupportedRequest("output_config.format", "output_config.format is not supported by this model")
		}
	}

	return nil
}

// unsupportedRequest 构造稳定的"请求字段不被支持"错误；param 用于 HTTP 层定位到具体字段。
func unsupportedRequest(param, message string) error {
	return failure.New(
		failure.CodeAdapterRequestUnsupported,
		failure.WithMessage(message),
		failure.WithField("param", param),
	)
}
