package capability

// RequestSignals 是协议无关的能力推断输入。
//
// 分层约定：协议字段如何 decode、content/tools JSON 如何解析，随协议各异，留在 app 层
// 各自的 ingress 包抽取；本结构只承载已抽取的「能力相关信号」。Infer 把信号映射成
// required capability 集合，规则集中在此处可单测，不被协议 DTO 形态污染。
//
// 所有字段都是显式信号位，零值表示「请求未触发该能力」。未识别的协议字段不应设置任何信号，
// 因此未知字段天然不污染推断结果。
type RequestSignals struct {
	// Stream 表示请求流式响应。
	Stream bool
	// StreamUsage 表示流式下请求回传 usage（如 OpenAI stream_options.include_usage）。
	StreamUsage bool

	// HasImageInput / HasAudioInput / HasFileInput 表示输入消息含对应模态内容。
	HasImageInput bool
	HasAudioInput bool
	HasFileInput  bool
	// AudioOutput 表示请求输出音频模态（如 OpenAI modalities 含 audio）。
	AudioOutput bool

	// HasFunctionTool / HasCustomTool 表示请求定义了对应类型工具。
	HasFunctionTool bool
	HasCustomTool   bool
	// ParallelToolCalls 表示显式开启并行工具调用。
	ParallelToolCalls bool
	// ToolChoiceRequired 表示强制至少调用一个工具（OpenAI required / Anthropic any|tool）。
	ToolChoiceRequired bool

	// 内置（server-side）工具信号。
	BuiltinWebSearch       bool
	BuiltinFileSearch      bool
	BuiltinCodeInterpreter bool
	BuiltinComputerUse     bool
	BuiltinImageGeneration bool
	BuiltinMCP             bool

	// ReasoningEffort 表示请求带 reasoning effort 档位（OpenAI reasoning_effort / Responses reasoning.effort）。
	ReasoningEffort bool
	// ReasoningEffortLevel 是请求声明的 reasoning effort 档位值（"low"/"medium"/"high" 等，原文小写归一前的值）。
	//
	// 仅承载「带值」约束供闸门 limited 超限判定（见 InferLimits / gate.go RequestLimits）；
	// 空值表示请求未声明档位或协议无该概念（如 Anthropic 用 thinking budget，不置位）。
	ReasoningEffortLevel string
	// ReasoningBudget 表示请求带思考预算（Anthropic thinking 启用）。
	ReasoningBudget bool
	// ReasoningSummary 表示请求要求返回推理摘要（Responses reasoning.summary）。
	ReasoningSummary bool

	// ResponseFormatJSONObject / ResponseFormatJSONSchema 表示请求结构化输出格式。
	ResponseFormatJSONObject bool
	ResponseFormatJSONSchema bool

	// PromptCache 表示请求带 prompt cache 相关字段。
	PromptCache bool
	// Logprobs 表示请求 token logprobs。
	Logprobs bool
	// ServiceTier 表示请求指定 service tier。
	ServiceTier bool

	// ServerStateStore / ServerStateBackground 表示请求要求服务端状态能力。
	ServerStateStore      bool
	ServerStateBackground bool

	// EncryptedContent 表示请求要求 Responses 推理项 encrypted_content 跨轮携带。
	EncryptedContent bool
}

// Infer 把协议无关信号映射成 required capability 集合（纯函数，无 IO，输入只读）。
//
// 文本输入/输出作为三协议统一基线始终纳入 required：对话类模型普遍支持，把它放入 required
// 让闸门即便面对「声明不支持文本」的异常模型也能稳定拒绝，且不会引入误拒（真实模型不会缺）。
// Infer 只产出 keys.go 中已注册的稳定 key，未触发的信号不产生任何 key。
func Infer(signals RequestSignals) Set {
	required := NewSet(KeyTextInput, KeyTextOutput)

	if signals.Stream {
		required.Add(KeyStream)
		if signals.HasFunctionTool || signals.HasCustomTool {
			required.Add(KeyStreamTools)
		}
		if signals.StreamUsage {
			required.Add(KeyStreamUsage)
		}
	}

	if signals.HasImageInput {
		required.Add(KeyImageInput)
	}
	if signals.HasAudioInput {
		required.Add(KeyAudioInput)
	}
	if signals.HasFileInput {
		required.Add(KeyFileInput)
	}
	if signals.AudioOutput {
		required.Add(KeyAudioOutput)
	}

	if signals.HasFunctionTool {
		required.Add(KeyToolsFunction)
	}
	if signals.HasCustomTool {
		required.Add(KeyToolsCustom)
	}
	if signals.ParallelToolCalls {
		required.Add(KeyToolsParallel)
	}
	if signals.ToolChoiceRequired {
		required.Add(KeyToolsChoiceRequired)
	}

	if signals.BuiltinWebSearch {
		required.Add(KeyToolsBuiltinWebSearch)
	}
	if signals.BuiltinFileSearch {
		required.Add(KeyToolsBuiltinFileSearch)
	}
	if signals.BuiltinCodeInterpreter {
		required.Add(KeyToolsBuiltinCodeInterpreter)
	}
	if signals.BuiltinComputerUse {
		required.Add(KeyToolsBuiltinComputerUse)
	}
	if signals.BuiltinImageGeneration {
		required.Add(KeyToolsBuiltinImageGeneration)
	}
	if signals.BuiltinMCP {
		required.Add(KeyToolsBuiltinMCP)
	}

	if signals.ReasoningEffort {
		required.Add(KeyReasoningEffort)
	}
	if signals.ReasoningBudget {
		required.Add(KeyReasoningBudget)
	}
	if signals.ReasoningSummary {
		required.Add(KeyReasoningSummary)
	}

	if signals.ResponseFormatJSONObject {
		required.Add(KeyResponseFormatJSONObject)
	}
	if signals.ResponseFormatJSONSchema {
		required.Add(KeyResponseFormatJSONSchema)
	}

	if signals.PromptCache {
		required.Add(KeyPromptCache)
	}
	if signals.Logprobs {
		required.Add(KeyLogprobs)
	}
	if signals.ServiceTier {
		required.Add(KeyServiceTier)
	}

	if signals.ServerStateStore {
		required.Add(KeyServerStateStore)
	}
	if signals.ServerStateBackground {
		required.Add(KeyServerStateBackground)
	}

	if signals.EncryptedContent {
		required.Add(KeyResponsesEncryptedContent)
	}

	return required
}

// InferLimits 把协议无关信号映射成请求侧「带值」能力约束，供 capability 闸门判定 limited 是否超限（纯函数）。
//
// 与 Infer 分工：Infer 决定「需要哪些能力 key」，InferLimits 决定「这些能力的请求档位值」。
// 当前只建模 reasoning.effort 档位；其余能力是布尔触发、无 limits 维度。未声明档位返回零值，
// 闸门据此对 limited 能力放行（见 gate.go limitViolated）。
func InferLimits(signals RequestSignals) RequestLimits {
	return RequestLimits{
		ReasoningEffort: signals.ReasoningEffortLevel,
	}
}
