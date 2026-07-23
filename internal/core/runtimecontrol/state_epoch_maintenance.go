package runtimecontrol

import (
	"bytes"
	"context"
	"errors"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type BeginStateEpochRecoveryInput struct {
	RecoveryID                      string
	ExpectedCurrentRevision         int64
	Reason                          StateEpochReason
	DetectedAt                      time.Time
	NotBefore                       time.Time
	OperatorRef                     string
	StateLossConfirmed              bool
	ExternalIngressBlockedConfirmed bool
}

// BeginRecovery creates the only durable non-bootstrap epoch operation. The
// caller must have blocked external ingress before invoking it; the operation
// then establishes the Redis pending fence before advancing PostgreSQL.
func (c *StateEpochCoordinator) BeginRecovery(
	ctx context.Context,
	in BeginStateEpochRecoveryInput,
) (StateEpochEnsureResult, error) {
	if in.Reason != StateEpochReasonStateLoss && in.Reason != StateEpochReasonRestore {
		return StateEpochEnsureResult{}, maintenanceInputError("recovery reason must be state_loss or restore")
	}
	if !in.StateLossConfirmed {
		return StateEpochEnsureResult{}, maintenanceInputError("runtime state loss must be explicitly confirmed")
	}
	if !in.ExternalIngressBlockedConfirmed {
		return StateEpochEnsureResult{}, maintenanceInputError("external ingress blocking must be explicitly confirmed")
	}
	if !validRecoveryID(in.RecoveryID) || in.ExpectedCurrentRevision < 1 {
		return StateEpochEnsureResult{}, maintenanceInputError("recovery_id and expected current revision are required")
	}
	now := c.now().UTC()
	if in.DetectedAt.IsZero() || in.NotBefore.IsZero() || in.DetectedAt.After(now) || in.NotBefore.Before(in.DetectedAt) {
		return StateEpochEnsureResult{}, maintenanceInputError("recovery timestamps are invalid")
	}
	if in.NotBefore.After(in.DetectedAt.Add(maximumStateEpochRecoveryDelay)) {
		return StateEpochEnsureResult{}, maintenanceInputError("not_before must be within 24 hours of detected_at")
	}

	tx, err := c.db.Begin(ctx)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "begin state epoch maintenance transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	row, err := q.GetAppSettingRecordForUpdate(ctx, RuntimeStateEpochKey)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "lock state epoch for maintenance")
	}
	current, err := DecodeStateEpoch(row.Value)
	if err != nil {
		return StateEpochEnsureResult{}, epochConflict("state epoch maintenance found an invalid current epoch")
	}
	if active, activeErr := q.GetNonterminalRuntimeStateEpochOperation(ctx); activeErr == nil {
		if err := matchingRecoveryBegin(active, current, row.Revision, in); err != nil {
			return StateEpochEnsureResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return StateEpochEnsureResult{}, wrapEpochStoreError(err, "commit idempotent state epoch maintenance check")
		}
		return c.Ensure(ctx)
	} else if !errors.Is(activeErr, pgx.ErrNoRows) {
		return StateEpochEnsureResult{}, wrapEpochStoreError(activeErr, "check active state epoch maintenance operation")
	}
	if current.State != StateEpochReady {
		return StateEpochEnsureResult{}, epochConflict("state epoch maintenance requires the current ready epoch")
	}
	latest, latestErr := q.GetLatestCommittedRuntimeStateEpochOperation(ctx)
	if latestErr == nil {
		duplicate, matchErr := matchingCommittedRecoveryBegin(latest, current, row.Revision, in)
		if matchErr != nil {
			return StateEpochEnsureResult{}, matchErr
		}
		if duplicate {
			if err := tx.Commit(ctx); err != nil {
				return StateEpochEnsureResult{}, wrapEpochStoreError(err, "commit idempotent completed state epoch maintenance check")
			}
			return c.Ensure(ctx)
		}
	} else if !errors.Is(latestErr, pgx.ErrNoRows) {
		return StateEpochEnsureResult{}, wrapEpochStoreError(latestErr, "read latest committed state epoch operation")
	}
	if row.Revision != in.ExpectedCurrentRevision {
		return StateEpochEnsureResult{}, epochConflict("state epoch maintenance expected current revision does not match")
	}
	if current.ActivatedAt == nil || in.DetectedAt.Before(*current.ActivatedAt) {
		return StateEpochEnsureResult{}, maintenanceInputError("detected_at predates the current epoch activation")
	}

	transition, err := NewStateEpochRecoveryTransition(
		current,
		row.Revision,
		in.RecoveryID,
		in.Reason,
		in.StateLossConfirmed,
		in.DetectedAt,
		in.NotBefore,
	)
	if err != nil {
		return StateEpochEnsureResult{}, maintenanceInputError("state epoch recovery transition is invalid")
	}
	collecting, err := NewCollectingStateEpochRecoveryEvidence(transition, in.OperatorRef, now)
	if err != nil {
		return StateEpochEnsureResult{}, maintenanceInputError("recovery operator reference is invalid")
	}
	collectingRaw, err := collecting.Marshal()
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	transitionRaw, err := transition.Marshal()
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	token, err := newStateEpochOperationToken()
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if _, err := q.CreateRuntimeControlOperation(ctx, sqlc.CreateRuntimeControlOperationParams{
		Token:            token,
		Kind:             "runtime_state_epoch",
		SettingKey:       pgtype.Text{String: RuntimeStateEpochKey, Valid: true},
		CurrentRevision:  row.Revision,
		NextRevision:     transition.NewRevision,
		PayloadHash:      breakerstore.HashPayload(string(transitionRaw)),
		EpochTransition:  transitionRaw,
		RecoveryEvidence: collectingRaw,
	}); err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "create state epoch maintenance operation")
	}
	if err := tx.Commit(ctx); err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "commit state epoch maintenance operation")
	}

	return c.Ensure(ctx)
}

