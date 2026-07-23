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

type EndpointRoutingRecoveryAPI interface {
	RecoverEndpointRouting(ctx context.Context, in breakerstore.EndpointRoutingRecovery) (breakerstore.FenceResult, error)
	RestoreMissingEndpointControl(ctx context.Context, endpointID, baseURLRevision, statusRevision int64, effectiveStatus string) (bool, error)
	Snapshot(ctx context.Context, scope breakerstore.Scope, id int64) (breakerstore.ScopeSnapshot, error)
}

// EndpointReconcileObserver receives validated control facts after recovery. Implementations must
// not persist sensitive endpoint data; only business IDs, revisions and bounded status are exposed.
type EndpointReconcileObserver interface {
	EndpointControlReconciled(endpointID, baseURLRevision, statusRevision int64, effectiveStatus string, restored bool)
}

// EndpointRoutingReconciler resolves endpoint_routing_operations and restores isolated missing
// Endpoint controls from locked PostgreSQL facts. It never overwrites a present control.
type EndpointRoutingReconciler struct {
	pool     *pgxpool.Pool
	control  EndpointRoutingRecoveryAPI
	observer EndpointReconcileObserver
}

// WithObserver attaches optional recovery observability without changing reconciliation semantics.
func (r *EndpointRoutingReconciler) WithObserver(observer EndpointReconcileObserver) *EndpointRoutingReconciler {
	r.observer = observer
	return r
}

func NewEndpointRoutingReconciler(pool *pgxpool.Pool, control EndpointRoutingRecoveryAPI) *EndpointRoutingReconciler {
	if pool == nil || control == nil {
		panic("runtimecontrol: endpoint routing reconciler requires pool and control")
	}
	return &EndpointRoutingReconciler{pool: pool, control: control}
}

