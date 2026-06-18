// Package calibration 实现「能力自动校正」：从真实成功流量被动学习模型实际具备的能力，
// 补齐 model_capabilities（DESIGN-capability-autocalibration / DEC-020）。
//
// 纪律：被动（不主动探针）、增量（watermark）、证据式（只有响应真用到才算强证据）、add-only、
// manual 永远优先、per-model 可控、全程可审计可撤销。Plan 是纯函数，规则集中可单测。
package calibration

import (
	"sort"
	"time"

	"github.com/ThankCat/unio-api/internal/core/capability"
)

// Mode 是 per-model 能力自动校正档位（models.capability_autocalibrate）。
type Mode string

const (
	// ModeOff 表示该模型不参与自动校正。
	ModeOff Mode = "off"
	// ModeSuggest 表示只产生建议待人工采纳（默认）。
	ModeSuggest Mode = "suggest"
	// ModeAuto 表示强证据能力自动补；弱证据仍只建议。
	ModeAuto Mode = "auto"
)

// EvidenceKind 标记一条决策的证据强弱。
type EvidenceKind string

const (
	// EvidenceStrong 表示响应真用到了该能力（finish_class=tool_use / cache 命中 / reasoning token）。
	EvidenceStrong EvidenceKind = "strong"
	// EvidenceWeak 表示仅带该能力字段且请求成功，未证明真生效。
	EvidenceWeak EvidenceKind = "weak"
)

// strongEvidenceKeys 是「响应可证明真用到」的能力集合（其余能力 evidence 维度无意义，恒弱证据）。
var strongEvidenceKeys = map[capability.Key]struct{}{
	capability.KeyToolsFunction:       {},
	capability.KeyToolsCustom:         {},
	capability.KeyToolsParallel:       {},
	capability.KeyToolsChoiceRequired: {},
	capability.KeyPromptCache:         {},
	capability.KeyReasoningEffort:     {},
	capability.KeyReasoningBudget:     {},
}

// toolCallEvidenceKeys 是「finish_class=tool_use 即可作强证据」的工具能力集合。
//
// 内置工具（tools.builtin.*）不在此列：tool_use 只证明模型发起了工具调用，不证明上游服务端真执行了
// 联网搜索 / MCP 等内置工具，故内置工具恒为弱证据（只建议）。
var toolCallEvidenceKeys = map[capability.Key]struct{}{
	capability.KeyToolsFunction:       {},
	capability.KeyToolsCustom:         {},
	capability.KeyToolsParallel:       {},
	capability.KeyToolsChoiceRequired: {},
}

// limitedDimensionKeys 是「带 limits 细粒度」的能力：自动校正只能粗粒度 full，可能放过不支持的档位，
// 故即便强证据也只进建议、不自动补（细粒度仍人工）。
var limitedDimensionKeys = map[capability.Key]struct{}{
	capability.KeyReasoningEffort: {},
	capability.KeyReasoningBudget: {},
}

// AttemptHasEvidence 判断某次成功尝试是否为某能力提供了强证据（聚合阶段按 key 归因）。
func AttemptHasEvidence(key capability.Key, finishClass string, cacheReadTokens, reasoningTokens int64) bool {
	if _, ok := toolCallEvidenceKeys[key]; ok {
		return finishClass == "tool_use"
	}
	switch key {
	case capability.KeyPromptCache:
		return cacheReadTokens > 0
	case capability.KeyReasoningEffort, capability.KeyReasoningBudget:
		return reasoningTokens > 0
	default:
		return false
	}
}

func isStrongEvidenceKey(key capability.Key) bool {
	_, ok := strongEvidenceKeys[key]
	return ok
}

func keyHasLimitedDimension(key capability.Key) bool {
	_, ok := limitedDimensionKeys[key]
	return ok
}

// Observation 是一条 (模型, 渠道, 能力) 的成功/证据观测计数（rollup 行，决策输入）。
type Observation struct {
	ModelID   int64
	ChannelID int64
	Key       capability.Key
	Success   int64
	Evidence  int64
	LastSeen  time.Time
}

