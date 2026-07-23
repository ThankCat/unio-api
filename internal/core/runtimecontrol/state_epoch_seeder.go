package runtimecontrol

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

var ErrStateEpochConflict = errors.New("runtimecontrol: state epoch recovery conflict")

type StateEpochDB interface {
	sqlc.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

type StateEpochStore interface {
	StateIntegrity(ctx context.Context) (breakerstore.StateIntegritySnapshot, error)
	RecoverRuntimeStateEpochFence(ctx context.Context, in breakerstore.StateEpochFenceInput) (breakerstore.StateEpochPrepareResult, error)
	CommitRuntimeStateEpoch(ctx context.Context, in breakerstore.StateEpochFenceInput) (bool, error)
}

type StateEpochEnsureState string

const (
	StateEpochEnsureReady               StateEpochEnsureState = "ready"
	StateEpochEnsureNotReady            StateEpochEnsureState = "not_ready"
	StateEpochEnsureAwaitingMaintenance StateEpochEnsureState = "awaiting_maintenance"
	StateEpochEnsureAwaitingRelease     StateEpochEnsureState = "awaiting_release"
)

type StateEpochRecord struct {
	Value    StateEpoch
	Revision int64
}

type StateEpochEnsureResult struct {
	State          StateEpochEnsureState
	Record         StateEpochRecord
	Created        bool
	OperationToken string
}

// StateEpochCoordinator 是唯一可以创建/恢复完整性 marker 的启动编排器。
// 它不实现 Abort；任何无法证明的分支都保持 fail-closed。
type StateEpochCoordinator struct {
	db    StateEpochDB
	store StateEpochStore
	now   func() time.Time
}

func NewStateEpochCoordinator(db StateEpochDB, store StateEpochStore) *StateEpochCoordinator {
	if db == nil || store == nil {
		panic("runtimecontrol: state epoch coordinator requires database and store")
	}
	return &StateEpochCoordinator{db: db, store: store, now: time.Now}
}

// EnsureStateEpochSeed 将维护保留行真正接入启动链：首次启动先在一条
// PostgreSQL statement 中原子创建 recovering 行与 preparing operation，再经 Redis
// pending/commit 和 PostgreSQL ready/committed 收口。它从不使用 SET NX。
func EnsureStateEpochSeed(ctx context.Context, coordinator *StateEpochCoordinator) (StateEpochEnsureResult, error) {
	if coordinator == nil {
		return StateEpochEnsureResult{}, failure.New(
			failure.CodeConfigInvalid,
			failure.WithMessage("runtimecontrol: state epoch coordinator is required"),
		)
	}
	return coordinator.Ensure(ctx)
}

func (c *StateEpochCoordinator) Ensure(ctx context.Context) (StateEpochEnsureResult, error) {
	q := sqlc.New(c.db)
	transition, err := NewBootstrapStateEpochTransition(c.now())
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
	epochRaw, err := (StateEpoch{
		Epoch: transition.NewEpoch, State: StateEpochRecovering, Reason: StateEpochReasonBootstrap,
	}).Marshal()
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	payloadHash := breakerstore.HashPayload(string(transitionRaw))

	op, createErr := q.CreateBootstrapRuntimeStateEpoch(ctx, sqlc.CreateBootstrapRuntimeStateEpochParams{
		Token: token, PayloadHash: payloadHash, EpochTransition: transitionRaw, StateEpochValue: epochRaw,
	})
	created := createErr == nil
	if createErr != nil && !errors.Is(createErr, pgx.ErrNoRows) {
		return StateEpochEnsureResult{}, wrapEpochStoreError(createErr, "create bootstrap epoch and operation")
	}

	row, err := q.GetAppSettingRecord(ctx, RuntimeStateEpochKey)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "read state epoch row")
	}
	record, err := decodeStateEpochRecord(row.Value, row.Revision)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}

	if !created {
		op, err = q.GetNonterminalRuntimeStateEpochOperation(ctx)
		if errors.Is(err, pgx.ErrNoRows) {
			if record.Value.State != StateEpochReady {
				return StateEpochEnsureResult{}, epochConflict("recovering epoch has no durable operation")
			}
			marker, markerErr := c.store.StateIntegrity(ctx)
			if markerErr != nil {
				return StateEpochEnsureResult{}, markerErr
			}
			state := StateEpochEnsureNotReady
			if marker.Ready(record.Value.Epoch, record.Revision) {
				state = StateEpochEnsureReady
			}
			return StateEpochEnsureResult{State: state, Record: record}, nil
		}
		if err != nil {
			return StateEpochEnsureResult{}, wrapEpochStoreError(err, "read active state epoch operation")
		}
	}

	result, err := c.reconcileOperation(ctx, op)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	result.Created = created
	return result, nil
}

