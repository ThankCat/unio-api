package chatcompletions

import (
	"bytes"
	"encoding/json"
	"regexp"
	"sort"
	"strings"

	chatcompletionsadapter "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
)

// deepseekUserIDPattern / deepseekUserIDMaxLen 是 DeepSeek user_id 的合法约束。
//
// DeepSeek OpenAI 兼容上游源站的终端用户标识是顶层 user_id（字符集 [a-zA-Z0-9_-]、长度 ≤512），
// 与 OpenAI 自由格式的 user 不同（见 DEEPSEEK_OPENAI_MAPPING.md §2 与 DeepSeek API 文档）。
var deepseekUserIDPattern = regexp.MustCompile(`^[a-zA-Z0-9_-]+$`)

const deepseekUserIDMaxLen = 512

// deepseekAllowedExtensions 是 DeepSeek OpenAI origin 允许进入 upstream wire 的顶层 extension 白名单。
//
// 见 DEEPSEEK_OPENAI_MAPPING.md §2 与 DEC-012：thinking / logprobs / top_logprobs 为 Pass，
// 其余未登记或不可转换的 extension 一律 Drop（不进入 upstream body）。
var deepseekAllowedExtensions = map[string]bool{
	"thinking":     true,
	"logprobs":     true,
	"top_logprobs": true,
}

// unsupportedContentPartTypes 是 DeepSeek OpenAI origin 无法保持语义的 message content part 类型。
//
// 见 DEEPSEEK_OPENAI_MAPPING.md §3：image_url / input_audio / file 多模态 part 在出站时 Drop。
var unsupportedContentPartTypes = map[string]bool{
	"image_url":   true,
	"input_audio": true,
	"file":        true,
}