func matchingRecoveryBegin(
	op sqlc.RuntimeControlOperation,
	current StateEpoch,
	currentRevision int64,
	in BeginStateEpochRecoveryInput,
) error {
	transition, _, err := validateEpochOperation(op)
	if err != nil {
		return err
	}
	if transition.Reason != in.Reason || !transition.StateLossConfirmed ||
		transition.RecoveryID == nil || *transition.RecoveryID != in.RecoveryID ||
		transition.OldRevision == nil || *transition.OldRevision != in.ExpectedCurrentRevision ||
		!transition.DetectedAt.Equal(in.DetectedAt) || !transition.NotBefore.Equal(in.NotBefore) {
		return epochConflict("active state epoch maintenance operation does not match begin input")
	}
	evidence, err := DecodeStateEpochRecoveryEvidence(op.RecoveryEvidence)
	if err != nil || evidence.OperatorRef != in.OperatorRef {
		return epochConflict("active state epoch maintenance operator does not match begin input")
	}
	oldEpoch, oldRevision := transition.OldIdentity()
	oldReady := current.State == StateEpochReady && current.Epoch == oldEpoch && currentRevision == oldRevision
	newRecovering := current.State == StateEpochRecovering && current.Epoch == transition.NewEpoch && currentRevision == transition.NewRevision
	newReadyLocked := op.State == "awaiting_release" && current.State == StateEpochReady &&
		current.Epoch == transition.NewEpoch && currentRevision == transition.NewRevision
	if !oldReady && !newRecovering && !newReadyLocked {
		return epochConflict("active state epoch maintenance durable identity does not match begin input")
	}
	return nil
}

