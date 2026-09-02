package chatbridge

import (
	"encoding/json"
	"strings"

	chatcompletions "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/chatcompletions"
	responses "github.com/ThankCat/unio-gateway/internal/core/adapter/openai/responses"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
)

// wireResponsesResult 是译回 chat 所需的 Responses 响应最小形状。
type wireResponsesResult struct {
	ID                string `json:"id"`
	Model             string `json:"model"`
	Status            string `json:"status"`
	CreatedAt         int64  `json:"created_at"`
	IncompleteDetails *struct {
		Reason string `json:"reason"`
	} `json:"incomplete_details"`
	ServiceTier string             `json:"service_tier"`
	Output      []wireResponsesOut `json:"output"`
}

type wireResponsesOut struct {
	Type    string `json:"type"`
	Role    string `json:"role"`
	CallID  string `json:"call_id"`
	Name    string `json:"name"`
	Args    string `json:"arguments"`
	Content []struct {
		Type    string  `json:"type"`
		Text    string  `json:"text"`
		Refusal *string `json:"refusal"`
	} `json:"content"`
}

// chatResponseFromResponses 把非流式 Responses 结果译回 chat 形状。
// 账务事实（Usage / ServiceTier / Finish）沿用 responses adapter 的同一次解析（resp.Facts），
// usage 降级口径：attribution 等 Responses 独有明细不进 chat usage，结算取值不受影响
// （settlement 只消费 Facts）。
func chatResponseFromResponses(resp *responses.Response) (*chatcompletions.ChatResponse, error) {
	var parsed wireResponsesResult
	if err := json.Unmarshal(resp.Raw, &parsed); err != nil {
		return nil, failure.Wrap(
			failure.CodeAdapterDecodeResponseFailed, err,
			failure.WithMessage("chat bridge decode responses payload"),
		)
	}

	var content strings.Builder
	var refusal *string
	toolCalls := make([]chatcompletions.ChatToolCall, 0)
	for _, item := range parsed.Output {
		switch item.Type {
		case "message":
			for _, part := range item.Content {
				switch part.Type {
				case "output_text":
					content.WriteString(part.Text)
				case "refusal":
					if part.Refusal != nil {
						refusal = part.Refusal
					} else if part.Text != "" {
						text := part.Text
						refusal = &text
					}
				}
			}
		case "function_call", "custom_tool_call":
			// Codex 私有 namespace 的 custom_tool_call 与标准 function_call 同构（call_id/name/arguments）。
			toolCalls = append(toolCalls, chatcompletions.ChatToolCall{
				ID:   item.CallID,
				Type: "function",
				Function: chatcompletions.ChatToolCallFunction{
					Name:      item.Name,
					Arguments: item.Args,
				},
			})
		}
	}

	out := &chatcompletions.ChatResponse{
		ID:           parsed.ID,
		Model:        resp.Model,
		Content:      content.String(),
		ToolCalls:    toolCalls,
		FinishReason: chatFinishReason(parsed, len(toolCalls) > 0),
		Usage:        resp.Usage,
		Created:      parsed.CreatedAt,
		Refusal:      refusal,
		Upstream:     resp.Upstream,
		Facts:        resp.Facts,
	}
	if parsed.ServiceTier != "" {
		tier := parsed.ServiceTier
		out.ServiceTier = &tier
	}
	return out, nil
}

// chatFinishReason 把 Responses 终态映射成 chat finish_reason 稳定值。
func chatFinishReason(parsed wireResponsesResult, hasToolCalls bool) string {
	if hasToolCalls {
		return "tool_calls"
	}
	if parsed.Status == "incomplete" && parsed.IncompleteDetails != nil {
		switch parsed.IncompleteDetails.Reason {
		case "max_output_tokens", "max_tokens":
			return "length"
		case "content_filter":
			return "content_filter"
		}
	}
	return "stop"
}
