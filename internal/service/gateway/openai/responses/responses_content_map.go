package responses

import (
	"encoding/json"
	"strings"
)

// Responses content part 类型（input message content union，BRIDGE §2.1）。
const (
	contentPartInputText  = "input_text"
	contentPartOutputText = "output_text"
	contentPartRefusal    = "refusal"
	contentPartInputImage = "input_image"
	contentPartInputFile  = "input_file"
	contentPartInputAudio = "input_audio"
)

// responsesContentPart 是 message content 数组里单个 part 的桥接视图。
type responsesContentPart struct {
	Type       string          `json:"type"`
	Text       *string         `json:"text"`
	Refusal    *string         `json:"refusal"`
	ImageURL   json.RawMessage `json:"image_url"`
	FileID     *string         `json:"file_id"`
	FileData   *string         `json:"file_data"`
	Filename   *string         `json:"filename"`
	Detail     *string         `json:"detail"`
	InputAudio json.RawMessage `json:"input_audio"`
}

// translateInputContent 把 Responses message content（string | content part 数组）翻译成 Chat content。
//
//   - string：原样透传（已是 JSON string）。
//   - 纯文本 part（input_text/output_text/refusal）：合并为单条 JSON string（DeepSeek 友好）。
//   - 含多模态 part（image/file/audio）：产出 Chat content part 数组，文本→{type:"text"}，
//     多模态 best-effort 翻译；provider 不支持时由 adapter 出站 Drop（BRIDGE §2.1）。
func translateInputContent(content json.RawMessage) json.RawMessage {
	if len(content) == 0 {
		return nil
	}

	if isJSONString(content) {
		return cloneRawMessage(content)
	}

	var rawParts []json.RawMessage
	if err := json.Unmarshal(content, &rawParts); err != nil {
		// 非 string、非数组：保守透传原始 JSON，交由下游处理（不在桥接层丢内容）。
		return cloneRawMessage(content)
	}

	parts := make([]responsesContentPart, 0, len(rawParts))
	textOnly := true
	for _, raw := range rawParts {
		var part responsesContentPart
		if err := json.Unmarshal(raw, &part); err != nil {
			continue
		}
		parts = append(parts, part)
		if !isTextPart(part.Type) {
			textOnly = false
		}
	}

	if textOnly {
		return jsonString(joinTextParts(parts))
	}

	return marshalChatContentParts(parts)
}

// joinTextParts 把纯文本 part 合并为单段文本（input_text/output_text 取 text，refusal 取 refusal）。
func joinTextParts(parts []responsesContentPart) string {
	segments := make([]string, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case contentPartInputText, contentPartOutputText:
			if part.Text != nil {
				segments = append(segments, *part.Text)
			}
		case contentPartRefusal:
			if part.Refusal != nil {
				segments = append(segments, *part.Refusal)
			}
		}
	}
	return strings.Join(segments, "\n")
}

// marshalChatContentParts 把混合 content 翻译成 Chat content part 数组（best-effort 多模态）。
func marshalChatContentParts(parts []responsesContentPart) json.RawMessage {
	out := make([]map[string]any, 0, len(parts))
	for _, part := range parts {
		switch part.Type {
		case contentPartInputText, contentPartOutputText:
			if part.Text != nil {
				out = append(out, map[string]any{"type": "text", "text": *part.Text})
			}
		case contentPartRefusal:
			if part.Refusal != nil {
				out = append(out, map[string]any{"type": "text", "text": *part.Refusal})
			}
		case contentPartInputImage:
			if img := chatImagePart(part); img != nil {
				out = append(out, img)
			}
		case contentPartInputFile:
			out = append(out, chatFilePart(part))
		case contentPartInputAudio:
			if len(part.InputAudio) > 0 {
				out = append(out, map[string]any{"type": "input_audio", "input_audio": part.InputAudio})
			}
		}
	}

	encoded, err := json.Marshal(out)
	if err != nil {
		return nil
	}
	return encoded
}

// chatImagePart 把 Responses input_image（image_url 为 string url 或 object）翻译成 Chat image_url part。
func chatImagePart(part responsesContentPart) map[string]any {
	imageURL := map[string]any{}
	if len(part.ImageURL) > 0 {
		var url string
		if err := json.Unmarshal(part.ImageURL, &url); err == nil {
			imageURL["url"] = url
		} else {
			var obj map[string]any
			if err := json.Unmarshal(part.ImageURL, &obj); err == nil {
				imageURL = obj
			}
		}
	}
	if part.FileID != nil {
		imageURL["file_id"] = *part.FileID
	}
	if part.Detail != nil {
		imageURL["detail"] = *part.Detail
	}
	if len(imageURL) == 0 {
		return nil
	}
	return map[string]any{"type": "image_url", "image_url": imageURL}
}

// chatFilePart 把 Responses input_file 翻译成 Chat file part（best-effort）。
func chatFilePart(part responsesContentPart) map[string]any {
	file := map[string]any{}
	if part.FileID != nil {
		file["file_id"] = *part.FileID
	}
	if part.FileData != nil {
		file["file_data"] = *part.FileData
	}
	if part.Filename != nil {
		file["filename"] = *part.Filename
	}
	return map[string]any{"type": "file", "file": file}
}

// toolOutputContent 把 function_call_output 的 output（string | parts[]）翻译成 Chat tool message content。
func toolOutputContent(output json.RawMessage) json.RawMessage {
	if len(output) == 0 {
		return nil
	}
	if isJSONString(output) {
		return cloneRawMessage(output)
	}

	var rawParts []json.RawMessage
	if err := json.Unmarshal(output, &rawParts); err == nil {
		parts := make([]responsesContentPart, 0, len(rawParts))
		for _, raw := range rawParts {
			var part responsesContentPart
			if err := json.Unmarshal(raw, &part); err == nil {
				parts = append(parts, part)
			}
		}
		return jsonString(joinTextParts(parts))
	}

	// 其它形态（object 等）：编码为字符串，保证 Chat tool content 是合法字符串。
	return jsonString(string(output))
}

func isTextPart(partType string) bool {
	switch partType {
	case contentPartInputText, contentPartOutputText, contentPartRefusal:
		return true
	default:
		return false
	}
}

// isJSONString 判断原始 JSON 是否为字符串字面量。
func isJSONString(raw json.RawMessage) bool {
	var s string
	return json.Unmarshal(raw, &s) == nil
}

// jsonString 把 Go 字符串编码为 JSON string 字面量。
func jsonString(s string) json.RawMessage {
	out, _ := json.Marshal(s)
	return out
}

// cloneRawMessage 深拷贝原始 JSON，避免与 ingress DTO 共享底层数组。
func cloneRawMessage(src json.RawMessage) json.RawMessage {
	if len(src) == 0 {
		return nil
	}
	return append(json.RawMessage(nil), src...)
}

// derefString 解引用 *string，nil 返回空串。
func derefString(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}