// Reconcile first terminates every durable non-terminal operation, then verifies/restores every
// stable Endpoint control. A conflict stops the pass and leaves admission fail-closed.
func (r *EndpointRoutingReconciler) Reconcile(ctx context.Context) (int, error) {
	ops, err := sqlc.New(r.pool).ListNonterminalEndpointRoutingOperations(ctx)
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

func (r *EndpointRoutingReconciler) reconcileOperation(ctx context.Context, listed sqlc.EndpointRoutingOperation) (bool, error) {
	envelope, err := ParseEndpointRoutingEnvelope(listed.Transitions, 1024)
	if err != nil {
		return false, fmt.Errorf("runtimecontrol: operation %s has invalid transitions: %w", listed.Token, err)
	}
	if !listed.ProviderID.Valid || listed.ProviderID.Int64 != envelope.ProviderID {
		return false, fmt.Errorf("runtimecontrol: operation %s provider conflicts with transitions", listed.Token)
	}
	endpointIDs := endpointIDsOf(envelope.Transitions)
	if envelope.Kind == EndpointFenceKindProviderStatusBatch {
		if listed.EndpointID.Valid {
			return false, fmt.Errorf("runtimecontrol: provider batch %s has endpoint_id", listed.Token)
		}
	} else if !listed.EndpointID.Valid || len(endpointIDs) != 1 || listed.EndpointID.Int64 != endpointIDs[0] {
		return false, fmt.Errorf("runtimecontrol: operation %s endpoint conflicts with transitions", listed.Token)
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
	endpointRows, err := lockEndpointRecoveryRows(ctx, tx, endpointIDs)
	if err != nil {
		return false, err
	}
	locked, err := lockEndpointOperation(ctx, tx, listed.Token)
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
		!sameEndpointTransitionJSON(locked.Transitions, listed.Transitions) || !sameNullableEndpointTarget(locked, listed) {
		return false, fmt.Errorf("runtimecontrol: operation %s changed during recovery", listed.Token)
	}

	mode := breakerstore.EndpointRecoveryAborted
	expectedProviderStatus := envelope.CurrentProviderStatus
	switch locked.State {
	case "preparing", "prepared":
	case "db_committed":
		mode = breakerstore.EndpointRecoveryCommitted
		expectedProviderStatus = envelope.NextProviderStatus
	default:
		return false, fmt.Errorf("runtimecontrol: unsupported endpoint operation state %q", locked.State)
	}
	if providerStatus != expectedProviderStatus {
		return false, fmt.Errorf("runtimecontrol: provider status does not match operation %s", listed.Token)
	}

	recoveryTransitions := make([]breakerstore.EndpointRoutingRecoveryTransition, 0, len(envelope.Transitions))
	for i, transition := range envelope.Transitions {
		row := endpointRows[i]
		effective := EffectiveEndpointStatus(providerStatus, row.status)
		expectedBase := transition.CurrentBaseURLRevision
		expectedStatus := transition.CurrentStatusRevision
		expectedEffective := transition.CurrentEffectiveStatus
		if mode == breakerstore.EndpointRecoveryCommitted {
			expectedBase = transition.NextBaseURLRevision
			expectedStatus = transition.NextStatusRevision
			expectedEffective = transition.NextEffectiveStatus
		}
		if row.baseURLRevision != expectedBase || row.statusRevision != expectedStatus || effective != expectedEffective {
			return false, fmt.Errorf("runtimecontrol: endpoint %d does not match operation %s business fact", row.id, listed.Token)
		}
		recoveryTransitions = append(recoveryTransitions, breakerstore.EndpointRoutingRecoveryTransition{
			EndpointID:        row.id,
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
	if err := verifyRecoverablePayloadHash(envelope, endpointRows, mode, locked.PayloadHash); err != nil {
		return false, fmt.Errorf("runtimecontrol: operation %s payload: %w", listed.Token, err)
	}
	result, err := r.control.RecoverEndpointRouting(ctx, breakerstore.EndpointRoutingRecovery{
		Mode: mode, Kind: envelope.Kind, ProviderID: envelope.ProviderID,
		Token: locked.Token, PayloadHash: locked.PayloadHash, Transitions: recoveryTransitions,
	})
	if err != nil {
		return false, err
	}
	if string(result) != string(mode) {
		return false, fmt.Errorf("runtimecontrol: endpoint recovery rejected operation %s (%s)", listed.Token, result)
	}
	queries := sqlc.New(tx)
	var affected int64
	if mode == breakerstore.EndpointRecoveryCommitted {
		affected, err = queries.MarkEndpointRoutingOperationCommitted(ctx, sqlc.MarkEndpointRoutingOperationCommittedParams{
			Token: locked.Token, PayloadHash: locked.PayloadHash,
		})
	} else {
		affected, err = queries.MarkEndpointRoutingOperationAborted(ctx, sqlc.MarkEndpointRoutingOperationAbortedParams{
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

type endpointRecoveryRow struct {
	id              int64
	baseURL         string
	baseURLRevision int64
	status          string
	statusRevision  int64
}

type endpointRestoreFact struct {
	providerID     int64
	providerStatus string
	endpointID     int64
	baseRevision   int64
	statusRevision int64
	endpointStatus string
}

func lockEndpointRecoveryRows(ctx context.Context, tx pgx.Tx, endpointIDs []int64) ([]endpointRecoveryRow, error) {
	rows, err := tx.Query(ctx, `SELECT id, base_url, base_url_revision, status, status_revision
		FROM provider_endpoints WHERE id = ANY($1::bigint[]) ORDER BY id FOR UPDATE`, endpointIDs)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := make([]endpointRecoveryRow, 0, len(endpointIDs))
	for rows.Next() {
		var row endpointRecoveryRow
		if err := rows.Scan(&row.id, &row.baseURL, &row.baseURLRevision, &row.status, &row.statusRevision); err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if len(out) != len(endpointIDs) {
		return nil, fmt.Errorf("runtimecontrol: endpoint recovery target set changed")
	}
	for i, id := range endpointIDs {
		if out[i].id != id {
			return nil, fmt.Errorf("runtimecontrol: endpoint recovery target order changed")
		}
	}
	return out, nil
}

func lockEndpointOperation(ctx context.Context, tx pgx.Tx, token string) (sqlc.EndpointRoutingOperation, error) {
	var op sqlc.EndpointRoutingOperation
	err := tx.QueryRow(ctx, `SELECT id, token, kind, provider_id, endpoint_id, transitions, payload_hash,
		state, created_at, updated_at, completed_at FROM endpoint_routing_operations WHERE token=$1 FOR UPDATE`, token).
		Scan(&op.ID, &op.Token, &op.Kind, &op.ProviderID, &op.EndpointID, &op.Transitions, &op.PayloadHash,
			&op.State, &op.CreatedAt, &op.UpdatedAt, &op.CompletedAt)
	return op, err
}

func sameNullableEndpointTarget(a, b sqlc.EndpointRoutingOperation) bool {
	return a.ProviderID == b.ProviderID && a.EndpointID == b.EndpointID
}

func verifyRecoverablePayloadHash(envelope EndpointRoutingEnvelope, rows []endpointRecoveryRow, mode breakerstore.EndpointRoutingRecoveryMode, want string) error {
	if mode == breakerstore.EndpointRecoveryAborted &&
		(envelope.Kind == EndpointFenceKindBaseURL || envelope.Kind == EndpointFenceKindBaseURLStatus) {
		// The uncommitted target URL is intentionally absent from durable transitions. Redis still checks
		// the durable hash against its pending operation before Abort.
		return nil
	}
	nextBaseURL := ""
	if envelope.Kind == EndpointFenceKindBaseURL || envelope.Kind == EndpointFenceKindBaseURLStatus {
		nextBaseURL = rows[0].baseURL
	}
	_, payload, err := CanonicalEndpointRoutingOperation(envelope, nextBaseURL, 1024)
	if err != nil {
		return err
	}
	if breakerstore.HashPayload(payload) != want {
		return fmt.Errorf("canonical payload hash mismatch")
	}
	return nil
}

func (r *EndpointRoutingReconciler) restoreStableControls(ctx context.Context) error {
	rows, err := r.pool.Query(ctx, `SELECT p.id, p.status, pe.id, pe.base_url_revision, pe.status_revision, pe.status
		FROM providers p JOIN provider_endpoints pe ON pe.provider_id=p.id ORDER BY p.id, pe.id`)
	if err != nil {
		return err
	}
	facts := make([]endpointRestoreFact, 0)
	for rows.Next() {
		var fact endpointRestoreFact
		if err := rows.Scan(&fact.providerID, &fact.providerStatus, &fact.endpointID,
			&fact.baseRevision, &fact.statusRevision, &fact.endpointStatus); err != nil {
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

func (r *EndpointRoutingReconciler) restoreProviderControls(ctx context.Context, facts []endpointRestoreFact) error {
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
		return fmt.Errorf("runtimecontrol: provider %d changed during endpoint restore", facts[0].providerID)
	}
	endpointIDs := make([]int64, len(facts))
	for i, fact := range facts {
		endpointIDs[i] = fact.endpointID
	}
	locked, err := lockEndpointRecoveryRows(ctx, tx, endpointIDs)
	if err != nil {
		return err
	}
	for i, fact := range facts {
		row := locked[i]
		if row.baseURLRevision != fact.baseRevision || row.statusRevision != fact.statusRevision || row.status != fact.endpointStatus {
			return fmt.Errorf("runtimecontrol: endpoint %d changed during control restore", fact.endpointID)
		}
		effective := EffectiveEndpointStatus(providerStatus, row.status)
		restored, err := r.control.RestoreMissingEndpointControl(ctx, row.id, row.baseURLRevision, row.statusRevision, effective)
		if err != nil {
			return err
		}
		snapshot, err := r.control.Snapshot(ctx, breakerstore.ScopeEndpoint, row.id)
		if err != nil {
			return err
		}
		if !snapshot.Exists || !snapshot.ControlPresent || snapshot.BaseURLRevisionState != "active" ||
			snapshot.StatusRevisionState != "active" || snapshot.PendingBaseURLRevision != 0 || snapshot.PendingStatusRevision != 0 ||
			snapshot.BaseURLRevision != row.baseURLRevision || snapshot.StatusRevision != row.statusRevision ||
			snapshot.EffectiveStatus != effective {
			return fmt.Errorf("runtimecontrol: endpoint %d control conflicts with PostgreSQL", row.id)
		}
		if r.observer != nil {
			r.observer.EndpointControlReconciled(
				row.id, row.baseURLRevision, row.statusRevision, effective, restored,
			)
		}
	}
	return tx.Commit(ctx)
}

// CleanupTerminal removes only bounded, already-terminal durable operations.
func (r *EndpointRoutingReconciler) CleanupTerminal(ctx context.Context, now time.Time) (int64, error) {
	cutoff := pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true}
	return sqlc.New(r.pool).DeleteTerminalEndpointRoutingOperationsBefore(ctx, cutoff)
}
