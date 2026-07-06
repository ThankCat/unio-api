package lifecycle

import (
	"encoding/json"
	"strings"
)

// ReasoningInfo 是归一后的推理强度：Effort 为统一档位（none/minimal/low/medium/high/xhigh），
// BudgetTokens 为原始思考预算（仅 Anthropic thinking 提供；OpenAI 无预算，为 nil）。
type ReasoningInfo struct {
	Effort       *string
	BudgetTokens *int32
}

// 统一推理强度档位词表（与 sub2api / aiproxy / 官方 Anthropic effort 看齐）。
const (
	effortNone    = "none"
	effortMinimal = "minimal"
	effortLow     = "low"
	effortMedium  = "medium"
	effortHigh    = "high"
	effortXHigh   = "xhigh"
	effortMax     = "max" // 官方 Anthropic output_config.effort 顶格档（高于 xhigh，如 Opus 4.6+）。
)

// NormalizeOpenAIEffort 归一 OpenAI reasoning_effort（含 Responses reasoning.effort）为统一档位。
// 空串 → 无（nil）；max → xhigh；其余小写保留（未知档位也可展示）。
//
// 参考：sub2api UsageLog.ReasoningEffort（跨协议、max→xhigh）
//
//	https://github.com/Wei-Shaw/sub2api/blob/main/backend/internal/service/usage_log.go
func NormalizeOpenAIEffort(effort string) ReasoningInfo {
	e := strings.ToLower(strings.TrimSpace(effort))
	if e == "" {
		return ReasoningInfo{}
	}
	if e == "max" {
		e = effortXHigh
	}
	return ReasoningInfo{Effort: &e}
}

// anthropicThinking 是 Anthropic thinking union 的最小解析形状。
type anthropicThinking struct {
	Type         string `json:"type"`
	BudgetTokens *int   `json:"budget_tokens"`
}

// anthropicOutputConfig 是 Anthropic output_config 的最小解析形状（只取 effort）。
type anthropicOutputConfig struct {
	Effort string `json:"effort"`
}

// NormalizeAnthropicReasoning 归一 Anthropic Messages 请求的推理强度，按官方两处信号取值。
//
// 官方推理强度有两个合法来源（见 platform.claude.com effort / adaptive-thinking 文档）：
//   - output_config.effort：新的 effort 档位旋钮（low/medium/high/xhigh/max），常与
//     thinking:{type:"adaptive"} 搭配；这是权威档位，优先。
//   - thinking.budget_tokens：经典 extended thinking 的思考预算（一个 token 数），按区间换算档位。
//
// 优先级：output_config.effort > thinking.budget_tokens 换算 > 无（nil）。
// 不对「adaptive/enabled 但既无 effort 又无 budget」编造默认档位：官方该场景默认其实是 high 而非
// medium，编造任何值都会与真实语义/上游账单口径不符，故留空表示「客户端未显式指定」（与 sub2api /
// new-api 记录 Claude 的口径一致）。budget→effort 区间映射见下 effortFromBudget。
//
// 参考：
//   - 官方 effort：https://platform.claude.com/docs/en/build-with-claude/effort
//   - budget↔effort 区间：https://github.com/labring/aiproxy/blob/main/docs/REASONING_COMPATIBILITY.md
func NormalizeAnthropicReasoning(outputConfig, thinking json.RawMessage) ReasoningInfo {
	info := ReasoningInfo{}

	// 思考预算始终保留（若客户端提供）：既作为 output_config 缺失时的档位来源，也作为细分事实落库。
	if budget := anthropicThinkingBudget(thinking); budget != nil {
		info.BudgetTokens = budget
	}

	// 1) output_config.effort 优先（官方权威档位）。
	if e := effortFromOutputConfig(outputConfig); e != "" {
		info.Effort = &e
		return info
	}

	// 2) 退回 thinking.budget_tokens 换算档位。
	if info.BudgetTokens != nil {
		e := effortFromBudget(int(*info.BudgetTokens))
		info.Effort = &e
		return info
	}

	// 3) 两处都没有 → 留空，不编造 medium。
	return info
}

// effortFromOutputConfig 读取并校验 Anthropic output_config.effort（官方推理档位）。
// 官方取值 low/medium/high/xhigh/max；原样归一为小写返回（保持与上游账单一致，max 不塌缩为 xhigh），
// 未提供或非法值返回空串。
func effortFromOutputConfig(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var oc anthropicOutputConfig
	if err := json.Unmarshal(raw, &oc); err != nil {
		return ""
	}
	switch e := strings.ToLower(strings.TrimSpace(oc.Effort)); e {
	case effortLow, effortMedium, effortHigh, effortXHigh, effortMax:
		return e
	default:
		return ""
	}
}

// anthropicThinkingBudget 提取 thinking.budget_tokens（经典 extended thinking 预算）。
// type=disabled 或无预算返回 nil；否则返回预算值（含 0）。
func anthropicThinkingBudget(raw json.RawMessage) *int32 {
	if len(raw) == 0 {
		return nil
	}
	var t anthropicThinking
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil
	}
	if t.Type == "disabled" || t.BudgetTokens == nil {
		return nil
	}
	b := int32(*t.BudgetTokens)
	return &b
}

// effortFromBudget 把 Anthropic thinking budget_tokens 归一成统一档位（区间边界取自 aiproxy 文档）。
func effortFromBudget(budget int) string {
	switch {
	case budget <= 0:
		return effortNone
	case budget <= 1024:
		return effortMinimal
	case budget <= 4096:
		return effortLow
	case budget <= 12288:
		return effortMedium
	case budget <= 24576:
		return effortHigh
	default:
		return effortXHigh
	}
}
