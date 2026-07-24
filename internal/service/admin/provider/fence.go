package provider

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

type providerStatusFenceOps interface {
	PrepareOriginStatusRevisionBatch(ctx context.Context, providerID int64, transitions []breakerstore.OriginStatusRevisionTransition, maxBatch int, token, payload string) (breakerstore.FenceResult, error)
	CommitOriginStatusRevisionBatch(ctx context.Context, providerID int64, transitions []breakerstore.OriginStatusRevisionTransition, token, payload string) (breakerstore.FenceResult, error)
	AbortOriginStatusRevisionBatch(ctx context.Context, providerID int64, transitions []breakerstore.OriginStatusRevisionTransition, token, payload string) (breakerstore.FenceResult, error)
}

type providerStatusFencePublisher interface {
	Publish(ctx context.Context, req runtimecontrol.OriginFenceRequest) (runtimecontrol.PublishResult, error)
	WithOriginLocks(ctx context.Context, providerID int64, originIDs []int64, fn func(context.Context, pgx.Tx) error) error
}

// StatusFencer atomically publishes a Provider effective-status change to all affected Origins.
type StatusFencer struct {
	pub providerStatusFencePublisher
	ops providerStatusFenceOps
}

func NewStatusFencer(pub providerStatusFencePublisher, ops providerStatusFenceOps) *StatusFencer {
	if pub == nil || ops == nil {
		panic("provider: status fencer requires publisher and store")
	}
	return &StatusFencer{pub: pub, ops: ops}
}

type providerStatusChange struct {
	Current              sqlc.Provider
	NextName             string
	NextStatus           string
	Origins            []sqlc.ProviderOrigin
	MaxBatch             int
	ArchiveReplacementID *int64
	Archive              bool
	Restore              bool
}

func (f *StatusFencer) publish(ctx context.Context, change providerStatusChange) (StatusChangeResult, error) {
	transitions := providerOriginTransitions(change)
	result := StatusChangeResult{AffectedOriginCount: len(transitions)}
	allOriginIDs := make([]int64, len(change.Origins))
	for i, origin := range change.Origins {
		allOriginIDs[i] = origin.ID
	}
	if len(transitions) == 0 {
		err := f.pub.WithOriginLocks(ctx, change.Current.ID, allOriginIDs, func(ctx context.Context, tx pgx.Tx) error {
			if err := validateProviderChangeLocked(ctx, tx, change, nil); err != nil {
				return err
			}
			return commitProviderChange(ctx, tx, change, nil)
		})
		if err != nil {
			return StatusChangeResult{}, err
		}
		return result, nil
	}
	if change.MaxBatch < 1 || change.MaxBatch > 1024 || len(transitions) > change.MaxBatch {
		return StatusChangeResult{}, conflict("provider origin status batch is too large")
	}

	routingTransitions := make([]runtimecontrol.OriginRoutingTransition, 0, len(transitions))
	redisTransitions := make([]breakerstore.OriginStatusRevisionTransition, 0, len(transitions))
	for _, transition := range transitions {
		routingTransitions = append(routingTransitions, runtimecontrol.OriginRoutingTransition{
			OriginID:             transition.origin.ID,
			CurrentBaseURLRevision: transition.origin.BaseUrlRevision,
			NextBaseURLRevision:    transition.origin.BaseUrlRevision,
			CurrentStatusRevision:  transition.origin.StatusRevision,
			NextStatusRevision:     transition.origin.StatusRevision + 1,
			CurrentEffectiveStatus: transition.currentEffective,
			NextEffectiveStatus:    transition.nextEffective,
		})
		redisTransitions = append(redisTransitions, breakerstore.OriginStatusRevisionTransition{
			OriginID:          transition.origin.ID,
			CurrentStatusRev:    transition.origin.StatusRevision,
			NextStatusRev:       transition.origin.StatusRevision + 1,
			NextEffectiveStatus: transition.nextEffective,
		})
	}
	envelope := runtimecontrol.OriginRoutingEnvelope{
		Kind:                  runtimecontrol.OriginFenceKindProviderStatusBatch,
		ProviderID:            change.Current.ID,
		CurrentProviderStatus: change.Current.Status,
		NextProviderStatus:    change.NextStatus,
		Transitions:           routingTransitions,
	}
	durable, payload, err := runtimecontrol.CanonicalOriginRoutingOperation(envelope, "", change.MaxBatch)
	if err != nil {
		return StatusChangeResult{}, err
	}
	token := providerFenceToken()
	providerID := change.Current.ID
	published, err := f.pub.Publish(ctx, runtimecontrol.OriginFenceRequest{
		Kind:  runtimecontrol.OriginFenceKindProviderStatusBatch,
		Token: token, ProviderID: &providerID, Transitions: durable, Payload: payload, MaxBatch: change.MaxBatch,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.PrepareOriginStatusRevisionBatch(ctx, providerID, redisTransitions, change.MaxBatch, token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.CommitOriginStatusRevisionBatch(ctx, providerID, redisTransitions, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.AbortOriginStatusRevisionBatch(ctx, providerID, redisTransitions, token, payload)
		},
		ValidateLocked: func(ctx context.Context, tx pgx.Tx) error {
			return validateProviderChangeLocked(ctx, tx, change, transitions)
		},
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			return commitProviderChange(ctx, tx, change, transitions)
		},
	})
	if err != nil {
		return StatusChangeResult{}, err
	}
	result.RuntimeSyncPending = published.State == runtimecontrol.PublishRuntimeSyncPending
	return result, nil
}