func (c *StateEpochCoordinator) reconcileOperation(ctx context.Context, op sqlc.RuntimeControlOperation) (StateEpochEnsureResult, error) {
	transition, canonicalTransition, err := validateEpochOperation(op)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	_ = canonicalTransition
	if op.State == "awaiting_release" {
		return c.validateAwaitingRelease(ctx, op, transition, op.RecoveryEvidence)
	}

	marker, err := c.store.StateIntegrity(ctx)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	expected, shouldCAS, err := classifyExpectedMarker(marker, op, transition)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if shouldCAS {
		op, err = c.compareAndSetExpectedMarker(ctx, op, expected)
		if err != nil {
			return StateEpochEnsureResult{}, err
		}
	}
	if !op.ExpectedMarkerHash.Valid || op.ExpectedMarkerHash.String == "" {
		return StateEpochEnsureResult{}, epochConflict("epoch operation has no classified expected marker")
	}

	fence := stateEpochFence(op, transition)
	prepareResult, err := c.store.RecoverRuntimeStateEpochFence(ctx, fence)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if prepareResult == breakerstore.StateEpochConflict {
		return StateEpochEnsureResult{}, epochConflict("redis marker changed while preparing epoch fence")
	}

	q := sqlc.New(c.db)
	if prepareResult == breakerstore.StateEpochNewReadyObserved {
		fresh, loadErr := q.GetRuntimeControlOperationByToken(ctx, op.Token)
		if loadErr != nil {
			return StateEpochEnsureResult{}, wrapEpochStoreError(loadErr, "reload new-ready epoch operation")
		}
		if fresh.State != "db_committed" && fresh.State != "awaiting_release" && fresh.State != "committed" {
			return StateEpochEnsureResult{}, epochConflict("new ready marker observed before durable db_committed")
		}
		return c.finalizeReady(ctx, fresh, transition)
	}

	if op.State == "preparing" {
		rows, markErr := q.MarkRuntimeControlOperationPrepared(ctx, sqlc.MarkRuntimeControlOperationPreparedParams{
			Token: op.Token, PayloadHash: op.PayloadHash,
		})
		if markErr != nil {
			return StateEpochEnsureResult{}, wrapEpochStoreError(markErr, "mark epoch operation prepared")
		}
		if rows != 1 {
			fresh, loadErr := q.GetRuntimeControlOperationByToken(ctx, op.Token)
			if loadErr != nil || (fresh.State != "prepared" && fresh.State != "db_committed") {
				return StateEpochEnsureResult{}, epochConflict("epoch operation did not reach prepared")
			}
			op = fresh
		} else {
			op.State = "prepared"
		}
	}

	if op.State == "prepared" {
		if err := c.advanceDatabase(ctx, op, transition); err != nil {
			return StateEpochEnsureResult{}, err
		}
	}
	op, err = q.GetRuntimeControlOperationByToken(ctx, op.Token)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "reload db-committed epoch operation")
	}
	if op.State == "committed" {
		row, rowErr := q.GetAppSettingRecord(ctx, RuntimeStateEpochKey)
		if rowErr != nil {
			return StateEpochEnsureResult{}, wrapEpochStoreError(rowErr, "read committed epoch")
		}
		record, decodeErr := decodeStateEpochRecord(row.Value, row.Revision)
		return StateEpochEnsureResult{State: StateEpochEnsureReady, Record: record, OperationToken: op.Token}, decodeErr
	}
	if op.State != "db_committed" {
		return StateEpochEnsureResult{}, epochConflict("epoch operation failed to reach db_committed")
	}

	// Bootstrap 是无外部流量时的安全例外，可在 not_before 到达后自动 Commit。
	// state_loss/restore 必须由维护 use-case 完成 drain/window/permission/evidence 授权，
	// 启动 coordinator 只恢复 pending fence，不擅自放流。
	if transition.Reason != StateEpochReasonBootstrap || c.now().Before(transition.NotBefore) {
		row, rowErr := q.GetAppSettingRecord(ctx, RuntimeStateEpochKey)
		if rowErr != nil {
			return StateEpochEnsureResult{}, wrapEpochStoreError(rowErr, "read recovering epoch")
		}
		record, decodeErr := decodeStateEpochRecord(row.Value, row.Revision)
		return StateEpochEnsureResult{
			State: StateEpochEnsureAwaitingMaintenance, Record: record, OperationToken: op.Token,
		}, decodeErr
	}

	committed, err := c.store.CommitRuntimeStateEpoch(ctx, fence)
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	if !committed {
		return StateEpochEnsureResult{}, epochConflict("redis epoch commit conflicted")
	}
	return c.finalizeReady(ctx, op, transition)
}

