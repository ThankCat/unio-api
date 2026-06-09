package deepseek

import (
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

// CapabilityProfile 返回 DeepSeek OpenAI 兼容 adapter 的能力画像，作为 model_capabilities 的
// adapter_seed 初始事实来源（source=adapter_seed）。
//
// 画像与本包 dropUnsupported 同源：
//   - unsupported：出站会被 Drop 的能力（多模态输入、custom tool、parallel_tool_calls、
//     json_schema、service_tier、store、prompt_cache、web_search、audio 输出）。
//   - limited：被 Adapt 的能力（reasoning.effort 归一为 high/max，见 DEEPSEEK_OPENAI_MAPPING §2）。
//   - full：透传放行的能力（text、function tool、json_object、logprobs、流式）。
//
// capability_profile_test.go 以真实 dropUnsupported 行为守护本画像不漂移；新增/调整 Drop 规则
// 必须同步本画像，否则一致性测试报错（GAP-12-007）。
func CapabilityProfile() capability.AdapterProfile {
	return capability.AdapterProfile{
		Provider: "deepseek",
		Protocol: "openai",
		Declarations: []capability.Declaration{
			// full：透传放行（部分无对应 Drop 探针，由协议契约保证）。
			{Key: capability.KeyTextInput, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyTextOutput, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyToolsFunction, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyToolsChoiceRequired, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyResponseFormatJSONObject, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyLogprobs, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyStream, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyStreamTools, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyStreamUsage, SupportLevel: capability.SupportLevelFull},

			// limited：reasoning.effort 出站归一为 high/max（任意请求档位均被 Adapt 向上、不拒绝）。
			// limits 必须用 capability 闸门可消费的 schema：{"max_effort":<档位>}（见 capability.gate
			// limitViolated，唯一消费者，按 effortRank 做上限判定）。此前写成 {"effort":[...]} 闸门无法解析，
			// limited 退化为恒满足的死声明。上限取 "high"（请求域最高档；effortRank 不识别 "max"，若写 "max"
			// 会令任意请求被误判超限），表达「接受到 high 为止的全部标准请求」，与 dropUnsupported 的归一行为一致。
			{
				Key:          capability.KeyReasoningEffort,
				SupportLevel: capability.SupportLevelLimited,
				Limits:       json.RawMessage(`{"max_effort":"high"}`),
			},

			// unsupported：出站 Drop。
			{Key: capability.KeyImageInput, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyAudioInput, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyFileInput, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyAudioOutput, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyToolsCustom, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyToolsParallel, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyResponseFormatJSONSchema, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyServiceTier, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyServerStateStore, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyPromptCache, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyToolsBuiltinWebSearch, SupportLevel: capability.SupportLevelUnsupported},
		},
	}
}
