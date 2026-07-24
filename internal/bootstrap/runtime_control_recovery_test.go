package bootstrap

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

func TestReconcileAllRuntimeControlsRestoresStableEndpointAndChannel(t *testing.T) {
	databaseURL := os.Getenv("DATABASE_URL")
	redisAddr := os.Getenv("REDIS_ADDR")
	if databaseURL == "" || redisAddr == "" {
		t.Skip("DATABASE_URL and REDIS_ADDR are required")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, databaseURL)
	if err != nil {
		t.Fatalf("create postgres pool: %v", err)
	}
	if err := pool.Ping(ctx); err != nil {
		pool.Close()
		t.Skipf("postgres unavailable: %v", err)
	}
	rc := redis.NewClient(&redis.Options{Addr: redisAddr})
	if err := rc.Ping(ctx).Err(); err != nil {
		pool.Close()
		_ = rc.Close()
		t.Skipf("redis unavailable: %v", err)
	}

	suffix := time.Now().UnixNano()
	namespace := fmt.Sprintf("unio-runtime-recovery-test:%d", suffix)
	controls := breakerstore.NewStore(rc, namespace)
	var providerID, originID, channelID int64
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if channelID != 0 {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM runtime_control_operations WHERE channel_id=$1`, channelID)
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM channels WHERE id=$1`, channelID)
		}
		if originID != 0 {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM origin_routing_operations WHERE origin_id=$1`, originID)
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM provider_origins WHERE id=$1`, originID)
		}
		if providerID != 0 {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM providers WHERE id=$1`, providerID)
		}
		iter := rc.Scan(cleanupCtx, 0, namespace+":*", 0).Iterator()
		for iter.Next(cleanupCtx) {
			_ = rc.Del(cleanupCtx, iter.Val()).Err()
		}
		_ = rc.Close()
		pool.Close()
	})

	if err := pool.QueryRow(ctx, `INSERT INTO providers (slug, name, status)
		VALUES ($1, 'runtime recovery', 'enabled') RETURNING id`,
		fmt.Sprintf("runtime-recovery-%d", suffix)).Scan(&providerID); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO provider_origins (provider_id, name, base_url, status)
		VALUES ($1, 'primary', $2, 'enabled') RETURNING id`, providerID,
		fmt.Sprintf("https://runtime-recovery-%d.example.test", suffix)).Scan(&originID); err != nil {
		t.Fatalf("seed origin: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO channels (
		provider_id, provider_origin_id, name, protocol, adapter_key, credential, status, priority
	) VALUES ($1, $2, 'primary', 'openai', 'openai', 'sk-runtime-recovery', 'enabled', 1)
	RETURNING id`, providerID, originID).Scan(&channelID); err != nil {
		t.Fatalf("seed channel: %v", err)
	}

	settings := appsettings.NewSettingsStore(
		sqlc.New(pool), rc, namespace, appsettings.DefaultRegistry(), zap.NewNop(),
	)
	recorder := metrics.New()
	telemetry := newRuntimeControlTelemetry(recorder, zap.NewNop())
	if err := settings.SeedDefaults(ctx); err != nil {
		t.Fatalf("seed runtime settings: %v", err)
	}
	if err := reconcileAllRuntimeControls(ctx, pool, settings, controls, telemetry); err != nil {
		t.Fatalf("reconcile runtime controls: %v", err)
	}

	origin, err := controls.Snapshot(ctx, breakerstore.ScopeOrigin, originID)
	if err != nil {
		t.Fatalf("read restored origin control: %v", err)
	}
	if !origin.Exists || !origin.ControlPresent || origin.BaseURLRevision != 1 ||
		origin.StatusRevision != 1 || origin.EffectiveStatus != "enabled" {
		t.Fatalf("unexpected restored origin control: %+v", origin)
	}
	channel, err := controls.ReadControl(ctx, controls.ChannelAdmissionControl(channelID), 1)
	if err != nil {
		t.Fatalf("read restored channel control: %v", err)
	}
	if channel.SyncState != "active" || channel.ActiveRevision != 1 || channel.PendingRevision != 0 {
		t.Fatalf("unexpected restored channel control: %+v", channel)
	}
	routeRate, err := controls.ReadControl(ctx, controls.RouteRateLimitControl(), 1)
	if err != nil {
		t.Fatalf("read restored route rate control: %v", err)
	}
	if routeRate.SyncState != "active" || routeRate.ActiveRevision != 1 || routeRate.PendingRevision != 0 {
		t.Fatalf("unexpected restored route rate control: %+v", routeRate)
	}
	channelRate, err := controls.ReadControl(ctx, controls.ChannelRateLimitControl(), 1)
	if err != nil {
		t.Fatalf("read restored channel rate control: %v", err)
	}
	if channelRate.SyncState != "active" || channelRate.ActiveRevision != 1 || channelRate.PendingRevision != 0 {
		t.Fatalf("unexpected restored channel rate control: %+v", channelRate)
	}
	metricsBody := scrapeRuntimeControlMetrics(t, recorder)
	for _, want := range []string{
		fmt.Sprintf(`unio_gateway_origin_base_url_revision_fence{origin_id="%d",state="active"} 1`, originID),
		fmt.Sprintf(`unio_gateway_origin_status_revision_fence{origin_id="%d",state="active"} 1`, originID),
		`unio_gateway_runtime_control_recovery_total{result="restored",target="channel_admission"}`,
		`unio_gateway_runtime_control_recovery_total{result="restored",target="route_rate"} 1`,
		`unio_gateway_runtime_control_recovery_total{result="restored",target="channel_rate"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("recovery metrics missing %q\n%s", want, metricsBody)
		}
	}

	if err := reconcileAllRuntimeControls(ctx, pool, settings, controls, telemetry); err != nil {
		t.Fatalf("idempotent reconcile runtime controls: %v", err)
	}
}