type originStatusChange struct {
	origin         sqlc.ProviderOrigin
	currentEffective string
	nextEffective    string
}

func providerOriginTransitions(change providerStatusChange) []originStatusChange {
	out := make([]originStatusChange, 0, len(change.Origins))
	for _, origin := range change.Origins {
		currentEffective := runtimecontrol.EffectiveOriginStatus(change.Current.Status, origin.Status)
		nextOriginStatus := origin.Status
		if change.Archive && origin.Status != StatusArchived {
			nextOriginStatus = StatusArchived
		}
		nextEffective := runtimecontrol.EffectiveOriginStatus(change.NextStatus, nextOriginStatus)
		if currentEffective != nextEffective {
			out = append(out, originStatusChange{origin: origin, currentEffective: currentEffective, nextEffective: nextEffective})
		}
	}
	return out
}

func validateProviderChangeLocked(ctx context.Context, tx pgx.Tx, change providerStatusChange, expected []originStatusChange) error {
	var name, status string
	if err := tx.QueryRow(ctx, `SELECT name, status FROM providers WHERE id=$1`, change.Current.ID).Scan(&name, &status); err != nil {
		return err
	}
	if name != change.Current.Name || status != change.Current.Status {
		return fmt.Errorf("provider changed concurrently")
	}
	rows, err := tx.Query(ctx, `SELECT id, provider_id, name, base_url, base_url_revision, status, status_revision,
		archived_at, created_at, updated_at FROM provider_origins WHERE provider_id=$1 ORDER BY id`, change.Current.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	current := make([]sqlc.ProviderOrigin, 0, len(change.Origins))
	for rows.Next() {
		var origin sqlc.ProviderOrigin
		if err := rows.Scan(&origin.ID, &origin.ProviderID, &origin.Name, &origin.BaseUrl,
			&origin.BaseUrlRevision, &origin.Status, &origin.StatusRevision,
			&origin.ArchivedAt, &origin.CreatedAt, &origin.UpdatedAt); err != nil {
			return err
		}
		current = append(current, origin)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !sameProviderOrigins(current, change.Origins) {
		return fmt.Errorf("provider origin set changed concurrently")
	}
	if expected != nil {
		currentChange := change
		currentChange.Origins = current
		actual := providerOriginTransitions(currentChange)
		if !sameOriginStatusChanges(actual, expected) {
			return fmt.Errorf("provider affected origin set changed concurrently")
		}
	}
	return nil
}

func commitProviderChange(ctx context.Context, tx pgx.Tx, change providerStatusChange, transitions []originStatusChange) error {
	queries := sqlc.New(tx)
	if change.Archive {
		var affected int64
		var err error
		if change.ArchiveReplacementID != nil {
			affected, err = queries.ArchiveProviderWithReplacement(ctx, sqlc.ArchiveProviderWithReplacementParams{
				ID: change.Current.ID, ReplacementChannelID: *change.ArchiveReplacementID,
			})
		} else {
			affected, err = queries.ArchiveProviderCascade(ctx, change.Current.ID)
		}
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("provider archive changed concurrently")
		}
	} else if change.Restore {
		affected, err := queries.RestoreProvider(ctx, change.Current.ID)
		if err != nil {
			return err
		}
		if affected != 1 {
			return fmt.Errorf("provider restore changed concurrently")
		}
	} else {
		command, err := tx.Exec(ctx, `UPDATE providers SET name=$1, status=$2, updated_at=now()
			WHERE id=$3 AND name=$4 AND status=$5`, change.NextName, change.NextStatus,
			change.Current.ID, change.Current.Name, change.Current.Status)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("provider update changed concurrently")
		}
	}
	for _, transition := range transitions {
		nextOriginStatus := transition.origin.Status
		if change.Archive {
			nextOriginStatus = StatusArchived
		}
		command, err := tx.Exec(ctx, `UPDATE provider_origins
			SET status=$1, status_revision=$2,
			    archived_at=CASE WHEN $1::text='archived' THEN now() ELSE archived_at END,
			    updated_at=now()
			WHERE id=$3 AND provider_id=$4 AND base_url_revision=$5 AND status_revision=$6 AND status=$7`,
			nextOriginStatus, transition.origin.StatusRevision+1,
			transition.origin.ID, change.Current.ID, transition.origin.BaseUrlRevision,
			transition.origin.StatusRevision, transition.origin.Status)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("provider origin %d changed concurrently", transition.origin.ID)
		}
	}
	return nil
}

func sameProviderOrigins(a, b []sqlc.ProviderOrigin) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].ID != b[i].ID || a[i].ProviderID != b[i].ProviderID || a[i].BaseUrl != b[i].BaseUrl ||
			a[i].BaseUrlRevision != b[i].BaseUrlRevision || a[i].Status != b[i].Status || a[i].StatusRevision != b[i].StatusRevision {
			return false
		}
	}
	return true
}

func sameOriginStatusChanges(a, b []originStatusChange) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].origin.ID != b[i].origin.ID || a[i].origin.StatusRevision != b[i].origin.StatusRevision ||
			a[i].currentEffective != b[i].currentEffective || a[i].nextEffective != b[i].nextEffective {
			return false
		}
	}
	return true
}

func providerFenceToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("provider: secure token generation failed")
	}
	return "provider-status-" + hex.EncodeToString(b)
}
