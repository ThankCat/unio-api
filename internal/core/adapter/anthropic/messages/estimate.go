package messages

import (
	"bytes"
	"encoding/json"

	"github.com/ThankCat/unio-gateway/internal/core/tokenest"
)

// buildMessagesEstimate 遍历 Anthropic Messages 请求，把「文本内容 + 多模态附件 + 消息/工具计数」
// 喂给 tokenest.Builder。Anthropic 无公开本地 tokenizer，用 tiktoken 近似（对齐 new-api 的取舍）；
// 图片走 tile 数学（claude 族用其像素公式），绝不把 base64 当文本计数。
func buildMessagesEstimate(req MessagesInputTokenizeRequest) *tokenest.Builder {
	b := tokenest.NewBuilder(req.Model)

	addAnthropicSystem(b, req.System)
	for _, msg := range req.Messages {
		b.AddMessage()
		addAnthropicContent(b, msg.Content)
	}
	addAnthropicTools(b, req.Tools)

	return b
}

// addAnthropicSystem 解析 system（字符串或 text block 数组）。
func addAnthropicSystem(b *tokenest.Builder, raw []byte) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return
	}
	b.AddMessage()

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		b.AddText(asString)
		return
	}

	var blocks []anthropicBlockView
	if err := json.Unmarshal(data, &blocks); err != nil {
		return
	}
	for _, block := range blocks {
		if block.Type == "text" {
			b.AddText(block.Text)
		}
	}
}

// anthropicBlockView 解析 content block 中做估算所需的字段。
type anthropicBlockView struct {
	Type     string          `json:"type"`
	Text     string          `json:"text"`
	Thinking string          `json:"thinking"`
	Name     string          `json:"name"`
	Input    json.RawMessage `json:"input"`
	Content  json.RawMessage `json:"content"`
	Source   json.RawMessage `json:"source"`
}

// addAnthropicContent 解析 message content union（字符串或 content block 数组）。
func addAnthropicContent(b *tokenest.Builder, raw json.RawMessage) {
	data := bytes.TrimSpace(raw)
	if len(data) == 0 {
		return
	}

	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		b.AddText(asString)
		return
	}

	var blocks []anthropicBlockView
	if err := json.Unmarshal(data, &blocks); err != nil {
		return
	}
	for _, block := range blocks {
		switch block.Type {
		case "text":
			b.AddText(block.Text)
		case "thinking":
			b.AddText(block.Thinking)
		case "image":
			b.AddMedia(anthropicImageMedia(block.Source))
		case "document":
			b.AddMedia(tokenest.Media{Kind: tokenest.MediaFile})
		case "tool_use":
			b.AddName()
			b.AddText(block.Name)
			b.AddText(anthropicRawText(block.Input))
		case "tool_result":
			b.AddText(anthropicRawText(block.Content))
		}
	}
}

// anthropicImageMedia 从 image block 的 source 解析图片附件（base64 内联或 url）。
func anthropicImageMedia(raw json.RawMessage) tokenest.Media {
	var source struct {
		Type string `json:"type"`
		Data string `json:"data"`
		URL  string `json:"url"`
	}
	if err := json.Unmarshal(raw, &source); err == nil {
		switch source.Type {
		case "base64":
			return tokenest.ImageFromBase64(source.Data, "")
		case "url":
			return tokenest.ImageFromURL(source.URL, "")
		}
	}
	return tokenest.Media{Kind: tokenest.MediaImage}
}

// addAnthropicTools 逐个 tool 计入框架开销与名称/描述/schema 文本。
func addAnthropicTools(b *tokenest.Builder, raw []byte) {
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
		b.AddText(anthropicRawText(rawTool))
	}
}

// anthropicRawText 把一段 JSON 折算成可计数文本：字符串取其值，其余按原始 JSON 文本。
func anthropicRawText(raw json.RawMessage) string {
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
