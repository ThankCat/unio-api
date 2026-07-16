package messages

import (
	"encoding/json"

	"github.com/ThankCat/unio-gateway/internal/core/capability"
)

// CapabilityProfile 返回 DeepSeek Anthropic 兼容 adapter 的能力画像，作为 model_capabilities 的
// adapter_seed 初始事实来源（source=adapter_seed）。
//
// 画像与本包 dropUnsupported 同源：
//   - unsupported：出站会被 Drop 的能力（image/document content block、内置 server tool web_search、
//     mcp_servers、service_tier、output_config.format 的 json_schema 语义）。
//   - limited：被 Adapt 的能力（output_config.effort 归一为 high/max）。
//   - full：透传放行的能力（text、client custom tool 作为 function、thinking budget、流式）。
//
// capability_profile_test.go 以真实 dropUnsupported 行为守护本画像不漂移（GAP-12-007）。
func CapabilityProfile() capability.AdapterProfile {
	return capability.AdapterProfile{
		Provider: "deepseek",
		Protocol: "anthropic",
		Declarations: []capability.Declaration{
			// full：透传放行（部分无对应 Drop 探针，由协议契约保证）。
			{Key: "text.input", SupportLevel: capability.SupportLevelFull},
			{Key: "text.output", SupportLevel: capability.SupportLevelFull},
			{Key: "tools.function", SupportLevel: capability.SupportLevelFull},
			{Key: "tools.choice_required", SupportLevel: capability.SupportLevelFull},
			{Key: "reasoning.budget", SupportLevel: capability.SupportLevelFull},
			{Key: "stream", SupportLevel: capability.SupportLevelFull},
			{Key: "stream.tools", SupportLevel: capability.SupportLevelFull},
			{Key: "stream.usage", SupportLevel: capability.SupportLevelFull},

			// limited：output_config.effort 出站归一为 high/max（任意请求档位均被 Adapt 向上、不拒绝）。
			// limits 形如 {"max_effort":<档位>}；DEC-024 删闸门后仅作展示记录，不参与运行时判定。
			{
				Key:          "reasoning.effort",
				SupportLevel: capability.SupportLevelLimited,
				Limits:       json.RawMessage(`{"max_effort":"high"}`),
			},

			// unsupported：出站 Drop。
			{Key: "image.input", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "file.input", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "service_tier", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "tools.builtin.web_search", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "tools.builtin.mcp", SupportLevel: capability.SupportLevelUnsupported},
			{Key: "response_format.json_schema", SupportLevel: capability.SupportLevelUnsupported},
		},
	}
}