// dropUnsupported 按 DEC-012「协议为先」策略，清理 DeepSeek OpenAI 无法转换的请求字段。
//
// 返回清理后的请求与被 Drop 的字段名列表（仅字段名，供脱敏审计 log）。被 Drop 的字段不会
// 进入 upstream wire；调用方仍按合法 OpenAI 协议成功请求，不再返回 400。
//
// 该函数不修改传入 req 底层的 map / slice / RawMessage，避免对调用方产生副作用：typed 字段
// 因值传递可直接置零，引用类型（tools / messages / Extensions）在需要修改时复制。
func dropUnsupported(req chatcompletionsadapter.ChatRequest) (chatcompletionsadapter.ChatRequest, []string) {
	var dropped []string

	// 路线 C 下沉的两条 DeepSeek 方言（原 base 行为，base 去方言化后由本层承接，行为不变）：
	// max_completion_tokens → max_tokens（优先 completion tokens）、developer role → system。
	// 两者均为 Adapt，不计入 dropped 审计。
	req = adaptMaxCompletionTokens(req)
	req.Messages = adaptDeveloperRole(req.Messages)

	// typed Drop：DeepSeek deprecated 或无法保持语义的顶层 typed 字段。
	// logprobs / top_logprobs 是 Pass（见 mapping §2），不在此 Drop。
	if req.FrequencyPenalty != nil {
		req.FrequencyPenalty = nil
		dropped = append(dropped, "frequency_penalty")
	}
	if req.PresencePenalty != nil {
		req.PresencePenalty = nil
		dropped = append(dropped, "presence_penalty")
	}
	if req.ParallelToolCalls != nil {
		req.ParallelToolCalls = nil
		dropped = append(dropped, "parallel_tool_calls")
	}
	if req.ResponseFormat != nil && req.ResponseFormat.Type == "json_schema" {
		req.ResponseFormat = nil
		dropped = append(dropped, "response_format")
	}

	// reasoning_effort Adapt：归一为 DeepSeek 文档支持的 high/max（mapping §2）。
	// minimal/low/medium/high→high、xhigh/max→max；未知枚举 Drop。req 为值传递，赋新指针对调用方无副作用。
	// ReasoningDisabled（Responses 显式非 reasoning 意图）时 reasoning_effort 与 thinking:disabled 矛盾，直接 Drop。
	if req.ReasoningEffort != nil {
		if normalized, ok := normalizeReasoningEffort(*req.ReasoningEffort); ok && !req.ReasoningDisabled {
			req.ReasoningEffort = &normalized
		} else {
			req.ReasoningEffort = nil
			dropped = append(dropped, "reasoning_effort")
		}
	}

	// OpenAI 规范但 DeepSeek 不支持/不发送的标量字段：出站 Drop（mapping §2）。
	if req.N != nil {
		req.N = nil
		dropped = append(dropped, "n")
	}
	if req.Seed != nil {
		req.Seed = nil
		dropped = append(dropped, "seed")
	}
	if req.Store != nil {
		req.Store = nil
		dropped = append(dropped, "store")
	}
	if req.ServiceTier != nil {
		req.ServiceTier = nil
		dropped = append(dropped, "service_tier")
	}
	if req.Verbosity != nil {
		req.Verbosity = nil
		dropped = append(dropped, "verbosity")
	}
	if req.PromptCacheKey != nil {
		req.PromptCacheKey = nil
		dropped = append(dropped, "prompt_cache_key")
	}
	if req.PromptCacheRetention != nil {
		req.PromptCacheRetention = nil
		dropped = append(dropped, "prompt_cache_retention")
	}
	if req.SafetyIdentifier != nil {
		req.SafetyIdentifier = nil
		dropped = append(dropped, "safety_identifier")
	}
	if len(req.Modalities) > 0 {
		req.Modalities = nil
		dropped = append(dropped, "modalities")
	}

	// OpenAI 规范但 DeepSeek 不支持的复杂对象字段（原始 JSON）：出站 Drop（mapping §2）。
	if len(req.LogitBias) > 0 {
		req.LogitBias = nil
		dropped = append(dropped, "logit_bias")
	}
	if len(req.Audio) > 0 {
		req.Audio = nil
		dropped = append(dropped, "audio")
	}
	if len(req.Prediction) > 0 {
		req.Prediction = nil
		dropped = append(dropped, "prediction")
	}
	if len(req.Metadata) > 0 {
		req.Metadata = nil
		dropped = append(dropped, "metadata")
	}
	if len(req.WebSearchOptions) > 0 {
		req.WebSearchOptions = nil
		dropped = append(dropped, "web_search_options")
	}

	// deprecated legacy function 字段 Adapt → tools / tool_choice（mapping §2）。
	// 必须在 dropCustomTools 之前，转换出的 function tool 随后仍会被 custom 过滤正常放行。
	var legacyDropped []string
	req, legacyDropped = adaptLegacyFunctions(req)
	dropped = append(dropped, legacyDropped...)

	// tools：剔除 DeepSeek 不支持的 custom tool，仅保留 function tool。
	if cleaned, removed := dropCustomTools(req.Tools); removed {
		req.Tools = cleaned
		dropped = append(dropped, "tools")
	}

	// messages：剔除多模态 content part（image_url / input_audio / file）。
	if cleaned, removed := dropUnsupportedMessageParts(req.Messages); removed {
		req.Messages = cleaned
		dropped = append(dropped, "messages")
	}

	// Extensions：仅保留白名单字段，其余 Drop。
	if cleaned, removedKeys := filterExtensions(req.Extensions); len(removedKeys) > 0 {
		req.Extensions = cleaned
		dropped = append(dropped, removedKeys...)
	}

	// user → user_id Adapt：必须在 filterExtensions 之后注入 user_id，否则会被 Extensions
	// 白名单过滤掉。无法无损转换（非法字符 / 超长）时 Drop，避免 DeepSeek 上游 422。
	var userDropped bool
	req, userDropped = adaptUserID(req)
	if userDropped {
		dropped = append(dropped, "user")
	}

	// ReasoningDisabled → 注入 thinking:disabled：必须在 filterExtensions 之后，否则会被白名单
	// 过滤（thinking 在白名单内，但注入时机需晚于过滤）。已显式带 thinking 时不覆盖。
	req = adaptThinkingDisabled(req)

	// 稳定排序，保证 log 与测试断言可预期（Extensions map 迭代顺序不确定）。
	sort.Strings(dropped)

	return req, dropped
}

// adaptUserID 把 OpenAI 的 user 转换为 DeepSeek 的 user_id。
//
// DeepSeek 上游 wire 用 user_id 而非标准 user，因此本函数始终清空 req.User（base 不向 DeepSeek
// 发送标准 user 字段）。当 user 满足 DeepSeek user_id 约束（字符集 [a-zA-Z0-9_-]、长度 ≤512）时，
// 注入 Extensions 的 user_id 供 base wire merge；否则不发送并返回 dropped=true 供审计。
//
// 返回的 req 不修改调用方原始 Extensions map（按需复制）。
func adaptUserID(req chatcompletionsadapter.ChatRequest) (chatcompletionsadapter.ChatRequest, bool) {
	if req.User == nil {
		return req, false
	}

	userID := strings.TrimSpace(*req.User)
	req.User = nil // value 复制，清空对调用方无副作用

	if userID == "" {
		return req, false
	}

	if len(userID) > deepseekUserIDMaxLen || !deepseekUserIDPattern.MatchString(userID) {
		return req, true
	}

	ext := make(map[string]json.RawMessage, len(req.Extensions)+1)
	for key, value := range req.Extensions {
		ext[key] = value
	}
	if _, exists := ext["user_id"]; !exists {
		if encoded, err := json.Marshal(userID); err == nil {
			ext["user_id"] = encoded
		}
	}
	req.Extensions = ext

	return req, false
}

