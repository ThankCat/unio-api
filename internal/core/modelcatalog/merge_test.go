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

func TestPlanSyncInsertsNewModels(t *testing.T) {
	plan := PlanSync(feedFromCanonical("deepseek/a", "deepseek/b"), nil)

	if len(plan.Inserts) != 2 || len(plan.Updates) != 0 || len(plan.Conflicts) != 0 || len(plan.Removals) != 0 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestPlanSyncUpdatesSeedRows(t *testing.T) {
	existing := []ExistingModel{
		{ID: 1, CanonicalID: "deepseek/a", Source: modelSourceSeedModelsDev},
	}
	plan := PlanSync(feedFromCanonical("deepseek/a"), existing)

	if len(plan.Updates) != 1 || plan.Updates[0].CanonicalID != "deepseek/a" {
		t.Fatalf("want update for deepseek/a, got %+v", plan)
	}
	if len(plan.Inserts) != 0 || len(plan.Conflicts) != 0 || len(plan.Removals) != 0 {
		t.Fatalf("unexpected plan: %+v", plan)
	}
}

func TestPlanSyncNeverTouchesManualRows(t *testing.T) {
	existing := []ExistingModel{
		{ID: 1, CanonicalID: "deepseek/a", Source: modelSourceManual},
		{ID: 2, CanonicalID: "deepseek/imported", Source: "import"},
	}
	plan := PlanSync(feedFromCanonical("deepseek/a"), existing)

	if !reflect.DeepEqual(plan.Conflicts, []string{"deepseek/a"}) {
		t.Fatalf("want manual conflict for deepseek/a, got %+v", plan.Conflicts)
	}
	if len(plan.Updates) != 0 || len(plan.Inserts) != 0 {
		t.Fatalf("manual row must not be updated/inserted: %+v", plan)
	}
	// import 行不在 feed 中，但 source != seed_models_dev，不能标记 removal。
	if len(plan.Removals) != 0 {
		t.Fatalf("only seed rows can be removed, got %+v", plan.Removals)
	}
}

func TestPlanSyncMarksMissingSeedRowsRemoved(t *testing.T) {
	existing := []ExistingModel{
		{ID: 1, CanonicalID: "deepseek/gone", Source: modelSourceSeedModelsDev},
		{ID: 2, CanonicalID: "deepseek/already-gone", Source: modelSourceSeedModelsDev, Removed: true},
		{ID: 3, CanonicalID: "deepseek/manual-gone", Source: modelSourceManual},
	}
	plan := PlanSync(feedFromCanonical("deepseek/kept"), existing)

	if !reflect.DeepEqual(plan.Removals, []string{"deepseek/gone"}) {
		t.Fatalf("want removal for deepseek/gone only, got %+v", plan.Removals)
	}
	if len(plan.Inserts) != 1 || plan.Inserts[0].CanonicalID != "deepseek/kept" {
		t.Fatalf("want insert for deepseek/kept, got %+v", plan.Inserts)
	}
}
