package providerendpoint

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

// FenceOps is the Redis capability required by single Endpoint updates.
type FenceOps interface {
	PrepareEndpointStatusRevision(ctx context.Context, endpointID, currentStatusRev, nextStatusRev int64, nextEffectiveStatus, token, payload string) (breakerstore.FenceResult, error)
	CommitEndpointStatusRevision(ctx context.Context, endpointID int64, token, payload string) (breakerstore.FenceResult, error)
	AbortEndpointStatusRevision(ctx context.Context, endpointID int64, token, payload string) (breakerstore.FenceResult, error)
	PrepareEndpointBaseURLRevision(ctx context.Context, endpointID, currentBaseURLRev, nextBaseURLRev int64, token, payload string) (breakerstore.FenceResult, error)
	CommitEndpointBaseURLRevision(ctx context.Context, endpointID int64, token, payload string) (breakerstore.FenceResult, error)
	AbortEndpointBaseURLRevision(ctx context.Context, endpointID int64, token, payload string) (breakerstore.FenceResult, error)
	PrepareEndpointRoutingChange(ctx context.Context, change breakerstore.EndpointRoutingChange, token, payload string) (breakerstore.FenceResult, error)
	CommitEndpointRoutingChange(ctx context.Context, endpointID int64, token, payload string) (breakerstore.FenceResult, error)
	AbortEndpointRoutingChange(ctx context.Context, endpointID int64, token, payload string) (breakerstore.FenceResult, error)
}

type FencePublisher interface {
	Publish(ctx context.Context, req runtimecontrol.EndpointFenceRequest) (runtimecontrol.PublishResult, error)
	WithEndpointLocks(ctx context.Context, providerID int64, endpointIDs []int64, fn func(context.Context, pgx.Tx) error) error
}

// EndpointFencer performs BaseURL/status/combined changes through one durable publisher.
type EndpointFencer struct {
	pub FencePublisher
	ops FenceOps
}

func NewEndpointFencer(pub FencePublisher, ops FenceOps) *EndpointFencer {
	if pub == nil || ops == nil {
		panic("providerendpoint: endpoint fencer requires publisher and store")
	}
	return &EndpointFencer{pub: pub, ops: ops}
}

func newToken(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("providerendpoint: secure token generation failed")
	}
	return prefix + hex.EncodeToString(b)
}

type endpointFenceFact struct {
	EndpointID          int64
	ProviderID          int64
	ProviderStatus      string
	BaseURL             string
	BaseURLRevision     int64
	Status              string
	StatusRevision      int64
	EffectiveStatus     string
	NextEffectiveStatus string
}

func (f *EndpointFencer) updateStatusWithoutRevision(ctx context.Context, fact endpointFenceFact, nextStatus string) error {
	return f.pub.WithEndpointLocks(ctx, fact.ProviderID, []int64{fact.EndpointID}, func(ctx context.Context, tx pgx.Tx) error {
		if err := validateEndpointFactLocked(fact)(ctx, tx); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `UPDATE provider_endpoints
			SET status=$1, archived_at=CASE WHEN $1::text='archived' THEN now() ELSE NULL END, updated_at=now()
			WHERE id=$2 AND provider_id=$3 AND base_url_revision=$4 AND status_revision=$5 AND status=$6`,
			nextStatus, fact.EndpointID, fact.ProviderID, fact.BaseURLRevision, fact.StatusRevision, fact.Status)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("provider endpoint status changed concurrently")
		}
		return nil
	})
}