func (c *StateEpochCoordinator) compareAndSetExpectedMarker(ctx context.Context, op sqlc.RuntimeControlOperation, next string) (sqlc.RuntimeControlOperation, error) {
	q := sqlc.New(c.db)
	rows, err := q.CompareAndSetRuntimeStateEpochExpectedMarkerHash(ctx, sqlc.CompareAndSetRuntimeStateEpochExpectedMarkerHashParams{
		NextExpectedMarkerHash:    pgtype.Text{String: next, Valid: true},
		Token:                     op.Token,
		PayloadHash:               op.PayloadHash,
		CurrentExpectedMarkerHash: op.ExpectedMarkerHash,
	})
	if err != nil {
		return sqlc.RuntimeControlOperation{}, wrapEpochStoreError(err, "record expected state integrity marker")
	}
	if rows != 1 {
		fresh, loadErr := q.GetRuntimeControlOperationByToken(ctx, op.Token)
		if loadErr != nil || !fresh.ExpectedMarkerHash.Valid || fresh.ExpectedMarkerHash.String != next {
			return sqlc.RuntimeControlOperation{}, epochConflict("expected marker changed concurrently")
		}
		return fresh, nil
	}
	op.ExpectedMarkerHash = pgtype.Text{String: next, Valid: true}
	return op, nil
}

func (c *StateEpochCoordinator) advanceDatabase(ctx context.Context, op sqlc.RuntimeControlOperation, transition StateEpochTransition) error {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return wrapEpochStoreError(err, "begin epoch db-commit transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	q := sqlc.New(tx)
	row, err := q.GetAppSettingRecordForUpdate(ctx, RuntimeStateEpochKey)
	if err != nil {
		return wrapEpochStoreError(err, "lock state epoch row")
	}
	lockedOp, err := q.GetRuntimeControlOperationByTokenForUpdate(ctx, op.Token)
	if err != nil {
		return wrapEpochStoreError(err, "lock state epoch operation")
	}
	if lockedOp.State == "db_committed" || lockedOp.State == "committed" {
		return tx.Commit(ctx)
	}
	if lockedOp.State != "prepared" || lockedOp.PayloadHash != op.PayloadHash {
		return epochConflict("locked epoch operation is not prepared")
	}
	current, err := DecodeStateEpoch(row.Value)
	if err != nil {
		return epochConflict("locked state epoch payload is invalid")
	}

	if transition.Reason == StateEpochReasonBootstrap {
		if row.Revision != 1 || current.Epoch != transition.NewEpoch || current.State != StateEpochRecovering || current.Reason != StateEpochReasonBootstrap {
			return epochConflict("bootstrap epoch row no longer matches durable transition")
		}
	} else {
		oldEpoch, oldRevision := transition.OldIdentity()
		if row.Revision != oldRevision || current.Epoch != oldEpoch || current.State != StateEpochReady {
			return epochConflict("old ready epoch no longer matches durable transition")
		}
		nextRaw, marshalErr := (StateEpoch{
			Epoch: transition.NewEpoch, State: StateEpochRecovering, Reason: transition.Reason,
		}).Marshal()
		if marshalErr != nil {
			return marshalErr
		}
		rows, updateErr := q.AdvanceRuntimeStateEpochRecovering(ctx, sqlc.AdvanceRuntimeStateEpochRecoveringParams{
			NextValue: nextRaw, NextRevision: transition.NewRevision,
			CurrentRevision: oldRevision, CurrentValue: row.Value,
		})
		if updateErr != nil {
			return wrapEpochStoreError(updateErr, "advance state epoch to recovering")
		}
		if rows != 1 {
			return epochConflict("state epoch recovering CAS failed")
		}
	}

	rows, err := q.MarkRuntimeControlOperationDBCommitted(ctx, sqlc.MarkRuntimeControlOperationDBCommittedParams{
		Token: op.Token, PayloadHash: op.PayloadHash,
	})
	if err != nil {
		return wrapEpochStoreError(err, "mark epoch operation db_committed")
	}
	if rows != 1 {
		return epochConflict("epoch operation db_committed CAS failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return wrapEpochStoreError(err, "commit epoch db-commit transaction")
	}
	return nil
}

func (c *StateEpochCoordinator) finalizeReady(ctx context.Context, op sqlc.RuntimeControlOperation, transition StateEpochTransition) (StateEpochEnsureResult, error) {
	tx, err := c.db.Begin(ctx)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "begin epoch finalize transaction")
	}
	defer func() { _ = tx.Rollback(ctx) }()
	return c.finalizeReadyTx(ctx, tx, op, transition)
}

