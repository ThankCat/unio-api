package bootstrap

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"
	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"

	"github.com/ThankCat/unio-gateway/internal/core/runtimecontrol"
	"github.com/ThankCat/unio-gateway/internal/platform/observability/metrics"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

func TestRuntimeControlTelemetryPublishesRecoveryFacts(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	recorder := metrics.New()
	core, logs := observer.New(zap.DebugLevel)
	telemetry := newRuntimeControlTelemetry(recorder, zap.New(core))
	telemetry.now = func() time.Time { return now }

	runtimeOperation := sqlc.RuntimeControlOperation{
		Kind:            runtimecontrol.KindAppSetting,
		SettingKey:      pgtype.Text{String: appsettings.GatewayRouteRateLimitDefaultsKey, Valid: true},
		CurrentRevision: 2,
		NextRevision:    3,
		PayloadHash:     "0123456789abcdef",
		State:           "db_committed",
		CreatedAt:       pgtype.Timestamptz{Time: now.Add(-5 * time.Second), Valid: true},
	}
	envelope := runtimecontrol.EndpointRoutingEnvelope{
		Kind:                  runtimecontrol.EndpointFenceKindBaseURL,
		ProviderID:            11,
		CurrentProviderStatus: "enabled",
		NextProviderStatus:    "enabled",
		Transitions: []runtimecontrol.EndpointRoutingTransition{{
			EndpointID:             23,
			CurrentBaseURLRevision: 1,
			NextBaseURLRevision:    2,
			CurrentStatusRevision:  1,
			NextStatusRevision:     1,
			CurrentEffectiveStatus: "enabled",
			NextEffectiveStatus:    "enabled",
		}},
	}
	endpointOperation := endpointOperationObservation{
		operation: sqlc.EndpointRoutingOperation{
			Kind: "base_url", State: "prepared", PayloadHash: "fedcba9876543210",
		},
		envelope: envelope,
		age:      7 * time.Second,
	}
	observation := runtimeControlReconcileObservation{
		runtimeOperations:  []sqlc.RuntimeControlOperation{runtimeOperation},
		endpointOperations: []endpointOperationObservation{endpointOperation},
	}

	telemetry.observeRuntimePending(observation.runtimeOperations)
	telemetry.observeEndpointPending(envelope, endpointOperation.age)
	body := scrapeRuntimeControlMetrics(t, recorder)
	for _, want := range []string{
		`unio_gateway_runtime_control_pending{target="route_rate"} 1`,
		`unio_gateway_runtime_control_pending_seconds{target="route_rate"} 5`,
		`unio_gateway_endpoint_base_url_revision_fence{endpoint_id="23",state="pending"} 1`,
		`unio_gateway_endpoint_base_url_revision_pending_seconds{endpoint_id="23"} 7`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("pending metrics missing %q\n%s", want, body)
		}
	}

	telemetry.passSucceeded(observation)
	telemetry.EndpointControlReconciled(23, 2, 1, "enabled", true)
	telemetry.criticalSettingReconciled(appsettings.GatewayRouteRateLimitDefaultsKey, 3, true)
	telemetry.channelControlReconciled(42, 4, true)
	body = scrapeRuntimeControlMetrics(t, recorder)
	for _, want := range []string{
		`unio_gateway_runtime_control_pending{target="route_rate"} 0`,
		`unio_gateway_runtime_control_recovery_total{result="committed",target="route_rate"} 1`,
		`unio_gateway_runtime_control_recovery_total{result="restored",target="channel_admission"} 1`,
		`unio_gateway_endpoint_base_url_revision_fence{endpoint_id="23",state="active"} 1`,
		`unio_gateway_endpoint_base_url_revision_pending_seconds{endpoint_id="23"} 0`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("recovery metrics missing %q\n%s", want, body)
		}
	}
	if logs.FilterMessage("runtime control operation reconciled").Len() != 1 ||
		logs.FilterMessage("endpoint routing operation reconciled").Len() != 1 ||
		logs.FilterMessage("endpoint runtime control restored").Len() != 1 {
		t.Fatalf("missing structured recovery logs: %+v", logs.All())
	}
}

func TestRuntimeControlTelemetryRateLimitsRepeatedFailures(t *testing.T) {
	now := time.Date(2026, time.July, 22, 12, 0, 0, 0, time.UTC)
	core, logs := observer.New(zap.DebugLevel)
	telemetry := newRuntimeControlTelemetry(nil, zap.New(core))
	telemetry.now = func() time.Time { return now }
	err := errors.New("redis unavailable")

	telemetry.logFailure("endpoint_routing", err)
	now = now.Add(5 * time.Second)
	telemetry.logFailure("endpoint_routing", err)
	if logs.Len() != 1 {
		t.Fatalf("repeated failure should be suppressed, got %d logs", logs.Len())
	}

	now = now.Add(runtimeControlFailureLogInterval)
	telemetry.logFailure("endpoint_routing", err)
	if logs.Len() != 2 {
		t.Fatalf("failure should be emitted after sampling interval, got %d logs", logs.Len())
	}
	if got := logs.All()[1].ContextMap()["suppressed_failures"]; got != int64(1) {
		t.Fatalf("suppressed_failures=%v, want 1", got)
	}
}

func TestRuntimeStateEpochEnsurePublishesStateLossOutcome(t *testing.T) {
	recorder := metrics.New()
	core, logs := observer.New(zap.DebugLevel)
	observeRuntimeStateEpochEnsure(recorder, zap.New(core), runtimecontrol.StateEpochEnsureResult{
		State:          runtimecontrol.StateEpochEnsureAwaitingMaintenance,
		OperationToken: "state-loss-operation",
		Record: runtimecontrol.StateEpochRecord{
			Value: runtimecontrol.StateEpoch{
				State:  runtimecontrol.StateEpochRecovering,
				Reason: runtimecontrol.StateEpochReasonStateLoss,
			},
			Revision: 2,
		},
	})

	body := scrapeRuntimeControlMetrics(t, recorder)
	for _, want := range []string{
		`unio_gateway_runtime_state_integrity{state="lost"} 1`,
		`unio_gateway_runtime_state_loss_recovery_total{result="awaiting_maintenance"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("state-loss metrics missing %q\n%s", want, body)
		}
	}
	if logs.FilterMessage("runtime state epoch not ready").Len() != 1 {
		t.Fatalf("missing state-loss structured log: %+v", logs.All())
	}
}

func TestRuntimeStateEpochEnsureDoesNotRecountExistingReadyEpoch(t *testing.T) {
	recorder := metrics.New()
	observeRuntimeStateEpochEnsure(recorder, nil, runtimecontrol.StateEpochEnsureResult{
		State: runtimecontrol.StateEpochEnsureReady,
		Record: runtimecontrol.StateEpochRecord{
			Value: runtimecontrol.StateEpoch{
				State:  runtimecontrol.StateEpochReady,
				Reason: runtimecontrol.StateEpochReasonRestore,
			},
			Revision: 2,
		},
	})

	body := scrapeRuntimeControlMetrics(t, recorder)
	if strings.Contains(body, "unio_gateway_runtime_state_loss_recovery_total") {
		t.Fatalf("existing ready epoch must not be counted as a new recovery\n%s", body)
	}
}

func scrapeRuntimeControlMetrics(t *testing.T, recorder *metrics.Metrics) string {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	rec := httptest.NewRecorder()
	recorder.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("metrics status=%d", rec.Code)
	}
	return rec.Body.String()
}
