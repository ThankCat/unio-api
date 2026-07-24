package providerorigin

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"fmt"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

// FenceOps is the Redis capability required by single Origin updates.
type FenceOps interface {
	PrepareOriginStatusRevision(ctx context.Context, originID, currentStatusRev, nextStatusRev int64, nextEffectiveStatus, token, payload string) (breakerstore.FenceResult, error)
	CommitOriginStatusRevision(ctx context.Context, originID int64, token, payload string) (breakerstore.FenceResult, error)
	AbortOriginStatusRevision(ctx context.Context, originID int64, token, payload string) (breakerstore.FenceResult, error)
	PrepareOriginBaseURLRevision(ctx context.Context, originID, currentBaseURLRev, nextBaseURLRev int64, token, payload string) (breakerstore.FenceResult, error)
	CommitOriginBaseURLRevision(ctx context.Context, originID int64, token, payload string) (breakerstore.FenceResult, error)
	AbortOriginBaseURLRevision(ctx context.Context, originID int64, token, payload string) (breakerstore.FenceResult, error)
	PrepareOriginRoutingChange(ctx context.Context, change breakerstore.OriginRoutingChange, token, payload string) (breakerstore.FenceResult, error)
	CommitOriginRoutingChange(ctx context.Context, originID int64, token, payload string) (breakerstore.FenceResult, error)
	AbortOriginRoutingChange(ctx context.Context, originID int64, token, payload string) (breakerstore.FenceResult, error)
}

type FencePublisher interface {
	Publish(ctx context.Context, req runtimecontrol.OriginFenceRequest) (runtimecontrol.PublishResult, error)
	WithOriginLocks(ctx context.Context, providerID int64, originIDs []int64, fn func(context.Context, pgx.Tx) error) error
}

// OriginFencer performs BaseURL/status/combined changes through one durable publisher.
type OriginFencer struct {
	pub FencePublisher
	ops FenceOps
}

func NewOriginFencer(pub FencePublisher, ops FenceOps) *OriginFencer {
	if pub == nil || ops == nil {
		panic("providerorigin: origin fencer requires publisher and store")
	}
	return &OriginFencer{pub: pub, ops: ops}
}

func newToken(prefix string) string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		panic("providerorigin: secure token generation failed")
	}
	return prefix + hex.EncodeToString(b)
}

type originFenceFact struct {
	OriginID          int64
	ProviderID          int64
	ProviderStatus      string
	BaseURL             string
	BaseURLRevision     int64
	Status              string
	StatusRevision      int64
	EffectiveStatus     string
	NextEffectiveStatus string
}

func (f *OriginFencer) updateStatusWithoutRevision(ctx context.Context, fact originFenceFact, nextStatus string) error {
	return f.pub.WithOriginLocks(ctx, fact.ProviderID, []int64{fact.OriginID}, func(ctx context.Context, tx pgx.Tx) error {
		if err := validateOriginFactLocked(fact)(ctx, tx); err != nil {
			return err
		}
		command, err := tx.Exec(ctx, `UPDATE provider_origins
			SET status=$1, archived_at=CASE WHEN $1::text='archived' THEN now() ELSE NULL END, updated_at=now()
			WHERE id=$2 AND provider_id=$3 AND base_url_revision=$4 AND status_revision=$5 AND status=$6`,
			nextStatus, fact.OriginID, fact.ProviderID, fact.BaseURLRevision, fact.StatusRevision, fact.Status)
		if err != nil {
			return err
		}
		if command.RowsAffected() != 1 {
			return fmt.Errorf("provider origin status changed concurrently")
		}
		return nil
	})
}

