package breakerstore

import (
	"context"
	"testing"
)

func obsEntry(kind TPMObservationKind, id, minute int64, delta TPMObservationDelta) TPMObservationEntry {
	return TPMObservationEntry{
		Scope: TPMObservationScope{Kind: kind, ID: id, Minute: minute},
		Delta: delta,
	}
}

func TestRecordTPMObservationsIsIdempotentPerOperationID(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	entries := []TPMObservationEntry{
		obsEntry(TPMScopeRoute, 4001, 100, TPMObservationDelta{InputTokens: 120, ProvisionalTokens: 120, ObservedAttempts: 1}),
		obsEntry(TPMScopeChannel, 4002, 100, TPMObservationDelta{OutputTokens: 30, ProvisionalTokens: 30}),
	}

	applied, err := s.RecordTPMObservations(ctx, "batch-1", entries)
	if err != nil || !applied {
		t.Fatalf("first flush applied=%v err=%v", applied, err)
	}
	// 结果未知时观测器会用同一个 operation id 重试；脚本必须原样跳过。
	replayed, err := s.RecordTPMObservations(ctx, "batch-1", entries)
	if err != nil || replayed {
		t.Fatalf("replayed flush applied=%v err=%v, want applied=false", replayed, err)
	}

	routes, err := s.TPMObservations(ctx, TPMScopeRoute, []int64{4001}, 100)
	if err != nil {
		t.Fatalf("read route observation: %v", err)
	}
	if got := routes[4001]; got.InputTokens != 120 || got.ObservedAttempts != 1 || got.TPM() != 120 {
		t.Fatalf("route bucket = %+v, want a single application", got)
	}
	channels, err := s.TPMObservations(ctx, TPMScopeChannel, []int64{4002}, 100)
	if err != nil {
		t.Fatalf("read channel observation: %v", err)
	}
	if got := channels[4002]; got.OutputTokens != 30 || got.TPM() != 30 {
		t.Fatalf("channel bucket = %+v", got)
	}
}

func TestRecordTPMObservationsAccumulatesAcrossBatches(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	for _, id := range []string{"acc-1", "acc-2"} {
		if _, err := s.RecordTPMObservations(ctx, id, []TPMObservationEntry{
			obsEntry(TPMScopeRoute, 4010, 200, TPMObservationDelta{OutputTokens: 25, ProvisionalTokens: 25}),
		}); err != nil {
			t.Fatalf("flush %s: %v", id, err)
		}
	}
	got, err := s.TPMObservations(ctx, TPMScopeRoute, []int64{4010}, 200)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got[4010].OutputTokens != 50 || got[4010].ProvisionalTokens != 50 {
		t.Fatalf("distinct batches must accumulate, got %+v", got[4010])
	}
}

