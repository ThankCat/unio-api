package runtimecontrol_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

func TestStateEpochCoordinatorBootstrapsThroughDurableOperation(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	redisAddr := os.Getenv("REDIS_ADDR")
	if databaseURL == "" || redisAddr == "" {
		t.Skip("DATABASE_URL and REDIS_ADDR are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create postgres pool: %v", err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin isolation transaction: %v", err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `DELETE FROM runtime_control_operations WHERE setting_key = 'gateway.runtime_state_epoch'`); err != nil {
		t.Fatalf("clear epoch operations in test transaction: %v", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app_settings WHERE key = 'gateway.runtime_state_epoch'`); err != nil {
		t.Fatalf("clear epoch row in test transaction: %v", err)
	}

	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer client.Close()
	namespace := fmt.Sprintf("unio-epoch-bootstrap-test:%d", time.Now().UnixNano())
	t.Cleanup(func() {
		iter := client.Scan(context.Background(), 0, namespace+":*", 0).Iterator()
		for iter.Next(context.Background()) {
			_ = client.Del(context.Background(), iter.Val()).Err()
		}
	})
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
	row, err := queries.GetAppSettingRecord(ctx, runtimecontrol.RuntimeStateEpochKey)
	if err != nil {
		t.Fatalf("read epoch row: %v", err)
	}
	epoch, err := runtimecontrol.DecodeStateEpoch(row.Value)
	if err != nil || epoch.State != runtimecontrol.StateEpochReady || epoch.Epoch != result.Record.Value.Epoch {
		t.Fatalf("unexpected durable epoch: %+v err=%v", epoch, err)
	}
	op, err := queries.GetRuntimeControlOperationByToken(ctx, result.OperationToken)
	if err != nil {
		t.Fatalf("read durable operation: %v", err)
	}
	if op.State != "committed" || !op.ExpectedMarkerHash.Valid || op.ExpectedMarkerHash.String != breakerstore.StateEpochExpectedMarkerAbsent ||
		op.CurrentRevision != 0 || op.NextRevision != 1 || op.CompletedAt.Valid == false {
		t.Fatalf("unexpected durable operation: %+v", op)
	}
	marker, err := store.StateIntegrity(ctx)
	if err != nil || !marker.Ready(epoch.Epoch, row.Revision) || marker.LastOperationToken != result.OperationToken {
		t.Fatalf("unexpected redis marker: %+v err=%v", marker, err)
	}

	// 模拟 Redis Commit 已成功，但 PostgreSQL 终结事务丢失/回档到
	// recovering+db_committed。同 operation new-ready 分支必须只收口 PostgreSQL，不换 epoch。
	recoveringRaw, err := (runtimecontrol.StateEpoch{
		Epoch: epoch.Epoch, State: runtimecontrol.StateEpochRecovering, Reason: runtimecontrol.StateEpochReasonBootstrap,
	}).Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM runtime_control_operations WHERE token = $1`, result.OperationToken); err != nil {
		t.Fatalf("remove committed operation fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE app_settings SET value = $1::jsonb WHERE key = 'gateway.runtime_state_epoch'`, recoveringRaw); err != nil {
		t.Fatalf("rewind epoch row fixture: %v", err)
	}
	recreated, err := queries.CreateRuntimeControlOperation(ctx, sqlc.CreateRuntimeControlOperationParams{
		Token: op.Token, Kind: op.Kind, SettingKey: op.SettingKey,
		CurrentRevision: op.CurrentRevision, NextRevision: op.NextRevision,
		PayloadHash: op.PayloadHash, EpochTransition: op.EpochTransition,
		ExpectedMarkerHash: op.ExpectedMarkerHash,
	})
	if err != nil {
		t.Fatalf("recreate bootstrap operation fixture: %v", err)
	}
	if rows, err := queries.MarkRuntimeControlOperationPrepared(ctx, sqlc.MarkRuntimeControlOperationPreparedParams{
		Token: recreated.Token, PayloadHash: recreated.PayloadHash,
	}); err != nil || rows != 1 {
		t.Fatalf("prepare bootstrap response-loss fixture: rows=%d err=%v", rows, err)
	}
	if rows, err := queries.MarkRuntimeControlOperationDBCommitted(ctx, sqlc.MarkRuntimeControlOperationDBCommittedParams{
		Token: recreated.Token, PayloadHash: recreated.PayloadHash,
	}); err != nil || rows != 1 {
		t.Fatalf("db-commit bootstrap response-loss fixture: rows=%d err=%v", rows, err)
	}
	recovered, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator)
	if err != nil || recovered.State != runtimecontrol.StateEpochEnsureReady || recovered.Record.Value.Epoch != epoch.Epoch {
		t.Fatalf("recover same-operation new ready: result=%+v err=%v", recovered, err)
	}

	again, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator)
	if err != nil || again.Created || again.State != runtimecontrol.StateEpochEnsureReady || again.Record.Value.Epoch != epoch.Epoch {
		t.Fatalf("idempotent ensure changed epoch: result=%+v err=%v", again, err)
	}
	var operationCount int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM runtime_control_operations WHERE setting_key = 'gateway.runtime_state_epoch'`).Scan(&operationCount); err != nil {
		t.Fatalf("count epoch operations: %v", err)
	}
	if operationCount != 1 {
		t.Fatalf("idempotent ensure created %d operations, want 1", operationCount)
	}
}

func TestStateEpochCoordinatorConflictPreservesExpectedMarkerHash(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	redisAddr := os.Getenv("REDIS_ADDR")
	if databaseURL == "" || redisAddr == "" {
		t.Skip("DATABASE_URL and REDIS_ADDR are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `DELETE FROM runtime_control_operations WHERE setting_key = 'gateway.runtime_state_epoch'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app_settings WHERE key = 'gateway.runtime_state_epoch'`); err != nil {
		t.Fatal(err)
	}

	oldEpoch := "00112233445566778899aabbccddeeff"
	newEpoch := "ffeeddccbbaa99887766554433221100"
	activatedAt := time.Date(2026, time.July, 22, 1, 0, 0, 0, time.UTC)
	oldValue := runtimecontrol.StateEpoch{
		Epoch: oldEpoch, State: runtimecontrol.StateEpochReady,
		Reason: runtimecontrol.StateEpochReasonBootstrap, ActivatedAt: &activatedAt,
	}
	oldRaw, err := oldValue.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	queries := sqlc.New(tx)
	if rows, err := queries.SeedRuntimeStateEpoch(ctx, oldRaw); err != nil || rows != 1 {
		t.Fatalf("seed old epoch: rows=%d err=%v", rows, err)
	}
	oldRevision := int64(1)
	recoveryID := "recovery-conflict"
	transition := runtimecontrol.StateEpochTransition{
		RecoveryID: &recoveryID,
		OldEpoch:   &oldEpoch, OldRevision: &oldRevision,
		NewEpoch: newEpoch, NewRevision: 2,
		Reason: runtimecontrol.StateEpochReasonStateLoss, StateLossConfirmed: true,
		DetectedAt: activatedAt.Add(time.Hour), NotBefore: activatedAt.Add(2 * time.Hour),
	}
	transitionRaw, err := transition.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	collecting, err := runtimecontrol.NewCollectingStateEpochRecoveryEvidence(transition, "test-conflict", transition.DetectedAt)
	if err != nil {
		t.Fatal(err)
	}
	collectingRaw, err := collecting.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	expected := breakerstore.StateIntegrityReadyMarkerHash(oldEpoch, oldRevision)
	op, err := queries.CreateRuntimeControlOperation(ctx, sqlc.CreateRuntimeControlOperationParams{
		Token: "durable-conflict-operation", Kind: "runtime_state_epoch",
		SettingKey:      pgtype.Text{String: runtimecontrol.RuntimeStateEpochKey, Valid: true},
		CurrentRevision: 1, NextRevision: 2,
		PayloadHash: breakerstore.HashPayload(string(transitionRaw)), EpochTransition: transitionRaw,
		ExpectedMarkerHash: pgtype.Text{String: expected, Valid: true},
		RecoveryEvidence:   collectingRaw,
	})
	if err != nil {
		t.Fatalf("create durable operation: %v", err)
	}

	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer client.Close()
	namespace := fmt.Sprintf("unio-epoch-conflict-test:%d", time.Now().UnixNano())
	t.Cleanup(func() {
		iter := client.Scan(context.Background(), 0, namespace+":*", 0).Iterator()
		for iter.Next(context.Background()) {
			_ = client.Del(context.Background(), iter.Val()).Err()
		}
	})
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
	marker, err := store.StateIntegrity(ctx)
	if err != nil || !marker.Ready(conflictingEpoch, 9) {
		t.Fatalf("conflict overwrote redis marker: %+v err=%v", marker, err)
	}
}

func TestStateEpochMaintenanceBeginCommitAndResponseLossRetry(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	redisAddr := os.Getenv("REDIS_ADDR")
	if databaseURL == "" || redisAddr == "" {
		t.Skip("DATABASE_URL and REDIS_ADDR are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `DELETE FROM runtime_control_operations WHERE setting_key = 'gateway.runtime_state_epoch'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app_settings WHERE key = 'gateway.runtime_state_epoch'`); err != nil {
		t.Fatal(err)
	}

	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer client.Close()
	namespace := fmt.Sprintf("unio-epoch-maintenance-test:%d", time.Now().UnixNano())
	clearNamespace := func() {
		var keys []string
		iter := client.Scan(context.Background(), 0, namespace+":*", 0).Iterator()
		for iter.Next(context.Background()) {
			keys = append(keys, iter.Val())
		}
		if len(keys) > 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
	}
	t.Cleanup(clearNamespace)
	epochStore := breakerstore.NewStore(client, namespace)
	coordinator := runtimecontrol.NewStateEpochCoordinator(tx, epochStore)
	bootstrap, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator)
	if err != nil || bootstrap.State != runtimecontrol.StateEpochEnsureReady {
		t.Fatalf("bootstrap epoch: result=%+v err=%v", bootstrap, err)
	}
	if bootstrap.Record.Value.ActivatedAt == nil {
		t.Fatal("bootstrap epoch has no activated_at")
	}
	for _, key := range []string{
		"gateway.route_rate_limit_defaults", "gateway.channel_rate_limit_defaults",
		"gateway.concurrency_defaults",
		"gateway.circuit_breaker", "gateway.routing_balance",
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO app_settings (key, value) VALUES ($1, '{}'::jsonb) ON CONFLICT (key) DO NOTHING`, key); err != nil {
			t.Fatalf("seed readiness setting %s: %v", key, err)
		}
	}
	if _, err := coordinator.BeginRecovery(ctx, runtimecontrol.BeginStateEpochRecoveryInput{
		RecoveryID: "recovery-before-activation", ExpectedCurrentRevision: 1,
		Reason:     runtimecontrol.StateEpochReasonStateLoss,
		DetectedAt: bootstrap.Record.Value.ActivatedAt.Add(-time.Nanosecond), NotBefore: *bootstrap.Record.Value.ActivatedAt,
		OperatorRef: "change-before", StateLossConfirmed: true, ExternalIngressBlockedConfirmed: true,
	}); failure.CodeOf(err) != failure.CodeConfigInvalid {
		t.Fatalf("detected_at before activation must be rejected: code=%q err=%v", failure.CodeOf(err), err)
	}
	currentTime := time.Now().UTC()
	if _, err := coordinator.BeginRecovery(ctx, runtimecontrol.BeginStateEpochRecoveryInput{
		RecoveryID: "recovery-wrong-revision", ExpectedCurrentRevision: 9,
		Reason: runtimecontrol.StateEpochReasonStateLoss, DetectedAt: currentTime, NotBefore: currentTime,
		OperatorRef: "change-revision", StateLossConfirmed: true, ExternalIngressBlockedConfirmed: true,
	}); !errors.Is(err, runtimecontrol.ErrStateEpochConflict) {
		t.Fatalf("unexpected current revision must be rejected: %v", err)
	}

	detectedAt := time.Now().UTC()
	notBefore := detectedAt
	firstBeginInput := runtimecontrol.BeginStateEpochRecoveryInput{
		RecoveryID: "recovery-first", ExpectedCurrentRevision: 1,
		Reason: runtimecontrol.StateEpochReasonStateLoss, DetectedAt: detectedAt, NotBefore: notBefore,
		OperatorRef: "change-first", StateLossConfirmed: true, ExternalIngressBlockedConfirmed: true,
	}
	begin, err := coordinator.BeginRecovery(ctx, firstBeginInput)
	if err != nil || begin.State != runtimecontrol.StateEpochEnsureAwaitingMaintenance || begin.Record.Value.State != runtimecontrol.StateEpochRecovering {
		t.Fatalf("begin state-loss recovery: result=%+v err=%v", begin, err)
	}
	retriedBegin, err := coordinator.BeginRecovery(ctx, firstBeginInput)
	if err != nil || retriedBegin.State != runtimecontrol.StateEpochEnsureAwaitingMaintenance || retriedBegin.Record.Revision != 2 {
		t.Fatalf("retry matching begin: result=%+v err=%v", retriedBegin, err)
	}
	if _, err := coordinator.BeginRecovery(ctx, runtimecontrol.BeginStateEpochRecoveryInput{
		RecoveryID: "recovery-other", ExpectedCurrentRevision: 1,
		Reason: runtimecontrol.StateEpochReasonStateLoss, DetectedAt: detectedAt, NotBefore: notBefore,
		OperatorRef: "change-other", StateLossConfirmed: true, ExternalIngressBlockedConfirmed: true,
	}); !errors.Is(err, runtimecontrol.ErrStateEpochConflict) {
		t.Fatalf("different begin input must conflict: %v", err)
	}
	queries := sqlc.New(tx)
	op, err := queries.GetNonterminalRuntimeStateEpochOperation(ctx)
	if err != nil || op.State != "db_committed" {
		t.Fatalf("read db-committed operation: op=%+v err=%v", op, err)
	}
	firstTransition, err := runtimecontrol.DecodeStateEpochTransition(op.EpochTransition)
	if err != nil {
		t.Fatal(err)
	}
	firstCommitInput := runtimecontrol.CommitStateEpochRecoveryInput{
		RecoveryID: "recovery-first", Revision: 2, RecoveryEvidence: op.RecoveryEvidence,
	}
	if _, err := coordinator.CommitRecovery(ctx, firstCommitInput); failure.CodeOf(err) != failure.CodeConfigInvalid {
		t.Fatalf("collecting evidence must not commit: code=%q err=%v", failure.CodeOf(err), err)
	}

	checkedAt := time.Now().UTC()
	firstEvidence := approvedRecoveryEvidence(firstTransition, "change-first", checkedAt, checkedAt)
	firstRaw, err := firstEvidence.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	firstCommitInput.RecoveryEvidence = firstRaw
	committed, err := coordinator.CommitRecovery(ctx, firstCommitInput)
	if err != nil || committed.State != runtimecontrol.StateEpochEnsureAwaitingRelease || committed.Record.Value.State != runtimecontrol.StateEpochReady || committed.Record.Revision != 2 {
		t.Fatalf("commit state-loss recovery: result=%+v err=%v cause=%v", committed, err, errors.Unwrap(err))
	}
	readiness, err := queries.GetGatewayRuntimeReadinessSnapshot(ctx)
	if err != nil || readiness.RuntimeOperationsReconciled || !readiness.RuntimeMaintenanceSmokeAllowed {
		t.Fatalf("awaiting-release lock must keep readiness closed: snapshot=%+v err=%v", readiness, err)
	}
	// A lost Commit response retries against the same awaiting-release operation.
	retried, err := coordinator.CommitRecovery(ctx, firstCommitInput)
	if err != nil || retried.State != runtimecontrol.StateEpochEnsureAwaitingRelease || retried.Record.Revision != 2 {
		t.Fatalf("retry committed recovery: result=%+v err=%v", retried, err)
	}
	different := firstEvidence
	differentHash := "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	different.Gates.Control.SummaryHash = &differentHash
	differentRaw, err := different.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.CommitRecovery(ctx, runtimecontrol.CommitStateEpochRecoveryInput{
		RecoveryID: "recovery-first", Revision: 2, RecoveryEvidence: differentRaw,
	}); !errors.Is(err, runtimecontrol.ErrStateEpochConflict) {
		t.Fatalf("different evidence reused committed operation: %v", err)
	}
	if got, err := coordinator.BeginRecovery(ctx, firstBeginInput); err != nil || got.State != runtimecontrol.StateEpochEnsureAwaitingRelease {
		t.Fatalf("awaiting-release begin retry must be idempotent: result=%+v err=%v", got, err)
	}
	firstReleaseEvidence := passedReleaseEvidence(firstTransition, time.Now().UTC().Add(-time.Millisecond))
	firstReleaseRaw, err := firstReleaseEvidence.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	firstReleaseInput := runtimecontrol.ReleaseStateEpochRecoveryInput{
		RecoveryID: "recovery-first", Revision: 2, ReleaseEvidence: firstReleaseRaw,
	}
	if released, err := coordinator.ReleaseRecovery(ctx, firstReleaseInput); err != nil || released.State != runtimecontrol.StateEpochEnsureReady {
		t.Fatalf("release first recovery: result=%+v err=%v cause=%v", released, err, errors.Unwrap(err))
	}
	readiness, err = queries.GetGatewayRuntimeReadinessSnapshot(ctx)
	if err != nil || !readiness.RuntimeOperationsReconciled || readiness.RuntimeMaintenanceSmokeAllowed {
		t.Fatalf("release must reopen runtime operation readiness: snapshot=%+v err=%v", readiness, err)
	}
	if released, err := coordinator.ReleaseRecovery(ctx, firstReleaseInput); err != nil || released.State != runtimecontrol.StateEpochEnsureReady {
		t.Fatalf("retry released recovery: result=%+v err=%v", released, err)
	}
	if got, err := coordinator.BeginRecovery(ctx, firstBeginInput); err != nil || got.State != runtimecontrol.StateEpochEnsureReady {
		t.Fatalf("completed begin retry must be idempotent: result=%+v err=%v", got, err)
	}

	secondDetectedAt := time.Now().UTC()
	secondNotBefore := secondDetectedAt
	secondBegin, err := coordinator.BeginRecovery(ctx, runtimecontrol.BeginStateEpochRecoveryInput{
		RecoveryID: "recovery-second", ExpectedCurrentRevision: 2,
		Reason: runtimecontrol.StateEpochReasonRestore, DetectedAt: secondDetectedAt, NotBefore: secondNotBefore,
		OperatorRef: "change-second", StateLossConfirmed: true, ExternalIngressBlockedConfirmed: true,
	})
	if err != nil || secondBegin.State != runtimecontrol.StateEpochEnsureAwaitingMaintenance || secondBegin.Record.Revision != 3 {
		t.Fatalf("begin restore recovery: result=%+v err=%v", secondBegin, err)
	}
	// Lose the Redis marker and pending operation after PostgreSQL db_committed.
	// Commit must first recover the same durable transition, never Abort it.
	clearNamespace()
	if recovered, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator); err != nil || recovered.State != runtimecontrol.StateEpochEnsureAwaitingMaintenance {
		t.Fatalf("recover deleted redis pending fence: result=%+v err=%v", recovered, err)
	}
	secondOp, err := queries.GetNonterminalRuntimeStateEpochOperation(ctx)
	if err != nil || secondOp.State != "db_committed" || !secondOp.ExpectedMarkerHash.Valid {
		t.Fatalf("reload recovered second operation: op=%+v err=%v", secondOp, err)
	}
	secondTransition, err := runtimecontrol.DecodeStateEpochTransition(secondOp.EpochTransition)
	if err != nil {
		t.Fatal(err)
	}
	secondCheckedAt := time.Now().UTC()
	secondEvidence := approvedRecoveryEvidence(secondTransition, "change-second", secondCheckedAt, secondCheckedAt)
	secondRaw, err := secondEvidence.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	rows, err := queries.CompareAndSetRuntimeStateEpochRecoveryEvidence(ctx, sqlc.CompareAndSetRuntimeStateEpochRecoveryEvidenceParams{
		NextRecoveryEvidence: secondRaw, Token: secondOp.Token, PayloadHash: secondOp.PayloadHash,
		CurrentRecoveryEvidence: secondOp.RecoveryEvidence,
	})
	if err != nil || rows != 1 {
		t.Fatalf("approve second recovery evidence: rows=%d err=%v", rows, err)
	}
	oldEpoch, oldRevision := secondTransition.OldIdentity()
	redisCommitted, err := epochStore.CommitRuntimeStateEpoch(ctx, breakerstore.StateEpochFenceInput{
		Token: secondOp.Token, TransitionHash: secondOp.PayloadHash,
		ExpectedMarkerHash: secondOp.ExpectedMarkerHash.String,
		OldEpoch:           oldEpoch, OldRevision: oldRevision,
		NewEpoch: secondTransition.NewEpoch, NewRevision: secondTransition.NewRevision,
	})
	if err != nil || !redisCommitted {
		t.Fatalf("simulate redis commit with lost response: committed=%v err=%v", redisCommitted, err)
	}
	// PostgreSQL is intentionally still recovering/db_committed. A retry must
	// observe the same-operation new-ready marker and atomically finalize PG.
	secondCommitInput := runtimecontrol.CommitStateEpochRecoveryInput{
		RecoveryID: "recovery-second", Revision: 3, RecoveryEvidence: secondRaw,
	}
	secondCommitted, err := coordinator.CommitRecovery(ctx, secondCommitInput)
	if err != nil || secondCommitted.State != runtimecontrol.StateEpochEnsureAwaitingRelease || secondCommitted.Record.Revision != 3 {
		t.Fatalf("commit after redis state loss: result=%+v err=%v", secondCommitted, err)
	}
	if _, err := coordinator.CommitRecovery(ctx, firstCommitInput); !errors.Is(err, runtimecontrol.ErrStateEpochConflict) {
		t.Fatalf("old evidence must not match a newer latest commit: %v", err)
	}
	secondRelease := passedReleaseEvidence(secondTransition, time.Now().UTC().Add(-time.Millisecond))
	secondReleaseRaw, err := secondRelease.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.ReleaseRecovery(ctx, runtimecontrol.ReleaseStateEpochRecoveryInput{
		RecoveryID: "recovery-first", Revision: 2, ReleaseEvidence: firstReleaseRaw,
	}); !errors.Is(err, runtimecontrol.ErrStateEpochConflict) {
		t.Fatalf("old release must not unlock newer operation: %v", err)
	}
	if _, err := coordinator.ReleaseRecovery(ctx, runtimecontrol.ReleaseStateEpochRecoveryInput{
		RecoveryID: "recovery-second", Revision: 3, ReleaseEvidence: secondReleaseRaw,
	}); err != nil {
		t.Fatalf("release second recovery: %v cause=%v", err, errors.Unwrap(err))
	}
	latest, err := queries.GetLatestCommittedRuntimeStateEpochOperation(ctx)
	if err != nil || latest.State != "committed" || latest.RecoveryEvidence == nil {
		t.Fatalf("latest committed operation: op=%+v err=%v", latest, err)
	}
}