// ModelContext 是某模型的决策上下文（档位 / 启用渠道数 / 已声明 / 已忽略）。
type ModelContext struct {
	Mode            Mode
	EnabledChannels int
	Declared        map[capability.Key]struct{}
	Dismissed       map[capability.Key]struct{}
}

// Thresholds 是判定阈值。
type Thresholds struct {
	MinSuccess       int64
	MinEvidenceRatio float64
	Lookback         time.Duration
}

// Rationale 是一条决策的可审计依据（写入 suggestion.rationale）。
type Rationale struct {
	SuccessCount  int64   `json:"success_count"`
	EvidenceCount int64   `json:"evidence_count"`
	EvidenceRatio float64 `json:"evidence_ratio"`
	ChannelIDs    []int64 `json:"channel_ids"`
	LookbackHours float64 `json:"lookback_hours"`
}

// Decision 是一条「建议给某模型补某能力」的判定结果。
type Decision struct {
	ModelID      int64
	Key          capability.Key
	Level        capability.SupportLevel
	EvidenceKind EvidenceKind
	Rationale    Rationale
}

// Plan 是一轮校正的产出：AutoApply 直接写 model_capabilities，Suggestions 等人工采纳。
type Plan struct {
	AutoApply   []Decision
	Suggestions []Decision
}

// groupKey 是 (模型, 能力) 聚合键（跨渠道汇总到模型级再决策）。
type groupKey struct {
	ModelID int64
	Key     capability.Key
}

type groupAgg struct {
	success  int64
	evidence int64
	channels map[int64]struct{}
	lastSeen time.Time
}

// BuildPlan 纯函数：把跨渠道观测按模型聚合，结合各模型上下文与阈值，产出自动补/建议两组决策。
//
// 规则：① 已声明（manual 优先）或已忽略 → 跳过；② 档位 off → 跳过；③ 成功数未达阈值或窗口外 → 跳过；
// ④ 强证据键且比例达标 = strong，否则 weak；⑤ 自动补全部前置：档位 auto + strong + 非 limits 维度键 +
// 模型单渠道；其余 → 建议。
func BuildPlan(observations []Observation, models map[int64]ModelContext, th Thresholds, now time.Time) Plan {
	grouped := make(map[groupKey]*groupAgg)
	staleBefore := now.Add(-th.Lookback)

	for _, obs := range observations {
		mc, ok := models[obs.ModelID]
		if !ok || mc.Mode == ModeOff {
			continue
		}
		if !capability.IsRegisteredKey(obs.Key) {
			continue
		}
		if _, declared := mc.Declared[obs.Key]; declared {
			continue
		}
		if _, dismissed := mc.Dismissed[obs.Key]; dismissed {
			continue
		}

		gk := groupKey{ModelID: obs.ModelID, Key: obs.Key}
		agg := grouped[gk]
		if agg == nil {
			agg = &groupAgg{channels: make(map[int64]struct{})}
			grouped[gk] = agg
		}
		agg.success += obs.Success
		agg.evidence += obs.Evidence
		agg.channels[obs.ChannelID] = struct{}{}
		if obs.LastSeen.After(agg.lastSeen) {
			agg.lastSeen = obs.LastSeen
		}
	}

	var plan Plan
	for gk, agg := range grouped {
		if agg.lastSeen.Before(staleBefore) {
			continue
		}
		if agg.success < th.MinSuccess {
			continue
		}

		ratio := 0.0
		if agg.success > 0 {
			ratio = float64(agg.evidence) / float64(agg.success)
		}
		strong := isStrongEvidenceKey(gk.Key) && ratio >= th.MinEvidenceRatio
		kind := EvidenceWeak
		if strong {
			kind = EvidenceStrong
		}

		mc := models[gk.ModelID]
		decision := Decision{
			ModelID:      gk.ModelID,
			Key:          gk.Key,
			Level:        capability.SupportLevelFull,
			EvidenceKind: kind,
			Rationale: Rationale{
				SuccessCount:  agg.success,
				EvidenceCount: agg.evidence,
				EvidenceRatio: ratio,
				ChannelIDs:    sortedChannels(agg.channels),
				LookbackHours: th.Lookback.Hours(),
			},
		}

		autoEligible := mc.Mode == ModeAuto && strong && !keyHasLimitedDimension(gk.Key) && mc.EnabledChannels <= 1
		if autoEligible {
			plan.AutoApply = append(plan.AutoApply, decision)
		} else {
			plan.Suggestions = append(plan.Suggestions, decision)
		}
	}

	sortDecisions(plan.AutoApply)
	sortDecisions(plan.Suggestions)
	return plan
}

