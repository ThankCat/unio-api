package normalizer

const DefaultKey Key = "default"

// Default 是 OpenAI-compatible 协议的基线 Normalizer。
type Default struct{}

func (Default) Key() Key { return DefaultKey }

// NormalizeStreamEvent 把单个上游 SSE event 转成 0..N 个内部 stream event。
// 基线规则：
// 1. 先输出 content/role/finish_reason 事件；
// 2. 只要 usage 非 nil 就追加 usage 事件（不要求 choices 为空）；
// 3. 跳过无 role/content/reasoning/finish_reason 的空心跳。
func (Default) NormalizeStreamEvent(in StreamInput) ([]StreamEvent, error) {
	out := make([]StreamEvent, 0, len(in.Choices)+1)

	for _, choice := range in.Choices {
		if isSkippableStreamChoice(choice) {
			continue
		}

		out = append(out, StreamEvent{
			ID:           in.ID,
			Model:        in.Model,
			Role:         choice.Role,
			Content:      choice.Content,
			FinishReason: choice.FinishReason,
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
		choice.FinishReason == nil
}
