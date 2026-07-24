package p4fault_test

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"testing"
	"time"
)

func TestP4FaultE2E(t *testing.T) {
	if os.Getenv("P4_FAULT_E2E") != "1" {
		t.Skip("set P4_FAULT_E2E=1 to run the isolated P4 fault suite")
	}

	h := setupFaultHarness(t)
	mustRun(t, "maintenance_marker_endpoint_recovery_smoke_release", func(t *testing.T) {
		runStateLossMaintenanceE2E(t, h, maintenanceMarkerEndpointLoss)
	})
	mustRun(t, "baseline_six_protocol_modes", func(t *testing.T) {
		before := h.upstream.snapshot()
		for index, mode := range allProtocolModes {
			status, body := h.request(t, h.gateways[index%len(h.gateways)], mode)
			if status != http.StatusOK {
				t.Fatalf("%s baseline status=%d want=200 body=%s", mode, status, body)
			}
		}
		after := h.upstream.snapshot()
		if after.total-before.total != int64(len(allProtocolModes)) {
			t.Fatalf("baseline upstream calls=%d want=%d", after.total-before.total, len(allProtocolModes))
		}
		for _, mode := range allProtocolModes {
			if after.byMode[mode]-before.byMode[mode] != 1 {
				t.Fatalf("%s baseline upstream calls=%d want=1", mode, after.byMode[mode]-before.byMode[mode])
			}
		}
	})

	mustRun(t, "each_rate_control_loss_is_shared_and_auto_repaired", func(t *testing.T) {
		for _, suffix := range []string{"route-rate-limits", "channel-rate-limits"} {
			key := h.namespace + ":admission:v1:" + suffix
			for _, gateway := range h.gateways {
				h.redisDelete(t, key)
				h.assertReadiness(t, gateway, http.StatusServiceUnavailable)
			}
			assertSixModesRejectedWithoutUpstream(t, h, func(t *testing.T) {
				h.redisDelete(t, key)
			})
			for _, gateway := range h.gateways {
				h.waitReadiness(t, gateway, http.StatusOK, 8*time.Second)
			}
		}
	})

	mustRun(t, "redis_stop_restart_preserves_epoch_and_fails_closed", func(t *testing.T) {
		markerKey := h.namespace + ":runtime-control:v1:state-integrity-marker"
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		epochBefore, err := h.redis.HGet(ctx, markerKey, "epoch").Result()
		cancel()
		if err != nil || epochBefore == "" {
			t.Fatalf("read epoch before redis restart: epoch_empty=%v err=%v", epochBefore == "", err)
		}

		h.infra.stopRedis(t)
		for _, gateway := range h.gateways {
			h.waitReadiness(t, gateway, http.StatusServiceUnavailable, 5*time.Second)
		}
		assertSixModesRejectedWithoutUpstream(t, h, nil)

		h.infra.startRedis(t)
		h.waitRedis(t, 20*time.Second)
		for _, gateway := range h.gateways {
			h.waitReadiness(t, gateway, http.StatusOK, 15*time.Second)
		}
		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		epochAfter, err := h.redis.HGet(ctx, markerKey, "epoch").Result()
		cancel()
		if err != nil {
			t.Fatalf("read epoch after redis restart: %v", err)
		}
		if epochAfter != epochBefore {
			t.Fatalf("redis restart changed runtime epoch: before=%q after=%q", epochBefore, epochAfter)
		}
	})

	mustRun(t, "breaker_opened_on_a_blocks_b", func(t *testing.T) {
		h.upstream.setFailure(true)
		defer h.upstream.setFailure(false)

		for attempt := 1; attempt <= 3; attempt++ {
			before := h.upstream.snapshot()
			status, body := h.request(t, h.gateways[0], modeOpenAIChatNonStream)
			if status < 500 {
				t.Fatalf("breaker trigger attempt %d status=%d want 5xx body=%s", attempt, status, body)
			}
			after := h.upstream.snapshot()
			if after.total != before.total+1 {
				t.Fatalf("breaker trigger attempt %d upstream calls delta=%d want=1", attempt, after.total-before.total)
			}
		}

		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		state, err := h.redis.HGet(
			ctx,
			h.namespace+":breaker:v2:channel:"+formatID(h.seed.openAIChannelID),
			"state",
		).Result()
		cancel()
		if err != nil || state != "open" {
			t.Fatalf("shared channel breaker state=%q want=open err=%v", state, err)
		}

		before := h.upstream.snapshot()
		status, body := h.request(t, h.gateways[1], modeOpenAIChatNonStream)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("gateway B breaker status=%d want=503 body=%s", status, body)
		}
		after := h.upstream.snapshot()
		if after.total != before.total {
			t.Fatalf("gateway B reached upstream after gateway A opened breaker: delta=%d", after.total-before.total)
		}
	})

	mustRun(t, "flushdb_state_loss_stays_not_ready_and_calls_no_upstream", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		err := h.redis.FlushDB(ctx).Err()
		cancel()
		if err != nil {
			t.Fatalf("flush isolated redis: %v", err)
		}
		for _, gateway := range h.gateways {
			h.waitReadiness(t, gateway, http.StatusServiceUnavailable, 5*time.Second)
		}
		assertSixModesRejectedWithoutUpstream(t, h, nil)

		// Cross one full reconciliation interval. Controls may be restored, but the
		// integrity marker must never be recreated without the maintenance flow.
		time.Sleep(6 * time.Second)
		markerKey := h.namespace + ":runtime-control:v1:state-integrity-marker"
		ctx, cancel = context.WithTimeout(context.Background(), time.Second)
		exists, err := h.redis.Exists(ctx, markerKey).Result()
		cancel()
		if err != nil {
			t.Fatalf("check marker after flush: %v", err)
		}
		if exists != 0 {
			t.Fatalf("runtime integrity marker was recreated after FLUSHDB without maintenance")
		}
		for _, gateway := range h.gateways {
			h.assertReadiness(t, gateway, http.StatusServiceUnavailable)
		}
	})
}

func assertSixModesRejectedWithoutUpstream(
	t *testing.T,
	h *faultHarness,
	beforeEach func(*testing.T),
) {
	t.Helper()
	before := h.upstream.snapshot()
	for index, mode := range allProtocolModes {
		if beforeEach != nil {
			beforeEach(t)
		}
		status, body := h.request(t, h.gateways[index%len(h.gateways)], mode)
		if status != http.StatusServiceUnavailable {
			t.Fatalf("%s fault status=%d want=503 body=%s", mode, status, body)
		}
	}
	after := h.upstream.snapshot()
	if after.total != before.total {
		t.Fatalf("fault batch reached upstream: before=%d after=%d", before.total, after.total)
	}
	for _, mode := range allProtocolModes {
		if after.byMode[mode] != before.byMode[mode] {
			t.Fatalf("fault batch reached upstream for %s: before=%d after=%d", mode, before.byMode[mode], after.byMode[mode])
		}
	}
}

func mustRun(t *testing.T, name string, fn func(*testing.T)) {
	t.Helper()
	if !t.Run(name, fn) {
		t.FailNow()
	}
}

func formatID(id int64) string {
	return fmt.Sprintf("%d", id)
}
