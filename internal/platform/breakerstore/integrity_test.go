package breakerstore

import (
	"context"
	"testing"
)

func TestRuntimeStateEpochFenceFiveBranches(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()

	first := StateEpochFenceInput{
		Token: "epoch-op-a", TransitionHash: HashPayload("transition-a"),
		ExpectedMarkerHash: StateEpochExpectedMarkerAbsent,
		NewEpoch:           "00112233445566778899aabbccddeeff", NewRevision: 1,
	}
	if result, err := store.RecoverRuntimeStateEpochFence(ctx, first); err != nil || result != StateEpochPrepared {
		t.Fatalf("absent prepare: result=%s err=%v", result, err)
	}
	if result, err := store.RecoverRuntimeStateEpochFence(ctx, first); err != nil || result != StateEpochPrepared {
		t.Fatalf("same pending: result=%s err=%v", result, err)
	}
	pending, err := store.StateIntegrity(ctx)
	if err != nil || pending.State != "pending" || pending.OperationToken != first.Token || pending.ExpectedMarkerHash != StateEpochExpectedMarkerAbsent {
		t.Fatalf("unexpected pending marker: %+v err=%v", pending, err)
	}

	other := first
	other.Token = "epoch-op-other"
	other.TransitionHash = HashPayload("transition-other")
	other.NewEpoch = "11112233445566778899aabbccddeeff"
	if result, err := store.RecoverRuntimeStateEpochFence(ctx, other); err != nil || result != StateEpochConflict {
		t.Fatalf("other pending must conflict: result=%s err=%v", result, err)
	}
	stillPending, _ := store.StateIntegrity(ctx)
	if stillPending.OperationToken != first.Token || stillPending.TransitionHash != first.TransitionHash {
		t.Fatalf("conflict overwrote pending marker: before=%+v after=%+v", pending, stillPending)
	}

	if committed, err := store.CommitRuntimeStateEpoch(ctx, first); err != nil || !committed {
		t.Fatalf("commit first epoch: committed=%v err=%v", committed, err)
	}
	readyA, err := store.StateIntegrity(ctx)
	if err != nil || !readyA.Ready(first.NewEpoch, first.NewRevision) {
		t.Fatalf("first marker not ready: %+v err=%v", readyA, err)
	}
	if result, err := store.RecoverRuntimeStateEpochFence(ctx, first); err != nil || result != StateEpochNewReadyObserved {
		t.Fatalf("same operation new ready: result=%s err=%v", result, err)
	}

	second := StateEpochFenceInput{
		Token: "epoch-op-b", TransitionHash: HashPayload("transition-b"),
		ExpectedMarkerHash: readyA.MarkerHash,
		OldEpoch:           first.NewEpoch, OldRevision: first.NewRevision,
		NewEpoch: "ffeeddccbbaa99887766554433221100", NewRevision: 2,
	}
	if result, err := store.RecoverRuntimeStateEpochFence(ctx, second); err != nil || result != StateEpochPrepared {
		t.Fatalf("old ready prepare: result=%s err=%v", result, err)
	}

	// 模拟 Redis flush/回档覆盖 pending+op：application 严格观测 absent 并先 CAS durable
	// expected 后，同 token/hash 可重建 pending，不创建新 operation。
	if err := store.client.Del(ctx, store.keys.stateIntegrityMarker(), store.keys.runtimeControlOp(second.Token)).Err(); err != nil {
		t.Fatalf("delete pending marker/op: %v", err)
	}
	second.ExpectedMarkerHash = StateEpochExpectedMarkerAbsent
	if result, err := store.RecoverRuntimeStateEpochFence(ctx, second); err != nil || result != StateEpochPrepared {
		t.Fatalf("recover absent pending: result=%s err=%v", result, err)
	}
	if committed, err := store.CommitRuntimeStateEpoch(ctx, second); err != nil || !committed {
		t.Fatalf("commit recovered epoch: committed=%v err=%v", committed, err)
	}
	readyB, _ := store.StateIntegrity(ctx)
	if !readyB.Ready(second.NewEpoch, second.NewRevision) {
		t.Fatalf("second marker not ready: %+v", readyB)
	}

	conflict := StateEpochFenceInput{
		Token: "epoch-op-conflict", TransitionHash: HashPayload("transition-conflict"),
		ExpectedMarkerHash: readyA.MarkerHash,
		OldEpoch:           first.NewEpoch, OldRevision: first.NewRevision,
		NewEpoch: "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", NewRevision: 2,
	}
	if result, err := store.RecoverRuntimeStateEpochFence(ctx, conflict); err != nil || result != StateEpochConflict {
		t.Fatalf("unrelated ready marker must conflict: result=%s err=%v", result, err)
	}
	afterConflict, _ := store.StateIntegrity(ctx)
	if !afterConflict.Ready(second.NewEpoch, second.NewRevision) || afterConflict.MarkerHash != readyB.MarkerHash {
		t.Fatalf("conflict overwrote ready marker: before=%+v after=%+v", readyB, afterConflict)
	}
}

