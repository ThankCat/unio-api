package deepseek

import (
	"bytes"
	"encoding/json"
	"sort"

	anthropicadapter "github.com/ThankCat/unio-api/internal/core/adapter/anthropic"
)

// deepseekSupportedContentBlocks 是 DeepSeek Anthropic endpoint 支持的 content block 类型。
//
// 见 DEEPSEEK_ANTHROPIC_MAPPING.md §5 与 DEC-012：其余 block（image/document/redacted_thinking/
// MCP/container_upload 等）在出站时 Drop，不写入 upstream content。
var deepseekSupportedContentBlocks = map[string]bool{
	"text":                   true,
	"thinking":               true,
	"tool_use":               true,
	"tool_result":            true,
	"server_tool_use":        true,
	"web_search_tool_result": true,
}

// deepseekDroppedExtensions 是 DeepSeek 忽略且 Unio 不透传的顶层 extension。
//
// 见 DEEPSEEK_ANTHROPIC_MAPPING.md §4/§10：container / service_tier / inference_geo / mcp_servers
// 出站 Drop（不写入 upstream body）。output_config 单独处理（保留 effort，剔除 format）。
var deepseekDroppedExtensions = []string{"container", "inference_geo", "mcp_servers", "service_tier"}

// dropUnsupported 按 DEC-012「协议为先」清理 DeepSeek Anthropic 无法转换的请求字段。
//
// 返回清理后的请求与被 Drop 的字段名列表（仅字段名，供脱敏审计 log）。被 Drop 的字段不会
// 进入 upstream wire；调用方仍按合法 Anthropic 协议成功请求，不再返回 400。该函数不修改传入
// req 底层的 map / slice / RawMessage：typed 字段因值传递可直接置零，引用类型在需要修改时复制。
func dropUnsupported(req anthropicadapter.MessageRequest) (anthropicadapter.MessageRequest, []string) {
	var dropped []string

	// typed Drop：DeepSeek 忽略 top_k。
	if req.TopK != nil {
		req.TopK = nil
		dropped = append(dropped, "top_k")
	}

	// messages：剔除 DeepSeek 不支持的 content block。
	if cleaned, removed := dropUnsupportedContentBlocks(req.Messages); removed {
		req.Messages = cleaned
		dropped = append(dropped, "messages")
	}

	// tools：剔除内置 server tool，仅保留 client custom tool。
	if cleaned, removed := dropServerTools(req.Tools); removed {
		req.Tools = cleaned
		dropped = append(dropped, "tools")
	}

	// metadata：仅保留 user_id。
	if cleaned, removed := dropNonUserIDMetadata(req.Metadata); removed {
		req.Metadata = cleaned
		dropped = append(dropped, "metadata")
	}

	// Extensions：删除 DeepSeek 忽略的顶层字段；output_config 仅保留 effort（剔除 format）。
	if cleaned, removedKeys := dropIgnoredExtensions(req.Extensions); len(removedKeys) > 0 {
		req.Extensions = cleaned
		dropped = append(dropped, removedKeys...)
	}

	// 稳定排序，保证 log 与测试断言可预期。
	sort.Strings(dropped)

	return req, dropped
}

// dropUnsupportedContentBlocks 剔除每条消息 content 数组中 DeepSeek 不支持的 block。
// 仅在确实发生剔除时复制 messages。
func dropUnsupportedContentBlocks(messages []anthropicadapter.Message) ([]anthropicadapter.Message, bool) {
	var cleaned []anthropicadapter.Message
	removedAny := false

	for i, msg := range messages {
		newContent, removed := dropContentBlocks(msg.Content)
		if !removed {
			continue
		}

		if cleaned == nil {
			cleaned = append([]anthropicadapter.Message(nil), messages...)
		}
		msgCopy := msg
		msgCopy.Content = newContent
		cleaned[i] = msgCopy
		removedAny = true
	}

	if !removedAny {
		return messages, false
	}

	return cleaned, true
}

