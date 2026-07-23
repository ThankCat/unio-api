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

// TestEndpointFencePublisherStatusChange 验证 status 围栏发布：Redis fence commit 激活 + DB status/status_revision +1 + op committed。
func TestEndpointFencePublisherStatusChange(t *testing.T) {
	pool, store, _ := newPublisherTest(t)
	ctx := context.Background()

	suffix := time.Now().UnixNano()
	var providerID, endpointID int64
	if err := pool.QueryRow(ctx, `INSERT INTO providers (slug, name, status) VALUES ($1,$2,'enabled') RETURNING id`,
		fmt.Sprintf("epfence-prov-%d", suffix), "EPFence Prov").Scan(&providerID); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO provider_endpoints (provider_id, name, base_url, status) VALUES ($1,$2,$3,'enabled') RETURNING id`,
		providerID, "ep", fmt.Sprintf("https://epfence-%d.example.test", suffix)).Scan(&endpointID); err != nil {
		t.Fatalf("seed endpoint: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DELETE FROM endpoint_routing_operations WHERE endpoint_id=$1`, endpointID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM provider_endpoints WHERE id=$1`, endpointID)
		_, _ = pool.Exec(context.Background(), `DELETE FROM providers WHERE id=$1`, providerID)
	})

	// 建立初始 Endpoint control（revision 1/1, enabled）。
	if _, err := store.InitEndpointControl(ctx, endpointID, 1, 1, "enabled"); err != nil {
		t.Fatalf("init endpoint control: %v", err)
	}

	pub := runtimecontrol.NewEndpointFencePublisher(pool)
	token := fmt.Sprintf("epfence-tok-%d", suffix)
	envelope := runtimecontrol.EndpointRoutingEnvelope{
		Kind: runtimecontrol.EndpointFenceKindStatus, ProviderID: providerID,
		CurrentProviderStatus: "enabled", NextProviderStatus: "enabled",
		Transitions: []runtimecontrol.EndpointRoutingTransition{{
			EndpointID:             endpointID,
			CurrentBaseURLRevision: 1, NextBaseURLRevision: 1,
			CurrentStatusRevision: 1, NextStatusRevision: 2,
			CurrentEffectiveStatus: "enabled", NextEffectiveStatus: "disabled",
		}},
	}
	transitions, payload, err := runtimecontrol.CanonicalEndpointRoutingOperation(envelope, "", 1)
	if err != nil {
		t.Fatalf("canonical operation: %v", err)
	}

	res, err := pub.Publish(ctx, runtimecontrol.EndpointFenceRequest{
		Kind:        runtimecontrol.EndpointFenceKindStatus,
		Token:       token,
		EndpointID:  endpointID,
		ProviderID:  &providerID,
		Transitions: transitions,
		Payload:     payload,
		MaxBatch:    1,
		Prepare: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return store.PrepareEndpointStatusRevision(ctx, endpointID, 1, 2, "disabled", token, payload)
		},
		Commit: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return store.CommitEndpointStatusRevision(ctx, endpointID, token, payload)
		},
		Abort: func(ctx context.Context) (breakerstore.FenceResult, error) {
			return store.AbortEndpointStatusRevision(ctx, endpointID, token, payload)
		},
		ValidateLocked: func(ctx context.Context, tx pgx.Tx) error { return nil },
		BusinessCommit: func(ctx context.Context, tx pgx.Tx) error {
			ct, err := tx.Exec(ctx, `UPDATE provider_endpoints SET status='disabled', status_revision=2, archived_at=NULL, updated_at=now() WHERE id=$1 AND status_revision=1`, endpointID)
			if err != nil {
				return err
			}
			if ct.RowsAffected() != 1 {
				return fmt.Errorf("status revision CAS failed")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatalf("publish endpoint status fence: %v", err)
	}
	if res.State != runtimecontrol.PublishCommitted {
		t.Fatalf("want committed, got %s", res.State)
	}

	// DB status/status_revision 更新。
	var status string
	var statusRev int64
	if err := pool.QueryRow(ctx, `SELECT status, status_revision FROM provider_endpoints WHERE id=$1`, endpointID).Scan(&status, &statusRev); err != nil {
		t.Fatalf("read endpoint: %v", err)
	}
	if status != "disabled" || statusRev != 2 {
		t.Fatalf("endpoint status/revision want disabled/2, got %s/%d", status, statusRev)
	}
}