func TestRuntimeControlOperationRejectsMalformedRecoveryEvidenceShape(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	if databaseURL == "" {
		t.Skip("DATABASE_URL is required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if _, err := tx.Exec(ctx, `DELETE FROM runtime_control_operations WHERE setting_key = 'gateway.runtime_state_epoch'`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM app_settings WHERE key = 'gateway.runtime_state_epoch'`); err != nil {
		t.Fatal(err)
	}
	activatedAt := time.Now().UTC().Add(-time.Hour)
	old := runtimecontrol.StateEpoch{
		Epoch: "00112233445566778899aabbccddeeff", State: runtimecontrol.StateEpochReady,
		Reason: runtimecontrol.StateEpochReasonBootstrap, ActivatedAt: &activatedAt,
	}
	oldRaw, err := old.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if rows, err := sqlc.New(tx).SeedRuntimeStateEpoch(ctx, oldRaw); err != nil || rows != 1 {
		t.Fatalf("seed epoch: rows=%d err=%v", rows, err)
	}
	transition, err := runtimecontrol.NewStateEpochRecoveryTransition(
		old, 1, "recovery-shape", runtimecontrol.StateEpochReasonStateLoss, true, activatedAt.Add(time.Minute), activatedAt.Add(2*time.Minute),
	)
	if err != nil {
		t.Fatal(err)
	}
	validTransition, err := transition.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	collecting, err := runtimecontrol.NewCollectingStateEpochRecoveryEvidence(transition, "shape-check", time.Now().UTC())
	if err != nil {
		t.Fatal(err)
	}
	validEvidence, err := collecting.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	mismatchedRecovery := collecting
	mismatchedRecovery.RecoveryID = "another-recovery"
	mismatchedRecoveryRaw, err := mismatchedRecovery.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	mismatchedRevision := collecting
	mismatchedRevision.CurrentRevision++
	mismatchedRevisionRaw, err := mismatchedRevision.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	mismatchedReason := collecting
	mismatchedReason.Reason = runtimecontrol.StateEpochReasonRestore
	mismatchedReasonRaw, err := mismatchedReason.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	mismatchedDetectedAt := collecting
	mismatchedDetectedAt.DetectedAt = mismatchedDetectedAt.DetectedAt.Add(time.Second)
	mismatchedDetectedAtRaw, err := mismatchedDetectedAt.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	mismatchedNotBefore := collecting
	mismatchedNotBefore.NotBefore = mismatchedNotBefore.NotBefore.Add(time.Second)
	mismatchedNotBeforeRaw, err := mismatchedNotBefore.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	tooLateTransition := transition
	tooLateTransition.NotBefore = tooLateTransition.DetectedAt.Add(24*time.Hour + time.Second)
	tooLateTransitionRaw, err := json.Marshal(tooLateTransition)
	if err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name       string
		transition string
		evidence   string
	}{
		{
			name: "missing transition reason",
			transition: `{"old_epoch":"00112233445566778899aabbccddeeff","old_revision":1,` +
				`"new_epoch":"ffeeddccbbaa99887766554433221100","new_revision":2}`,
			evidence: string(validEvidence),
		},
		{name: "transition window exceeds 24 hours", transition: string(tooLateTransitionRaw), evidence: string(validEvidence)},
		{name: "missing evidence gates", transition: string(validTransition), evidence: `{"schema_version":1,"operator_ref":"shape-check","status":"collecting","recorded_at":"2026-07-22T00:00:00Z"}`},
		{name: "extra sensitive evidence field", transition: string(validTransition), evidence: string(validEvidence[:len(validEvidence)-1]) + `,"credential":"forbidden"}`},
		{name: "different recovery id", transition: string(validTransition), evidence: string(mismatchedRecoveryRaw)},
		{name: "different current revision", transition: string(validTransition), evidence: string(mismatchedRevisionRaw)},
		{name: "different reason", transition: string(validTransition), evidence: string(mismatchedReasonRaw)},
		{name: "different detected at", transition: string(validTransition), evidence: string(mismatchedDetectedAtRaw)},
		{name: "different not before", transition: string(validTransition), evidence: string(mismatchedNotBeforeRaw)},
	}
	for index, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			savepoint, err := tx.Begin(ctx)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = savepoint.Rollback(ctx) }()
			_, err = savepoint.Exec(ctx, `INSERT INTO runtime_control_operations (
				token, kind, setting_key, current_revision, next_revision, payload_hash,
				epoch_transition, recovery_evidence, state
			) VALUES ($1, 'runtime_state_epoch', 'gateway.runtime_state_epoch', 1, 2, 'shape-hash', $2::jsonb, $3::jsonb, 'preparing')`,
				fmt.Sprintf("shape-token-%d", index), tc.transition, tc.evidence)
			if err == nil {
				t.Fatal("malformed epoch operation passed database CHECK")
			}
		})
	}
	validOp, err := sqlc.New(tx).CreateRuntimeControlOperation(ctx, sqlc.CreateRuntimeControlOperationParams{
		Token: "shape-valid-operation", Kind: "runtime_state_epoch",
		SettingKey:      pgtype.Text{String: runtimecontrol.RuntimeStateEpochKey, Valid: true},
		CurrentRevision: 1, NextRevision: 2,
		PayloadHash: breakerstore.HashPayload(string(validTransition)), EpochTransition: validTransition,
		RecoveryEvidence: validEvidence,
	})
	if err != nil {
		t.Fatalf("create valid epoch operation fixture: %v", err)
	}
	for name, statement := range map[string]string{
		"epoch aborted": `UPDATE runtime_control_operations SET state='aborted', completed_at=now() WHERE token=$1`,
		"state skipped": `UPDATE runtime_control_operations SET state='db_committed' WHERE token=$1`,
	} {
		savepoint, err := tx.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := savepoint.Exec(ctx, statement, validOp.Token); err == nil {
			t.Fatalf("invalid operation mutation passed: %s", name)
		}
		if err := savepoint.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
	}
}