func (f *EndpointFencer) updateStatus(ctx context.Context, fact endpointFenceFact, nextStatus string) (runtimecontrol.PublishResult, error) {
	token := newToken("ep-status-")
	transition := runtimecontrol.EndpointRoutingTransition{
		EndpointID:             fact.EndpointID,
		CurrentBaseURLRevision: fact.BaseURLRevision, NextBaseURLRevision: fact.BaseURLRevision,
		CurrentStatusRevision: fact.StatusRevision, NextStatusRevision: fact.StatusRevision + 1,
		CurrentEffectiveStatus: fact.EffectiveStatus, NextEffectiveStatus: fact.NextEffectiveStatus,
	}
	envelope := runtimecontrol.EndpointRoutingEnvelope{
		Kind: runtimecontrol.EndpointFenceKindStatus, ProviderID: fact.ProviderID,
		CurrentProviderStatus: fact.ProviderStatus, NextProviderStatus: fact.ProviderStatus,
		Transitions: []runtimecontrol.EndpointRoutingTransition{transition},
	}
	durable, payload, err := runtimecontrol.CanonicalEndpointRoutingOperation(envelope, "", 1)
	if err != nil {
		return runtimecontrol.PublishResult{}, err
	}
	providerID := fact.ProviderID
	return f.pub.Publish(ctx, runtimecontrol.EndpointFenceRequest{
		Kind: runtimecontrol.EndpointFenceKindStatus, Token: token, EndpointID: fact.EndpointID,
		ProviderID: &providerID, Transitions: durable, Payload: payload, MaxBatch: 1,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.PrepareEndpointStatusRevision(ctx, fact.EndpointID, fact.StatusRevision, fact.StatusRevision+1, fact.NextEffectiveStatus, token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.CommitEndpointStatusRevision(ctx, fact.EndpointID, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.AbortEndpointStatusRevision(ctx, fact.EndpointID, token, payload)
		},
		ValidateLocked: validateEndpointFactLocked(fact),
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			command, err := tx.Exec(ctx, `UPDATE provider_endpoints
				SET status=$1, status_revision=$2,
				    archived_at=CASE WHEN $1::text='archived' THEN now() ELSE NULL END,
				    updated_at=now()
				WHERE id=$3 AND provider_id=$4 AND base_url_revision=$5 AND status_revision=$6 AND status=$7`,
				nextStatus, fact.StatusRevision+1, fact.EndpointID, fact.ProviderID,
				fact.BaseURLRevision, fact.StatusRevision, fact.Status)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return fmt.Errorf("provider endpoint status changed concurrently")
			}
			return nil
		},
	})
}

func (f *EndpointFencer) updateBaseURL(ctx context.Context, fact endpointFenceFact, nextBaseURL string) (runtimecontrol.PublishResult, error) {
	token := newToken("ep-base-")
	transition := runtimecontrol.EndpointRoutingTransition{
		EndpointID:             fact.EndpointID,
		CurrentBaseURLRevision: fact.BaseURLRevision, NextBaseURLRevision: fact.BaseURLRevision + 1,
		CurrentStatusRevision: fact.StatusRevision, NextStatusRevision: fact.StatusRevision,
		CurrentEffectiveStatus: fact.EffectiveStatus, NextEffectiveStatus: fact.EffectiveStatus,
	}
	envelope := runtimecontrol.EndpointRoutingEnvelope{
		Kind: runtimecontrol.EndpointFenceKindBaseURL, ProviderID: fact.ProviderID,
		CurrentProviderStatus: fact.ProviderStatus, NextProviderStatus: fact.ProviderStatus,
		Transitions: []runtimecontrol.EndpointRoutingTransition{transition},
	}
	durable, payload, err := runtimecontrol.CanonicalEndpointRoutingOperation(envelope, nextBaseURL, 1)
	if err != nil {
		return runtimecontrol.PublishResult{}, err
	}
	providerID := fact.ProviderID
	return f.pub.Publish(ctx, runtimecontrol.EndpointFenceRequest{
		Kind: runtimecontrol.EndpointFenceKindBaseURL, Token: token, EndpointID: fact.EndpointID,
		ProviderID: &providerID, Transitions: durable, Payload: payload, MaxBatch: 1,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.PrepareEndpointBaseURLRevision(ctx, fact.EndpointID, fact.BaseURLRevision, fact.BaseURLRevision+1, token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.CommitEndpointBaseURLRevision(ctx, fact.EndpointID, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.AbortEndpointBaseURLRevision(ctx, fact.EndpointID, token, payload)
		},
		ValidateLocked: validateEndpointFactLocked(fact),
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			command, err := tx.Exec(ctx, `UPDATE provider_endpoints
				SET base_url=$1, base_url_revision=$2, updated_at=now()
				WHERE id=$3 AND provider_id=$4 AND base_url_revision=$5 AND status_revision=$6 AND status=$7`,
				nextBaseURL, fact.BaseURLRevision+1, fact.EndpointID, fact.ProviderID,
				fact.BaseURLRevision, fact.StatusRevision, fact.Status)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return fmt.Errorf("provider endpoint BaseURL changed concurrently")
			}
			return nil
		},
	})
}

