package gatewayruntime

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"go.uber.org/zap"

	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

func TestSnapshotMergesClosedHealthWorstWins(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	first := breakerServer(t, "token", lifecycle.ChannelBreakerSnapshot{
		Enabled: true, Instance: "gw-a", ObservedAt: now,
		Channels: []lifecycle.ChannelBreakerEntry{{ChannelID: 7, State: lifecycle.CircuitStateClosed, HealthScore: 0.2, ErrorRate: 0.1, LatencyEWMAMs: 100}},
	})
	defer first.Close()
	second := breakerServer(t, "token", lifecycle.ChannelBreakerSnapshot{
		Enabled: true, Instance: "gw-b", ObservedAt: now.Add(time.Second),
		Channels: []lifecycle.ChannelBreakerEntry{{ChannelID: 7, State: lifecycle.CircuitStateClosed, HealthScore: 0.7, ErrorRate: 0.6, LatencyEWMAMs: 900}},
	})
	defer second.Close()

	client := NewClient([]string{first.URL, second.URL}, "token", zap.NewNop())
	snapshot := client.Snapshot(context.Background())
	got := snapshot.Channels[7]
	if !snapshot.Available || len(snapshot.Sources) != 2 || got.HealthScore != 0.7 || got.ErrorRate != 0.6 || got.LatencyEWMAMs != 900 {
		t.Fatalf("unexpected merged snapshot: %+v channel=%+v", snapshot, got)
	}
	encoded, err := json.Marshal(snapshot)
	if err != nil {
		t.Fatalf("marshal snapshot: %v", err)
	}
	if strings.Contains(string(encoded), first.URL) || strings.Contains(string(encoded), second.URL) {
		t.Fatalf("snapshot must not expose complete gateway URLs: %s", encoded)
	}
}

func TestSnapshotReportsPartialGatewayFailure(t *testing.T) {
	now := time.Now().UTC()
	healthy := breakerServer(t, "token", lifecycle.ChannelBreakerSnapshot{Enabled: true, Instance: "gw-ok", ObservedAt: now})
	defer healthy.Close()
	client := NewClient([]string{healthy.URL, "http://127.0.0.1:1"}, "token", zap.NewNop())
	snapshot := client.Snapshot(context.Background())
	if snapshot.Available || len(snapshot.Sources) != 2 {
		t.Fatalf("partial source failure must be visible: %+v", snapshot)
	}
}

func breakerServer(t *testing.T, token string, snapshot lifecycle.ChannelBreakerSnapshot) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/internal/v1/circuit-breaker" || r.Header.Get("X-Unio-Internal-Token") != token {
			http.Error(w, "unauthorized", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(snapshot)
	}))
}
