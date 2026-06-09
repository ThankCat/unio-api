package capability

import (
	"context"
	"encoding/json"
	"fmt"
)

// Declaration 是 adapter 能力种子的一条声明：某能力 key 在该 adapter 下的支持级别与可选 limits。
type Declaration struct {
	// Key 是已注册的稳定能力标识。
	Key Key

	// SupportLevel 是该 adapter 对该能力的支持级别（full / limited / unsupported）。
	SupportLevel SupportLevel

	// Limits 是 limited 级别下的进一步约束（如 reasoning.effort 的可选枚举）；其余级别必须为空。
	Limits json.RawMessage
}

// AdapterProfile 是某个 provider adapter（按协议族）对外声明的能力画像。
//
// 它与该 adapter 的出站 dropUnsupported 同源维护：unsupported 对应出站会被 Drop 的能力，
// limited 对应被 Adapt（如归一）的能力，full 对应透传放行的能力。作为 model_capabilities 的
// 初始 adapter_seed 来源（source=adapter_seed），admin / models.dev 后续可覆盖。
//
// adapter 包的一致性测试以真实 dropUnsupported 行为守护本画像不漂移：闸门放行（full/limited）
// 的能力不应再被 adapter Drop，被 Drop 的能力必须声明 unsupported（见 DEC-015、阶段 12
// TASK-12.06 与 GAP-12-007）。
type AdapterProfile struct {
	// Provider 是上游 provider 标识（如 "deepseek"）。
	Provider string

	// Protocol 是协议族（如 "openai" / "anthropic"）；同一 provider 不同协议族画像可不同。
	Protocol string

	// Declarations 是该 adapter 的能力声明集合，key 唯一。
	Declarations []Declaration
}

// Validate 校验画像自洽：provider/protocol 非空、每条声明 key 已注册、支持级别合法、key 不重复，
// 且 limits 仅在 limited 级别出现。它是 adapter 画像写入前的开发期不变量守护。
func (p AdapterProfile) Validate() error {
	if p.Provider == "" {
		return fmt.Errorf("capability: adapter profile provider is empty")
	}
	if p.Protocol == "" {
		return fmt.Errorf("capability: adapter profile %q protocol is empty", p.Provider)
	}

	seen := make(map[Key]struct{}, len(p.Declarations))
	for _, d := range p.Declarations {
		if !IsRegisteredKey(d.Key) {
			return fmt.Errorf("capability: adapter profile %s/%s declares unregistered key %q", p.Provider, p.Protocol, d.Key)
		}
		if !IsValidSupportLevel(d.SupportLevel) {
			return fmt.Errorf("capability: adapter profile %s/%s key %q has invalid support level %q", p.Provider, p.Protocol, d.Key, d.SupportLevel)
		}
		if _, dup := seen[d.Key]; dup {
			return fmt.Errorf("capability: adapter profile %s/%s declares duplicate key %q", p.Provider, p.Protocol, d.Key)
		}
		seen[d.Key] = struct{}{}

		if d.SupportLevel != SupportLevelLimited && len(d.Limits) > 0 {
			return fmt.Errorf("capability: adapter profile %s/%s key %q sets limits at non-limited level %q", p.Provider, p.Protocol, d.Key, d.SupportLevel)
		}
	}

	return nil
}

// MaterializeAdapterSeed 把 adapter 能力画像幂等 upsert 进给定模型的 model_capabilities，
// source 固定为 adapter_seed。每条声明独立 upsert（ON CONFLICT 覆盖），可重入。
//
// 现阶段尚无模型 provisioning，调用方（未来的 admin CRUD 或同步任务）负责决定把哪个 adapter
// 画像应用到哪些 model_id；本函数只负责忠实写入，不感知 channel/model 拓扑。
func MaterializeAdapterSeed(ctx context.Context, store Store, modelID int64, profile AdapterProfile, updatedBy *string) error {
	if err := profile.Validate(); err != nil {
		return err
	}

	for _, d := range profile.Declarations {
		if _, err := store.UpsertModelCapability(ctx, UpsertModelCapabilityParams{
			ModelID:      modelID,
			Key:          d.Key,
			SupportLevel: d.SupportLevel,
			Limits:       d.Limits,
			Source:       SourceAdapterSeed,
			UpdatedBy:    updatedBy,
		}); err != nil {
			return err
		}
	}

	return nil
}