// deepseekThinkingDisabled 是 DeepSeek 关闭思考模式的 vendor 字段值（thinking 默认 enabled）。
var deepseekThinkingDisabled = json.RawMessage(`{"type":"disabled"}`)

// adaptThinkingDisabled 在 ReasoningDisabled（Responses 显式非 reasoning 意图）时注入 thinking:disabled。
//
// DeepSeek thinking 默认 enabled，Responses 非 reasoning run 不注入则上游仍产生 CoT（额外成本）。
// 客户已显式带 thinking（如 chat ingress 的 extra_body.thinking）时不覆盖，尊重显式意图。
// req 为值传递、按需复制 Extensions，对调用方无副作用。
func adaptThinkingDisabled(req chatcompletionsadapter.ChatRequest) chatcompletionsadapter.ChatRequest {
	if !req.ReasoningDisabled {
		return req
	}
	if _, exists := req.Extensions["thinking"]; exists {
		return req
	}

	ext := make(map[string]json.RawMessage, len(req.Extensions)+1)
	for key, value := range req.Extensions {
		ext[key] = value
	}
	ext["thinking"] = append(json.RawMessage(nil), deepseekThinkingDisabled...)
	req.Extensions = ext

	return req
}

// dropCustomTools 剔除 type=custom 的 tool，仅保留 DeepSeek 支持的 function tool。
// removed 为 true 表示发生了剔除；返回的 slice 是新分配的，不影响调用方。
func dropCustomTools(tools []chatcompletionsadapter.ChatTool) ([]chatcompletionsadapter.ChatTool, bool) {
	if len(tools) == 0 {
		return tools, false
	}

	kept := make([]chatcompletionsadapter.ChatTool, 0, len(tools))
	removed := false
	for _, tool := range tools {
		if tool.Type == "custom" {
			removed = true
			continue
		}
		kept = append(kept, tool)
	}

	if !removed {
		return tools, false
	}
	if len(kept) == 0 {
		return nil, true
	}

	return kept, true
}

// dropUnsupportedMessageParts 剔除每条消息 content 数组中的多模态 part。
// 仅在确实发生剔除时复制 messages，避免无谓分配。
func dropUnsupportedMessageParts(messages []chatcompletionsadapter.ChatMessage) ([]chatcompletionsadapter.ChatMessage, bool) {
	var cleaned []chatcompletionsadapter.ChatMessage
	removedAny := false

	for i, msg := range messages {
		newContent, removed := dropContentParts(msg.Content)
		if !removed {
			continue
		}

		if cleaned == nil {
			cleaned = append([]chatcompletionsadapter.ChatMessage(nil), messages...)
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

// dropContentParts 解析 content union：string 形态原样返回；array 形态剔除多模态 part 后重新编码。
// 结构异常（malformed）不在此处理，交由 ingress 协议校验，返回原值不标记剔除。
func dropContentParts(content json.RawMessage) (json.RawMessage, bool) {
	data := bytes.TrimSpace(content)
	if len(data) == 0 || data[0] != '[' {
		return content, false
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return content, false
	}

	kept := make([]json.RawMessage, 0, len(parts))
	removed := false
	for _, part := range parts {
		var head struct {
			Type string `json:"type"`
		}
		if err := json.Unmarshal(part, &head); err != nil {
			kept = append(kept, part)
			continue
		}
		if unsupportedContentPartTypes[head.Type] {
			removed = true
			continue
		}
		kept = append(kept, part)
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

// filterExtensions 仅保留白名单内的 extension，其余 Drop。
// removedKeys 是被 Drop 的 key 列表（用于审计）；无剔除时返回原 map。
func filterExtensions(extensions map[string]json.RawMessage) (map[string]json.RawMessage, []string) {
	if len(extensions) == 0 {
		return extensions, nil
	}

	var removedKeys []string
	cleaned := make(map[string]json.RawMessage, len(extensions))
	for key, value := range extensions {
		if deepseekAllowedExtensions[key] {
			cleaned[key] = value
			continue
		}
		removedKeys = append(removedKeys, key)
	}

	if len(removedKeys) == 0 {
		return extensions, nil
	}

	return cleaned, removedKeys
}
