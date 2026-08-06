package runtimecontrol_test

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func TestStateEpochCoordinatorBootstrapsAndRebuildsMissingMarker(t *testing.T) {
	ctx, tx, client, namespace := stateEpochFixture(t)
	store := breakerstore.NewStore(client, namespace)
	coordinator := runtimecontrol.NewStateEpochCoordinator(tx, store)

	result, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator)
	if err != nil {
		t.Fatalf("ensure state epoch seed: %v", err)
	}
	if !result.Created || result.State != runtimecontrol.StateEpochEnsureReady || result.Record.Revision != 1 ||
		result.Record.Value.State != runtimecontrol.StateEpochReady || result.OperationToken == "" {
		t.Fatalf("unexpected bootstrap result: %+v", result)
	}

	queries := sqlc.New(tx)
	op, err := queries.GetRuntimeControlOperationByToken(ctx, result.OperationToken)
	if err != nil {
		t.Fatalf("read durable operation: %v", err)
	}
	if op.State != "committed" || !op.ExpectedMarkerHash.Valid ||
		op.ExpectedMarkerHash.String != breakerstore.StateEpochExpectedMarkerAbsent || !op.CompletedAt.Valid {
		t.Fatalf("unexpected durable operation: %+v", op)
	}
	marker, err := store.StateIntegrity(ctx)
	if err != nil || !marker.Ready(result.Record.Value.Epoch, result.Record.Revision) {
		t.Fatalf("unexpected redis marker: %+v err=%v", marker, err)
	}

	requestKey := namespace + ":admission:v1:request:preserved-after-marker-loss"
	if err := client.HSet(ctx, requestKey, "state", "active").Err(); err != nil {
		t.Fatalf("seed unrelated runtime key: %v", err)
	}
	if err := client.Del(ctx, namespace+":runtime-control:v1:state-integrity-marker").Err(); err != nil {
		t.Fatalf("delete ready marker: %v", err)
	}
	rebuilt, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator)
	if err != nil || rebuilt.State != runtimecontrol.StateEpochEnsureReady ||
		rebuilt.Record.Value.Epoch != result.Record.Value.Epoch {
		t.Fatalf("rebuild deleted marker: result=%+v err=%v", rebuilt, err)
	}
	if exists, err := client.Exists(ctx, requestKey).Result(); err != nil || exists != 1 {
		t.Fatalf("marker rebuild touched unrelated runtime key: exists=%d err=%v", exists, err)
	}
}

func TestStateEpochCoordinatorAutomaticallyFinalizesStateLossOperation(t *testing.T) {
	ctx, tx, client, namespace := stateEpochFixture(t)
	store := breakerstore.NewStore(client, namespace)
	coordinator := runtimecontrol.NewStateEpochCoordinator(tx, store)
	bootstrap, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator)
	if err != nil {
		t.Fatalf("bootstrap epoch: %v", err)
	}

	detectedAt := time.Now().UTC()
	transition, err := runtimecontrol.NewStateEpochRecoveryTransition(
		bootstrap.Record.Value,
		bootstrap.Record.Revision,
		"automatic-state-loss-recovery",
		runtimecontrol.StateEpochReasonStateLoss,
		true,
		detectedAt,
		detectedAt,
	)
	if err != nil {
		t.Fatalf("create state-loss transition: %v", err)
	}
	transitionRaw, err := transition.Marshal()
	if err != nil {
		t.Fatalf("marshal state-loss transition: %v", err)
	}
	op, err := sqlc.New(tx).CreateRuntimeControlOperation(ctx, sqlc.CreateRuntimeControlOperationParams{
		Token:           "automatic-state-loss-operation",
		Kind:            "runtime_state_epoch",
		SettingKey:      pgtype.Text{String: runtimecontrol.RuntimeStateEpochKey, Valid: true},
		CurrentRevision: bootstrap.Record.Revision,
		NextRevision:    bootstrap.Record.Revision + 1,
		PayloadHash:     breakerstore.HashPayload(string(transitionRaw)),
		EpochTransition: transitionRaw,
	})
	if err != nil {
		t.Fatalf("create state-loss operation: %v", err)
	}

	recovered, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator)
	if err != nil {
		t.Fatalf("automatically recover state loss: %v", err)
	}
	if recovered.State != runtimecontrol.StateEpochEnsureReady ||
		recovered.Record.Value.State != runtimecontrol.StateEpochReady ||
		recovered.Record.Value.Reason != runtimecontrol.StateEpochReasonStateLoss ||
		recovered.Record.Revision != bootstrap.Record.Revision+1 {
		t.Fatalf("unexpected automatic recovery result: %+v", recovered)
	}
	finalOp, err := sqlc.New(tx).GetRuntimeControlOperationByToken(ctx, op.Token)
	if err != nil {
		t.Fatalf("read finalized state-loss operation: %v", err)
	}
	if finalOp.State != "committed" || !finalOp.CompletedAt.Valid || finalOp.ReleaseEvidence != nil {
		t.Fatalf("state-loss operation was not automatically finalized: %+v", finalOp)
	}
	marker, err := store.StateIntegrity(ctx)
	if err != nil || !marker.Ready(recovered.Record.Value.Epoch, recovered.Record.Revision) {
		t.Fatalf("automatic recovery marker mismatch: %+v err=%v", marker, err)
	}

	seedReadinessSettings(t, ctx, tx)
	readiness, err := sqlc.New(tx).GetGatewayRuntimeReadinessSnapshot(ctx)
	if err != nil || !readiness.RuntimeOperationsReconciled {
		t.Fatalf("automatic recovery did not reopen durable readiness: snapshot=%+v err=%v", readiness, err)
	}
}