func (c *StateEpochCoordinator) finalizeReadyTx(
	ctx context.Context,
	tx pgx.Tx,
	op sqlc.RuntimeControlOperation,
	transition StateEpochTransition,
) (StateEpochEnsureResult, error) {
	q := sqlc.New(tx)
	row, err := q.GetAppSettingRecordForUpdate(ctx, RuntimeStateEpochKey)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "lock recovering epoch row")
	}
	lockedOp, err := q.GetRuntimeControlOperationByTokenForUpdate(ctx, op.Token)
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "lock db-committed epoch operation")
	}
	current, err := DecodeStateEpoch(row.Value)
	if err != nil {
		return StateEpochEnsureResult{}, epochConflict("recovering epoch payload is invalid")
	}
	if transition.Reason != StateEpochReasonBootstrap {
		evidence, evidenceErr := DecodeStateEpochRecoveryEvidence(lockedOp.RecoveryEvidence)
		if evidenceErr != nil || evidence.ValidateDurableBinding(transition, evidence.OperatorRef) != nil {
			return StateEpochEnsureResult{}, epochConflict("approved recovery evidence is required before epoch finalize")
		}
	}
	if (lockedOp.State == "awaiting_release" || lockedOp.State == "committed") && current.State == StateEpochReady &&
		current.Epoch == transition.NewEpoch && row.Revision == transition.NewRevision {
		if err := tx.Commit(ctx); err != nil {
			return StateEpochEnsureResult{}, wrapEpochStoreError(err, "commit idempotent epoch finalize")
		}
		state := StateEpochEnsureReady
		if lockedOp.State == "awaiting_release" {
			state = StateEpochEnsureAwaitingRelease
		}
		return StateEpochEnsureResult{
			State:          state,
			Record:         StateEpochRecord{Value: current, Revision: row.Revision},
			OperationToken: op.Token,
		}, nil
	}
	if lockedOp.State != "db_committed" || lockedOp.PayloadHash != op.PayloadHash ||
		row.Revision != transition.NewRevision || current.Epoch != transition.NewEpoch || current.State != StateEpochRecovering {
		return StateEpochEnsureResult{}, epochConflict("epoch finalize durable state mismatch")
	}
	ready, err := current.ReadyAt(c.now())
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	readyRaw, err := ready.Marshal()
	if err != nil {
		return StateEpochEnsureResult{}, err
	}
	rows, err := q.MarkRuntimeStateEpochReady(ctx, sqlc.MarkRuntimeStateEpochReadyParams{
		ReadyValue: readyRaw, Revision: transition.NewRevision, RecoveringValue: row.Value,
	})
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "mark state epoch ready")
	}
	if rows != 1 {
		return StateEpochEnsureResult{}, epochConflict("state epoch ready CAS failed")
	}
	state := StateEpochEnsureReady
	if transition.Reason == StateEpochReasonBootstrap {
		rows, err = q.MarkRuntimeControlOperationCommitted(ctx, sqlc.MarkRuntimeControlOperationCommittedParams{
			Token: op.Token, PayloadHash: op.PayloadHash,
		})
	} else {
		rows, err = q.MarkRuntimeStateEpochAwaitingRelease(ctx, sqlc.MarkRuntimeStateEpochAwaitingReleaseParams{
			Token: op.Token, PayloadHash: op.PayloadHash,
		})
		state = StateEpochEnsureAwaitingRelease
	}
	if err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "mark epoch operation ready for release")
	}
	if rows != 1 {
		return StateEpochEnsureResult{}, epochConflict("epoch operation release-state CAS failed")
	}
	if err := tx.Commit(ctx); err != nil {
		return StateEpochEnsureResult{}, wrapEpochStoreError(err, "commit epoch finalize transaction")
	}
	return StateEpochEnsureResult{
		State:          state,
		Record:         StateEpochRecord{Value: ready, Revision: transition.NewRevision},
		OperationToken: op.Token,
	}, nil
}

