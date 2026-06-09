package capability

import (
	"encoding/json"
	"sort"
	"strings"
)

// GateResult 是 capability 闸门对一次「模型 × 候选 channel × required」评估的稳定结论。
//
// 它既驱动 observe 模式的 metric/审计，也作为 enforce 模式（TASK-12.08）拒绝路径的判据。
type GateResult string

const (
	// GateResultOK 表示模型声明支持全部 required，且至少有一个候选 channel 未被 override 关闭。
	GateResultOK GateResult = "ok"

	// GateResultModelUnavailable 表示模型本身缺失（无声明/unsupported/limited 超限）某些 required 能力。
	GateResultModelUnavailable GateResult = "model_unavailable"

	// GateResultChannelUnavailable 表示模型支持全部 required，但所有候选 channel 都被 override 关闭了某能力。
	GateResultChannelUnavailable GateResult = "channel_unavailable"

	// GateResultUnprovisioned 表示模型还没有任何能力声明行（Layer 2 未铺数据），闸门跳过判定放行。
	GateResultUnprovisioned GateResult = "unprovisioned"

	// GateResultNoRequired 表示本次请求没有任何 required 能力（理论上不会出现，文本基线恒在），跳过判定。
	GateResultNoRequired GateResult = "no_required"

	// GateResultError 表示闸门读取能力数据失败而降级（observe/enforce 都 fail-open），仅供 metric/审计区分。
	// 注意：纯函数 Evaluate 不会产出该值，它由读取存储的 checker 在异常时赋予。
	GateResultError GateResult = "error"
)

// ChannelCaps 是一个候选 channel 的能力收紧事实，供 gate 评估某 channel 是否仍满足 required。
//
// Overrides 只能做减法（limited/unsupported），不会声明模型层未声明的能力（见 DEC-015 / 生产验收 #6）。
type ChannelCaps struct {
	ChannelID int64
	Overrides []ChannelOverride
}

// RequestLimits 承载请求侧触发的「带值」能力约束，供 gate 判定 limited 是否超限。
//
// 当前只建模 reasoning.effort 档位；其余能力是布尔触发，仅判定 key 存在性。
// 档位值由三协议 ingress `RequestLimits(req)`（经 capability.InferLimits）抽取，
// 透传 routing.ChatRouteRequest.RequestLimits → CapabilityCheckInput.Limits → 此处；
// 零值（请求未声明档位或协议无该概念，如 Anthropic thinking budget）时 limited 视为满足。
type RequestLimits struct {
	// ReasoningEffort 是请求声明的 reasoning effort 档位（"", "low", "medium", "high"）。
	ReasoningEffort string
}

// Evaluation 是 gate 评估的完整结论，含 model 层与 channel 层缺失能力明细，供审计与错误渲染。
type Evaluation struct {
	// Result 是稳定结论分类。
	Result GateResult
	// Provisioned 表示模型是否已有能力声明行（false 时 Result 必为 unprovisioned）。
	Provisioned bool
	// MissingModel 是模型层缺失（无声明/unsupported/limited 超限）的 required 能力，升序去重。
	MissingModel []Key
	// MissingChannel 是模型支持、但所有候选 channel 都 override 关闭的能力，升序去重。
	MissingChannel []Key
}