// 可靠 usage 到达后把 provisional 换成实际值：输入补差额、输出按权重修正、provisional 清零。
func TestCorrectTPMObservationsReplacesProvisionalWithActual(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := s.RecordTPMObservations(ctx, "corr-seed", []TPMObservationEntry{
		obsEntry(TPMScopeChannel, 4020, 300, TPMObservationDelta{
			InputTokens: 100, OutputTokens: 40, ProvisionalTokens: 140, ObservedAttempts: 1,
		}),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	result, err := s.CorrectTPMObservations(ctx, "channel:attempt-4020", []TPMObservationEntry{
		obsEntry(TPMScopeChannel, 4020, 300, TPMObservationDelta{
			InputTokens: 20, OutputTokens: 10, ProvisionalTokens: -140,
		}),
	})
	if err != nil || result.Duplicate || result.Applied != 1 || result.Expired != 0 {
		t.Fatalf("correction result=%+v err=%v", result, err)
	}

	got, err := s.TPMObservations(ctx, TPMScopeChannel, []int64{4020}, 300)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got[4020].InputTokens != 120 || got[4020].OutputTokens != 50 || got[4020].ProvisionalTokens != 0 {
		t.Fatalf("corrected bucket = %+v", got[4020])
	}
}

// recovery worker 在另一个进程重放时必须命中同一个 Redis marker，不能二次修正。
func TestCorrectTPMObservationsIsIdempotentAcrossProcesses(t *testing.T) {
	s, client, namespace := newTestStore(t)
	ctx := context.Background()
	if _, err := s.RecordTPMObservations(ctx, "replay-seed", []TPMObservationEntry{
		obsEntry(TPMScopeRoute, 4030, 400, TPMObservationDelta{OutputTokens: 100, ProvisionalTokens: 100}),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	correction := []TPMObservationEntry{
		obsEntry(TPMScopeRoute, 4030, 400, TPMObservationDelta{OutputTokens: -40, ProvisionalTokens: -100}),
	}
	if _, err := s.CorrectTPMObservations(ctx, "route:req-4030", correction); err != nil {
		t.Fatalf("first correction: %v", err)
	}

	// 另一个进程（独立 Store 实例，同一 namespace）重放同一次结算。
	replayStore := NewStore(client, namespace)
	result, err := replayStore.CorrectTPMObservations(ctx, "route:req-4030", correction)
	if err != nil || !result.Duplicate {
		t.Fatalf("cross-process replay result=%+v err=%v, want duplicate", result, err)
	}

	got, err := s.TPMObservations(ctx, TPMScopeRoute, []int64{4030}, 400)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got[4030].OutputTokens != 60 {
		t.Fatalf("replay double-corrected the bucket: %+v", got[4030])
	}
}

// 桶已过保留期时放弃修正，绝不用一条负增量把过期分钟复活（B-7 的失效模式）。
func TestCorrectTPMObservationsSkipsMissingBucket(t *testing.T) {
	s, client, _ := newTestStore(t)
	ctx := context.Background()
	result, err := s.CorrectTPMObservations(ctx, "route:req-expired", []TPMObservationEntry{
		obsEntry(TPMScopeRoute, 4040, 500, TPMObservationDelta{OutputTokens: -50, ProvisionalTokens: -50}),
	})
	if err != nil || result.Applied != 0 || result.Expired != 1 {
		t.Fatalf("expired correction result=%+v err=%v", result, err)
	}
	if exists := client.Exists(ctx, s.keys.obsTPMRoute(4040, 500)).Val(); exists != 0 {
		t.Fatal("correction recreated an expired observation bucket")
	}
}

func TestCorrectTPMObservationsClampsFieldsAtZero(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := s.RecordTPMObservations(ctx, "clamp-seed", []TPMObservationEntry{
		obsEntry(TPMScopeChannel, 4050, 600, TPMObservationDelta{OutputTokens: 10, ProvisionalTokens: 10}),
	}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	if _, err := s.CorrectTPMObservations(ctx, "channel:clamp", []TPMObservationEntry{
		obsEntry(TPMScopeChannel, 4050, 600, TPMObservationDelta{OutputTokens: -999, ProvisionalTokens: -999}),
	}); err != nil {
		t.Fatalf("correction: %v", err)
	}
	got, err := s.TPMObservations(ctx, TPMScopeChannel, []int64{4050}, 600)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got[4050].OutputTokens != 0 || got[4050].ProvisionalTokens != 0 {
		t.Fatalf("Admin must never read a negative observation: %+v", got[4050])
	}
}

func TestTPMObservationsMissingBucketIsZero(t *testing.T) {
	s, _, _ := newTestStore(t)
	got, err := s.TPMObservations(context.Background(), TPMScopeRoute, []int64{4060}, 700)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if got[4060].TPM() != 0 || got[4060].ObservedAttempts != 0 {
		t.Fatalf("absent bucket must read as zero: %+v", got[4060])
	}
}

// 观测写入不得触碰共享基础设施故障 latch：一次观测失败不能 fence 掉整个 namespace 的准入。
func TestTPMObservationDoesNotLatchInfrastructureFault(t *testing.T) {
	s, _, _ := newTestStore(t)
	ctx := context.Background()
	if _, err := s.RecordTPMObservations(ctx, "latch-check", []TPMObservationEntry{
		obsEntry(TPMScopeRoute, 4070, 800, TPMObservationDelta{InputTokens: 5, ProvisionalTokens: 5}),
	}); err != nil {
		t.Fatalf("record: %v", err)
	}
	if _, _, latched := s.fault.snapshot(); latched {
		t.Fatal("observation write latched the shared infrastructure fault")
	}
}
