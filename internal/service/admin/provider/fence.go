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
	PrepareEndpointStatusRevisionBatch(ctx context.Context, providerID int64, transitions []breakerstore.EndpointStatusRevisionTransition, maxBatch int, token, payload string) (breakerstore.FenceResult, error)
	CommitEndpointStatusRevisionBatch(ctx context.Context, providerID int64, transitions []breakerstore.EndpointStatusRevisionTransition, token, payload string) (breakerstore.FenceResult, error)
	AbortEndpointStatusRevisionBatch(ctx context.Context, providerID int64, transitions []breakerstore.EndpointStatusRevisionTransition, token, payload string) (breakerstore.FenceResult, error)
}

type providerStatusFencePublisher interface {
	Publish(ctx context.Context, req runtimecontrol.EndpointFenceRequest) (runtimecontrol.PublishResult, error)
	WithEndpointLocks(ctx context.Context, providerID int64, endpointIDs []int64, fn func(context.Context, pgx.Tx) error) error
}

// StatusFencer atomically publishes a Provider effective-status change to all affected Endpoints.
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
	Endpoints            []sqlc.ProviderEndpoint
	MaxBatch             int
	ArchiveReplacementID *int64
	Archive              bool
	Restore              bool
}

func (f *StatusFencer) publish(ctx context.Context, change providerStatusChange) (StatusChangeResult, error) {
	transitions := providerEndpointTransitions(change)
	result := StatusChangeResult{AffectedEndpointCount: len(transitions)}
	allEndpointIDs := make([]int64, len(change.Endpoints))
	for i, endpoint := range change.Endpoints {
		allEndpointIDs[i] = endpoint.ID
	}
	if len(transitions) == 0 {
		err := f.pub.WithEndpointLocks(ctx, change.Current.ID, allEndpointIDs, func(ctx context.Context, tx pgx.Tx) error {
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
		return StatusChangeResult{}, conflict("provider endpoint status batch is too large")
	}

	routingTransitions := make([]runtimecontrol.EndpointRoutingTransition, 0, len(transitions))
	redisTransitions := make([]breakerstore.EndpointStatusRevisionTransition, 0, len(transitions))
	for _, transition := range transitions {
		routingTransitions = append(routingTransitions, runtimecontrol.EndpointRoutingTransition{
			EndpointID:             transition.endpoint.ID,
			CurrentBaseURLRevision: transition.endpoint.BaseUrlRevision,
			NextBaseURLRevision:    transition.endpoint.BaseUrlRevision,
			CurrentStatusRevision:  transition.endpoint.StatusRevision,
			NextStatusRevision:     transition.endpoint.StatusRevision + 1,
			CurrentEffectiveStatus: transition.currentEffective,
			NextEffectiveStatus:    transition.nextEffective,
		})
		redisTransitions = append(redisTransitions, breakerstore.EndpointStatusRevisionTransition{
			EndpointID:          transition.endpoint.ID,
			CurrentStatusRev:    transition.endpoint.StatusRevision,
			NextStatusRev:       transition.endpoint.StatusRevision + 1,
			NextEffectiveStatus: transition.nextEffective,
		})
	}
	envelope := runtimecontrol.EndpointRoutingEnvelope{
		Kind:                  runtimecontrol.EndpointFenceKindProviderStatusBatch,
		ProviderID:            change.Current.ID,
		CurrentProviderStatus: change.Current.Status,
		NextProviderStatus:    change.NextStatus,
		Transitions:           routingTransitions,
	}
	durable, payload, err := runtimecontrol.CanonicalEndpointRoutingOperation(envelope, "", change.MaxBatch)
	if err != nil {
		return StatusChangeResult{}, err
	}
	token := providerFenceToken()
	providerID := change.Current.ID
	published, err := f.pub.Publish(ctx, runtimecontrol.EndpointFenceRequest{
		Kind:  runtimecontrol.EndpointFenceKindProviderStatusBatch,
		Token: token, ProviderID: &providerID, Transitions: durable, Payload: payload, MaxBatch: change.MaxBatch,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.PrepareEndpointStatusRevisionBatch(ctx, providerID, redisTransitions, change.MaxBatch, token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.CommitEndpointStatusRevisionBatch(ctx, providerID, redisTransitions, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.AbortEndpointStatusRevisionBatch(ctx, providerID, redisTransitions, token, payload)
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

type endpointStatusChange struct {
	endpoint         sqlc.ProviderEndpoint
	currentEffective string
	nextEffective    string
}

func providerEndpointTransitions(change providerStatusChange) []endpointStatusChange {
	out := make([]endpointStatusChange, 0, len(change.Endpoints))
	for _, endpoint := range change.Endpoints {
		currentEffective := runtimecontrol.EffectiveEndpointStatus(change.Current.Status, endpoint.Status)
		nextEndpointStatus := endpoint.Status
		if change.Archive && endpoint.Status != StatusArchived {
			nextEndpointStatus = StatusArchived
		}
		nextEffective := runtimecontrol.EffectiveEndpointStatus(change.NextStatus, nextEndpointStatus)
		if currentEffective != nextEffective {
			out = append(out, endpointStatusChange{endpoint: endpoint, currentEffective: currentEffective, nextEffective: nextEffective})
		}
	}
	return out
}

func validateProviderChangeLocked(ctx context.Context, tx pgx.Tx, change providerStatusChange, expected []endpointStatusChange) error {
	var name, status string
	if err := tx.QueryRow(ctx, `SELECT name, status FROM providers WHERE id=$1`, change.Current.ID).Scan(&name, &status); err != nil {
		return err
	}
	if name != change.Current.Name || status != change.Current.Status {
		return fmt.Errorf("provider changed concurrently")
	}
	rows, err := tx.Query(ctx, `SELECT id, provider_id, name, base_url, base_url_revision, status, status_revision,
		archived_at, created_at, updated_at FROM provider_endpoints WHERE provider_id=$1 ORDER BY id`, change.Current.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	current := make([]sqlc.ProviderEndpoint, 0, len(change.Endpoints))
	for rows.Next() {
		var endpoint sqlc.ProviderEndpoint
		if err := rows.Scan(&endpoint.ID, &endpoint.ProviderID, &endpoint.Name, &endpoint.BaseUrl,
			&endpoint.BaseUrlRevision, &endpoint.Status, &endpoint.StatusRevision,
			&endpoint.ArchivedAt, &endpoint.CreatedAt, &endpoint.UpdatedAt); err != nil {
			return err
		}
		current = append(current, endpoint)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if !sameProviderEndpoints(current, change.Endpoints) {
		return fmt.Errorf("provider endpoint set changed concurrently")
	}
	if expected != nil {
		currentChange := change
		currentChange.Endpoints = current
		actual := providerEndpointTransitions(currentChange)
		if !sameEndpointStatusChanges(actual, expected) {
			return fmt.Errorf("provider affected endpoint set changed concurrently")
		}
	}
	return nil
}

func commitProviderChange(ctx context.Context, tx pgx.Tx, change providerStatusChange, transitions []endpointStatusChange) error {
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
		nextEndpointStatus := transition.endpoint.Status
		if change.Archive {
			nextEndpointStatus = StatusArchived
		}
		command, err := tx.Exec(ctx, `UPDATE provider_endpoints
			SET status=$1, status_revision=$2,
			    archived_at=CASE WHEN $1::text='archived' THEN now() ELSE archived_at END,
			    updated_at=now()
			WHERE id=$3 AND provider_id=$4 AND base_url_revision=$5 AND status_revision=$6 AND status=$7`,
			nextEndpointStatus, transition.endpoint.StatusRevision+1,
			transition.endpoint.ID, change.Current.ID, transition.endpoint.BaseUrlRevision,
			transition.endpoint.StatusRevision, transition.endpoint.Status)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("provider endpoint %d changed concurrently", transition.endpoint.ID)
		}
	}
	return nil
}

func sameProviderEndpoints(a, b []sqlc.ProviderEndpoint) bool {
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

func sameEndpointStatusChanges(a, b []endpointStatusChange) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i].endpoint.ID != b[i].endpoint.ID || a[i].endpoint.StatusRevision != b[i].endpoint.StatusRevision ||
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