// degradationEvidenceRatio 是「已声明强证据能力近期证据比例」低于此值即怀疑上游退化的告警阈值。
// 远低于自动补的 MinEvidenceRatio，留足缓冲避免抖动误报；退化只告警、绝不据此删能力（add-only 纪律，
// 删除永远人工）。例：模型声明 tools.function，近期足量成功请求都带 tools 却几乎不再 tool_use → 疑似上游静默忽略工具。
const degradationEvidenceRatio = 0.2

// Degradation 是一条「已声明强证据能力近期几乎无证据生效」的上游退化告警候选。
type Degradation struct {
	ModelID       int64
	Key           capability.Key
	SuccessCount  int64
	EvidenceCount int64
	EvidenceRatio float64
	ChannelIDs    []int64
}

// DetectDegradations 纯函数：从「近期窗口」观测中，找出已声明的强证据能力却近期成功量足够、
// 但证据比例塌陷（< degradationEvidenceRatio）的 (模型,能力)——提示上游可能静默退化（如不再真执行工具/缓存/推理）。
//
// 与 BuildPlan 互补：BuildPlan 处理「未声明 → 是否补」，本函数处理「已声明 → 是否还成立」。
// 纪律：只产出告警，绝不据此自动降级或删除能力声明（删除永远人工，避免一次抖动误删）。
// 仅看强证据键（弱证据键证据比例天然低，非退化信号）。recent 应是本轮新窗口的增量观测，不是历史累计。
func DetectDegradations(recent []Observation, models map[int64]ModelContext, th Thresholds) []Degradation {
	grouped := make(map[groupKey]*groupAgg)
	for _, obs := range recent {
		mc, ok := models[obs.ModelID]
		if !ok {
			continue
		}
		if _, declared := mc.Declared[obs.Key]; !declared {
			continue
		}
		if !isStrongEvidenceKey(obs.Key) {
			continue
		}

		gk := groupKey{ModelID: obs.ModelID, Key: obs.Key}
		agg := grouped[gk]
		if agg == nil {
			agg = &groupAgg{channels: make(map[int64]struct{})}
			grouped[gk] = agg
		}
		agg.success += obs.Success
		agg.evidence += obs.Evidence
		agg.channels[obs.ChannelID] = struct{}{}
	}

	var out []Degradation
	for gk, agg := range grouped {
		if agg.success < th.MinSuccess {
			continue
		}
		ratio := 0.0
		if agg.success > 0 {
			ratio = float64(agg.evidence) / float64(agg.success)
		}
		if ratio >= degradationEvidenceRatio {
			continue
		}

		out = append(out, Degradation{
			ModelID:       gk.ModelID,
			Key:           gk.Key,
			SuccessCount:  agg.success,
			EvidenceCount: agg.evidence,
			EvidenceRatio: ratio,
			ChannelIDs:    sortedChannels(agg.channels),
		})
	}

	sort.Slice(out, func(i, j int) bool {
		if out[i].ModelID != out[j].ModelID {
			return out[i].ModelID < out[j].ModelID
		}
		return out[i].Key < out[j].Key
	})
	return out
}

func sortedChannels(set map[int64]struct{}) []int64 {
	out := make([]int64, 0, len(set))
	for id := range set {
		out = append(out, id)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}

func sortDecisions(decisions []Decision) {
	sort.Slice(decisions, func(i, j int) bool {
		if decisions[i].ModelID != decisions[j].ModelID {
			return decisions[i].ModelID < decisions[j].ModelID
		}
		return decisions[i].Key < decisions[j].Key
	})
}