func matchingCommittedRecoveryBegin(
	op sqlc.RuntimeControlOperation,
	current StateEpoch,
	currentRevision int64,
	in BeginStateEpochRecoveryInput,
) (bool, error) {
	transition, _, err := validateEpochOperation(op)
	if err != nil {
		return false, err
	}
	if transition.Reason != StateEpochReasonStateLoss && transition.Reason != StateEpochReasonRestore {
		return false, nil
	}
	if transition.Reason != in.Reason || !transition.DetectedAt.Equal(in.DetectedAt) ||
		transition.RecoveryID == nil || *transition.RecoveryID != in.RecoveryID ||
		transition.OldRevision == nil || *transition.OldRevision != in.ExpectedCurrentRevision ||
		!transition.NotBefore.Equal(in.NotBefore) {
		return false, nil
	}
	evidence, err := DecodeStateEpochRecoveryEvidence(op.RecoveryEvidence)
	if err != nil {
		return false, epochConflict("latest committed state epoch recovery evidence is invalid")
	}
	if evidence.OperatorRef != in.OperatorRef {
		return false, nil
	}
	if current.State != StateEpochReady || current.Epoch != transition.NewEpoch || currentRevision != transition.NewRevision {
		return false, epochConflict("latest committed state epoch does not match current ready identity")
	}
	return true, nil
}

type CommitStateEpochRecoveryInput struct {
	RecoveryID       string
	Revision         int64
	RecoveryEvidence []byte
}

// CommitRecovery is the only non-bootstrap Commit authorization. It persists
// approved evidence first, re-locks and revalidates PostgreSQL, commits the
// Redis marker, then atomically marks the PostgreSQL epoch ready and keeps the
// operation awaiting_release. No Abort transition exists.
func (c *StateEpochCoordinator) CommitRecovery(
	ctx context.Context,
	in CommitStateEpochRecoveryInput,
) (StateEpochEnsureResult, error) {
	if !validRecoveryID(in.RecoveryID) || in.Revision < 2 {
		return StateEpochEnsureResult{}, maintenanceInputError("recovery_id and target revision are required")
	}
	q := sqlc.New(c.db)
	op, err := q.GetNonterminalRuntimeStateEpochOperation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.validateLatestCommittedRecovery(ctx, in)
	}
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "read state epoch maintenance operation")
	}
	transition, _, err := validateEpochOperation(op)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if transition.Reason != StateEpochReasonStateLoss && transition.Reason != StateEpochReasonRestore {
		return StateEpochEnsureResult{}, epochConflict("bootstrap epoch cannot use maintenance commit")
	}
	if err := validateRecoveryCommandIdentity(transition, in.RecoveryID, in.Revision); err != nil {
		return StateEpochEnsureResult{}, err
	}

	// Resume any crash point through pending and db_committed. Ensure never
	// commits a non-bootstrap epoch by itself.
	if _, err := c.Ensure(ctx); err != nil {
		return StateEpochEnsureResult{}, err
	}
	op, err = q.GetRuntimeControlOperationByToken(ctx, op.Token)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "reload state epoch maintenance operation")
	}
	if op.State == "committed" {
		return c.validateCommittedRecovery(ctx, op, transition, in)
	}
	if op.State == "awaiting_release" {
		return c.validateAwaitingRelease(ctx, op, transition, in.RecoveryEvidence)
	}
	if op.State != "db_committed" {
		return StateEpochEnsureResult{}, epochConflict("state epoch maintenance operation is not db_committed")
	}

	currentEvidence, err := DecodeStateEpochRecoveryEvidence(op.RecoveryEvidence)
	if err != nil {
		return StateEpochEnsureResult{}, epochConflict("durable state epoch recovery evidence is invalid")
	}
	nextEvidence, err := DecodeStateEpochRecoveryEvidence(in.RecoveryEvidence)
	if err != nil {
		return StateEpochEnsureResult{}, maintenanceInputError("state epoch recovery evidence is invalid")
	}
	if err := nextEvidence.ValidateCommit(transition, currentEvidence.OperatorRef, c.now()); err != nil {
		return StateEpochEnsureResult{}, maintenanceInputError("state epoch recovery evidence does not satisfy commit gates")
	}
	nextEvidenceRaw, err := nextEvidence.Marshal()
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	currentEvidenceRaw, err := currentEvidence.Marshal()
	if err != nil {
		return StateEpochEnsureResult{}, err
	}

	if !sameRecoveryEvidence(currentEvidence, nextEvidence) {
		rows, updateErr := q.CompareAndSetRuntimeStateEpochRecoveryEvidence(ctx, sqlc.CompareAndSetRuntimeStateEpochRecoveryEvidenceParams{
			NextRecoveryEvidence:    nextEvidenceRaw,
			Token:                   op.Token,
			PayloadHash:             op.PayloadHash,
			CurrentRecoveryEvidence: currentEvidenceRaw,
		})
		if updateErr != nil {
			return StateEpochEnsureResult{}, wrapEpochStoreError(updateErr, "record approved state epoch recovery evidence")
		}
		if rows != 1 {
			fresh, loadErr := q.GetRuntimeControlOperationByToken(ctx, op.Token)
			if loadErr != nil {
				return StateEpochEnsureResult{}, epochConflict("state epoch recovery evidence changed concurrently")
			}
			freshEvidence, decodeErr := DecodeStateEpochRecoveryEvidence(fresh.RecoveryEvidence)
			if decodeErr != nil || !sameRecoveryEvidence(freshEvidence, nextEvidence) {
				return StateEpochEnsureResult{}, epochConflict("state epoch recovery evidence changed concurrently")
			}
		}
	}

	return c.commitApprovedRecovery(ctx, op.Token, op.PayloadHash, transition, nextEvidence)
}

