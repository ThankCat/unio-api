// Package capability 承载能力架构（DEC-015）的稳定能力标识、支持级别与数据访问层。
//
// 能力 key 是公开稳定契约，发布后只增不删，权威列表见 docs/protocol/CAPABILITY_KEYS.md。
// 本包是 Layer 1/2/3 能力事实的读写基座；推断、闸门、同步逻辑归后续任务（12.02~12.04）。
package capability

import "sort"

// Key 是稳定能力标识，命名形如 <domain>.<feature>[.<sub>]。
type Key string

// 能力 key v1 注册表。新增 key 只能追加，禁止改名或删除（公开契约）。
const (
	KeyTextInput  Key = "text.input"
	KeyTextOutput Key = "text.output"

	KeyImageInput  Key = "image.input"
	KeyImageOutput Key = "image.output"

	KeyAudioInput  Key = "audio.input"
	KeyAudioOutput Key = "audio.output"

	KeyFileInput Key = "file.input"

	KeyToolsFunction       Key = "tools.function"
	KeyToolsCustom         Key = "tools.custom"
	KeyToolsParallel       Key = "tools.parallel"
	KeyToolsChoiceRequired Key = "tools.choice_required"

	KeyToolsBuiltinWebSearch       Key = "tools.builtin.web_search"
	KeyToolsBuiltinFileSearch      Key = "tools.builtin.file_search"
	KeyToolsBuiltinCodeInterpreter Key = "tools.builtin.code_interpreter"
	KeyToolsBuiltinComputerUse     Key = "tools.builtin.computer_use"
	KeyToolsBuiltinImageGeneration Key = "tools.builtin.image_generation"
	KeyToolsBuiltinMCP             Key = "tools.builtin.mcp"

	KeyReasoningEffort  Key = "reasoning.effort"
	KeyReasoningBudget  Key = "reasoning.budget"
	KeyReasoningSummary Key = "reasoning.summary"

	KeyResponseFormatJSONObject Key = "response_format.json_object"
	KeyResponseFormatJSONSchema Key = "response_format.json_schema"

	KeyPromptCache Key = "prompt_cache"
	KeyLogprobs    Key = "logprobs"
	KeyServiceTier Key = "service_tier"

	KeyStream      Key = "stream"
	KeyStreamTools Key = "stream.tools"
	KeyStreamUsage Key = "stream.usage"

	KeyServerStateStore      Key = "server_state.store"
	KeyServerStateBackground Key = "server_state.background"

	KeyResponsesEncryptedContent Key = "responses.encrypted_content"
)

// registeredKeys 是 v1 已发布能力 key 集合，作为写入/推断的合法性边界。
var registeredKeys = map[Key]struct{}{
	KeyTextInput:                   {},
	KeyTextOutput:                  {},
	KeyImageInput:                  {},
	KeyImageOutput:                 {},
	KeyAudioInput:                  {},
	KeyAudioOutput:                 {},
	KeyFileInput:                   {},
	KeyToolsFunction:               {},
	KeyToolsCustom:                 {},
	KeyToolsParallel:               {},
	KeyToolsChoiceRequired:         {},
	KeyToolsBuiltinWebSearch:       {},
	KeyToolsBuiltinFileSearch:      {},
	KeyToolsBuiltinCodeInterpreter: {},
	KeyToolsBuiltinComputerUse:     {},
	KeyToolsBuiltinImageGeneration: {},
	KeyToolsBuiltinMCP:             {},
	KeyReasoningEffort:             {},
	KeyReasoningBudget:             {},
	KeyReasoningSummary:            {},
	KeyResponseFormatJSONObject:    {},
	KeyResponseFormatJSONSchema:    {},
	KeyPromptCache:                 {},
	KeyLogprobs:                    {},
	KeyServiceTier:                 {},
	KeyStream:                      {},
	KeyStreamTools:                 {},
	KeyStreamUsage:                 {},
	KeyServerStateStore:            {},
	KeyServerStateBackground:       {},
	KeyResponsesEncryptedContent:   {},
}

// IsRegisteredKey 判断能力 key 是否在已发布注册表内，写入与推断必须先通过该校验。
func IsRegisteredKey(key Key) bool {
	_, ok := registeredKeys[key]
	return ok
}

// RegisteredKeys 返回升序排序的全部已注册能力 key，供文档生成与一致性测试使用。
func RegisteredKeys() []Key {
	keys := make([]Key, 0, len(registeredKeys))
	for key := range registeredKeys {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	return keys
}

// SupportLevel 表示某模型或渠道对某能力的支持级别。
type SupportLevel string

const (
	// SupportLevelFull 表示完整支持该能力。
	SupportLevelFull SupportLevel = "full"

	// SupportLevelLimited 表示部分支持，受 limits 进一步约束。
	SupportLevelLimited SupportLevel = "limited"

	// SupportLevelUnsupported 表示不支持该能力。
	SupportLevelUnsupported SupportLevel = "unsupported"
)

// IsValidSupportLevel 判断支持级别是否是合法的模型层取值。
func IsValidSupportLevel(level SupportLevel) bool {
	switch level {
	case SupportLevelFull, SupportLevelLimited, SupportLevelUnsupported:
		return true
	default:
		return false
	}
}

// IsValidChannelOverrideLevel 判断支持级别是否是合法的渠道收紧取值（只能做减法，禁止 full）。
func IsValidChannelOverrideLevel(level SupportLevel) bool {
	switch level {
	case SupportLevelLimited, SupportLevelUnsupported:
		return true
	default:
		return false
	}
}

// Source 表示能力声明或模型元数据的来源。
type Source string

const (
	// SourceModelsDev 表示来自 models.dev 同步。
	SourceModelsDev Source = "models_dev"

	// SourceManual 表示运营手工维护。
	SourceManual Source = "manual"

	// SourceAdapterSeed 表示由 adapter 能力种子回填。
	SourceAdapterSeed Source = "adapter_seed"
)

// IsValidCapabilitySource 判断能力声明来源是否合法（model_capabilities.source 取值）。
func IsValidCapabilitySource(source Source) bool {
	switch source {
	case SourceModelsDev, SourceManual, SourceAdapterSeed:
		return true
	default:
		return false
	}
}

// IsValidSyncJobSource 判断同步任务来源是否合法（model_capability_sync_jobs.source 取值，禁止 adapter_seed）。
func IsValidSyncJobSource(source Source) bool {
	switch source {
	case SourceModelsDev, SourceManual:
		return true
	default:
		return false
	}
}
