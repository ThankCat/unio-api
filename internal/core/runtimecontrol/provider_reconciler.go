package runtimecontrol

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type ProviderRoutingRecoveryAPI interface {
	RestoreMissingProviderControl(context.Context, int64, int64, int64, string) (bool, error)
	ReconcileProviderControl(context.Context, int64, int64, int64, string) (bool, error)
	PrepareOriginRevision(context.Context, int64, int64, int64, string, string) (breakerstore.FenceResult, error)
	CommitOriginRevision(context.Context, int64, string, string) (breakerstore.FenceResult, error)
	AbortOriginRevision(context.Context, int64, string, string) (breakerstore.FenceResult, error)
	PrepareProviderStatusRevision(context.Context, int64, int64, int64, string, string, string) (breakerstore.FenceResult, error)
	CommitProviderStatusRevision(context.Context, int64, string, string) (breakerstore.FenceResult, error)
	AbortProviderStatusRevision(context.Context, int64, string, string) (breakerstore.FenceResult, error)
	Snapshot(context.Context, breakerstore.Scope, int64) (breakerstore.ScopeSnapshot, error)
}

type ProviderReconcileObserver interface {
	ProviderControlReconciled(providerID, originRevision, statusRevision int64, status string, restored bool)
}

type ProviderRoutingReconciler struct {
	pool                       *pgxpool.Pool
	control                    ProviderRoutingRecoveryAPI
	observer                   ProviderReconcileObserver
	authoritativeStableRestore bool
}

func NewProviderRoutingReconciler(pool *pgxpool.Pool, control ProviderRoutingRecoveryAPI) *ProviderRoutingReconciler {
	if pool == nil || control == nil {
		panic("runtimecontrol: provider routing reconciler requires pool and control")
	}
	return &ProviderRoutingReconciler{pool: pool, control: control}
}

func (reconciler *ProviderRoutingReconciler) WithObserver(observer ProviderReconcileObserver) *ProviderRoutingReconciler {
	reconciler.observer = observer
	return reconciler
}

// WithAuthoritativeStableRestore enables startup-only PostgreSQL-authoritative repair after all
// durable Provider operations have been reconciled. Periodic reconciliation must not enable it.
func (reconciler *ProviderRoutingReconciler) WithAuthoritativeStableRestore() *ProviderRoutingReconciler {
	reconciler.authoritativeStableRestore = true
	return reconciler
}

func (reconciler *ProviderRoutingReconciler) Reconcile(ctx context.Context) (int, error) {
	operations, err := sqlc.New(reconciler.pool).ListNonterminalProviderRoutingOperations(ctx)
	if err != nil {
		return 0, err
	}
	handled := 0
	for _, operation := range operations {
		changed, err := reconciler.reconcileOperation(ctx, operation)
		if err != nil {
			return handled, err
		}
		if changed {
			handled++
		}
	}
	if err := reconciler.restoreStableControls(ctx); err != nil {
		return handled, err
	}
	return handled, nil
}