func TestStateEpochCoordinatorAutomaticallyFinalizesLegacyAwaitingRelease(t *testing.T) {
	ctx, tx, client, namespace := stateEpochFixture(t)
	store := breakerstore.NewStore(client, namespace)
	coordinator := runtimecontrol.NewStateEpochCoordinator(tx, store)
	bootstrap, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator)
	if err != nil {
		t.Fatalf("bootstrap epoch: %v", err)
	}

	detectedAt := time.Now().UTC()
	transition, err := runtimecontrol.NewStateEpochRecoveryTransition(
		bootstrap.Record.Value,
		bootstrap.Record.Revision,
		"legacy-awaiting-release",
		runtimecontrol.StateEpochReasonStateLoss,
		true,
		detectedAt,
		detectedAt,
	)
	if err != nil {
		t.Fatalf("create legacy transition: %v", err)
	}
	transitionRaw, err := transition.Marshal()
	if err != nil {
		t.Fatalf("marshal legacy transition: %v", err)
	}
	queries := sqlc.New(tx)
	op, err := queries.CreateRuntimeControlOperation(ctx, sqlc.CreateRuntimeControlOperationParams{
		Token:           "legacy-awaiting-release-operation",
		Kind:            "runtime_state_epoch",
		SettingKey:      pgtype.Text{String: runtimecontrol.RuntimeStateEpochKey, Valid: true},
		CurrentRevision: bootstrap.Record.Revision,
		NextRevision:    bootstrap.Record.Revision + 1,
		PayloadHash:     breakerstore.HashPayload(string(transitionRaw)),
		EpochTransition: transitionRaw,
		ExpectedMarkerHash: pgtype.Text{
			String: breakerstore.StateIntegrityReadyMarkerHash(bootstrap.Record.Value.Epoch, bootstrap.Record.Revision),
			Valid:  true,
		},
	})
	if err != nil {
		t.Fatalf("create legacy operation: %v", err)
	}
	if rows, err := queries.MarkRuntimeControlOperationPrepared(ctx, sqlc.MarkRuntimeControlOperationPreparedParams{
		Token: op.Token, PayloadHash: op.PayloadHash,
	}); err != nil || rows != 1 {
		t.Fatalf("mark legacy operation prepared: rows=%d err=%v", rows, err)
	}
	oldRaw, err := bootstrap.Record.Value.Marshal()
	if err != nil {
		t.Fatalf("marshal old epoch: %v", err)
	}
	recoveringRaw, err := (runtimecontrol.StateEpoch{
		Epoch: transition.NewEpoch, State: runtimecontrol.StateEpochRecovering, Reason: transition.Reason,
	}).Marshal()
	if err != nil {
		t.Fatalf("marshal recovering epoch: %v", err)
	}
	if rows, err := queries.AdvanceRuntimeStateEpochRecovering(ctx, sqlc.AdvanceRuntimeStateEpochRecoveringParams{
		NextValue: recoveringRaw, NextRevision: transition.NewRevision,
		CurrentRevision: bootstrap.Record.Revision, CurrentValue: oldRaw,
	}); err != nil || rows != 1 {
		t.Fatalf("advance legacy epoch: rows=%d err=%v", rows, err)
	}
	if rows, err := queries.MarkRuntimeControlOperationDBCommitted(ctx, sqlc.MarkRuntimeControlOperationDBCommittedParams{
		Token: op.Token, PayloadHash: op.PayloadHash,
	}); err != nil || rows != 1 {
		t.Fatalf("mark legacy operation db committed: rows=%d err=%v", rows, err)
	}
	fence := breakerstore.StateEpochFenceInput{
		Token:              op.Token,
		TransitionHash:     op.PayloadHash,
		ExpectedMarkerHash: op.ExpectedMarkerHash.String,
		OldEpoch:           bootstrap.Record.Value.Epoch,
		OldRevision:        bootstrap.Record.Revision,
		NewEpoch:           transition.NewEpoch,
		NewRevision:        transition.NewRevision,
	}
	if prepared, err := store.RecoverRuntimeStateEpochFence(ctx, fence); err != nil || prepared != breakerstore.StateEpochPrepared {
		t.Fatalf("prepare legacy redis fence: result=%v err=%v", prepared, err)
	}
	if committed, err := store.CommitRuntimeStateEpoch(ctx, fence); err != nil || !committed {
		t.Fatalf("commit legacy redis fence: committed=%v err=%v", committed, err)
	}
	current, err := runtimecontrol.DecodeStateEpoch(recoveringRaw)
	if err != nil {
		t.Fatalf("decode recovering epoch: %v", err)
	}
	ready, err := current.ReadyAt(time.Now().UTC())
	if err != nil {
		t.Fatalf("ready legacy epoch: %v", err)
	}
	readyRaw, err := ready.Marshal()
	if err != nil {
		t.Fatalf("marshal ready epoch: %v", err)
	}
	if rows, err := queries.MarkRuntimeStateEpochReady(ctx, sqlc.MarkRuntimeStateEpochReadyParams{
		ReadyValue: readyRaw, Revision: transition.NewRevision, RecoveringValue: recoveringRaw,
	}); err != nil || rows != 1 {
		t.Fatalf("mark legacy epoch ready: rows=%d err=%v", rows, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE runtime_control_operations SET state = 'awaiting_release' WHERE token = $1`, op.Token); err != nil {
		t.Fatalf("seed legacy awaiting_release state: %v", err)
	}

	recovered, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator)
	if err != nil || recovered.State != runtimecontrol.StateEpochEnsureReady {
		t.Fatalf("auto-finalize legacy operation: result=%+v err=%v", recovered, err)
	}
	finalOp, err := queries.GetRuntimeControlOperationByToken(ctx, op.Token)
	if err != nil {
		t.Fatalf("read finalized legacy operation: %v", err)
	}
	if finalOp.State != "committed" || !finalOp.CompletedAt.Valid {
		t.Fatalf("legacy operation remains unfinished: %+v", finalOp)
	}
}

func TestStateEpochCoordinatorConflictPreservesExpectedMarkerHash(t *testing.T) {
	ctx, tx, client, namespace := stateEpochFixture(t)
	activatedAt := time.Now().UTC().Add(-time.Hour)
	oldEpoch := runtimecontrol.StateEpoch{
		Epoch:       "00112233445566778899aabbccddeeff",
		State:       runtimecontrol.StateEpochReady,
		Reason:      runtimecontrol.StateEpochReasonBootstrap,
		ActivatedAt: &activatedAt,
	}
	oldRaw, err := oldEpoch.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	queries := sqlc.New(tx)
	if rows, err := queries.SeedRuntimeStateEpoch(ctx, oldRaw); err != nil || rows != 1 {
		t.Fatalf("seed old epoch: rows=%d err=%v", rows, err)
	}
	detectedAt := activatedAt.Add(time.Minute)
	transition, err := runtimecontrol.NewStateEpochRecoveryTransition(
		oldEpoch, 1, "recovery-conflict", runtimecontrol.StateEpochReasonStateLoss, true, detectedAt, detectedAt,
	)
	if err != nil {
		t.Fatal(err)
	}
	transitionRaw, err := transition.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	expected := breakerstore.StateIntegrityReadyMarkerHash(oldEpoch.Epoch, 1)
	op, err := queries.CreateRuntimeControlOperation(ctx, sqlc.CreateRuntimeControlOperationParams{
		Token:              "durable-conflict-operation",
		Kind:               "runtime_state_epoch",
		SettingKey:         pgtype.Text{String: runtimecontrol.RuntimeStateEpochKey, Valid: true},
		CurrentRevision:    1,
		NextRevision:       2,
		PayloadHash:        breakerstore.HashPayload(string(transitionRaw)),
		EpochTransition:    transitionRaw,
		ExpectedMarkerHash: pgtype.Text{String: expected, Valid: true},
	})
	if err != nil {
		t.Fatalf("create durable operation: %v", err)
	}

	store := breakerstore.NewStore(client, namespace)
	conflictingEpoch := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if ok, err := store.BootstrapStateEpoch(ctx, conflictingEpoch, 9, breakerstore.HashPayload("unrelated")); err != nil || !ok {
		t.Fatalf("seed conflicting marker: ok=%v err=%v", ok, err)
	}
	_, err = runtimecontrol.EnsureStateEpochSeed(ctx, runtimecontrol.NewStateEpochCoordinator(tx, store))
	if !errors.Is(err, runtimecontrol.ErrStateEpochConflict) {
		t.Fatalf("expected marker conflict, got %v", err)
	}
	got, err := queries.GetRuntimeControlOperationByToken(ctx, op.Token)
	if err != nil {
		t.Fatal(err)
	}
	if !got.ExpectedMarkerHash.Valid || got.ExpectedMarkerHash.String != expected || got.State != "preparing" {
		t.Fatalf("conflict changed durable operation: %+v", got)
	}
}

func stateEpochFixture(t *testing.T) (context.Context, pgx.Tx, *redis.Client, string) {
	t.Helper()
	databaseURL := os.Getenv("DATABASE_URL")
	redisAddr := os.Getenv("REDIS_ADDR")
	if databaseURL == "" || redisAddr == "" {
		t.Skip("DATABASE_URL and REDIS_ADDR are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	t.Cleanup(cancel)
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create postgres pool: %v", err)
	}
	t.Cleanup(pool.Close)
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin isolation transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	if _, err := tx.Exec(ctx, `DELETE FROM runtime_control_operations WHERE setting_key = 'gateway.runtime_state_epoch'`); err != nil {
		t.Fatalf("clear epoch operations: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app_settings WHERE key = 'gateway.runtime_state_epoch'`); err != nil {
		t.Fatalf("clear epoch setting: %v", err)
	}

	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	t.Cleanup(func() { _ = client.Close() })
	namespace := fmt.Sprintf("unio-epoch-auto-recovery-test:%d", time.Now().UnixNano())
	t.Cleanup(func() {
		var keys []string
		iter := client.Scan(context.Background(), 0, namespace+":*", 0).Iterator()
		for iter.Next(context.Background()) {
			keys = append(keys, iter.Val())
		}
		if len(keys) > 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
	})
	return ctx, tx, client, namespace
}

func seedReadinessSettings(t *testing.T, ctx context.Context, tx pgx.Tx) {
	t.Helper()
	for _, key := range []string{
		"gateway.route_rate_limit_defaults",
		"gateway.concurrency_defaults",
		"gateway.circuit_breaker",
		"gateway.routing_balance",
	} {
		if _, err := tx.Exec(ctx, `
			INSERT INTO app_settings (key, value)
			VALUES ($1, '{}'::jsonb)
			ON CONFLICT (key) DO NOTHING`, key); err != nil {
			t.Fatalf("seed readiness setting %s: %v", key, err)
		}
	}
}
