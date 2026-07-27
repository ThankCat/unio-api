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

type fencePublisher interface {
	Publish(context.Context, runtimecontrol.ProviderFenceRequest) (runtimecontrol.PublishResult, error)
}

type fenceControl interface {
	PrepareOriginRevision(context.Context, int64, int64, int64, string, string) (breakerstore.FenceResult, error)
	CommitOriginRevision(context.Context, int64, string, string) (breakerstore.FenceResult, error)
	AbortOriginRevision(context.Context, int64, string, string) (breakerstore.FenceResult, error)
	PrepareProviderStatusRevision(context.Context, int64, int64, int64, string, string, string) (breakerstore.FenceResult, error)
	CommitProviderStatusRevision(context.Context, int64, string, string) (breakerstore.FenceResult, error)
	AbortProviderStatusRevision(context.Context, int64, string, string) (breakerstore.FenceResult, error)
}

type Fencer struct {
	publisher fencePublisher
	control   fenceControl
}

func NewFencer(publisher fencePublisher, control fenceControl) *Fencer {
	return &Fencer{publisher: publisher, control: control}
}

func (f *Fencer) UpdateOrigin(ctx context.Context, current sqlc.Provider, origin string, confirmed bool) (sqlc.Provider, bool, error) {
	envelope := runtimecontrol.ProviderRoutingEnvelope{
		Kind: runtimecontrol.ProviderFenceKindOrigin, ProviderID: current.ID,
		CurrentOriginRevision: current.OriginRevision, NextOriginRevision: current.OriginRevision + 1,
		CurrentStatusRevision: current.StatusRevision, NextStatusRevision: current.StatusRevision,
		CurrentStatus: current.Status, NextStatus: current.Status, NextOrigin: origin,
	}
	_, payload, err := runtimecontrol.CanonicalProviderRoutingOperation(envelope)
	if err != nil {
		return sqlc.Provider{}, false, err
	}
	token, err := randomToken()
	if err != nil {
		return sqlc.Provider{}, false, err
	}
	result, err := f.publisher.Publish(ctx, runtimecontrol.ProviderFenceRequest{
		Envelope: envelope, Token: token, Payload: payload,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.control.PrepareOriginRevision(ctx, current.ID, current.OriginRevision, current.OriginRevision+1, token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.control.CommitOriginRevision(ctx, current.ID, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.control.AbortOriginRevision(ctx, current.ID, token, payload)
		},
		ValidateLocked: func(ctx context.Context, tx pgx.Tx) error {
			var revision int64
			var status string
			if err := tx.QueryRow(ctx, `SELECT origin_revision, status FROM providers WHERE id=$1`, current.ID).Scan(&revision, &status); err != nil {
				return err
			}
			if revision != current.OriginRevision || status == StatusArchived {
				return conflict("provider origin revision is stale")
			}
			if !confirmed {
				var enabled int64
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM channels WHERE provider_id=$1 AND status='enabled'`, current.ID).Scan(&enabled); err != nil {
					return err
				}
				if enabled > 0 {
					return conflict("changing provider origin with enabled channels requires confirmation")
				}
			}
			return nil
		},
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			command, err := tx.Exec(ctx, `UPDATE providers SET origin=$1, origin_revision=origin_revision+1, updated_at=now() WHERE id=$2 AND origin_revision=$3`, origin, current.ID, current.OriginRevision)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return conflict("provider origin revision is stale")
			}
			return nil
		},
	})
	if err != nil {
		return sqlc.Provider{}, false, err
	}
	updated := current
	updated.Origin = origin
	updated.OriginRevision++
	return updated, result.State == runtimecontrol.PublishRuntimeSyncPending, nil
}

func (f *Fencer) UpdateStatus(ctx context.Context, current sqlc.Provider, status string) (sqlc.Provider, bool, error) {
	envelope := runtimecontrol.ProviderRoutingEnvelope{
		Kind: runtimecontrol.ProviderFenceKindStatus, ProviderID: current.ID,
		CurrentOriginRevision: current.OriginRevision, NextOriginRevision: current.OriginRevision,
		CurrentStatusRevision: current.StatusRevision, NextStatusRevision: current.StatusRevision + 1,
		CurrentStatus: current.Status, NextStatus: status,
	}
	_, payload, err := runtimecontrol.CanonicalProviderRoutingOperation(envelope)
	if err != nil {
		return sqlc.Provider{}, false, err
	}
	token, err := randomToken()
	if err != nil {
		return sqlc.Provider{}, false, err
	}
	result, err := f.publisher.Publish(ctx, runtimecontrol.ProviderFenceRequest{
		Envelope: envelope, Token: token, Payload: payload,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.control.PrepareProviderStatusRevision(ctx, current.ID, current.StatusRevision, current.StatusRevision+1, status, token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.control.CommitProviderStatusRevision(ctx, current.ID, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return f.control.AbortProviderStatusRevision(ctx, current.ID, token, payload)
		},
		ValidateLocked: func(ctx context.Context, tx pgx.Tx) error {
			var revision int64
			if err := tx.QueryRow(ctx, `SELECT status_revision FROM providers WHERE id=$1`, current.ID).Scan(&revision); err != nil {
				return err
			}
			if revision != current.StatusRevision {
				return conflict("provider status revision is stale")
			}
			if status == StatusDisabled {
				var enabled int64
				if err := tx.QueryRow(ctx, `SELECT count(*) FROM channels WHERE provider_id=$1 AND status='enabled'`, current.ID).Scan(&enabled); err != nil {
					return err
				}
				if enabled > 0 {
					return conflict("disable enabled channels before disabling provider")
				}
			}
			return nil
		},
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			command, err := tx.Exec(ctx, `UPDATE providers SET status=$1, status_revision=status_revision+1, updated_at=now() WHERE id=$2 AND status_revision=$3 AND status<>'archived'`, status, current.ID, current.StatusRevision)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return conflict("provider status revision is stale")
			}
			return nil
		},
	})
	if err != nil {
		return sqlc.Provider{}, false, err
	}
	updated := current
	updated.Status = status
	updated.StatusRevision++
	return updated, result.State == runtimecontrol.PublishRuntimeSyncPending, nil
}

func randomToken() (string, error) {
	bytes := make([]byte, 16)
	if _, err := rand.Read(bytes); err != nil {
		return "", fmt.Errorf("generate provider routing token: %w", err)
	}
	return hex.EncodeToString(bytes), nil
}
