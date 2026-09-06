package modelrouting

import (
	"testing"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// SnapshotMany 是 fail-closed 的：任何一条候选 revision 漂移都会让整批失败。
// 那对准入是对的，但观测接口不能因此白屏——这里守的就是降级。

func poolRow(channelID int64, bindingStatus string) sqlc.ModelRuntimePoolRow {
	return sqlc.ModelRuntimePoolRow{
		ChannelID:               channelID,
		ChannelName:             "ch",
		ChannelStatus:           "enabled",
		CredentialValid:         true,
		Priority:                10,
		ProviderID:              channelID * 10,
		ProviderName:            "prov",
		ProviderStatus:          "enabled",
		BindingStatus:           bindingStatus,
		ChannelConfigRevision:   1,
		ChannelCapacityRevision: 1,
		ProviderOriginRevision:  1,
		ProviderStatusRevision:  1,
	}
}

// SnapshotMany 失败时不能整体报错：改用不校验 revision 的观测读，并如实标记降级。
func TestCandidatesFallsBackWhenSnapshotFails(t *testing.T) {
	observed := []breakerstore.ObservedChannelRuntime{
		{
			ChannelID:     7,
			Concurrency:   breakerstore.CapacityUsage{Used: 3, Limit: 10},
			CapacityKnown: true,
		},
	}
	view, err := buildCandidateView(
		[]sqlc.ModelRuntimePoolRow{poolRow(7, "enabled")},
		nil,
		observed,
		nil,
		false,
		"stale_revision",
	)
	if err != nil {
		t.Fatalf("build candidate view: %v", err)
	}
	if view.RuntimeAvailable {
		t.Fatal("runtime must be reported as degraded when SnapshotMany failed")
	}
	if view.RuntimeErrorCode != "stale_revision" {
		t.Fatalf("runtime error code = %q, want stale_revision", view.RuntimeErrorCode)
	}
	if len(view.Candidates) != 1 {
		t.Fatalf("expected the candidate to survive the degrade, got %d", len(view.Candidates))
	}
	got := view.Candidates[0]
	// PG 事实必须保留，否则页面上什么都不剩。
	if got.ChannelID != 7 || !got.CredentialValid || got.Priority != 10 {
		t.Fatalf("database facts must survive the degrade: %+v", got)
	}
	// 降级路径仍能给出并发与容量，因为 ObserveChannels 不校验 revision。
	if got.ConcurrencyUsed == nil || *got.ConcurrencyUsed != 3 {
		t.Fatalf("degraded view should still carry concurrency: %+v", got)
	}
}

// 只有该模型的启用绑定才是候选。ModelRuntimePool 故意返回全部渠道（它要解释
// 「为什么没进候选」），候选视图必须自己筛掉未绑定与已停用绑定的渠道。
func TestCandidatesKeepsOnlyEnabledBindings(t *testing.T) {
	got := enabledBindings([]sqlc.ModelRuntimePoolRow{
		poolRow(1, "enabled"),
		poolRow(2, "disabled"),
		poolRow(3, ""),
	})
	if len(got) != 1 || got[0].ChannelID != 1 {
		t.Fatalf("only the enabled binding is a candidate: %+v", got)
	}
}

// 运行态读齐时，候选状态要来自 SnapshotMany 的判定而不是自己重算。
func TestCandidatesUsesSnapshotStatus(t *testing.T) {
	snapshots := map[int64]breakerstore.CandidateSnapshot{
		7: {
			Status:              breakerstore.CandidateSnapshotRateLimited,
			Concurrency:         breakerstore.CapacityUsage{Used: 1, Limit: 5},
			CooldownRemainingMs: 8_000,
		},
	}
	view, err := buildCandidateView(
		[]sqlc.ModelRuntimePoolRow{poolRow(7, "enabled")},
		snapshots, nil,
		map[int64]breakerstore.ChannelSampleWindow{7: {RPM: 42}},
		true, "",
	)
	if err != nil {
		t.Fatalf("build candidate view: %v", err)
	}
	got := view.Candidates[0]
	if got.RuntimeStatus != string(breakerstore.CandidateSnapshotRateLimited) {
		t.Fatalf("runtime status = %q, want rate_limited", got.RuntimeStatus)
	}
	if got.CooldownRemainingMs == nil || *got.CooldownRemainingMs != 8_000 {
		t.Fatalf("cooldown must come from the snapshot: %+v", got)
	}
	if got.RPM == nil || *got.RPM != 42 {
		t.Fatalf("rpm must come from the sample window: %+v", got)
	}
}
