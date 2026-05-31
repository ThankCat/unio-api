package streamtranslate

const DeepSeekKey Key = "deepseek"

// DeepSeek 消化 DeepSeek OpenAI-compatible 流式差异（与 Default 基线一致，保留独立 slug 绑定）。
type DeepSeek struct {
	base Default
}

func (DeepSeek) Key() Key { return DeepSeekKey }

func (d DeepSeek) TranslateStreamEvent(in StreamInput) ([]StreamEvent, error) {
	return d.base.TranslateStreamEvent(in)
}
