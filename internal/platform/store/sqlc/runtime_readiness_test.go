package sqlc_test

import (
	"fmt"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
)

// TestGatewayRuntimeReadinessSnapshotBlocksAllPendingRoutingControls verifies that readiness
// observes the two durable operation families which are scoped below the five critical settings.
func TestGatewayRuntimeReadinessSnapshotBlocksAllPendingRoutingControls(t *testing.T) {
	ctx, tx, queries, cleanup := newModelChannelTestTx(t)
	defer cleanup()

	for _, key := range []string{
		"gateway.runtime_state_epoch",
		"gateway.route_rate_limit_defaults",
		"gateway.channel_rate_limit_defaults",
		"gateway.concurrency_defaults",
		"gateway.circuit_breaker",
		"gateway.routing_balance",
	} {
		if _, err := tx.Exec(ctx, `INSERT INTO app_settings (key, value) VALUES ($1, '{}'::jsonb)
			ON CONFLICT (key) DO NOTHING`, key); err != nil {
			t.Fatalf("seed app setting %s: %v", key, err)
		}
	}

	suffix := time.Now().UnixNano()
	providerID := insertProvider(t, ctx, tx, fmt.Sprintf("readiness-%d", suffix), "enabled")
	endpointID := insertProviderEndpoint(
		t, ctx, tx, providerID, "readiness", fmt.Sprintf("https://readiness-%d.example.test", suffix), "enabled",
	)
	var channelID int64
	if err := tx.QueryRow(ctx, `INSERT INTO channels (
		provider_id, provider_endpoint_id, name, protocol, adapter_key, credential, status, priority
	) VALUES ($1, $2, 'readiness', 'openai', 'openai', 'sk-readiness', 'enabled', 1)
	RETURNING id`, providerID, endpointID).Scan(&channelID); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	baseline, err := queries.GetGatewayRuntimeReadinessSnapshot(ctx)
	if err != nil {
		t.Fatalf("read baseline readiness: %v", err)
	}
	if !baseline.RuntimeOperationsReconciled {
		t.Fatal("expected clean durable operation baseline")
	}

	channelToken := fmt.Sprintf("readiness-channel-%d", suffix)
	if _, err := tx.Exec(ctx, `INSERT INTO runtime_control_operations (
		token, kind, channel_id, current_revision, next_revision, payload_hash, state
	) VALUES ($1, 'channel_admission_limits', $2, 1, 2, 'pending-channel', 'preparing')`,
		channelToken, channelID); err != nil {
		t.Fatalf("seed pending channel operation: %v", err)
	}
	assertRuntimeOperationsPending(t, queries, "channel admission")
	if _, err := tx.Exec(ctx, `UPDATE runtime_control_operations
		SET state='aborted', completed_at=now() WHERE token=$1`, channelToken); err != nil {
		t.Fatalf("finish channel operation: %v", err)
	}

	endpointToken := fmt.Sprintf("readiness-endpoint-%d", suffix)
	if _, err := tx.Exec(ctx, `INSERT INTO endpoint_routing_operations (
		token, kind, provider_id, endpoint_id, transitions, payload_hash, state
	) VALUES ($1, 'status', $2, $3, '{}'::jsonb, 'pending-endpoint', 'preparing')`,
		endpointToken, providerID, endpointID); err != nil {
		t.Fatalf("seed pending endpoint operation: %v", err)
	}
	assertRuntimeOperationsPending(t, queries, "endpoint routing")
}

func assertRuntimeOperationsPending(t *testing.T, queries *sqlc.Queries, label string) {
	t.Helper()
	row, err := queries.GetGatewayRuntimeReadinessSnapshot(t.Context())
	if err != nil {
		t.Fatalf("read %s readiness: %v", label, err)
	}
	if row.RuntimeOperationsReconciled {
		t.Fatalf("pending %s operation must block readiness", label)
	}
}