// dropContentBlocks 解析 content union：string shorthand 原样返回；array 形态剔除不支持 block。
// 结构异常不在此处理，交由 ingress 协议校验，返回原值不标记剔除。
func dropContentBlocks(content json.RawMessage) (json.RawMessage, bool) {
	data := bytes.TrimSpace(content)
	if len(data) == 0 || data[0] != '[' {
		return content, false
	}

	var blocks []json.RawMessage
	if err := json.Unmarshal(data, &blocks); err != nil {
		return content, false
	}

	kept := make([]json.RawMessage, 0, len(blocks))
	removed := false
	for _, block := range blocks {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(block, &head); err != nil {
			kept = append(kept, block)
			continue
		}
		if !deepseekSupportedContentBlocks[head.Type] {
			removed = true
			continue
		}
		kept = append(kept, block)
	}

	if !removed {
		return content, false
	}

	newContent, err := json.Marshal(kept)
	if err != nil {
		return content, false
	}

	return newContent, true
}

// dropServerTools 剔除内置 server tool，仅保留 client custom tool（无 type 或 type=custom）。
func dropServerTools(raw json.RawMessage) (json.RawMessage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw, false
	}

	var tools []json.RawMessage
	if err := json.Unmarshal(raw, &tools); err != nil {
		return raw, false
	}

	kept := make([]json.RawMessage, 0, len(tools))
	removed := false
	for _, tool := range tools {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(tool, &head); err != nil {
			kept = append(kept, tool)
			continue
		}
		if head.Type != "" && head.Type != "custom" {
			removed = true
			continue
		}
		kept = append(kept, tool)
	}

	if !removed {
		return raw, false
	}
	if len(kept) == 0 {
		return nil, true
	}

	newTools, err := json.Marshal(kept)
	if err != nil {
		return raw, false
	}

	return newTools, true
}

// dropNonUserIDMetadata 仅保留 metadata.user_id，剔除其余字段。
func dropNonUserIDMetadata(raw json.RawMessage) (json.RawMessage, bool) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return raw, false
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return raw, false
	}

	kept := make(map[string]json.RawMessage, len(fields))
	removed := false
	for key, value := range fields {
		if key == "user_id" {
			kept[key] = value
			continue
		}
		removed = true
	}

	if !removed {
		return raw, false
	}
	if len(kept) == 0 {
		return nil, true
	}

	newMeta, err := json.Marshal(kept)
	if err != nil {
		return raw, false
	}

	return newMeta, true
}

// dropIgnoredExtensions 删除 DeepSeek 忽略的顶层 extension；output_config 保留 effort、剔除 format。
// removedKeys 是被 Drop 的字段名（用于审计）；无剔除时返回原 map。
func dropIgnoredExtensions(extensions map[string]json.RawMessage) (map[string]json.RawMessage, []string) {
	if len(extensions) == 0 {
		return extensions, nil
	}

	cleaned := make(map[string]json.RawMessage, len(extensions))
	for key, value := range extensions {
		cleaned[key] = value
	}

	var removedKeys []string
	for _, key := range deepseekDroppedExtensions {
		if _, ok := cleaned[key]; ok {
			delete(cleaned, key)
			removedKeys = append(removedKeys, key)
		}
	}

	if raw, ok := cleaned["output_config"]; ok {
		if newCfg, droppedFormat := dropOutputConfigFormat(raw); droppedFormat {
			if newCfg == nil {
				delete(cleaned, "output_config")
			} else {
				cleaned["output_config"] = newCfg
			}
			removedKeys = append(removedKeys, "output_config.format")
		}
	}

	if len(removedKeys) == 0 {
		return extensions, nil
	}

	return cleaned, removedKeys
}

// dropOutputConfigFormat 从 output_config 中剔除 format，保留 effort 等其余字段。
// 若剔除后 output_config 为空，返回 nil 表示整个 output_config 应被移除。
func dropOutputConfigFormat(raw json.RawMessage) (json.RawMessage, bool) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return raw, false
	}
	if _, ok := cfg["format"]; !ok {
		return raw, false
	}

	delete(cfg, "format")
	if len(cfg) == 0 {
		return nil, true
	}

	newCfg, err := json.Marshal(cfg)
	if err != nil {
		return raw, false
	}

	return newCfg, true
}
