package runtimecontrol

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type OriginRoutingRecoveryAPI interface {
	RecoverOriginRouting(ctx context.Context, in breakerstore.OriginRoutingRecovery) (breakerstore.FenceResult, error)
	RestoreMissingOriginControl(ctx context.Context, originID, baseURLRevision, statusRevision int64, effectiveStatus string) (bool, error)
	Snapshot(ctx context.Context, scope breakerstore.Scope, id int64) (breakerstore.ScopeSnapshot, error)
}

// OriginReconcileObserver receives validated control facts after recovery. Implementations must
// not persist sensitive origin data; only business IDs, revisions and bounded status are exposed.
type OriginReconcileObserver interface {
	OriginControlReconciled(originID, baseURLRevision, statusRevision int64, effectiveStatus string, restored bool)
}

// OriginRoutingReconciler resolves origin_routing_operations and restores isolated missing
// Origin controls from locked PostgreSQL facts. It never overwrites a present control.
type OriginRoutingReconciler struct {
	pool     *pgxpool.Pool
	control  OriginRoutingRecoveryAPI
	observer OriginReconcileObserver
}

// WithObserver attaches optional recovery observability without changing reconciliation semantics.
func (r *OriginRoutingReconciler) WithObserver(observer OriginReconcileObserver) *OriginRoutingReconciler {
	r.observer = observer
	return r
}

func NewOriginRoutingReconciler(pool *pgxpool.Pool, control OriginRoutingRecoveryAPI) *OriginRoutingReconciler {
	if pool == nil || control == nil {
		panic("runtimecontrol: origin routing reconciler requires pool and control")
	}
	return &OriginRoutingReconciler{pool: pool, control: control}
}

// Reconcile first terminates every durable non-terminal operation, then verifies/restores every
// stable Origin control. A conflict stops the pass and leaves admission fail-closed.
func (r *OriginRoutingReconciler) Reconcile(ctx context.Context) (int, error) {
	ops, err := sqlc.New(r.pool).ListNonterminalOriginRoutingOperations(ctx)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, op := range ops {
		changed, err := r.reconcileOperation(ctx, op)
		if err != nil {
			return handled, err
		}
		if changed {
			handled++
		}
	}
	if err := r.restoreStableControls(ctx); err != nil {
		return handled, err
	}
	return handled, nil
}

