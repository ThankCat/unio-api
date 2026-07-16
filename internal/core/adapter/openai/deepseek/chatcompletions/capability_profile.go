package chatcompletions

import (
	"encoding/json"

	"github.com/ThankCat/unio-gateway/internal/core/capability"
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
			{Key: "text.input", SupportLevel: capability.SupportLevelFull},
			{Key: "text.output", SupportLevel: capability.SupportLevelFull},
			{Key: "tools.function", SupportLevel: capability.SupportLevelFull},
			{Key: "tools.choice_required", SupportLevel: capability.SupportLevelFull},
			{Key: "response_format.json_object", SupportLevel: capability.SupportLevelFull},
			{Key: "logprobs", SupportLevel: capability.SupportLevelFull},
			{Key: "stream", SupportLevel: capability.SupportLevelFull},
			{Key: "stream.tools", SupportLevel: capability.SupportLevelFull},
			{Key: "stream.usage", SupportLevel: capability.SupportLevelFull},

			// limited：reasoning.effort 出站归一为 high/max（任意请求档位均被 Adapt 向上、不拒绝）。
			// limits 形如 {"max_effort":<档位>}；DEC-024 删闸门后仅作展示记录，不参与运行时判定。
			{
				Key:          "reasoning.effort",
				SupportLevel: capability.SupportLevelLimited,
				Limits:       json.RawMessage(`{"max_effort":"high"}`),
			},

			// unsupported：出站 Drop。
			{Key: "image.input", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "audio.input", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "file.input", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "audio.output", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "tools.custom", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "tools.parallel", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "response_format.json_schema", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "service_tier", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "server_state.store", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "prompt_cache", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "tools.builtin.web_search", SupportLevel: capability.SupportLevelUnsupported},
		},
	}
}