func (f *OriginFencer) updateStatus(ctx context.Context, fact originFenceFact, nextStatus string) (runtimecontrol.PublishResult, error) {
	token := newToken("ep-status-")
	transition := runtimecontrol.OriginRoutingTransition{
		OriginID:             fact.OriginID,
		CurrentBaseURLRevision: fact.BaseURLRevision, NextBaseURLRevision: fact.BaseURLRevision,
		CurrentStatusRevision: fact.StatusRevision, NextStatusRevision: fact.StatusRevision + 1,
		CurrentEffectiveStatus: fact.EffectiveStatus, NextEffectiveStatus: fact.NextEffectiveStatus,
	}
	envelope := runtimecontrol.OriginRoutingEnvelope{
		Kind: runtimecontrol.OriginFenceKindStatus, ProviderID: fact.ProviderID,
		CurrentProviderStatus: fact.ProviderStatus, NextProviderStatus: fact.ProviderStatus,
		Transitions: []runtimecontrol.OriginRoutingTransition{transition},
	}
	durable, payload, err := runtimecontrol.CanonicalOriginRoutingOperation(envelope, "", 1)
	if err != nil {
		return runtimecontrol.PublishResult{}, err
	}
	providerID := fact.ProviderID
	return f.pub.Publish(ctx, runtimecontrol.OriginFenceRequest{
		Kind: runtimecontrol.OriginFenceKindStatus, Token: token, OriginID: fact.OriginID,
		ProviderID: &providerID, Transitions: durable, Payload: payload, MaxBatch: 1,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.PrepareOriginStatusRevision(ctx, fact.OriginID, fact.StatusRevision, fact.StatusRevision+1, fact.NextEffectiveStatus, token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.CommitOriginStatusRevision(ctx, fact.OriginID, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.AbortOriginStatusRevision(ctx, fact.OriginID, token, payload)
		},
		ValidateLocked: validateOriginFactLocked(fact),
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			command, err := tx.Exec(ctx, `UPDATE provider_origins
				SET status=$1, status_revision=$2,
				    archived_at=CASE WHEN $1::text='archived' THEN now() ELSE NULL END,
				    updated_at=now()
				WHERE id=$3 AND provider_id=$4 AND base_url_revision=$5 AND status_revision=$6 AND status=$7`,
				nextStatus, fact.StatusRevision+1, fact.OriginID, fact.ProviderID,
				fact.BaseURLRevision, fact.StatusRevision, fact.Status)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return fmt.Errorf("provider origin status changed concurrently")
			}
			return nil
		},
	})
}

func (f *OriginFencer) updateBaseURL(ctx context.Context, fact originFenceFact, nextBaseURL string) (runtimecontrol.PublishResult, error) {
	token := newToken("ep-base-")
	transition := runtimecontrol.OriginRoutingTransition{
		OriginID:             fact.OriginID,
		CurrentBaseURLRevision: fact.BaseURLRevision, NextBaseURLRevision: fact.BaseURLRevision + 1,
		CurrentStatusRevision: fact.StatusRevision, NextStatusRevision: fact.StatusRevision,
		CurrentEffectiveStatus: fact.EffectiveStatus, NextEffectiveStatus: fact.EffectiveStatus,
	}
	envelope := runtimecontrol.OriginRoutingEnvelope{
		Kind: runtimecontrol.OriginFenceKindBaseURL, ProviderID: fact.ProviderID,
		CurrentProviderStatus: fact.ProviderStatus, NextProviderStatus: fact.ProviderStatus,
		Transitions: []runtimecontrol.OriginRoutingTransition{transition},
	}
	durable, payload, err := runtimecontrol.CanonicalOriginRoutingOperation(envelope, nextBaseURL, 1)
	if err != nil {
		return runtimecontrol.PublishResult{}, err
	}
	providerID := fact.ProviderID
	return f.pub.Publish(ctx, runtimecontrol.OriginFenceRequest{
		Kind: runtimecontrol.OriginFenceKindBaseURL, Token: token, OriginID: fact.OriginID,
		ProviderID: &providerID, Transitions: durable, Payload: payload, MaxBatch: 1,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.PrepareOriginBaseURLRevision(ctx, fact.OriginID, fact.BaseURLRevision, fact.BaseURLRevision+1, token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.CommitOriginBaseURLRevision(ctx, fact.OriginID, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.AbortOriginBaseURLRevision(ctx, fact.OriginID, token, payload)
		},
		ValidateLocked: validateOriginFactLocked(fact),
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			command, err := tx.Exec(ctx, `UPDATE provider_origins
				SET base_url=$1, base_url_revision=$2, updated_at=now()
				WHERE id=$3 AND provider_id=$4 AND base_url_revision=$5 AND status_revision=$6 AND status=$7`,
				nextBaseURL, fact.BaseURLRevision+1, fact.OriginID, fact.ProviderID,
				fact.BaseURLRevision, fact.StatusRevision, fact.Status)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return fmt.Errorf("provider origin BaseURL changed concurrently")
			}
			return nil
		},
	})
}