func (r *OriginRoutingReconciler) reconcileOperation(ctx context.Context, listed sqlc.OriginRoutingOperation) (bool, error) {
	envelope, err := ParseOriginRoutingEnvelope(listed.Transitions, 1024)
	if err != nil {
		return false, fmt.Errorf("runtimecontrol: operation %s has invalid transitions: %w", listed.Token, err)
	}
	if !listed.ProviderID.Valid || listed.ProviderID.Int64 != envelope.ProviderID {
		return false, fmt.Errorf("runtimecontrol: operation %s provider conflicts with transitions", listed.Token)
	}
	originIDs := originIDsOf(envelope.Transitions)
	if envelope.Kind == OriginFenceKindProviderStatusBatch {
		if listed.OriginID.Valid {
			return false, fmt.Errorf("runtimecontrol: provider batch %s has origin_id", listed.Token)
		}
	} else if !listed.OriginID.Valid || len(originIDs) != 1 || listed.OriginID.Int64 != originIDs[0] {
		return false, fmt.Errorf("runtimecontrol: operation %s origin conflicts with transitions", listed.Token)
	}

	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	var providerStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM providers WHERE id=$1 FOR UPDATE`, envelope.ProviderID).Scan(&providerStatus); err != nil {
		return false, err
	}
	originRows, err := lockOriginRecoveryRows(ctx, tx, originIDs)
	if err != nil {
		return false, err
	}
	locked, err := lockOriginOperation(ctx, tx, listed.Token)
	if err != nil {
		return false, err
	}
	if locked.State == "committed" || locked.State == "aborted" {
		if err := tx.Commit(ctx); err != nil {
			return false, err
		}
		return false, nil
	}
	if locked.Kind != listed.Kind || locked.PayloadHash != listed.PayloadHash ||
		!sameOriginTransitionJSON(locked.Transitions, listed.Transitions) || !sameNullableOriginTarget(locked, listed) {
		return false, fmt.Errorf("runtimecontrol: operation %s changed during recovery", listed.Token)
	}

	mode := breakerstore.OriginRecoveryAborted
	expectedProviderStatus := envelope.CurrentProviderStatus
	switch locked.State {
	case "preparing", "prepared":
	case "db_committed":
		mode = breakerstore.OriginRecoveryCommitted
		expectedProviderStatus = envelope.NextProviderStatus
	default:
		return false, fmt.Errorf("runtimecontrol: unsupported origin operation state %q", locked.State)
	}
	if providerStatus != expectedProviderStatus {
		return false, fmt.Errorf("runtimecontrol: provider status does not match operation %s", listed.Token)
	}

	recoveryTransitions := make([]breakerstore.OriginRoutingRecoveryTransition, 0, len(envelope.Transitions))
	for i, transition := range envelope.Transitions {
		row := originRows[i]
		effective := EffectiveOriginStatus(providerStatus, row.status)
		expectedBase := transition.CurrentBaseURLRevision
		expectedStatus := transition.CurrentStatusRevision
		expectedEffective := transition.CurrentEffectiveStatus
		if mode == breakerstore.OriginRecoveryCommitted {
			expectedBase = transition.NextBaseURLRevision
			expectedStatus = transition.NextStatusRevision
			expectedEffective = transition.NextEffectiveStatus
		}
		if row.baseURLRevision != expectedBase || row.statusRevision != expectedStatus || effective != expectedEffective {
			return false, fmt.Errorf("runtimecontrol: origin %d does not match operation %s business fact", row.id, listed.Token)
		}
		recoveryTransitions = append(recoveryTransitions, breakerstore.OriginRoutingRecoveryTransition{
			OriginID:        row.id,
			CurrentBaseURLRev: transition.CurrentBaseURLRevision,
			NextBaseURLRev:    transition.NextBaseURLRevision,
			CurrentStatusRev:  transition.CurrentStatusRevision,
			NextStatusRev:     transition.NextStatusRevision,
			CurrentEffective:  transition.CurrentEffectiveStatus,
			NextEffective:     transition.NextEffectiveStatus,
			FactBaseURLRev:    row.baseURLRevision,
			FactStatusRev:     row.statusRevision,
			FactEffective:     effective,
		})
	}
	if err := verifyRecoverablePayloadHash(envelope, originRows, mode, locked.PayloadHash); err != nil {
		return false, fmt.Errorf("runtimecontrol: operation %s payload: %w", listed.Token, err)
	}
	result, err := r.control.RecoverOriginRouting(ctx, breakerstore.OriginRoutingRecovery{
		Mode: mode, Kind: envelope.Kind, ProviderID: envelope.ProviderID,
		Token: locked.Token, PayloadHash: locked.PayloadHash, Transitions: recoveryTransitions,
	})
	if err != nil {
		return false, err
	}
	if string(result) != string(mode) {
		return false, fmt.Errorf("runtimecontrol: origin recovery rejected operation %s (%s)", listed.Token, result)
	}
	queries := sqlc.New(tx)
	var affected int64
	if mode == breakerstore.OriginRecoveryCommitted {
		affected, err = queries.MarkOriginRoutingOperationCommitted(ctx, sqlc.MarkOriginRoutingOperationCommittedParams{
			Token: locked.Token, PayloadHash: locked.PayloadHash,
		})
	} else {
		affected, err = queries.MarkOriginRoutingOperationAborted(ctx, sqlc.MarkOriginRoutingOperationAbortedParams{
			Token: locked.Token, PayloadHash: locked.PayloadHash,
		})
	}
	if err != nil || affected != 1 {
		return false, fmt.Errorf("runtimecontrol: operation %s terminal CAS failed: %w", listed.Token, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return false, err
	}
	return true, nil
}

type originRecoveryRow struct {
	id              int64
	baseURL         string
	baseURLRevision int64
	status          string
	statusRevision  int64
}

type originRestoreFact struct {
	providerID     int64
	providerStatus string
	originID     int64
	baseRevision   int64
	statusRevision int64
	originStatus string
}

func lockOriginRecoveryRows(ctx context.Context, tx pgx.Tx, originIDs []int64) ([]originRecoveryRow, error) {
	rows, err := tx.Query(ctx, `SELECT id, base_url, base_url_revision, status, status_revision
		FROM provider_origins WHERE id = ANY($1::bigint[]) ORDER BY id FOR UPDATE`, originIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]originRecoveryRow, 0, len(originIDs))
	for rows.Next() {
		var row originRecoveryRow
		if err := rows.Scan(&row.id, &row.baseURL, &row.baseURLRevision, &row.status, &row.statusRevision); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) != len(originIDs) {
		return nil, fmt.Errorf("runtimecontrol: origin recovery target set changed")
	}
	for i, id := range originIDs {
		if out[i].id != id {
			return nil, fmt.Errorf("runtimecontrol: origin recovery target order changed")
		}
	}
	return out, nil
}

func lockOriginOperation(ctx context.Context, tx pgx.Tx, token string) (sqlc.OriginRoutingOperation, error) {
	var op sqlc.OriginRoutingOperation
	err := tx.QueryRow(ctx, `SELECT id, token, kind, provider_id, origin_id, transitions, payload_hash,
		state, created_at, updated_at, completed_at FROM origin_routing_operations WHERE token=$1 FOR UPDATE`, token).
		Scan(&op.ID, &op.Token, &op.Kind, &op.ProviderID, &op.OriginID, &op.Transitions, &op.PayloadHash,
			&op.State, &op.CreatedAt, &op.UpdatedAt, &op.CompletedAt)
	return op, err
}

func sameNullableOriginTarget(a, b sqlc.OriginRoutingOperation) bool {
	return a.ProviderID == b.ProviderID && a.OriginID == b.OriginID
}

func verifyRecoverablePayloadHash(envelope OriginRoutingEnvelope, rows []originRecoveryRow, mode breakerstore.OriginRoutingRecoveryMode, want string) error {
	if mode == breakerstore.OriginRecoveryAborted &&
		(envelope.Kind == OriginFenceKindBaseURL || envelope.Kind == OriginFenceKindBaseURLStatus) {
		// The uncommitted target URL is intentionally absent from durable transitions. Redis still checks
		// the durable hash against its pending operation before Abort.
		return nil
	}
	nextBaseURL := ""
	if envelope.Kind == OriginFenceKindBaseURL || envelope.Kind == OriginFenceKindBaseURLStatus {
		nextBaseURL = rows[0].baseURL
	}
	_, payload, err := CanonicalOriginRoutingOperation(envelope, nextBaseURL, 1024)
	if err != nil {
		return err
	}
	if breakerstore.HashPayload(payload) != want {
		return fmt.Errorf("canonical payload hash mismatch")
	}
	return nil
}

func (r *OriginRoutingReconciler) restoreStableControls(ctx context.Context) error {
	rows, err := r.pool.Query(ctx, `SELECT p.id, p.status, pe.id, pe.base_url_revision, pe.status_revision, pe.status
		FROM providers p JOIN provider_origins pe ON pe.provider_id=p.id ORDER BY p.id, pe.id`)
	if err != nil {
		return err
	}
	facts := make([]originRestoreFact, 0)
	for rows.Next() {
		var fact originRestoreFact
		if err := rows.Scan(&fact.providerID, &fact.providerStatus, &fact.originID,
			&fact.baseRevision, &fact.statusRevision, &fact.originStatus); err != nil {
			rows.Close()
			return err
		}
		facts = append(facts, fact)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	for i := 0; i < len(facts); {
		j := i + 1
		for j < len(facts) && facts[j].providerID == facts[i].providerID {
			j++
		}
		if err := r.restoreProviderControls(ctx, facts[i:j]); err != nil {
			return err
		}
		i = j
	}
	return nil
}

func (r *OriginRoutingReconciler) restoreProviderControls(ctx context.Context, facts []originRestoreFact) error {
	if len(facts) == 0 {
		return nil
	}
	tx, err := r.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var providerStatus string
	if err := tx.QueryRow(ctx, `SELECT status FROM providers WHERE id=$1 FOR UPDATE`, facts[0].providerID).Scan(&providerStatus); err != nil {
		return err
	}
	if providerStatus != facts[0].providerStatus {
		return fmt.Errorf("runtimecontrol: provider %d changed during origin restore", facts[0].providerID)
	}
	originIDs := make([]int64, len(facts))
	for i, fact := range facts {
		originIDs[i] = fact.originID
	}
	locked, err := lockOriginRecoveryRows(ctx, tx, originIDs)
	if err != nil {
		return err
	}
	for i, fact := range facts {
		row := locked[i]
		if row.baseURLRevision != fact.baseRevision || row.statusRevision != fact.statusRevision || row.status != fact.originStatus {
			return fmt.Errorf("runtimecontrol: origin %d changed during control restore", fact.originID)
		}
		effective := EffectiveOriginStatus(providerStatus, row.status)
		restored, err := r.control.RestoreMissingOriginControl(ctx, row.id, row.baseURLRevision, row.statusRevision, effective)
		if err != nil {
			return err
		}
		snapshot, err := r.control.Snapshot(ctx, breakerstore.ScopeOrigin, row.id)
		if err != nil {
			return err
		}
		if !snapshot.Exists || !snapshot.ControlPresent || snapshot.BaseURLRevisionState != "active" ||
			snapshot.StatusRevisionState != "active" || snapshot.PendingBaseURLRevision != 0 || snapshot.PendingStatusRevision != 0 ||
			snapshot.BaseURLRevision != row.baseURLRevision || snapshot.StatusRevision != row.statusRevision ||
			snapshot.EffectiveStatus != effective {
			return fmt.Errorf("runtimecontrol: origin %d control conflicts with PostgreSQL", row.id)
		}
		if r.observer != nil {
			r.observer.OriginControlReconciled(
				row.id, row.baseURLRevision, row.statusRevision, effective, restored,
			)
		}
	}
	return tx.Commit(ctx)
}

// CleanupTerminal removes only bounded, already-terminal durable operations.
func (r *OriginRoutingReconciler) CleanupTerminal(ctx context.Context, now time.Time) (int64, error) {
	cutoff := pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true}
	return sqlc.New(r.pool).DeleteTerminalOriginRoutingOperationsBefore(ctx, cutoff)
}
