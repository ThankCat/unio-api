package chatbridge

import (
	"encoding/json"
	"strings"

	chatcompletions "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// responsesBody 是出站 /responses 请求体（只声明桥接会写的字段）。
type responsesBody struct {
	Model             string           `json:"model"`
	Instructions      string           `json:"instructions,omitempty"`
	Input             []map[string]any `json:"input"`
	Tools             []map[string]any `json:"tools,omitempty"`
	ToolChoice        any              `json:"tool_choice,omitempty"`
	ParallelToolCalls *bool            `json:"parallel_tool_calls,omitempty"`
	Temperature       *float64         `json:"temperature,omitempty"`
	TopP              *float64         `json:"top_p,omitempty"`
	MaxOutputTokens   *int             `json:"max_output_tokens,omitempty"`
	Reasoning         *reasoningDoc    `json:"reasoning,omitempty"`
	Text              *textFormatDoc   `json:"text,omitempty"`
	PromptCacheKey    *string          `json:"prompt_cache_key,omitempty"`
	ServiceTier       *string          `json:"service_tier,omitempty"`
	// Store 恒 false：桥接绝不依赖上游存储（previous_response_id 不支持，codex 后端也不存）。
	Store  bool `json:"store"`
	Stream bool `json:"stream"`
}

type reasoningDoc struct {
	Effort string `json:"effort"`
}

type textFormatDoc struct {
	Format json.RawMessage `json:"format,omitempty"`
}

// buildResponsesBody 把 chat 请求译成 Responses 请求体。
//
// 语义映射（与 Sub2API apicompat 对照，按本仓库契约重写）：
//   - system / developer 消息 → instructions（多条以空行拼接，保持顺序）；
//   - user / assistant 文本消息 → message item（input_text / output_text）；
//   - assistant.tool_calls → function_call item（call_id 稳定对应）；
//   - role=tool 的工具结果 → function_call_output item（按 tool_call_id 回填 call_id）；
//   - tools[function] → responses function tool；tool_choice 四种形态齐备；
//   - max_tokens / max_completion_tokens → max_output_tokens（completion 优先，语义更精确）。
//
// 不可映射字段（stop / n>1 / logprobs / logit_bias / audio / prediction）静默 Drop：
// Responses 协议没有等价物，报错会把可服务请求挡在门外（DEC-012 出站 Drop 口径）。
func buildResponsesBody(req chatcompletions.ChatRequest, stream bool) (json.RawMessage, error) {
	body := responsesBody{
		Model:             req.Model,
		Temperature:       req.Temperature,
		TopP:              req.TopP,
		ParallelToolCalls: req.ParallelToolCalls,
		PromptCacheKey:    req.PromptCacheKey,
		ServiceTier:       req.ServiceTier,
		Store:             false,
		Stream:            stream,
	}
	if req.MaxCompletionTokens != nil {
		body.MaxOutputTokens = req.MaxCompletionTokens
	} else if req.MaxTokens != nil {
		body.MaxOutputTokens = req.MaxTokens
	}
	if req.ReasoningEffort != nil && *req.ReasoningEffort != "" {
		body.Reasoning = &reasoningDoc{Effort: *req.ReasoningEffort}
	}
	if req.ResponseFormat != nil {
		if format, ok := textFormatFromResponseFormat(*req.ResponseFormat); ok {
			body.Text = &textFormatDoc{Format: format}
		}
	}

	instructions := make([]string, 0, 2)
	items := make([]map[string]any, 0, len(req.Messages))
	for _, message := range req.Messages {
		switch message.Role {
		case "system", "developer":
			if text := messageText(message); text != "" {
				instructions = append(instructions, text)
			}
		case "tool":
			callID := ""
			if message.ToolCallID != nil {
				callID = *message.ToolCallID
			}
			items = append(items, map[string]any{
				"type":    "function_call_output",
				"call_id": callID,
				"output":  messageText(message),
			})
		case "assistant":
			if text := messageText(message); text != "" {
				items = append(items, map[string]any{
					"type": "message", "role": "assistant",
					"content": []map[string]any{{"type": "output_text", "text": text}},
				})
			}
			for _, call := range message.ToolCalls {
				items = append(items, map[string]any{
					"type":      "function_call",
					"call_id":   call.ID,
					"name":      call.Function.Name,
					"arguments": call.Function.Arguments,
				})
			}
		default: // user 与未知角色都按 user 输入处理（宽进）。
			content, err := userContentParts(message)
			if err != nil {
				return nil, err
			}
			items = append(items, map[string]any{
				"type": "message", "role": "user", "content": content,
			})
		}
	}
	body.Instructions = strings.Join(instructions, "\n\n")
	body.Input = items

	for _, tool := range req.Tools {
		if tool.Type != "function" {
			continue
		}
		doc := map[string]any{
			"type": "function",
			"name": tool.Function.Name,
		}
		if tool.Function.Description != "" {
			doc["description"] = tool.Function.Description
		}
		if len(tool.Function.Parameters) > 0 {
			doc["parameters"] = json.RawMessage(tool.Function.Parameters)
		}
		if tool.Function.Strict != nil {
			doc["strict"] = *tool.Function.Strict
		}
		body.Tools = append(body.Tools, doc)
	}
	if choice, ok := toolChoiceFromChat(req.ToolChoice); ok {
		body.ToolChoice = choice
	}

	raw, err := json.Marshal(body)
	if err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterEncodeRequestFailed, err,
			failure.WithMessage("chat bridge encode responses request"),
		)
	}
	return raw, nil
}