// Evaluate 是 capability 闸门的纯判定函数：给定模型层声明、候选 channel 收紧策略、required 能力集与请求限制，
// 计算稳定结论。无 IO、输入只读，闸门 observe/enforce 共用此判定。
//
// 判定顺序与 unprovisioned 跳过策略（DEC-015 灰度先行约定）：
//  1. required 为空 → no_required（跳过）。
//  2. 模型零声明行 → unprovisioned（跳过放行，等待 Layer 2 铺数据）。
//  3. 模型已声明：required key 缺声明 / unsupported / limited 超限 → model_unavailable。
//  4. 模型满足全部 required：逐候选 channel 应用 override（只能降级）；
//     至少一个候选满足则 ok，否则 channel_unavailable。
func Evaluate(modelCaps []ModelCapability, channels []ChannelCaps, required Set, limits RequestLimits) Evaluation {
	requiredKeys := required.Keys()
	if len(requiredKeys) == 0 {
		return Evaluation{Result: GateResultNoRequired}
	}

	if len(modelCaps) == 0 {
		return Evaluation{Result: GateResultUnprovisioned, Provisioned: false}
	}

	modelByKey := make(map[Key]ModelCapability, len(modelCaps))
	for _, mc := range modelCaps {
		modelByKey[mc.Key] = mc
	}

	var missingModel []Key
	for _, key := range requiredKeys {
		mc, ok := modelByKey[key]
		if !ok || mc.SupportLevel == SupportLevelUnsupported {
			missingModel = append(missingModel, key)
			continue
		}
		if mc.SupportLevel == SupportLevelLimited && limitViolated(key, mc.Limits, limits) {
			missingModel = append(missingModel, key)
		}
	}
	if len(missingModel) > 0 {
		return Evaluation{Result: GateResultModelUnavailable, Provisioned: true, MissingModel: missingModel}
	}

	// 模型满足全部 required。没有候选 channel 信息时（routing 进入此处必有至少一个候选），保守视为 ok。
	if len(channels) == 0 {
		return Evaluation{Result: GateResultOK, Provisioned: true}
	}

	missingChannel := make(map[Key]struct{})
	anyChannelOK := false
	for _, ch := range channels {
		overrideByKey := make(map[Key]ChannelOverride, len(ch.Overrides))
		for _, ov := range ch.Overrides {
			overrideByKey[ov.Key] = ov
		}

		channelOK := true
		for _, key := range requiredKeys {
			ov, ok := overrideByKey[key]
			if !ok {
				// 无 override：沿用模型层支持级别（已确认满足）。
				continue
			}
			if ov.SupportLevel == SupportLevelUnsupported {
				channelOK = false
				missingChannel[key] = struct{}{}
				continue
			}
			if ov.SupportLevel == SupportLevelLimited && limitViolated(key, ov.Limits, limits) {
				channelOK = false
				missingChannel[key] = struct{}{}
			}
		}
		if channelOK {
			anyChannelOK = true
		}
	}

	if anyChannelOK {
		return Evaluation{Result: GateResultOK, Provisioned: true}
	}

	return Evaluation{
		Result:         GateResultChannelUnavailable,
		Provisioned:    true,
		MissingChannel: sortedKeys(missingChannel),
	}
}

// limitViolated 判定某 limited 能力的声明 limits 是否被请求侧约束突破。
//
// 当前只理解 reasoning.effort 的 max_effort 上限；未知能力或缺约束一律视为「未突破」（保守放行），
// 避免在 limits 语义未建模时误判。
func limitViolated(key Key, declared json.RawMessage, req RequestLimits) bool {
	switch key {
	case KeyReasoningEffort:
		if req.ReasoningEffort == "" || len(declared) == 0 {
			return false
		}
		var limit struct {
			MaxEffort string `json:"max_effort"`
		}
		if err := json.Unmarshal(declared, &limit); err != nil || limit.MaxEffort == "" {
			return false
		}
		return effortRank(req.ReasoningEffort) > effortRank(limit.MaxEffort)
	default:
		return false
	}
}

// effortRank 把 reasoning effort 档位映射为可比较等级；未知档位记为 0（不参与超限判定）。
func effortRank(effort string) int {
	switch strings.ToLower(strings.TrimSpace(effort)) {
	case "low":
		return 1
	case "medium":
		return 2
	case "high":
		return 3
	default:
		return 0
	}
}

// sortedKeys 把缺失能力集合转成升序稳定序列，保证审计与断言可比较。
func sortedKeys(set map[Key]struct{}) []Key {
	keys := make([]Key, 0, len(set))
	for key := range set {
		keys = append(keys, key)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })

	return keys
}
