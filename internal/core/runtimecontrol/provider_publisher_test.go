package runtimecontrol_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
)

func TestProviderFencePublisherStatusChange(t *testing.T) {
	pool, store, _ := newPublisherTest(t)
	ctx := context.Background()
	suffix := time.Now().UnixNano()
	origin := fmt.Sprintf("https://provider-fence-%d.example.test", suffix)
	var providerID int64
	if err := pool.QueryRow(ctx, `INSERT INTO providers (slug, name, origin, status) VALUES ($1,$2,$3,'enabled') RETURNING id`,
		fmt.Sprintf("provider-fence-%d", suffix), "Provider Fence", origin).Scan(&providerID); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_routing_operations WHERE provider_id=$1`, providerID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM providers WHERE id=$1`, providerID)
	})
	if _, err := store.InitProviderControl(ctx, providerID, 1, 1, "enabled"); err != nil {
		t.Fatalf("init provider control: %v", err)
	}

	envelope := runtimecontrol.ProviderRoutingEnvelope{
		Kind: runtimecontrol.ProviderFenceKindStatus, ProviderID: providerID,
		CurrentOriginRevision: 1, NextOriginRevision: 1,
		CurrentStatusRevision: 1, NextStatusRevision: 2,
		CurrentStatus: "enabled", NextStatus: "disabled",
	}
	_, payload, err := runtimecontrol.CanonicalProviderRoutingOperation(envelope)
	if err != nil {
		t.Fatalf("canonical provider operation: %v", err)
	}
	token := fmt.Sprintf("provider-fence-token-%d", suffix)
	result, err := runtimecontrol.NewProviderFencePublisher(pool).Publish(ctx, runtimecontrol.ProviderFenceRequest{
		Envelope: envelope, Token: token, Payload: payload,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return store.PrepareProviderStatusRevision(ctx, providerID, 1, 2, "disabled", token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return store.CommitProviderStatusRevision(ctx, providerID, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return store.AbortProviderStatusRevision(ctx, providerID, token, payload)
		},
		ValidateLocked: func(context.Context, pgx.Tx) error { return nil },
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			command, err := tx.Exec(ctx, `UPDATE providers SET status='disabled', status_revision=2, updated_at=now() WHERE id=$1 AND status_revision=1`, providerID)
			if err != nil {
				return err
			}
			if command.RowsAffected() != 1 {
				return fmt.Errorf("status revision CAS failed")
			}
			return nil
		},
	})
	if err != nil || result.State != runtimecontrol.PublishCommitted {
		t.Fatalf("publish provider status fence: result=%+v err=%v", result, err)
	}
	var status string
	var statusRevision int64
	if err := pool.QueryRow(ctx, `SELECT status, status_revision FROM providers WHERE id=$1`, providerID).Scan(&status, &statusRevision); err != nil {
		t.Fatalf("read provider: %v", err)
	}
	if status != "disabled" || statusRevision != 2 {
		t.Fatalf("provider status/revision want disabled/2, got %s/%d", status, statusRevision)
	}
}