// BuildResponsesBodyForEstimate 暴露给 authorization 阶段的估算路径：与真实出站同一套映射，
// 保证估算与实际 wire 一致（stream 位不影响 token 计数）。
func BuildResponsesBodyForEstimate(req chatcompletions.ChatRequest) (json.RawMessage, error) {
	return buildResponsesBody(req, false)
}

// messageText 提取消息纯文本：string content 直接返回；parts 数组拼接全部 text 段。
func messageText(message chatcompletions.ChatMessage) string {
	if text := message.ContentString(); text != "" {
		return text
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if err := json.Unmarshal(message.Content, &parts); err != nil {
		return ""
	}
	var builder strings.Builder
	for _, part := range parts {
		if part.Type == "text" {
			builder.WriteString(part.Text)
		}
	}
	return builder.String()
}

// userContentParts 把 user 消息 content 译成 responses 输入内容段（文本 + 图像）。
func userContentParts(message chatcompletions.ChatMessage) ([]map[string]any, error) {
	if text := message.ContentString(); text != "" || len(message.Content) == 0 {
		return []map[string]any{{"type": "input_text", "text": text}}, nil
	}
	var parts []struct {
		Type     string `json:"type"`
		Text     string `json:"text"`
		ImageURL *struct {
			URL string `json:"url"`
		} `json:"image_url"`
	}
	if err := json.Unmarshal(message.Content, &parts); err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterEncodeRequestFailed, err,
			failure.WithMessage("chat bridge decode user message content"),
		)
	}
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case "text":
			out = append(out, map[string]any{"type": "input_text", "text": part.Text})
		case "image_url":
			if part.ImageURL != nil {
				out = append(out, map[string]any{"type": "input_image", "image_url": part.ImageURL.URL})
			}
		}
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"type": "input_text", "text": ""})
	}
	return out, nil
}

// toolChoiceFromChat 映射 tool_choice 的四种形态：auto/none/required 原样，
// {"type":"function","function":{"name":X}} → {"type":"function","name":X}。
func toolChoiceFromChat(raw json.RawMessage) (any, bool) {
	if len(raw) == 0 {
		return nil, false
	}
	var mode string
	if err := json.Unmarshal(raw, &mode); err == nil {
		switch mode {
		case "auto", "none", "required":
			return mode, true
		}
		return nil, false
	}
	var functionChoice struct {
		Type     string `json:"type"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if err := json.Unmarshal(raw, &functionChoice); err == nil &&
		functionChoice.Type == "function" && functionChoice.Function.Name != "" {
		return map[string]any{"type": "function", "name": functionChoice.Function.Name}, true
	}
	return nil, false
}

// textFormatFromResponseFormat 映射 response_format → responses text.format。
func textFormatFromResponseFormat(format chatcompletions.ChatResponseFormat) (json.RawMessage, bool) {
	switch format.Type {
	case "json_object":
		return json.RawMessage(`{"type":"json_object"}`), true
	case "json_schema":
		if len(format.JSONSchema) == 0 {
			return nil, false
		}
		var schema struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
			Strict *bool           `json:"strict"`
		}
		if err := json.Unmarshal(format.JSONSchema, &schema); err != nil {
			return nil, false
		}
		doc := map[string]any{"type": "json_schema", "name": schema.Name}
		if len(schema.Schema) > 0 {
			doc["schema"] = json.RawMessage(schema.Schema)
		}
		if schema.Strict != nil {
			doc["strict"] = *schema.Strict
		}
		raw, err := json.Marshal(doc)
		if err != nil {
			return nil, false
		}
		return raw, true
	default:
		return nil, false
	}
}
