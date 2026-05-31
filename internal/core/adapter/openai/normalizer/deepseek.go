package normalizer

const DeepSeekKey Key = "deepseek"

// DeepSeek 消化 DeepSeek OpenAI-compatible 流式差异。
type DeepSeek struct {
	base Default
}

func (DeepSeek) Key() Key { return DeepSeekKey }

func (d DeepSeek) NormalizeStreamEvent(in StreamInput) ([]StreamEvent, error) {
	in = d.normalizeChoices(in)
	return d.base.NormalizeStreamEvent(in)
}

func (d DeepSeek) normalizeChoices(in StreamInput) StreamInput {
	if len(in.Choices) == 0 {
		return in
	}
	choices := make([]StreamChoice, len(in.Choices))
	copy(choices, in.Choices)
	for i := range choices {
		choices[i] = d.normalizeChoice(choices[i])
	}
	in.Choices = choices
	return in
}
func (d DeepSeek) normalizeChoice(choice StreamChoice) StreamChoice {
	// content 为空时，用 reasoning_content 作为用户可见流式文本。
	if choice.Content == "" && choice.ReasoningContent != "" {
		choice.Content = choice.ReasoningContent
	}
	return choice
}
