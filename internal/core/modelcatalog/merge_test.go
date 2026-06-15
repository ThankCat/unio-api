package modelcatalog

import (
	"reflect"
	"testing"
)

func feedFromCanonical(ids ...string) Feed {
	models := make([]CanonicalModel, 0, len(ids))
	for _, id := range ids {
		lab, _ := splitCanonicalID(id)
		models = append(models, CanonicalModel{CanonicalID: id, Lab: lab, DisplayName: id})
	}
	return Feed{Models: models}
}

func TestPlanSyncUpsertsAllFeedEntries(t *testing.T) {
	plan := PlanSync(feedFromCanonical("deepseek/a", "deepseek/b"), nil)

	if len(plan.Upserts) != 2 {
		t.Fatalf("want 2 upserts, got %+v", plan.Upserts)
	}
	if len(plan.Removals) != 0 {
		t.Fatalf("no removals expected, got %+v", plan.Removals)
	}
}

func TestPlanSyncMarksMissingEntriesRemoved(t *testing.T) {
	existing := []ExistingCatalogEntry{
		{CanonicalID: "deepseek/gone"},
		{CanonicalID: "deepseek/already-gone", Removed: true},
		{CanonicalID: "deepseek/kept"},
	}
	plan := PlanSync(feedFromCanonical("deepseek/kept"), existing)

	// feed 全量都进 Upserts。
	if len(plan.Upserts) != 1 || plan.Upserts[0].CanonicalID != "deepseek/kept" {
		t.Fatalf("want upsert for deepseek/kept, got %+v", plan.Upserts)
	}
	// 只有「未标记下架且 feed 不含」的条目进 Removals；已下架的不重复标记。
	if !reflect.DeepEqual(plan.Removals, []string{"deepseek/gone"}) {
		t.Fatalf("want removal for deepseek/gone only, got %+v", plan.Removals)
	}
}

func TestPlanSyncFeedEntryNeverRemoved(t *testing.T) {
	existing := []ExistingCatalogEntry{{CanonicalID: "deepseek/a"}}
	plan := PlanSync(feedFromCanonical("deepseek/a"), existing)

	if len(plan.Removals) != 0 {
		t.Fatalf("feed entry must not be removed, got %+v", plan.Removals)
	}
}
