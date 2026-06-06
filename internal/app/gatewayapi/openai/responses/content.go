package responses

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Responses content part 类型（input message content union）。
const (
	contentPartInputText  = "input_text"
	contentPartOutputText = "output_text"
	contentPartInputImage = "input_image"
	contentPartInputFile  = "input_file"
	contentPartInputAudio = "input_audio"
	contentPartRefusal    = "refusal"
)

// responsesContentPart 用于结构化校验 message content 数组中的单个 part。
//
// 只解析校验所需字段；完整 part 仍以原始 JSON 透传给 translation，不在此处丢字段。
type responsesContentPart struct {
	Type    string  `json:"type"`
	Text    *string `json:"text,omitempty"`
	Refusal *string `json:"refusal,omitempty"`
}

// contentState 表示一条 message content 的解析结果，供 role 级必填校验使用。
type contentState struct {
	// hasContent 表示 content 是否含有效内容（非空 string，或至少一个有效 part）。
	hasContent bool
}

// validateInputContent 校验 Responses message content union（string 或 content part 数组）。
//
//   - string：非空即视为有内容。
//   - 数组：逐个 part 结构化校验。文本类（input_text/output_text/refusal）要求非空；
//     多模态（input_image/input_file/input_audio）是合法协议结构，ingress 放行（DEC-012），
//     provider 无法转换时由 adapter 出站 Drop。
//
// 仅结构非法（缺 type、malformed、未知 part type）返回 responsesValidationError（协议级 400）。
func validateInputContent(content json.RawMessage, param string) (contentState, *responsesValidationError) {
	if len(content) == 0 {
		return contentState{}, nil
	}

	// string 形态（含 JSON null：unmarshal 成空串，视为无内容）。
	var asString string
	if err := json.Unmarshal(content, &asString); err == nil {
		return contentState{hasContent: strings.TrimSpace(asString) != ""}, nil
	}

	// 数组形态。
	var rawParts []json.RawMessage
	if err := json.Unmarshal(content, &rawParts); err != nil {
		return contentState{}, &responsesValidationError{
			param:   param,
			message: "content must be a string or an array of content parts",
		}
	}

	hasContent := false
	for i, rawPart := range rawParts {
		partParam := fmt.Sprintf("%s.%d", param, i)

		var part responsesContentPart
		if err := json.Unmarshal(rawPart, &part); err != nil {
			return contentState{}, &responsesValidationError{
				param:   partParam,
				message: "content part must be an object",
			}
		}

		switch part.Type {
		case contentPartInputText, contentPartOutputText:
			if part.Text == nil || strings.TrimSpace(*part.Text) == "" {
				return contentState{}, &responsesValidationError{
					param:   fmt.Sprintf("%s.text", partParam),
					message: "text content part requires non-empty text",
				}
			}
			hasContent = true
		case contentPartRefusal:
			if part.Refusal == nil || strings.TrimSpace(*part.Refusal) == "" {
				return contentState{}, &responsesValidationError{
					param:   fmt.Sprintf("%s.refusal", partParam),
					message: "refusal content part requires non-empty refusal",
				}
			}
			hasContent = true
		case contentPartInputImage, contentPartInputFile, contentPartInputAudio:
			// DEC-012「协议为先」：多模态 part 合法，ingress 放行；provider 不支持时 adapter 出站 Drop。
			hasContent = true
		case "":
			return contentState{}, &responsesValidationError{
				param:   fmt.Sprintf("%s.type", partParam),
				message: "content part requires type",
			}
		default:
			return contentState{}, &responsesValidationError{
				param:   fmt.Sprintf("%s.type", partParam),
				message: fmt.Sprintf("unsupported content part type: %s", part.Type),
			}
		}
	}

	return contentState{hasContent: hasContent}, nil
}
