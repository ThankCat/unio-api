package messages

import (
	"bytes"
	"encoding/json"
	"sort"
	"strings"

	messagesadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/anthropic/messages"
)

// deepseekOutputConfigEfforts 把 Anthropic output_config.effort 归一为 DeepSeek 支持的 high/max。
//
// 与 OpenAI 侧 reasoning_effort 同一业务规则（minimal/low/medium/high→high，xhigh/max→max）。
// 各 adapter 包各自持有一份，保持适配层解耦，不跨 adapter 复用。出站显式归一，不依赖上游
// 隐式兼容行为（见 providers/deepseek/anthropic/protocol-and-params.md 与 adaptation.md）。
var deepseekOutputConfigEfforts = map[string]string{
	"minimal": "high",
	"low":     "high",
	"medium":  "high",
	"high":    "high",
	"xhigh":   "max",
	"max":     "max",
}

// normalizeOutputConfigEffort 归一 effort 为 DeepSeek 支持值（大小写/空白不敏感）。
// 未知枚举返回 ok=false，由调用方 Drop（让 DeepSeek 回退默认），不把非法值发上游。
func normalizeOutputConfigEffort(effort string) (string, bool) {
	normalized, ok := deepseekOutputConfigEfforts[strings.ToLower(strings.TrimSpace(effort))]
	return normalized, ok
}

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
// 见 providers/deepseek/anthropic/protocol-and-params.md §4/§10：container / service_tier /
// inference_geo / mcp_servers 出站 Drop（不写入 upstream body）。output_config 单独处理
// （归一 effort 为 high/max、剔除 format）。
var deepseekDroppedExtensions = []string{"container", "inference_geo", "mcp_servers", "service_tier"}

// dropUnsupported 按 DEC-012「协议为先」清理 DeepSeek Anthropic 无法转换的请求字段。
//
// 返回清理后的请求与被 Drop 的字段名列表（仅字段名，供脱敏审计 log）。被 Drop 的字段不会
// 进入 upstream wire；调用方仍按合法 Anthropic 协议成功请求，不再返回 400。该函数不修改传入
// req 底层的 map / slice / RawMessage：typed 字段因值传递可直接置零，引用类型在需要修改时复制。
func dropUnsupported(req messagesadapter.MessageRequest) (messagesadapter.MessageRequest, []string) {
	var dropped []string

	// anthropic-beta：DeepSeek 忽略且出站不发送（DEC-012 / protocol-and-params §2）。
	if len(req.AnthropicBeta) > 0 {
		req.AnthropicBeta = nil
		dropped = append(dropped, "anthropic-beta")
	}

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

	// Extensions：删除 DeepSeek 忽略的顶层字段；output_config 剔除 format、归一 effort。
	if cleaned, removedKeys, changed := dropIgnoredExtensions(req.Extensions); changed {
		req.Extensions = cleaned
		dropped = append(dropped, removedKeys...)
	}

	// 稳定排序，保证 log 与测试断言可预期。
	sort.Strings(dropped)

	return req, dropped
}

// dropUnsupportedContentBlocks 剔除每条消息 content 数组中 DeepSeek 不支持的 block。
// 仅在确实发生剔除时复制 messages。
func dropUnsupportedContentBlocks(messages []messagesadapter.Message) ([]messagesadapter.Message, bool) {
	var cleaned []messagesadapter.Message
	removedAny := false

	for i, msg := range messages {
		newContent, removed := dropContentBlocks(msg.Content)
		if !removed {
			continue
		}

		if cleaned == nil {
			cleaned = append([]messagesadapter.Message(nil), messages...)
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

// dropIgnoredExtensions 删除 DeepSeek 忽略的顶层 extension；output_config 剔除 format、归一 effort。
//
// removedKeys 是被 Drop 的字段名（用于审计，effort 归一是 Adapt 不计入）；changed 表示是否发生
// 任何改写（Drop 或 Adapt）。无改写时返回原 map，changed=false。
func dropIgnoredExtensions(extensions map[string]json.RawMessage) (map[string]json.RawMessage, []string, bool) {
	if len(extensions) == 0 {
		return extensions, nil, false
	}

	cleaned := make(map[string]json.RawMessage, len(extensions))
	for key, value := range extensions {
		cleaned[key] = value
	}

	var removedKeys []string
	changed := false
	for _, key := range deepseekDroppedExtensions {
		if _, ok := cleaned[key]; ok {
			delete(cleaned, key)
			removedKeys = append(removedKeys, key)
			changed = true
		}
	}

	if raw, ok := cleaned["output_config"]; ok {
		if newCfg, ocRemoved, ocChanged := adaptOutputConfig(raw); ocChanged {
			if newCfg == nil {
				delete(cleaned, "output_config")
			} else {
				cleaned["output_config"] = newCfg
			}
			removedKeys = append(removedKeys, ocRemoved...)
			changed = true
		}
	}

	if !changed {
		return extensions, nil, false
	}

	return cleaned, removedKeys, true
}

// adaptOutputConfig 处理 output_config：
//   - 剔除 format（DeepSeek 不支持 schema 语义）→ Drop，审计 "output_config.format"。
//   - 归一 effort 为 DeepSeek 支持的 high/max（Adapt，不计入 Drop 审计）；未知值剔除 effort →
//     Drop，审计 "output_config.effort"，让 DeepSeek 回退默认。
//
// 返回新 output_config（为空则 nil 表示整段移除）、被 Drop 的审计键、是否发生任何改写（含 Adapt）。
func adaptOutputConfig(raw json.RawMessage) (json.RawMessage, []string, bool) {
	var cfg map[string]json.RawMessage
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return raw, nil, false
	}

	var removedKeys []string
	changed := false

	if _, ok := cfg["format"]; ok {
		delete(cfg, "format")
		removedKeys = append(removedKeys, "output_config.format")
		changed = true
	}

	if rawEffort, ok := cfg["effort"]; ok {
		var effort string
		if err := json.Unmarshal(rawEffort, &effort); err == nil {
			if normalized, valid := normalizeOutputConfigEffort(effort); valid {
				if normalized != effort {
					if encoded, err := json.Marshal(normalized); err == nil {
						cfg["effort"] = encoded
						changed = true
					}
				}
			} else {
				delete(cfg, "effort")
				removedKeys = append(removedKeys, "output_config.effort")
				changed = true
			}
		}
		// effort 非字符串（异常形态）交由 ingress 协议校验，这里不动。
	}

	if !changed {
		return raw, nil, false
	}
	if len(cfg) == 0 {
		return nil, removedKeys, true
	}

	newCfg, err := json.Marshal(cfg)
	if err != nil {
		return raw, nil, false
	}

	return newCfg, removedKeys, true
}
