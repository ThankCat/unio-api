package codexresponses

import (
	"encoding/json"
	"strings"

	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// codexUnsupportedFields 是 Codex 订阅后端明确拒收的请求字段（400 "Unsupported parameter"）。
//
// 真机实测 + sub2api 同款清单（openAICodexOAuthUnsupportedFields）。静默剔除而非报错：
// 订阅通道按 CLI 的规矩来，采样参数在该后端本就不可用，兼容性优先（与 sub2api 决策一致）。
var codexUnsupportedFields = []string{
	"max_output_tokens",
	"max_completion_tokens",
	"temperature",
	"top_p",
	"frequency_penalty",
	"presence_penalty",
	"stream_options",
}

// normalizeCodexRequest 把出站请求体改写成 Codex 订阅后端能接受的形态（真机实测契约）：
//
//   - store 必须显式 false（显式 true 也覆盖，否则 400 "Store must be set to false"）；
//   - stream 必须与出站形态一致（后端只收流式，非流式入站由 adapter 聚合，这里统一置位）；
//   - 剔除不支持字段（codexUnsupportedFields）；
//   - input 必须是结构化数组：字符串简写转成单条 user message（400 实测）。文本原样保留
//     （含空串/空白），是否可服务由上游裁决——这里只修形状，不改语义；
//   - role:system 不被接受：文本并进 instructions（已有则换行拼接），消息本体降级为
//     developer 保留在 input（Responses JSON mode 依赖 input 内可见指令，与 sub2api 对齐）；
//   - 启用 reasoning 时确保 include 含 reasoning.encrypted_content：codex store=false 不存
//     服务端状态，多轮回放 reasoning item 依赖响应带回加密思维链（CLI 每笔自带，
//     SDK 客户不知道这个坑——第一轮正常第二轮才炸，在此替客户端补齐，与 sub2api 对齐）。
//
// 无需改写时原文返回（探测与 codex CLI 请求本就合规，零转换）；非法 JSON 交上游拒绝，不在此猜。
func normalizeCodexRequest(body []byte, stream bool) ([]byte, error) {
	var payload map[string]any
	if err := json.Unmarshal(body, &payload); err != nil {
		return body, nil
	}
	changed := false

	if v, ok := payload["store"].(bool); !ok || v {
		payload["store"] = false
		changed = true
	}
	if v, ok := payload["stream"].(bool); !ok || v != stream {
		payload["stream"] = stream
		changed = true
	}
	for _, key := range codexUnsupportedFields {
		if _, ok := payload[key]; ok {
			delete(payload, key)
			changed = true
		}
	}

	if raw, ok := payload["input"].(string); ok {
		payload["input"] = []any{map[string]any{
			"type": "message",
			"role": "user",
			"content": []any{
				map[string]any{"type": "input_text", "text": raw},
			},
		}}
		changed = true
	}

	if promoteSystemMessages(payload) {
		changed = true
	}
	if ensureReasoningInclude(payload) {
		changed = true
	}

	if !changed {
		return body, nil
	}
	out, err := json.Marshal(payload)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterEncodeRequestFailed, err,
			failure.WithMessage("codex responses adapter encode normalized request"),
		)
	}
	return out, nil
}

// reasoningEncryptedContentInclude 是 reasoning 加密思维链的 include 项：store=false 时
// 上游把思维链加密后随响应带回，客户端下一轮原样回放即可恢复推理上下文。
const reasoningEncryptedContentInclude = "reasoning.encrypted_content"

// ensureReasoningInclude 在请求启用 reasoning（字段存在且非 null）时，确保 include
// 含 reasoning.encrypted_content。include 已含该项零改动；include 为非法形态（非数组）
// 不遮掩不修补，交上游拒绝。返回是否有改动。
func ensureReasoningInclude(payload map[string]any) bool {
	if reasoning, ok := payload["reasoning"]; !ok || reasoning == nil {
		return false
	}
	existing, present := payload["include"]
	if !present || existing == nil {
		payload["include"] = []any{reasoningEncryptedContentInclude}
		return true
	}
	items, ok := existing.([]any)
	if !ok {
		return false
	}
	for _, item := range items {
		if s, _ := item.(string); s == reasoningEncryptedContentInclude {
			return false
		}
	}
	payload["include"] = append(items, reasoningEncryptedContentInclude)
	return true
}

// promoteSystemMessages 把 input 里 role:system 的消息文本并进 instructions，
// 消息本体降级为 developer 留在 input。返回是否有改动。
func promoteSystemMessages(payload map[string]any) bool {
	items, ok := payload["input"].([]any)
	if !ok {
		return false
	}
	changed := false
	var promoted []string
	for _, item := range items {
		message, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if role, _ := message["role"].(string); role != "system" {
			continue
		}
		if text := messageText(message); text != "" {
			promoted = append(promoted, text)
		}
		message["role"] = "developer"
		changed = true
	}
	if len(promoted) > 0 {
		joined := strings.Join(promoted, "\n\n")
		if existing, _ := payload["instructions"].(string); strings.TrimSpace(existing) != "" {
			payload["instructions"] = existing + "\n\n" + joined
		} else {
			payload["instructions"] = joined
		}
	}
	return changed
}

// messageText 提取 message 的纯文本（content 为字符串或 input_text/text 段数组）。
func messageText(message map[string]any) string {
	switch content := message["content"].(type) {
	case string:
		return strings.TrimSpace(content)
	case []any:
		var parts []string
		for _, seg := range content {
			m, ok := seg.(map[string]any)
			if !ok {
				continue
			}
			if text, _ := m["text"].(string); strings.TrimSpace(text) != "" {
				parts = append(parts, strings.TrimSpace(text))
			}
		}
		return strings.Join(parts, "\n")
	}
	return ""
}