func (c *StateEpochCoordinator) validateLatestCommittedRecovery(
	ctx context.Context,
	in CommitStateEpochRecoveryInput,
) (StateEpochEnsureResult, error) {
	op, err := sqlc.New(c.db).GetLatestCommittedRuntimeStateEpochOperation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return StateEpochEnsureResult{}, epochConflict("no state epoch maintenance operation is active")
	}
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "read latest committed state epoch operation")
	}
	transition, _, err := validateEpochOperation(op)
	if err != nil || (transition.Reason != StateEpochReasonStateLoss && transition.Reason != StateEpochReasonRestore) {
		return StateEpochEnsureResult{}, epochConflict("latest committed epoch is not a maintenance recovery")
	}
	if err := validateRecoveryCommandIdentity(transition, in.RecoveryID, in.Revision); err != nil {
		return StateEpochEnsureResult{}, err
	}
	return c.validateCommittedRecovery(ctx, op, transition, in)
}

func (c *StateEpochCoordinator) commitApprovedRecovery(
	ctx context.Context,
	token string,
	payloadHash string,
	transition StateEpochTransition,
	expectedEvidence StateEpochRecoveryEvidence,
) (StateEpochEnsureResult, error) {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "begin approved state epoch commit transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	row, err := q.GetAppSettingRecordForUpdate(ctx, RuntimeStateEpochKey)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "lock recovering epoch for maintenance commit")
	}
	lockedOp, err := q.GetRuntimeControlOperationByTokenForUpdate(ctx, token)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "lock approved state epoch operation")
	}
	if lockedOp.PayloadHash != payloadHash {
		return StateEpochEnsureResult{}, epochConflict("state epoch operation payload changed")
	}
	if _, _, err := validateEpochOperation(lockedOp); err != nil {
		return StateEpochEnsureResult{}, epochConflict("state epoch immutable transition changed")
	}
	lockedEvidence, err := DecodeStateEpochRecoveryEvidence(lockedOp.RecoveryEvidence)
	if err != nil || !sameRecoveryEvidence(lockedEvidence, expectedEvidence) {
		return StateEpochEnsureResult{}, epochConflict("approved state epoch recovery evidence changed")
	}
	if err := lockedEvidence.ValidateCommit(transition, expectedEvidence.OperatorRef, c.now()); err != nil {
		return StateEpochEnsureResult{}, epochConflict("approved state epoch recovery evidence is no longer valid")
	}
	current, err := DecodeStateEpoch(row.Value)
	if err != nil {
		return StateEpochEnsureResult{}, epochConflict("recovering epoch payload is invalid")
	}
	if lockedOp.State == "committed" && current.State == StateEpochReady &&
		current.Epoch == transition.NewEpoch && row.Revision == transition.NewRevision {
		if err := tx.Commit(ctx); err != nil {
			return StateEpochEnsureResult{}, wrapEpochStoreError(err, "commit idempotent approved epoch transaction")
		}
		return StateEpochEnsureResult{
			State:  StateEpochEnsureReady,
			Record: StateEpochRecord{Value: current, Revision: row.Revision},
		}, nil
	}
	if lockedOp.State != "db_committed" || current.State != StateEpochRecovering ||
		current.Epoch != transition.NewEpoch || row.Revision != transition.NewRevision {
		return StateEpochEnsureResult{}, epochConflict("approved state epoch commit durable state mismatch")
	}

	committed, err := c.store.CommitRuntimeStateEpoch(ctx, stateEpochFence(lockedOp, transition))
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if !committed {
		return StateEpochEnsureResult{}, epochConflict("redis epoch commit conflicted")
	}
	return c.finalizeReadyTx(ctx, tx, lockedOp, transition)
}