func (f *EndpointFencer) updateRouting(ctx context.Context, fact endpointFenceFact, nextBaseURL, nextStatus string) (runtimecontrol.PublishResult, error) {
	token := newToken("ep-routing-")
	transition := runtimecontrol.EndpointRoutingTransition{
		EndpointID:             fact.EndpointID,
		CurrentBaseURLRevision: fact.BaseURLRevision, NextBaseURLRevision: fact.BaseURLRevision + 1,
		CurrentStatusRevision: fact.StatusRevision, NextStatusRevision: fact.StatusRevision + 1,
		CurrentEffectiveStatus: fact.EffectiveStatus, NextEffectiveStatus: fact.NextEffectiveStatus,
	}
	envelope := runtimecontrol.EndpointRoutingEnvelope{
		Kind: runtimecontrol.EndpointFenceKindBaseURLStatus, ProviderID: fact.ProviderID,
		CurrentProviderStatus: fact.ProviderStatus, NextProviderStatus: fact.ProviderStatus,
		Transitions: []runtimecontrol.EndpointRoutingTransition{transition},
	}
	durable, payload, err := runtimecontrol.CanonicalEndpointRoutingOperation(envelope, nextBaseURL, 1)
	if err != nil {
		return runtimecontrol.PublishResult{}, err
	}
	providerID := fact.ProviderID
	change := breakerstore.EndpointRoutingChange{
		EndpointID:        fact.EndpointID,
		CurrentBaseURLRev: fact.BaseURLRevision, NextBaseURLRev: fact.BaseURLRevision + 1,
		CurrentStatusRev: fact.StatusRevision, NextStatusRev: fact.StatusRevision + 1,
		NextEffectiveStatus: fact.NextEffectiveStatus,
	}
	return f.pub.Publish(ctx, runtimecontrol.EndpointFenceRequest{
		Kind: runtimecontrol.EndpointFenceKindBaseURLStatus, Token: token, EndpointID: fact.EndpointID,
		ProviderID: &providerID, Transitions: durable, Payload: payload, MaxBatch: 1,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.PrepareEndpointRoutingChange(ctx, change, token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.CommitEndpointRoutingChange(ctx, fact.EndpointID, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.AbortEndpointRoutingChange(ctx, fact.EndpointID, token, payload)
		},
		ValidateLocked: validateEndpointFactLocked(fact),
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			command, err := tx.Exec(ctx, `UPDATE provider_endpoints
				SET base_url=$1, base_url_revision=$2, status=$3, status_revision=$4,
				    archived_at=CASE WHEN $3::text='archived' THEN now() ELSE NULL END,
				    updated_at=now()
				WHERE id=$5 AND provider_id=$6 AND base_url_revision=$7 AND status_revision=$8 AND status=$9`,
				nextBaseURL, fact.BaseURLRevision+1, nextStatus, fact.StatusRevision+1,
				fact.EndpointID, fact.ProviderID, fact.BaseURLRevision, fact.StatusRevision, fact.Status)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return fmt.Errorf("provider endpoint routing fields changed concurrently")
			}
			return nil
		},
	})
}

func validateEndpointFactLocked(fact endpointFenceFact) func(context.Context, pgx.Tx) error {
	return func(ctx context.Context, tx pgx.Tx) error {
		var providerStatus string
		if err := tx.QueryRow(ctx, `SELECT status FROM providers WHERE id=$1`, fact.ProviderID).Scan(&providerStatus); err != nil {
			return err
		}
		if providerStatus != fact.ProviderStatus {
			return fmt.Errorf("provider status changed concurrently")
		}
		var baseURL, status string
		var baseRevision, statusRevision int64
		if err := tx.QueryRow(ctx, `SELECT base_url, base_url_revision, status, status_revision
			FROM provider_endpoints WHERE id=$1 AND provider_id=$2`, fact.EndpointID, fact.ProviderID).
			Scan(&baseURL, &baseRevision, &status, &statusRevision); err != nil {
			return err
		}
		if baseURL != fact.BaseURL || baseRevision != fact.BaseURLRevision || status != fact.Status || statusRevision != fact.StatusRevision {
			return fmt.Errorf("provider endpoint changed concurrently")
		}
		return nil
	}
}
