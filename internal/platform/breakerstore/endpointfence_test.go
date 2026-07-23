package breakerstore

import (
	"context"
	"testing"
)

func TestEndpointRoutingChangeIsAtomicAndFirstTerminalWins(t *testing.T) {
	store, client, _ := newTestStore(t)
	ctx := context.Background()
	const endpointID int64 = 8101
	if created, err := store.InitEndpointControl(ctx, endpointID, 3, 4, "enabled"); err != nil || !created {
		t.Fatalf("init endpoint: created=%v err=%v", created, err)
	}
	for _, key := range store.allEndpointEvidenceKeys(endpointID) {
		if err := client.SAdd(ctx, key, "sample").Err(); err != nil {
			t.Fatalf("seed evidence: %v", err)
		}
	}
	change := EndpointRoutingChange{
		EndpointID: endpointID, CurrentBaseURLRev: 3, NextBaseURLRev: 4,
		CurrentStatusRev: 4, NextStatusRev: 5, NextEffectiveStatus: "disabled",
	}
	const token = "combined-token"
	const payload = `{"operation":"combined","next_base_url":"https://next.example.test"}`
	if result, err := store.PrepareEndpointRoutingChange(ctx, change, token, payload); err != nil || result != "prepared" {
		t.Fatalf("prepare: result=%s err=%v", result, err)
	}
	snapshot, err := store.Snapshot(ctx, ScopeEndpoint, endpointID)
	if err != nil {
		t.Fatalf("pending snapshot: %v", err)
	}
	if snapshot.PendingBaseURLRevision != 4 || snapshot.PendingStatusRevision != 5 ||
		snapshot.BaseURLRevisionState != "pending" || snapshot.StatusRevisionState != "pending" {
		t.Fatalf("combined prepare was partial: %+v", snapshot)
	}
	if result, err := store.CommitEndpointRoutingChange(ctx, endpointID, token, payload); err != nil || result != "committed" {
		t.Fatalf("commit: result=%s err=%v", result, err)
	}
	snapshot, err = store.Snapshot(ctx, ScopeEndpoint, endpointID)
	if err != nil {
		t.Fatalf("committed snapshot: %v", err)
	}
	if snapshot.BaseURLRevision != 4 || snapshot.StatusRevision != 5 || snapshot.EffectiveStatus != "disabled" ||
		snapshot.PendingBaseURLRevision != 0 || snapshot.PendingStatusRevision != 0 {
		t.Fatalf("combined commit was partial: %+v", snapshot)
	}
	for _, key := range store.allEndpointEvidenceKeys(endpointID) {
		if exists := client.Exists(ctx, key).Val(); exists != 0 {
			t.Fatalf("evidence key survived combined commit: %s", key)
		}
	}
	if result, err := store.CommitEndpointRoutingChange(ctx, endpointID, token, payload); err != nil || result != "committed" {
		t.Fatalf("idempotent commit: result=%s err=%v", result, err)
	}
	if result, err := store.AbortEndpointRoutingChange(ctx, endpointID, token, payload); err != nil || result != "conflict" {
		t.Fatalf("abort after commit must conflict: result=%s err=%v", result, err)
	}
	if result, err := store.PrepareEndpointRoutingChange(ctx, change, token, payload+"-different"); err != nil || result != "conflict" {
		t.Fatalf("same token with another payload must conflict: result=%s err=%v", result, err)
	}
}

func TestEndpointStatusBatchRejectsConflictWithoutPartialMutation(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()
	for _, seed := range []struct {
		id, statusRevision int64
	}{{8201, 1}, {8202, 2}, {8203, 1}} {
		if _, err := store.InitEndpointControl(ctx, seed.id, 1, seed.statusRevision, "enabled"); err != nil {
			t.Fatalf("init endpoint %d: %v", seed.id, err)
		}
	}
	conflicting := []EndpointStatusRevisionTransition{
		{EndpointID: 8201, CurrentStatusRev: 1, NextStatusRev: 2, NextEffectiveStatus: "disabled"},
		{EndpointID: 8202, CurrentStatusRev: 1, NextStatusRev: 2, NextEffectiveStatus: "disabled"},
		{EndpointID: 8203, CurrentStatusRev: 1, NextStatusRev: 2, NextEffectiveStatus: "disabled"},
	}
	if result, err := store.PrepareEndpointStatusRevisionBatch(ctx, 91, conflicting, 3, "batch-conflict", `{"batch":1}`); err != nil || result != "stale" {
		t.Fatalf("conflicting prepare: result=%s err=%v", result, err)
	}
	for _, id := range []int64{8201, 8202, 8203} {
		snapshot, err := store.Snapshot(ctx, ScopeEndpoint, id)
		if err != nil {
			t.Fatalf("snapshot endpoint %d: %v", id, err)
		}
		if snapshot.StatusRevisionState != "active" || snapshot.PendingStatusRevision != 0 {
			t.Fatalf("batch conflict partially mutated endpoint %d: %+v", id, snapshot)
		}
	}
}

