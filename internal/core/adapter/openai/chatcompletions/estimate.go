package chatcompletions

import (
	"bytes"
	"encoding/json"

	"github.com/ThankCat/unio-gateway/internal/core/tokenest"
)

// OpenAI Chat Completions message content part 类型（估算时用于分辨文本/多模态）。
const (
	contentPartTypeText       = "text"
	contentPartTypeRefusal    = "refusal"
	contentPartTypeImageURL   = "image_url"
	contentPartTypeInputAudio = "input_audio"
	contentPartTypeFile       = "file"
)

// buildChatEstimate 遍历 ChatRequest，把「文本内容 + 多模态附件 + 消息/工具计数」喂给 tokenest.Builder。
//
// 对齐 new-api EstimateRequestToken：只对真实文本跑 tiktoken，图片走 tile 数学，绝不把 base64 当文本计数。
func buildChatEstimate(req ChatRequest) *tokenest.Builder {
	b := tokenest.NewBuilder(req.Model)

	for _, msg := range req.Messages {
		b.AddMessage()
		addChatContent(b, msg.Content)
		if msg.ReasoningContent != nil {
			b.AddText(*msg.ReasoningContent)
		}
		for _, call := range msg.ToolCalls {
			b.AddName()
			b.AddText(call.Function.Name)
			b.AddText(call.Function.Arguments)
		}
	}

	for _, tool := range req.Tools {
		b.AddTool()
		b.AddText(tool.Function.Name)
		b.AddText(tool.Function.Description)
		if len(tool.Function.Parameters) > 0 {
			b.AddText(string(tool.Function.Parameters))
		}
	}

	return b
}

// chatContentPartView 解析 content part 中做 token 估算所需的字段（其余字段忽略）。
type chatContentPartView struct {
	Type     string `json:"type"`
	Text     string `json:"text"`
	Refusal  string `json:"refusal"`
	ImageURL struct {
		URL    string `json:"url"`
		Detail string `json:"detail"`
	} `json:"image_url"`
}

// addChatContent 解析 message content union（string 或 content-part 数组），把文本喂给 Builder、
// 把多模态 part 登记为 Media。malformed 内容静默跳过（ingress 已做协议校验）。
func addChatContent(b *tokenest.Builder, content json.RawMessage) {
	data := bytes.TrimSpace(content)
	if len(data) == 0 {
		return
	}

	// string 形态。
	var asString string
	if err := json.Unmarshal(data, &asString); err == nil {
		b.AddText(asString)
		return
	}

	// 数组形态。
	var parts []json.RawMessage
	if err := json.Unmarshal(data, &parts); err != nil {
		return
	}
	for _, raw := range parts {
		var part chatContentPartView
		if err := json.Unmarshal(raw, &part); err != nil {
			continue
		}
		switch part.Type {
		case contentPartTypeText:
			b.AddText(part.Text)
		case contentPartTypeRefusal:
			b.AddText(part.Refusal)
		case contentPartTypeImageURL:
			b.AddMedia(tokenest.ImageFromURL(part.ImageURL.URL, part.ImageURL.Detail))
		case contentPartTypeInputAudio:
			b.AddMedia(tokenest.Media{Kind: tokenest.MediaAudio})
		case contentPartTypeFile:
			b.AddMedia(tokenest.Media{Kind: tokenest.MediaFile})
		}
	}
}