func (c *StateEpochCoordinator) validateCommittedRecovery(
	ctx context.Context,
	op sqlc.RuntimeControlOperation,
	transition StateEpochTransition,
	in CommitStateEpochRecoveryInput,
) (StateEpochEnsureResult, error) {
	if err := validateRecoveryCommandIdentity(transition, in.RecoveryID, in.Revision); err != nil {
		return StateEpochEnsureResult{}, err
	}
	durable, err := DecodeStateEpochRecoveryEvidence(op.RecoveryEvidence)
	if err != nil {
		return StateEpochEnsureResult{}, epochConflict("committed recovery evidence is invalid")
	}
	provided, err := DecodeStateEpochRecoveryEvidence(in.RecoveryEvidence)
	if err != nil || !sameRecoveryEvidence(durable, provided) {
		return StateEpochEnsureResult{}, epochConflict("committed recovery evidence does not match")
	}
	if err := durable.ValidateDurableBinding(transition, durable.OperatorRef); err != nil {
		return StateEpochEnsureResult{}, epochConflict("committed recovery evidence no longer validates")
	}
	row, err := sqlc.New(c.db).GetAppSettingRecord(ctx, RuntimeStateEpochKey)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "read committed state epoch")
	}
	record, err := decodeStateEpochRecord(row.Value, row.Revision)
	if err != nil || record.Value.State != StateEpochReady || record.Value.Epoch != transition.NewEpoch || record.Revision != transition.NewRevision {
		return StateEpochEnsureResult{}, epochConflict("committed state epoch does not match operation")
	}
	marker, err := c.store.StateIntegrity(ctx)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if !marker.Ready(record.Value.Epoch, record.Revision) {
		return StateEpochEnsureResult{}, epochConflict("committed state epoch marker is not ready")
	}
	return StateEpochEnsureResult{State: StateEpochEnsureReady, Record: record}, nil
}

func (c *StateEpochCoordinator) validateAwaitingRelease(
	ctx context.Context,
	op sqlc.RuntimeControlOperation,
	transition StateEpochTransition,
	providedRaw []byte,
) (StateEpochEnsureResult, error) {
	if op.State != "awaiting_release" {
		return StateEpochEnsureResult{}, epochConflict("state epoch operation is not awaiting release")
	}
	durable, err := DecodeStateEpochRecoveryEvidence(op.RecoveryEvidence)
	if err != nil {
		return StateEpochEnsureResult{}, epochConflict("awaiting-release recovery evidence is invalid")
	}
	provided, err := DecodeStateEpochRecoveryEvidence(providedRaw)
	if err != nil || !sameRecoveryEvidence(durable, provided) {
		return StateEpochEnsureResult{}, epochConflict("awaiting-release recovery evidence does not match")
	}
	if err := durable.ValidateDurableBinding(transition, durable.OperatorRef); err != nil {
		return StateEpochEnsureResult{}, epochConflict("awaiting-release recovery evidence no longer validates")
	}
	row, err := sqlc.New(c.db).GetAppSettingRecord(ctx, RuntimeStateEpochKey)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "read awaiting-release state epoch")
	}
	record, err := decodeStateEpochRecord(row.Value, row.Revision)
	if err != nil || record.Value.State != StateEpochReady || record.Value.Epoch != transition.NewEpoch || record.Revision != transition.NewRevision {
		return StateEpochEnsureResult{}, epochConflict("awaiting-release state epoch does not match operation")
	}
	marker, err := c.store.StateIntegrity(ctx)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if !marker.Ready(record.Value.Epoch, record.Revision) {
		return StateEpochEnsureResult{}, epochConflict("awaiting-release state epoch marker is not ready")
	}
	return StateEpochEnsureResult{
		State: StateEpochEnsureAwaitingRelease, Record: record, OperationToken: op.Token,
	}, nil
}

