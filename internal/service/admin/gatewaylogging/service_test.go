package gatewaylogging

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/ThankCat/unio-gateway/internal/platform/logging"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
)

type controlStub struct {
	snapshot   appsettings.GatewayLoggingControlSnapshot
	startCalls int
}

func (s *controlStub) GatewayLoggingControl(context.Context) (appsettings.GatewayLoggingControlSnapshot, error) {
	return s.snapshot, nil
}

func (s *controlStub) StartGatewayDebugSession(context.Context, time.Duration, string, int64, time.Time) (appsettings.GatewayLoggingControlSnapshot, error) {
	s.startCalls++
	return s.snapshot, nil
}

func (s *controlStub) StopGatewayDebugSession(context.Context, int64, time.Time) (appsettings.GatewayLoggingControlSnapshot, error) {
	return s.snapshot, nil
}

func TestSnapshotDistinguishesAppliedAndPendingInstances(t *testing.T) {
	now := time.Date(2026, time.August, 1, 8, 0, 0, 0, time.UTC)
	control := &controlStub{snapshot: appsettings.GatewayLoggingControlSnapshot{
		Setting: appsettings.GatewayLoggingDebugSessionSetting{
			SessionID: "dbg_1", StartedAt: now, ExpiresAt: now.Add(15 * time.Minute),
			Reason: "investigate", Revision: 4,
		},
	}}
	applied := gatewayStatusServer(t, "internal-token", logging.GatewayStatus{
		InstanceID: "gw-1", Environment: "production", BaselineLevel: "info", EffectiveLevel: "debug",
		DebugSessionID: "dbg_1", AppliedRevision: 4,
	})
	defer applied.Close()
	pending := gatewayStatusServer(t, "internal-token", logging.GatewayStatus{
		InstanceID: "gw-2", Environment: "production", BaselineLevel: "info", EffectiveLevel: "info",
		AppliedRevision: 3,
	})
	defer pending.Close()

	service := NewService(control, http.DefaultClient, []string{applied.URL, pending.URL}, "internal-token", "http://loki.local")
	service.now = func() time.Time { return now }
	snapshot, err := service.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snapshot.Mode != "debug" || len(snapshot.Instances) != 2 ||
		snapshot.Instances[0].State != "applied" || snapshot.Instances[1].State != "pending" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestSnapshotReportsEnvironmentDebugWithoutControlSession(t *testing.T) {
	control := &controlStub{snapshot: appsettings.GatewayLoggingControlSnapshot{
		Setting: appsettings.GatewayLoggingDebugSessionSetting{Revision: 2},
	}}
	server := gatewayStatusServer(t, "", logging.GatewayStatus{
		InstanceID: "dev", Environment: "development", BaselineLevel: "debug", EffectiveLevel: "debug",
	})
	defer server.Close()
	service := NewService(control, http.DefaultClient, []string{server.URL}, "", "")
	snapshot, err := service.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snapshot.Mode != "environment_debug" || snapshot.Instances[0].State != "environment_debug" {
		t.Fatalf("snapshot = %+v", snapshot)
	}
}

func TestSnapshotEnvironmentDebugOverridesPersistedSession(t *testing.T) {
	now := time.Date(2026, time.August, 1, 8, 0, 0, 0, time.UTC)
	control := &controlStub{snapshot: appsettings.GatewayLoggingControlSnapshot{
		Setting: appsettings.GatewayLoggingDebugSessionSetting{
			SessionID: "dbg_stale", StartedAt: now, ExpiresAt: now.Add(15 * time.Minute),
			Reason: "persisted before restart", Revision: 4,
		},
	}}
	server := gatewayStatusServer(t, "", logging.GatewayStatus{
		InstanceID: "dev", Environment: "development", BaselineLevel: "debug", EffectiveLevel: "debug",
	})
	defer server.Close()
	service := NewService(control, http.DefaultClient, []string{server.URL}, "", "")
	service.now = func() time.Time { return now }

	snapshot, err := service.Get(context.Background())
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if snapshot.Mode != "environment_debug" {
		t.Fatalf("mode = %q", snapshot.Mode)
	}
}

func TestStartRejectsEnvironmentDebugWithoutCreatingControlSession(t *testing.T) {
	control := &controlStub{snapshot: appsettings.GatewayLoggingControlSnapshot{
		Setting: appsettings.GatewayLoggingDebugSessionSetting{Revision: 2},
	}}
	server := gatewayStatusServer(t, "", logging.GatewayStatus{
		InstanceID: "dev", Environment: "development", BaselineLevel: "debug", EffectiveLevel: "debug",
	})
	defer server.Close()
	service := NewService(control, http.DefaultClient, []string{server.URL}, "", "")

	_, err := service.Start(context.Background(), 15, "investigate", 0)
	if !errors.Is(err, appsettings.ErrGatewayDebugRequestInvalid) {
		t.Fatalf("error = %v, want invalid debug request", err)
	}
	if control.startCalls != 0 {
		t.Fatalf("control start calls = %d, want 0", control.startCalls)
	}
}

func gatewayStatusServer(t *testing.T, expectedToken string, status logging.GatewayStatus) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if expectedToken != "" && r.Header.Get("Authorization") != "Bearer "+expectedToken {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(status)
	}))
}