func (f *OriginFencer) updateRouting(ctx context.Context, fact originFenceFact, nextBaseURL, nextStatus string) (runtimecontrol.PublishResult, error) {
	token := newToken("ep-routing-")
	transition := runtimecontrol.OriginRoutingTransition{
		OriginID:             fact.OriginID,
		CurrentBaseURLRevision: fact.BaseURLRevision, NextBaseURLRevision: fact.BaseURLRevision + 1,
		CurrentStatusRevision: fact.StatusRevision, NextStatusRevision: fact.StatusRevision + 1,
		CurrentEffectiveStatus: fact.EffectiveStatus, NextEffectiveStatus: fact.NextEffectiveStatus,
	}
	envelope := runtimecontrol.OriginRoutingEnvelope{
		Kind: runtimecontrol.OriginFenceKindBaseURLStatus, ProviderID: fact.ProviderID,
		CurrentProviderStatus: fact.ProviderStatus, NextProviderStatus: fact.ProviderStatus,
		Transitions: []runtimecontrol.OriginRoutingTransition{transition},
	}
	durable, payload, err := runtimecontrol.CanonicalOriginRoutingOperation(envelope, nextBaseURL, 1)
	if err != nil {
		return runtimecontrol.PublishResult{}, err
	}
	providerID := fact.ProviderID
	change := breakerstore.OriginRoutingChange{
		OriginID:        fact.OriginID,
		CurrentBaseURLRev: fact.BaseURLRevision, NextBaseURLRev: fact.BaseURLRevision + 1,
		CurrentStatusRev: fact.StatusRevision, NextStatusRev: fact.StatusRevision + 1,
		NextEffectiveStatus: fact.NextEffectiveStatus,
	}
	return f.pub.Publish(ctx, runtimecontrol.OriginFenceRequest{
		Kind: runtimecontrol.OriginFenceKindBaseURLStatus, Token: token, OriginID: fact.OriginID,
		ProviderID: &providerID, Transitions: durable, Payload: payload, MaxBatch: 1,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.PrepareOriginRoutingChange(ctx, change, token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.CommitOriginRoutingChange(ctx, fact.OriginID, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.ops.AbortOriginRoutingChange(ctx, fact.OriginID, token, payload)
		},
		ValidateLocked: validateOriginFactLocked(fact),
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			command, err := tx.Exec(ctx, `UPDATE provider_origins
				SET base_url=$1, base_url_revision=$2, status=$3, status_revision=$4,
				    archived_at=CASE WHEN $3::text='archived' THEN now() ELSE NULL END,
				    updated_at=now()
				WHERE id=$5 AND provider_id=$6 AND base_url_revision=$7 AND status_revision=$8 AND status=$9`,
				nextBaseURL, fact.BaseURLRevision+1, nextStatus, fact.StatusRevision+1,
				fact.OriginID, fact.ProviderID, fact.BaseURLRevision, fact.StatusRevision, fact.Status)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return fmt.Errorf("provider origin routing fields changed concurrently")
			}
			return nil
		},
	})
}

func validateOriginFactLocked(fact originFenceFact) func(context.Context, pgx.Tx) error {
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
			FROM provider_origins WHERE id=$1 AND provider_id=$2`, fact.OriginID, fact.ProviderID).
			Scan(&baseURL, &baseRevision, &status, &statusRevision); err != nil {
			return err
		}
		if baseURL != fact.BaseURL || baseRevision != fact.BaseURLRevision || status != fact.Status || statusRevision != fact.StatusRevision {
			return fmt.Errorf("provider origin changed concurrently")
		}
		return nil
	}
}