func TestEndpointStatusBatchCommitsAllEndpointsAtomically(t *testing.T) {
	store, _, _ := newTestStore(t)
	ctx := context.Background()
	transitions := []EndpointStatusRevisionTransition{
		{EndpointID: 8301, CurrentStatusRev: 1, NextStatusRev: 2, NextEffectiveStatus: "disabled"},
		{EndpointID: 8302, CurrentStatusRev: 1, NextStatusRev: 2, NextEffectiveStatus: "disabled"},
		{EndpointID: 8303, CurrentStatusRev: 1, NextStatusRev: 2, NextEffectiveStatus: "disabled"},
	}
	for _, transition := range transitions {
		if _, err := store.InitEndpointControl(ctx, transition.EndpointID, 1, 1, "enabled"); err != nil {
			t.Fatalf("init endpoint %d: %v", transition.EndpointID, err)
		}
	}
	const payload = `{"provider_id":92,"status":"disabled"}`
	if result, err := store.PrepareEndpointStatusRevisionBatch(ctx, 92, transitions, 3, "batch-ok", payload); err != nil || result != "prepared" {
		t.Fatalf("prepare batch: result=%s err=%v", result, err)
	}
	if result, err := store.CommitEndpointStatusRevisionBatch(ctx, 92, transitions, "batch-ok", payload); err != nil || result != "committed" {
		t.Fatalf("commit batch: result=%s err=%v", result, err)
	}
	for _, transition := range transitions {
		snapshot, err := store.Snapshot(ctx, ScopeEndpoint, transition.EndpointID)
		if err != nil {
			t.Fatalf("snapshot endpoint %d: %v", transition.EndpointID, err)
		}
		if snapshot.StatusRevision != 2 || snapshot.EffectiveStatus != "disabled" || snapshot.PendingStatusRevision != 0 {
			t.Fatalf("batch endpoint %d not committed: %+v", transition.EndpointID, snapshot)
		}
	}
	if result, err := store.AbortEndpointStatusRevisionBatch(ctx, 92, transitions, "batch-ok", payload); err != nil || result != "conflict" {
		t.Fatalf("abort after batch commit must conflict: result=%s err=%v", result, err)
	}
}

func TestEndpointRoutingRecoveryRestoresMissingControl(t *testing.T) {
	store, client, _ := newTestStore(t)
	ctx := context.Background()
	const endpointID int64 = 8401
	if _, err := store.InitEndpointControl(ctx, endpointID, 7, 9, "enabled"); err != nil {
		t.Fatalf("init endpoint: %v", err)
	}
	change := EndpointRoutingChange{
		EndpointID: endpointID, CurrentBaseURLRev: 7, NextBaseURLRev: 8,
		CurrentStatusRev: 9, NextStatusRev: 10, NextEffectiveStatus: "disabled",
	}
	const token = "recover-combined"
	const payload = `{"recover":"combined"}`
	if result, err := store.PrepareEndpointRoutingChange(ctx, change, token, payload); err != nil || result != "prepared" {
		t.Fatalf("prepare: result=%s err=%v", result, err)
	}
	if err := client.Del(ctx, store.keys.endpoint(endpointID)).Err(); err != nil {
		t.Fatalf("delete endpoint control: %v", err)
	}
	result, err := store.RecoverEndpointRouting(ctx, EndpointRoutingRecovery{
		Mode: EndpointRecoveryCommitted, Kind: "base_url_status", ProviderID: 99,
		Token: token, PayloadHash: HashPayload(payload),
		Transitions: []EndpointRoutingRecoveryTransition{{
			EndpointID:        endpointID,
			CurrentBaseURLRev: 7, NextBaseURLRev: 8,
			CurrentStatusRev: 9, NextStatusRev: 10,
			CurrentEffective: "enabled", NextEffective: "disabled",
			FactBaseURLRev: 8, FactStatusRev: 10, FactEffective: "disabled",
		}},
	})
	if err != nil || result != "committed" {
		t.Fatalf("recover committed: result=%s err=%v", result, err)
	}
	snapshot, err := store.Snapshot(ctx, ScopeEndpoint, endpointID)
	if err != nil {
		t.Fatalf("snapshot: %v", err)
	}
	if snapshot.BaseURLRevision != 8 || snapshot.StatusRevision != 10 || snapshot.EffectiveStatus != "disabled" {
		t.Fatalf("missing control was not restored from fact: %+v", snapshot)
	}
}