func (reconciler *ProviderRoutingReconciler) reconcileOperation(ctx context.Context, operation sqlc.ProviderRoutingOperation) (bool, error) {
	envelope, err := ParseProviderRoutingEnvelope(operation.Transitions)
	if err != nil || envelope.ProviderID != operation.ProviderID || envelope.Kind != operation.Kind {
		return false, fmt.Errorf("runtimecontrol: provider operation %s has invalid transition", operation.Token)
	}
	var origin string
	var originRevision, statusRevision int64
	var status string
	if err := reconciler.pool.QueryRow(ctx, `SELECT origin, origin_revision, status, status_revision FROM providers WHERE id=$1`, envelope.ProviderID).
		Scan(&origin, &originRevision, &status, &statusRevision); err != nil {
		return false, err
	}
	payload := string(operation.Transitions)
	queries := sqlc.New(reconciler.pool)
	switch operation.State {
	case "preparing", "prepared":
		if operation.State == "prepared" {
			var result breakerstore.FenceResult
			if envelope.Kind == ProviderFenceKindOrigin {
				result, err = reconciler.control.AbortOriginRevision(ctx, envelope.ProviderID, operation.Token, payload)
			} else {
				result, err = reconciler.control.AbortProviderStatusRevision(ctx, envelope.ProviderID, operation.Token, payload)
			}
			if err != nil || (result != "aborted" && result != "conflict") {
				return false, fmt.Errorf("runtimecontrol: abort provider operation %s: %w", operation.Token, err)
			}
		}
		rows, err := queries.MarkProviderRoutingOperationAborted(ctx, sqlc.MarkProviderRoutingOperationAbortedParams{Token: operation.Token, PayloadHash: operation.PayloadHash})
		return rows == 1, err
	case "db_committed":
		if originRevision != envelope.NextOriginRevision || statusRevision != envelope.NextStatusRevision || status != envelope.NextStatus ||
			(envelope.Kind == ProviderFenceKindOrigin && origin != envelope.NextOrigin) {
			return false, fmt.Errorf("runtimecontrol: provider %d does not match committed operation", envelope.ProviderID)
		}
		var result breakerstore.FenceResult
		if envelope.Kind == ProviderFenceKindOrigin {
			result, err = reconciler.control.CommitOriginRevision(ctx, envelope.ProviderID, operation.Token, payload)
		} else {
			result, err = reconciler.control.CommitProviderStatusRevision(ctx, envelope.ProviderID, operation.Token, payload)
		}
		if err != nil || result != "committed" {
			if _, restoreErr := reconciler.control.RestoreMissingProviderControl(ctx, envelope.ProviderID, originRevision, statusRevision, status); restoreErr != nil {
				return false, restoreErr
			}
		}
		rows, err := queries.MarkProviderRoutingOperationCommitted(ctx, sqlc.MarkProviderRoutingOperationCommittedParams{Token: operation.Token, PayloadHash: operation.PayloadHash})
		return rows == 1, err
	default:
		return false, fmt.Errorf("runtimecontrol: unsupported provider operation state %q", operation.State)
	}
}

func (reconciler *ProviderRoutingReconciler) restoreStableControls(ctx context.Context) error {
	rows, err := reconciler.pool.Query(ctx, `SELECT id, origin_revision, status_revision, status FROM providers ORDER BY id`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var providerID, originRevision, statusRevision int64
		var status string
		if err := rows.Scan(&providerID, &originRevision, &statusRevision, &status); err != nil {
			return err
		}
		var restored bool
		if reconciler.authoritativeStableRestore {
			restored, err = reconciler.control.ReconcileProviderControl(ctx, providerID, originRevision, statusRevision, status)
		} else {
			restored, err = reconciler.control.RestoreMissingProviderControl(ctx, providerID, originRevision, statusRevision, status)
		}
		if err != nil {
			return err
		}
		snapshot, err := reconciler.control.Snapshot(ctx, breakerstore.ScopeProvider, providerID)
		if err != nil {
			return err
		}
		if !snapshot.Exists || !snapshot.ControlPresent || snapshot.OriginRevisionState != "active" ||
			snapshot.StatusRevisionState != "active" || snapshot.PendingOriginRevision != 0 || snapshot.PendingStatusRevision != 0 ||
			snapshot.OriginRevision != originRevision || snapshot.StatusRevision != statusRevision || snapshot.EffectiveStatus != status {
			return fmt.Errorf("runtimecontrol: provider %d control conflicts with PostgreSQL", providerID)
		}
		if reconciler.observer != nil {
			reconciler.observer.ProviderControlReconciled(providerID, originRevision, statusRevision, status, restored)
		}
	}
	return rows.Err()
}

func (reconciler *ProviderRoutingReconciler) CleanupTerminal(ctx context.Context, now time.Time) (int64, error) {
	cutoff := pgtype.Timestamptz{Time: now.Add(-24 * time.Hour), Valid: true}
	return sqlc.New(reconciler.pool).DeleteTerminalProviderRoutingOperationsBefore(ctx, cutoff)
}
