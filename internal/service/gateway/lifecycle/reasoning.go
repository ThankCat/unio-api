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

// 统一推理强度档位词表（与 sub2api / aiproxy 看齐）。
const (
	effortNone    = "none"
	effortMinimal = "minimal"
	effortLow     = "low"
	effortMedium  = "medium"
	effortHigh    = "high"
	effortXHigh   = "xhigh"
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

// NormalizeAnthropicThinking 解析 Anthropic thinking 并归一为统一档位 + 保留原始预算。
//
// 规则：type=disabled/空 → 无；有 budget_tokens → 按区间归一档位并保留原始预算；
// enabled/adaptive 但无 budget → 默认 medium。budget→effort 区间映射见下 effortFromBudget。
//
// 参考：aiproxy REASONING_COMPATIBILITY（budget↔effort 区间边界的来源）
//
//	https://github.com/labring/aiproxy/blob/main/docs/REASONING_COMPATIBILITY.md
func NormalizeAnthropicThinking(raw json.RawMessage) ReasoningInfo {
	if len(raw) == 0 {
		return ReasoningInfo{}
	}
	var t anthropicThinking
	if err := json.Unmarshal(raw, &t); err != nil {
		return ReasoningInfo{}
	}
	if t.Type == "disabled" || (t.Type == "" && t.BudgetTokens == nil) {
		return ReasoningInfo{}
	}
	if t.BudgetTokens != nil {
		b := int32(*t.BudgetTokens)
		e := effortFromBudget(*t.BudgetTokens)
		return ReasoningInfo{Effort: &e, BudgetTokens: &b}
	}
	// enabled/adaptive 无预算 → 默认 medium。
	e := effortMedium
	return ReasoningInfo{Effort: &e}
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