type ReleaseStateEpochRecoveryInput struct {
	RecoveryID      string
	Revision        int64
	ReleaseEvidence []byte
}

// ReleaseRecovery removes the durable maintenance lock only after post-commit
// smoke evidence is bound to the exact recovery and ready epoch revision.
func (c *StateEpochCoordinator) ReleaseRecovery(
	ctx context.Context,
	in ReleaseStateEpochRecoveryInput,
) (StateEpochEnsureResult, error) {
	if !validRecoveryID(in.RecoveryID) || in.Revision < 2 {
		return StateEpochEnsureResult{}, maintenanceInputError("recovery_id and target revision are required")
	}
	provided, err := DecodeStateEpochReleaseEvidence(in.ReleaseEvidence)
	if err != nil {
		return StateEpochEnsureResult{}, maintenanceInputError("state epoch release evidence is invalid")
	}
	q := sqlc.New(c.db)
	op, err := q.GetNonterminalRuntimeStateEpochOperation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return c.validateLatestReleasedRecovery(ctx, in, provided)
	}
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "read state epoch operation for release")
	}
	transition, _, err := validateEpochOperation(op)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if err := validateRecoveryCommandIdentity(transition, in.RecoveryID, in.Revision); err != nil {
		return StateEpochEnsureResult{}, err
	}
	if op.State != "awaiting_release" {
		return StateEpochEnsureResult{}, epochConflict("state epoch recovery is not awaiting release")
	}

	tx, err := c.db.Begin(ctx)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "begin state epoch release transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	lockedQ := sqlc.New(tx)
	row, err := lockedQ.GetAppSettingRecordForUpdate(ctx, RuntimeStateEpochKey)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "lock state epoch for release")
	}
	lockedOp, err := lockedQ.GetRuntimeControlOperationByTokenForUpdate(ctx, op.Token)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "lock state epoch operation for release")
	}
	lockedTransition, _, err := validateEpochOperation(lockedOp)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if err := validateRecoveryCommandIdentity(lockedTransition, in.RecoveryID, in.Revision); err != nil {
		return StateEpochEnsureResult{}, err
	}
	current, err := DecodeStateEpoch(row.Value)
	if err != nil || current.State != StateEpochReady || current.ActivatedAt == nil ||
		current.Epoch != lockedTransition.NewEpoch || row.Revision != lockedTransition.NewRevision {
		return StateEpochEnsureResult{}, epochConflict("ready state epoch does not match release operation")
	}
	if lockedOp.State == "committed" {
		if err := tx.Commit(ctx); err != nil {
			return StateEpochEnsureResult{}, wrapEpochStoreError(err, "commit idempotent state epoch release check")
		}
		return c.validateLatestReleasedRecovery(ctx, in, provided)
	}
	if lockedOp.State != "awaiting_release" {
		return StateEpochEnsureResult{}, epochConflict("state epoch release operation changed state")
	}
	if err := provided.ValidateRelease(lockedTransition, *current.ActivatedAt, c.now()); err != nil {
		return StateEpochEnsureResult{}, maintenanceInputError("post-commit Gateway smoke evidence does not satisfy release gates")
	}
	marker, err := c.store.StateIntegrity(ctx)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if !marker.Ready(current.Epoch, row.Revision) {
		return StateEpochEnsureResult{}, epochConflict("state epoch marker is not ready for release")
	}
	releaseRaw, err := provided.Marshal()
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	rows, err := lockedQ.MarkRuntimeStateEpochReleased(ctx, sqlc.MarkRuntimeStateEpochReleasedParams{
		ReleaseEvidence: releaseRaw, Token: lockedOp.Token, PayloadHash: lockedOp.PayloadHash,
	})
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "release state epoch maintenance lock")
	}
	if rows != 1 {
		return StateEpochEnsureResult{}, epochConflict("state epoch release CAS failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "commit state epoch maintenance release")
	}
	return StateEpochEnsureResult{
		State:          StateEpochEnsureReady,
		Record:         StateEpochRecord{Value: current, Revision: row.Revision},
		OperationToken: lockedOp.Token,
	}, nil
}

