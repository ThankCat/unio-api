package modelcatalog

import "sort"

// models 表 source 取值（与 migration 000007 CHECK 对齐）；与 model_capabilities.source 是不同枚举。
const (
	modelSourceSeedModelsDev = "seed_models_dev"
	modelSourceManual        = "manual"
)

// ExistingModel 是合并规划所需的库内已有模型快照（仅带 canonical_id 的行）。
type ExistingModel struct {
	ID          int64
	CanonicalID string
	Source      string
	Removed     bool
}

// Plan 是 models.dev 同步的合并决策结果，全部为纯函数推导，便于审计与测试。
type Plan struct {
	// Inserts 是 feed 中库内尚无的 canonical 模型（将以 disabled 新增并写粗能力位）。
	Inserts []CanonicalModel
	// Updates 是 feed 中已存在且 source=seed_models_dev 的模型（仅覆盖元数据）。
	Updates []CanonicalModel
	// Conflicts 是 feed 中已存在但 source=manual/import 的 canonical_id（跳过，登记供 review）。
	Conflicts []string
	// Removals 是库内 source=seed_models_dev、未标记删除、但 feed 已不含的 canonical_id（标记 disabled+removed）。
	Removals []string
}

// PlanSync 纯函数推导 models.dev 同步计划。
//
// 规则（DEC-015 / TASK-12.04）：
//   - feed 有、库内无 → Insert（disabled + 粗能力位）。
//   - feed 有、库内 source=seed_models_dev → Update（仅元数据）。
//   - feed 有、库内 source=manual/import → Conflict（永不覆盖运营数据）。
//   - 库内 source=seed_models_dev 未标记删除、feed 无 → Removal（标记不删除）。
func PlanSync(feed Feed, existing []ExistingModel) Plan {
	existingByCanonical := make(map[string]ExistingModel, len(existing))
	for _, model := range existing {
		existingByCanonical[model.CanonicalID] = model
	}

	feedCanonical := make(map[string]struct{}, len(feed.Models))
	var plan Plan

	for _, model := range feed.Models {
		feedCanonical[model.CanonicalID] = struct{}{}

		current, ok := existingByCanonical[model.CanonicalID]
		switch {
		case !ok:
			plan.Inserts = append(plan.Inserts, model)
		case current.Source == modelSourceSeedModelsDev:
			plan.Updates = append(plan.Updates, model)
		default:
			plan.Conflicts = append(plan.Conflicts, model.CanonicalID)
		}
	}

	for _, model := range existing {
		if model.Source != modelSourceSeedModelsDev || model.Removed {
			continue
		}
		if _, present := feedCanonical[model.CanonicalID]; !present {
			plan.Removals = append(plan.Removals, model.CanonicalID)
		}
	}

	sort.Strings(plan.Conflicts)
	sort.Strings(plan.Removals)

	return plan
}
