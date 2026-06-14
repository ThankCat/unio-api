package messages

import (
	"encoding/json"

	"github.com/ThankCat/unio-api/internal/core/capability"
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
			{Key: capability.KeyTextInput, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyTextOutput, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyToolsFunction, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyToolsChoiceRequired, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyReasoningBudget, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyStream, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyStreamTools, SupportLevel: capability.SupportLevelFull},
			{Key: capability.KeyStreamUsage, SupportLevel: capability.SupportLevelFull},

			// limited：output_config.effort 出站归一为 high/max（任意请求档位均被 Adapt 向上、不拒绝）。
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
			{Key: capability.KeyFileInput, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyServiceTier, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyToolsBuiltinWebSearch, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyToolsBuiltinMCP, SupportLevel: capability.SupportLevelUnsupported},
			{Key: capability.KeyResponseFormatJSONSchema, SupportLevel: capability.SupportLevelUnsupported},
		},
	}
}