func (c *StateEpochCoordinator) validateLatestReleasedRecovery(
	ctx context.Context,
	in ReleaseStateEpochRecoveryInput,
	provided StateEpochReleaseEvidence,
) (StateEpochEnsureResult, error) {
	op, err := sqlc.New(c.db).GetLatestCommittedRuntimeStateEpochOperation(ctx)
	if errors.Is(err, pgx.ErrNoRows) {
		return StateEpochEnsureResult{}, epochConflict("no released state epoch recovery exists")
	}
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "read latest released state epoch operation")
	}
	transition, _, err := validateEpochOperation(op)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if err := validateRecoveryCommandIdentity(transition, in.RecoveryID, in.Revision); err != nil {
		return StateEpochEnsureResult{}, err
	}
	durable, err := DecodeStateEpochReleaseEvidence(op.ReleaseEvidence)
	if err != nil || !sameReleaseEvidence(durable, provided) {
		return StateEpochEnsureResult{}, epochConflict("released state epoch evidence does not match")
	}
	row, err := sqlc.New(c.db).GetAppSettingRecord(ctx, RuntimeStateEpochKey)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "read released state epoch")
	}
	record, err := decodeStateEpochRecord(row.Value, row.Revision)
	if err != nil || record.Value.State != StateEpochReady || record.Value.ActivatedAt == nil ||
		record.Value.Epoch != transition.NewEpoch || record.Revision != transition.NewRevision {
		return StateEpochEnsureResult{}, epochConflict("released state epoch does not match latest operation")
	}
	if err := durable.ValidateDurableBinding(transition, *record.Value.ActivatedAt); err != nil {
		return StateEpochEnsureResult{}, epochConflict("released state epoch evidence no longer validates")
	}
	marker, err := c.store.StateIntegrity(ctx)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if !marker.Ready(record.Value.Epoch, record.Revision) {
		return StateEpochEnsureResult{}, epochConflict("released state epoch marker is not ready")
	}
	return StateEpochEnsureResult{State: StateEpochEnsureReady, Record: record, OperationToken: op.Token}, nil
}

func validateRecoveryCommandIdentity(transition StateEpochTransition, recoveryID string, revision int64) error {
	if transition.RecoveryID == nil || *transition.RecoveryID != recoveryID || transition.NewRevision != revision {
		return epochConflict("state epoch maintenance command identity does not match operation")
	}
	return nil
}

func stateEpochFence(op sqlc.RuntimeControlOperation, transition StateEpochTransition) breakerstore.StateEpochFenceInput {
	oldEpoch, oldRevision := transition.OldIdentity()
	return breakerstore.StateEpochFenceInput{
		Token:              op.Token,
		TransitionHash:     op.PayloadHash,
		ExpectedMarkerHash: op.ExpectedMarkerHash.String,
		OldEpoch:           oldEpoch,
		OldRevision:        oldRevision,
		NewEpoch:           transition.NewEpoch,
		NewRevision:        transition.NewRevision,
	}
}

func sameRecoveryEvidence(left, right StateEpochRecoveryEvidence) bool {
	leftRaw, leftErr := left.Marshal()
	rightRaw, rightErr := right.Marshal()
	return leftErr == nil && rightErr == nil && bytes.Equal(leftRaw, rightRaw)
}

func maintenanceInputError(message string) error {
	return failure.New(failure.CodeConfigInvalid, failure.WithMessage("runtimecontrol: "+message))
}
