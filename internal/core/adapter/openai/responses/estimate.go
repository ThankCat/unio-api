package responses

import (
	"bytes"
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/core/tokenest"
)

// buildResponsesEstimate 解析即将上送的 Responses 请求体，把「文本内容 + 多模态附件 + 消息/工具计数」
// 喂给 tokenest.Builder。对齐 new-api：只对真实文本跑 tiktoken，图片走 tile 数学，不把 base64 当文本。
func buildResponsesEstimate(body []byte) *tokenest.Builder {
	var head struct {
		Model        string          `json:"model"`
		Instructions *string         `json:"instructions"`
		Input        json.RawMessage `json:"input"`
		Tools        json.RawMessage `json:"tools"`
	}
	_ = json.Unmarshal(body, &head) // 容错：字段缺失/异常按空处理，估算仍给保守下界。

	b := tokenest.NewBuilder(head.Model)

	if head.Instructions != nil && *head.Instructions != "" {
		b.AddMessage()
		b.AddText(*head.Instructions)
	}

	addResponsesInput(b, head.Input)
	addResponsesTools(b, head.Tools)

	return b
}

// addResponsesInput 解析 input union：字符串直接计文本；数组逐 item 提取。
func addResponsesInput(b *tokenest.Builder, raw json.RawMessage) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		b.AddMessage()
		b.AddText(asString)
		return
	}

	var items []json.RawMessage
	if err := json.Unmarshal(data, &items); err != nil {
		return
	}
	for _, raw := range items {
		b.AddMessage()
		addResponsesItem(b, raw)
	}
}

// responsesItemView 解析一个 input item 中做估算所需的字段。
type responsesItemView struct {
	Type      string          `json:"type"`
	Content   json.RawMessage `json:"content"`
	Name      *string         `json:"name"`
	Arguments json.RawMessage `json:"arguments"`
	Output    json.RawMessage `json:"output"`
	Summary   json.RawMessage `json:"summary"`
}

// addResponsesItem 按 item 类型提取文本/媒体：message→content 部件；function_call→名+参数；
// function_call_output→输出；reasoning→summary 文本。
func addResponsesItem(b *tokenest.Builder, raw json.RawMessage) {
	var item responsesItemView
	if err := json.Unmarshal(raw, &item); err != nil {
		return
	}

	switch item.Type {
	case "function_call":
		if item.Name != nil {
			b.AddName()
			b.AddText(*item.Name)
		}
		b.AddText(rawJSONText(item.Arguments))
	case "function_call_output":
		b.AddText(rawJSONText(item.Output))
	case "reasoning":
		addResponsesSummary(b, item.Summary)
	default:
		addResponsesContent(b, item.Content)
	}
}

// responsesContentPartView 解析 content part 中做估算所需的字段。
type responsesContentPartView struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Refusal  string          `json:"refusal"`
	Detail   string          `json:"detail"`
	ImageURL json.RawMessage `json:"image_url"`
}

// addResponsesContent 解析 message content union（字符串或部件数组）。
func addResponsesContent(b *tokenest.Builder, raw json.RawMessage) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		b.AddText(asString)
		return
	}

	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return
	}
	for _, rawPart := range parts {
		var part responsesContentPartView
		if err := json.Unmarshal(rawPart, &part); err != nil {
			continue
		}
		switch part.Type {
		case "input_text", "output_text", "text":
			b.AddText(part.Text)
		case "refusal":
			b.AddText(part.Refusal)
		case "input_image":
			b.AddMedia(responsesImageMedia(part))
		case "input_file":
			b.AddMedia(tokenest.Media{Kind: tokenest.MediaFile})
		case "input_audio":
			b.AddMedia(tokenest.Media{Kind: tokenest.MediaAudio})
		}
	}
}

// responsesImageMedia 从 input_image 部件的 image_url（字符串或对象）解析图片附件。
func responsesImageMedia(part responsesContentPartView) tokenest.Media {
	detail := part.Detail
	if len(part.ImageURL) > 0 {
		var url string
		if err := json.Unmarshal(part.ImageURL, &url); err == nil {
			return tokenest.ImageFromURL(url, detail)
		}
		var obj struct {
			URL    string `json:"url"`
			Detail string `json:"detail"`
		}
		if err := json.Unmarshal(part.ImageURL, &obj); err == nil {
			if obj.Detail != "" {
				detail = obj.Detail
			}
			return tokenest.ImageFromURL(obj.URL, detail)
		}
	}
	// 仅 file_id 引用、无内联/URL：尺寸未知，按 image 走 3×base 兜底。
	return tokenest.Media{Kind: tokenest.MediaImage, Detail: detail}
}

// addResponsesSummary 提取 reasoning item 的 summary 文本（[{type,text}] 结构）。
func addResponsesSummary(b *tokenest.Builder, raw json.RawMessage) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return
	}
	var parts []struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal(data, &parts); err != nil {
		return
	}
	for _, p := range parts {
		b.AddText(p.Text)
	}
}

// addResponsesTools 逐个 tool 计入框架开销与名称/描述/参数文本。
func addResponsesTools(b *tokenest.Builder, raw json.RawMessage) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return
	}
	var tools []json.RawMessage
	if err := json.Unmarshal(data, &tools); err != nil {
		return
	}
	for _, rawTool := range tools {
		b.AddTool()
		b.AddText(rawJSONText(rawTool))
	}
}

// rawJSONText 把一段 JSON 折算成可计数文本：字符串取其值，其余按原始 JSON 文本（模型即看到该序列化）。
func rawJSONText(raw json.RawMessage) string {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return ""
	}
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		return asString
	}
	return string(data)
}
