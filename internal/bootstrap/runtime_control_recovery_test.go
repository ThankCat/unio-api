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
	var providerID, channelID int64
	t.Cleanup(func() {
		cleanupCtx := context.Background()
		if channelID != 0 {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM runtime_control_operations WHERE channel_id=$1`, channelID)
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM channels WHERE id=$1`, channelID)
		}
		if providerID != 0 {
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM provider_routing_operations WHERE provider_id=$1`, providerID)
			_, _ = pool.Exec(cleanupCtx, `DELETE FROM providers WHERE id=$1`, providerID)
		}
		iter := rc.Scan(cleanupCtx, 0, namespace+":*", 0).Iterator()
		for iter.Next(cleanupCtx) {
			_ = rc.Del(cleanupCtx, iter.Val()).Err()
		}
		_ = rc.Close()
		pool.Close()
	})

	if err := pool.QueryRow(ctx, `INSERT INTO providers (slug, name, origin, status)
		VALUES ($1, 'runtime recovery', $2, 'enabled') RETURNING id`,
		fmt.Sprintf("runtime-recovery-%d", suffix), fmt.Sprintf("https://runtime-recovery-%d.example.test", suffix)).Scan(&providerID); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	if err := pool.QueryRow(ctx, `INSERT INTO channels (
		provider_id, name, protocol, adapter_key, credential, status, priority
	) VALUES ($1, 'primary', 'openai', 'openai', 'sk-runtime-recovery', 'enabled', 1)
	RETURNING id`, providerID).Scan(&channelID); err != nil {
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
	if err := reconcileAllRuntimeControls(ctx, pool, settings, controls, telemetry, runtimeControlStartupAuthority); err != nil {
		t.Fatalf("reconcile runtime controls: %v", err)
	}

	origin, err := controls.Snapshot(ctx, breakerstore.ScopeProvider, providerID)
	if err != nil {
		t.Fatalf("read restored provider control: %v", err)
	}
	if !origin.Exists || !origin.ControlPresent || origin.OriginRevision != 1 ||
		origin.StatusRevision != 1 || origin.EffectiveStatus != "enabled" {
		t.Fatalf("unexpected restored provider control: %+v", origin)
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
		fmt.Sprintf(`unio_gateway_origin_revision_fence{provider_id="%d",state="active"} 1`, providerID),
		fmt.Sprintf(`unio_gateway_provider_status_revision_fence{provider_id="%d",state="active"} 1`, providerID),
		`unio_gateway_runtime_control_recovery_total{result="restored",target="channel_admission"}`,
		`unio_gateway_runtime_control_recovery_total{result="restored",target="route_rate"} 1`,
		`unio_gateway_runtime_control_recovery_total{result="restored",target="channel_rate"} 1`,
	} {
		if !strings.Contains(metricsBody, want) {
			t.Fatalf("recovery metrics missing %q\n%s", want, metricsBody)
		}
	}

	channelTarget := controls.ChannelAdmissionControl(channelID)
	staleChannelPayload := `{"rpm":99,"rpd":99,"tpm":99,"concurrency":99}`
	if _, err := controls.ReconcileControl(ctx, channelTarget, 9, staleChannelPayload); err != nil {
		t.Fatalf("seed stale channel control: %v", err)
	}
	if code, _, err := controls.PrepareControl(ctx, channelTarget, "stale-channel-pending", 9, 10, staleChannelPayload); err != nil || code != breakerstore.ControlPrepared {
		t.Fatalf("seed stale channel pending: code=%s err=%v", code, err)
	}
	globalTarget := controls.GlobalConcurrencyControl()
	if _, err := controls.ReconcileControl(ctx, globalTarget, 9, `{"key_limit":9,"channel_limit":9}`); err != nil {
		t.Fatalf("seed stale global control: %v", err)
	}
	if err := reconcileAllRuntimeControls(ctx, pool, settings, controls, telemetry, runtimeControlStrictMode); err == nil {
		t.Fatal("strict periodic reconciliation overwrote startup-only drift")
	}
	stale, err := controls.ReadControl(ctx, channelTarget, 1)
	if err != nil || stale.ActiveRevision != 9 || stale.PendingRevision != 10 {
		t.Fatalf("strict reconciliation changed stale channel: %+v err=%v", stale, err)
	}
	if err := reconcileAllRuntimeControls(ctx, pool, settings, controls, telemetry, runtimeControlStartupAuthority); err != nil {
		t.Fatalf("startup authoritative reconciliation: %v", err)
	}
	repaired, err := controls.ReadControl(ctx, channelTarget, 1)
	if err != nil || repaired.SyncState != "active" || repaired.ActiveRevision != 1 || repaired.PendingRevision != 0 ||
		repaired.ActivePayload != `{"rpm":null,"rpd":null,"tpm":null,"concurrency":null}` {
		t.Fatalf("unexpected repaired channel control: %+v err=%v", repaired, err)
	}
	global, err := controls.ReadControl(ctx, globalTarget, 1)
	if err != nil || global.SyncState != "active" || global.ActiveRevision != 1 || global.ActivePayload != `{"key_limit":0,"channel_limit":0}` {
		t.Fatalf("unexpected repaired global control: %+v err=%v", global, err)
	}

	if err := reconcileAllRuntimeControls(ctx, pool, settings, controls, telemetry, runtimeControlStrictMode); err != nil {
		t.Fatalf("idempotent reconcile runtime controls: %v", err)
	}
}
