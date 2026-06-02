package chatcompletions

import (
	"encoding/json"
	"fmt"
	"strings"
)

// content part 类型（OpenAI Chat Completions message content union）。
const (
	contentPartTypeText       = "text"
	contentPartTypeRefusal    = "refusal"
	contentPartTypeImageURL   = "image_url"
	contentPartTypeInputAudio = "input_audio"
	contentPartTypeFile       = "file"
)

// chatContentPart 用于结构化校验 message content 数组中的单个 part。
//
// 只解析校验所需字段；完整 part 仍以原始 JSON 透传给 adapter，不在此处丢字段。
type chatContentPart struct {
	Type    string  `json:"type"`
	Text    *string `json:"text,omitempty"`
	Refusal *string `json:"refusal,omitempty"`
}

// messageContentState 表示一条 message content 的解析结果，供 role 级必填校验使用。
type messageContentState struct {
	// hasContent 表示 content 是否含有效内容（非空 string，或至少一个有效 text/refusal part）。
	hasContent bool
}

// validateMessageContent 校验 OpenAI message content union（string 或 content part 数组）。
//
//   - string：非空即视为有内容。
//   - 数组：逐个 part 结构化校验。text/refusal 与多模态（image_url/input_audio/file）part 均放行；
//     按 DEC-012「协议为先」，多模态 part 是合法 OpenAI 协议结构，provider 无法转换时由 adapter
//     出站 Drop，ingress 不因 provider 能力 Reject。
//
// 仅结构非法（缺 type、malformed、未知 part type）返回 chatValidationError（协议级 400）。
func validateMessageContent(content json.RawMessage, index int) (messageContentState, *chatValidationError) {
	param := fmt.Sprintf("messages.%d.content", index)
	if len(content) == 0 {
		return messageContentState{}, nil
	}

	// string 形态（含 JSON null：unmarshal 成空串，视为无内容）。
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return messageContentState{hasContent: strings.TrimSpace(asString) != ""}, nil
	}

	// 数组形态。
	var rawParts []json.RawMessage
	if err := json.Unmarshal(content, &rawParts); err != nil {
		return messageContentState{}, &chatValidationError{
			param:   param,
			message: "content must be a string or an array of content parts",
		}
	}

	hasContent := false
	for i, rawPart := range rawParts {
		partParam := fmt.Sprintf("%s.%d", param, i)

		var part chatContentPart
		if err := json.Unmarshal(rawPart, &part); err != nil {
			return messageContentState{}, &chatValidationError{
				param:   partParam,
				message: "content part must be an object",
			}
		}

		switch part.Type {
		case contentPartTypeText:
			if part.Text == nil || strings.TrimSpace(*part.Text) == "" {
				return messageContentState{}, &chatValidationError{
					param:   fmt.Sprintf("%s.text", partParam),
					message: "text content part requires non-empty text",
				}
			}
			hasContent = true
		case contentPartTypeRefusal:
			if part.Refusal == nil || strings.TrimSpace(*part.Refusal) == "" {
				return messageContentState{}, &chatValidationError{
					param:   fmt.Sprintf("%s.refusal", partParam),
					message: "refusal content part requires non-empty refusal",
				}
			}
			hasContent = true
		case contentPartTypeImageURL, contentPartTypeInputAudio, contentPartTypeFile:
			// DEC-012「协议为先」：多模态 part 是合法 OpenAI 协议结构，ingress 放行；
			// provider（如 DeepSeek）无法转换时由 adapter 出站 Drop，不在此返回 400。
			hasContent = true
		case "":
			return messageContentState{}, &chatValidationError{
				param:   fmt.Sprintf("%s.type", partParam),
				message: "content part requires type",
			}
		default:
			return messageContentState{}, &chatValidationError{
				param:   fmt.Sprintf("%s.type", partParam),
				message: fmt.Sprintf("unsupported content part type: %s", part.Type),
			}
		}
	}

	return messageContentState{hasContent: hasContent}, nil
}
