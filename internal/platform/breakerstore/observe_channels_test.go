package breakerstore

import (
	"context"
	"testing"
)

// 观测批量读与 SnapshotMany 的差别是刻意的：SnapshotMany 服务准入，任何 revision 漂移都必须整批失败；
// 观测只是给管理员看一眼，读到什么算什么。这里守的就是「不因为缺 control 或 revision 不齐而失败」。

func TestObserveChannelsReadsBreakerConcurrencyAndCooldown(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	cfg := testConfig()
	const channelID, providerID = int64(9101), int64(91010)
	seedAttemptControls(t, s, cfg, channelID, `{"concurrency":5}`)

	// 占一个并发名额，观测应当看到 used=1。这里不能用 acquire 辅助函数：
	// 它内部会以 concurrency:null 重新 seed，会盖掉上面设的上限 5。
	adm, err := acquireAttempt(t, s, withAttemptControlRevisions(AcquireAttemptInput{
		PermitID: "observe-1", AdmissionFingerprint: "observe-1-fp", RequestAdmissionID: "req",
		ProviderID: providerID, ChannelID: channelID, OriginRevision: 1, ProviderStatusRevision: 1,
		ChannelConfigRevision: 1, ModelID: 100, UpstreamEndpoint: EndpointChatCompletions,
		RequestMode: ModeNonStream,
	}))
	if err != nil || adm.Mode != AdmissionPermit {
		t.Fatalf("acquire: mode=%s reason=%s err=%v", adm.Mode, adm.Reason, err)
	}
	if _, err := s.SetChannel429Cooldown(ctx, channelID, 60_000, 60_000); err != nil {
		t.Fatalf("set cooldown: %v", err)
	}

	rows, err := s.ObserveChannels(ctx, []int64{channelID})
	if err != nil {
		t.Fatalf("observe channels: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	got := rows[0]
	if got.ChannelID != channelID {
		t.Fatalf("channel id = %d, want %d", got.ChannelID, channelID)
	}
	if got.Concurrency.Used != 1 {
		t.Fatalf("concurrency used = %d, want 1", got.Concurrency.Used)
	}
	if got.Concurrency.Limit != 5 {
		t.Fatalf("concurrency limit = %d, want 5", got.Concurrency.Limit)
	}
	if !got.CapacityKnown {
		t.Fatal("capacity must be known when the control is present and parseable")
	}
	if got.CooldownRemainingMs <= 0 {
		t.Fatalf("cooldown remaining = %d, want > 0", got.CooldownRemainingMs)
	}
}

// 全新渠道没有任何 Redis 状态，观测要返回零值而不是报错——否则新建渠道会让整页打不开。
func TestObserveChannelsTreatsMissingStateAsZero(t *testing.T) {
	s, _, _ := newTestStore(t)

	rows, err := s.ObserveChannels(context.Background(), []int64{9201})
	if err != nil {
		t.Fatalf("observe channels: %v", err)
	}
	if len(rows) != 1 {
		t.Fatalf("expected 1 row, got %d", len(rows))
	}
	got := rows[0]
	if got.Breaker.Exists {
		t.Fatal("breaker must not exist for a channel that never ran")
	}
	if got.Concurrency.Used != 0 || got.CooldownRemainingMs != 0 {
		t.Fatalf("unexpected runtime for a fresh channel: %+v", got)
	}
	// 容量 control 缺失时上限不可信，必须标出来而不是伪装成 0（0 在这里意为「不限」）。
	if got.CapacityKnown {
		t.Fatal("capacity must be reported as unknown when the control is absent")
	}
}

func TestObserveChannelsAcceptsEmptyInput(t *testing.T) {
	s, _, _ := newTestStore(t)

	rows, err := s.ObserveChannels(context.Background(), nil)
	if err != nil {
		t.Fatalf("observe channels with no ids: %v", err)
	}
	if len(rows) != 0 {
		t.Fatalf("expected no rows, got %d", len(rows))
	}
}

// 容量 control 损坏只降级那一行，不拖垮整批——SnapshotMany 在同样情况下会整批失败，
// 那是准入该有的严格；观测要能继续把其余渠道显示出来。
func TestObserveChannelsDegradesBrokenCapacityControlPerRow(t *testing.T) {
	s, client, _ := newTestStore(t)
	ctx := context.Background()
	cfg := testConfig()
	const healthyChannel, brokenChannel = int64(9301), int64(9302)
	seedAttemptControls(t, s, cfg, healthyChannel, `{"concurrency":3}`)

	// 造一个是 hash 但没有 active_payload 的 control，模拟同步中断留下的半成品。
	if err := client.HSet(ctx, s.keys.channelCapacity(brokenChannel), "pending_payload", "{}").Err(); err != nil {
		t.Fatalf("seed broken capacity control: %v", err)
	}

	rows, err := s.ObserveChannels(ctx, []int64{healthyChannel, brokenChannel})
	if err != nil {
		t.Fatalf("a broken control must not fail the batch: %v", err)
	}
	if len(rows) != 2 {
		t.Fatalf("expected 2 rows, got %d", len(rows))
	}
	if !rows[0].CapacityKnown || rows[0].Concurrency.Limit != 3 {
		t.Fatalf("healthy row should keep its limit: %+v", rows[0])
	}
	if rows[1].CapacityKnown {
		t.Fatalf("broken row must be reported as unknown: %+v", rows[1])
	}
}