func validateEpochOperation(op sqlc.RuntimeControlOperation) (StateEpochTransition, []byte, error) {
	if op.Kind != "runtime_state_epoch" || !op.SettingKey.Valid || op.SettingKey.String != RuntimeStateEpochKey || op.ChannelID.Valid {
		return StateEpochTransition{}, nil, epochConflict("invalid runtime state epoch operation target")
	}
	transition, err := DecodeStateEpochTransition(op.EpochTransition)
	if err != nil {
		return StateEpochTransition{}, nil, epochConflict("invalid immutable state epoch transition")
	}
	canonical, err := transition.Marshal()
	if err != nil {
		return StateEpochTransition{}, nil, err
	}
	if breakerstore.HashPayload(string(canonical)) != op.PayloadHash {
		return StateEpochTransition{}, nil, epochConflict("state epoch transition hash mismatch")
	}
	oldEpoch, oldRevision := transition.OldIdentity()
	_ = oldEpoch
	if op.CurrentRevision != oldRevision || op.NextRevision != transition.NewRevision {
		return StateEpochTransition{}, nil, epochConflict("state epoch operation revision mismatch")
	}
	return transition, canonical, nil
}

func classifyExpectedMarker(marker breakerstore.StateIntegritySnapshot, op sqlc.RuntimeControlOperation, transition StateEpochTransition) (string, bool, error) {
	oldEpoch, oldRevision := transition.OldIdentity()
	if !marker.Exists {
		return breakerstore.StateEpochExpectedMarkerAbsent,
			!op.ExpectedMarkerHash.Valid || op.ExpectedMarkerHash.String != breakerstore.StateEpochExpectedMarkerAbsent, nil
	}
	if oldEpoch != "" && marker.Ready(oldEpoch, oldRevision) {
		return marker.MarkerHash, !op.ExpectedMarkerHash.Valid || op.ExpectedMarkerHash.String != marker.MarkerHash, nil
	}
	if marker.State == "pending" && marker.OperationToken == op.Token && marker.TransitionHash == op.PayloadHash &&
		marker.NewEpoch == transition.NewEpoch && marker.NewRevision == transition.NewRevision {
		if !op.ExpectedMarkerHash.Valid || op.ExpectedMarkerHash.String == "" || marker.ExpectedMarkerHash != op.ExpectedMarkerHash.String {
			return "", false, epochConflict("same-operation pending marker disagrees with durable expected marker")
		}
		return op.ExpectedMarkerHash.String, false, nil
	}
	if marker.State == "ready" && marker.Epoch == transition.NewEpoch && marker.Revision == transition.NewRevision &&
		marker.MarkerHash == breakerstore.StateIntegrityReadyMarkerHash(transition.NewEpoch, transition.NewRevision) &&
		marker.LastOperationToken == op.Token && marker.LastTransitionHash == op.PayloadHash {
		if !op.ExpectedMarkerHash.Valid || op.ExpectedMarkerHash.String == "" {
			return "", false, epochConflict("same-operation new ready marker has no durable expected marker")
		}
		return op.ExpectedMarkerHash.String, false, nil
	}
	return "", false, epochConflict("observed marker is not absent, durable old ready, same pending, or same new ready")
}

func decodeStateEpochRecord(raw []byte, revision int64) (StateEpochRecord, error) {
	if revision < 1 {
		return StateEpochRecord{}, epochConflict("state epoch revision is invalid")
	}
	value, err := DecodeStateEpoch(raw)
	if err != nil {
		return StateEpochRecord{}, epochConflict("state epoch payload is invalid")
	}
	return StateEpochRecord{Value: value, Revision: revision}, nil
}

func epochConflict(message string) error {
	return failure.Wrap(
		failure.CodeGatewayRuntimeStateLost,
		fmt.Errorf("%w: %s", ErrStateEpochConflict, message),
		failure.WithMessage(message),
	)
}

func wrapEpochStoreError(err error, operation string) error {
	return failure.Wrap(
		failure.CodeDependencyPostgresUnavailable,
		err,
		failure.WithMessage("runtimecontrol: "+operation),
	)
}