type readinessControlFixture struct {
	name        string
	target      func(*Store) ControlTarget
	revision    int64
	payload     string
	setExpected func(*RuntimeReadinessInput, int64)
}

func readinessControlFixtures() []readinessControlFixture {
	return []readinessControlFixture{
		{
			name: "route rate", target: func(store *Store) ControlTarget { return store.RouteRateLimitControl() },
			revision: 2, payload: `{"rpm":11,"tpm":1100,"rpd":111}`,
			setExpected: func(input *RuntimeReadinessInput, revision int64) { input.RouteRateLimitRevision = revision },
		},
		{
			name: "channel rate", target: func(store *Store) ControlTarget { return store.ChannelRateLimitControl() },
			revision: 3, payload: `{"rpm":23,"tpm":2300,"rpd":233}`,
			setExpected: func(input *RuntimeReadinessInput, revision int64) { input.ChannelRateLimitRevision = revision },
		},
		{
			name: "global concurrency", target: func(store *Store) ControlTarget { return store.GlobalConcurrencyControl() },
			revision: 4, payload: `{"key_limit":31,"channel_limit":37}`,
			setExpected: func(input *RuntimeReadinessInput, revision int64) { input.ConcurrencyRevision = revision },
		},
		{
			name: "circuit breaker", target: func(store *Store) ControlTarget { return store.SettingControl("gateway.circuit_breaker") },
			revision: 5, payload: `{"enabled":true}`,
			setExpected: func(input *RuntimeReadinessInput, revision int64) { input.CircuitBreakerRevision = revision },
		},
		{
			name: "routing balance", target: func(store *Store) ControlTarget { return store.SettingControl("gateway.routing_balance") },
			revision: 6, payload: `{"ttft_weight":0.35}`,
			setExpected: func(input *RuntimeReadinessInput, revision int64) { input.RoutingBalanceRevision = revision },
		},
	}
}

func seedRuntimeReadinessFixture(t *testing.T) (*Store, RuntimeReadinessInput) {
	t.Helper()
	store, _, _ := newTestStore(t)
	epoch := "00112233445566778899aabbccddeeff"
	if ok, err := store.BootstrapStateEpoch(context.Background(), epoch, 1, HashPayload("readiness-bootstrap")); err != nil || !ok {
		t.Fatalf("bootstrap readiness epoch: ok=%v err=%v", ok, err)
	}
	input := RuntimeReadinessInput{Epoch: epoch, EpochRevision: 1}
	for _, control := range readinessControlFixtures() {
		ensureTestControlAtRevision(t, store, control.target(store), control.revision, control.payload)
		control.setExpected(&input, control.revision)
	}
	return store, input
}

func TestRuntimeReadinessChecksMarkerAndFiveCriticalControls(t *testing.T) {
	t.Run("all controls ready with distinct rate revisions", func(t *testing.T) {
		store, input := seedRuntimeReadinessFixture(t)
		if input.RouteRateLimitRevision == input.ChannelRateLimitRevision {
			t.Fatalf("route and channel rate revisions must differ: %+v", input)
		}
		result, err := store.CheckRuntimeReadiness(context.Background(), input)
		if err != nil || !result.Ready {
			t.Fatalf("runtime should be ready: result=%+v err=%v", result, err)
		}
	})

	states := []struct {
		name       string
		wantReason string
		mutate     func(*testing.T, *Store, readinessControlFixture, *RuntimeReadinessInput)
	}{
		{
			name: "absent", wantReason: "control_absent",
			mutate: func(t *testing.T, store *Store, control readinessControlFixture, _ *RuntimeReadinessInput) {
				t.Helper()
				if err := store.client.Del(context.Background(), control.target(store).controlKey).Err(); err != nil {
					t.Fatal(err)
				}
			},
		},
		{
			name: "pending", wantReason: "control_pending",
			mutate: func(t *testing.T, store *Store, control readinessControlFixture, _ *RuntimeReadinessInput) {
				t.Helper()
				code, _, err := store.PrepareControl(
					context.Background(), control.target(store), "readiness-pending-"+control.name,
					control.revision, control.revision+1, control.payload+" ",
				)
				if err != nil || code != ControlPrepared {
					t.Fatalf("prepare pending %s: code=%s err=%v", control.name, code, err)
				}
			},
		},
		{
			name: "revision mismatch", wantReason: "control_revision_mismatch",
			mutate: func(_ *testing.T, _ *Store, control readinessControlFixture, input *RuntimeReadinessInput) {
				control.setExpected(input, control.revision+10)
			},
		},
	}

	for _, state := range states {
		for _, control := range readinessControlFixtures() {
			t.Run(state.name+"/"+control.name, func(t *testing.T) {
				store, input := seedRuntimeReadinessFixture(t)
				state.mutate(t, store, control, &input)
				result, err := store.CheckRuntimeReadiness(context.Background(), input)
				if err != nil || result.Ready || result.Reason != state.wantReason {
					t.Fatalf("%s %s readiness: result=%+v err=%v", state.name, control.name, result, err)
				}
			})
		}
	}
}
