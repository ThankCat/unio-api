package modelcatalog

import "sort"

// ExistingCatalogEntry 是库内已有目录条目的精简快照，用于推导上游下架。
type ExistingCatalogEntry struct {
	CanonicalID string
	Removed     bool
}

// Plan 是 models.dev 同步到目录表的合并决策结果，纯函数推导，便于审计与测试。
//
// 阶段 14 起目录是专表，不再有「manual 守护 / 共享表冲突」概念：
//   - feed 全量条目 → Upsert（覆盖元数据 + 能力提示 + 指纹）。
//   - 库内未标记下架、feed 已不含 → Removal（标记下架，不删本地行）。
type Plan struct {
	Upserts  []CanonicalModel
	Removals []string
}

// PlanSync 纯函数推导 models.dev 同步到目录表的计划。
func PlanSync(feed Feed, existing []ExistingCatalogEntry) Plan {
	feedCanonical := make(map[string]struct{}, len(feed.Models))
	for _, model := range feed.Models {
		feedCanonical[model.CanonicalID] = struct{}{}
	}

	plan := Plan{Upserts: feed.Models}
	for _, entry := range existing {
		if entry.Removed {
			continue
		}
		if _, present := feedCanonical[entry.CanonicalID]; !present {
			plan.Removals = append(plan.Removals, entry.CanonicalID)
		}
	}
	sort.Strings(plan.Removals)

	return plan
}
