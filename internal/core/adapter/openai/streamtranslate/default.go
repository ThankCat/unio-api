package streamtranslate

import "encoding/json"

const DefaultKey Key = "default"

// Default 是 OpenAI-compatible 协议的基线 stream translator。
type Default struct{}

func (Default) Key() Key { return DefaultKey }

// TranslateStreamEvent 把单个上游 SSE event 转成 0..N 个内部 stream event。
func (Default) TranslateStreamEvent(in StreamInput) ([]StreamEvent, error) {
	out := make([]StreamEvent, 0, len(in.Choices)+1)

	for _, choice := range in.Choices {
		if isSkippableStreamChoice(choice) {
			continue
		}

		out = append(out, StreamEvent{
			ID:               in.ID,
			Model:            in.Model,
			Role:             choice.Role,
			Content:          choice.Content,
			ReasoningContent: choice.ReasoningContent,
			ToolCalls:        append(json.RawMessage(nil), choice.ToolCalls...),
			FinishReason:     choice.FinishReason,
		})
	}

	if in.Usage != nil {
		usage := *in.Usage
		out = append(out, StreamEvent{
			ID:    in.ID,
			Model: in.Model,
			Usage: &usage,
		})
	}

	return out, nil
}

func isSkippableStreamChoice(choice StreamChoice) bool {
	return choice.Role == "" &&
		choice.Content == "" &&
		choice.ReasoningContent == "" &&
		len(choice.ToolCalls) == 0 &&
		choice.FinishReason == nil
}