func TestStateEpochMaintenanceConcurrentEvidenceCAS(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	redisAddr := os.Getenv("REDIS_ADDR")
	if databaseURL == "" || redisAddr == "" {
		t.Skip("DATABASE_URL and REDIS_ADDR are required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	cleanupDB := func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM runtime_control_operations WHERE setting_key = 'gateway.runtime_state_epoch'`)
		_, _ = pool.Exec(context.Background(), `DELETE FROM app_settings WHERE key = 'gateway.runtime_state_epoch'`)
	}
	cleanupDB()
	defer cleanupDB()
	client := redis.NewClient(&redis.Options{Addr: redisAddr})
	defer client.Close()
	namespace := fmt.Sprintf("unio-epoch-maintenance-race:%d", time.Now().UnixNano())
	defer func() {
		var keys []string
		iter := client.Scan(context.Background(), 0, namespace+":*", 0).Iterator()
		for iter.Next(context.Background()) {
			keys = append(keys, iter.Val())
		}
		if len(keys) > 0 {
			_ = client.Del(context.Background(), keys...).Err()
		}
	}()
	store := breakerstore.NewStore(client, namespace)
	coordinator := runtimecontrol.NewStateEpochCoordinator(pool, store)
	if result, err := runtimecontrol.EnsureStateEpochSeed(ctx, coordinator); err != nil || result.State != runtimecontrol.StateEpochEnsureReady {
		t.Fatalf("bootstrap concurrent fixture: result=%+v err=%v", result, err)
	}

	begin := func(operator, recoveryID string, expectedRevision int64, reason runtimecontrol.StateEpochReason) runtimecontrol.StateEpochTransition {
		detectedAt := time.Now().UTC()
		result, err := coordinator.BeginRecovery(ctx, runtimecontrol.BeginStateEpochRecoveryInput{
			RecoveryID: recoveryID, ExpectedCurrentRevision: expectedRevision,
			Reason: reason, DetectedAt: detectedAt, NotBefore: detectedAt,
			OperatorRef: operator, StateLossConfirmed: true, ExternalIngressBlockedConfirmed: true,
		})
		if err != nil || result.State != runtimecontrol.StateEpochEnsureAwaitingMaintenance {
			t.Fatalf("begin concurrent fixture: result=%+v err=%v", result, err)
		}
		op, err := sqlc.New(pool).GetNonterminalRuntimeStateEpochOperation(ctx)
		if err != nil {
			t.Fatal(err)
		}
		transition, err := runtimecontrol.DecodeStateEpochTransition(op.EpochTransition)
		if err != nil {
			t.Fatal(err)
		}
		return transition
	}
	commitConcurrently := func(recoveryID string, revision int64, left, right []byte) [2]error {
		start := make(chan struct{})
		results := make(chan struct {
			index int
			err   error
		}, 2)
		for index, raw := range [][]byte{left, right} {
			go func(index int, raw []byte) {
				<-start
				result, err := coordinator.CommitRecovery(ctx, runtimecontrol.CommitStateEpochRecoveryInput{
					RecoveryID: recoveryID, Revision: revision, RecoveryEvidence: raw,
				})
				if err == nil && result.State != runtimecontrol.StateEpochEnsureAwaitingRelease {
					err = fmt.Errorf("unexpected commit state %q", result.State)
				}
				results <- struct {
					index int
					err   error
				}{index: index, err: err}
			}(index, raw)
		}
		close(start)
		var errorsByIndex [2]error
		for range 2 {
			result := <-results
			errorsByIndex[result.index] = result.err
		}
		return errorsByIndex
	}

	firstTransition := begin("concurrent-same", "recovery-concurrent-same", 1, runtimecontrol.StateEpochReasonStateLoss)
	checkedAt := time.Now().UTC()
	sameEvidence := approvedRecoveryEvidence(firstTransition, "concurrent-same", checkedAt, checkedAt)
	sameRaw, err := sameEvidence.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	sameResults := commitConcurrently("recovery-concurrent-same", 2, sameRaw, sameRaw)
	if sameResults[0] != nil || sameResults[1] != nil {
		t.Fatalf("same evidence must commit idempotently: %v", sameResults)
	}
	firstRelease := passedReleaseEvidence(firstTransition, time.Now().UTC().Add(-time.Millisecond))
	firstReleaseRaw, err := firstRelease.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := coordinator.ReleaseRecovery(ctx, runtimecontrol.ReleaseStateEpochRecoveryInput{
		RecoveryID: "recovery-concurrent-same", Revision: 2, ReleaseEvidence: firstReleaseRaw,
	}); err != nil {
		t.Fatalf("release concurrent fixture: %v cause=%v", err, errors.Unwrap(err))
	}

	secondTransition := begin("concurrent-different", "recovery-concurrent-different", 2, runtimecontrol.StateEpochReasonRestore)
	checkedAt = time.Now().UTC()
	leftEvidence := approvedRecoveryEvidence(secondTransition, "concurrent-different", checkedAt, checkedAt)
	rightEvidence := leftEvidence
	rightHash := "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	rightEvidence.Gates.Permission.SummaryHash = &rightHash
	leftRaw, err := leftEvidence.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	rightRaw, err := rightEvidence.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	differentResults := commitConcurrently("recovery-concurrent-different", 3, leftRaw, rightRaw)
	successes, conflicts := 0, 0
	for _, err := range differentResults {
		switch {
		case err == nil:
			successes++
		case errors.Is(err, runtimecontrol.ErrStateEpochConflict):
			conflicts++
		default:
			t.Fatalf("unexpected concurrent different-evidence error: %v", err)
		}
	}
	if successes != 1 || conflicts != 1 {
		t.Fatalf("different evidence results: successes=%d conflicts=%d errors=%v", successes, conflicts, differentResults)
	}
}
