package routeruntime

import (
	"context"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/ratelimit"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/gatewayruntime"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

type fakeRuntimeStore struct {
	route sqlc.Route
	pool  []sqlc.RouteRuntimePoolRow
	stats []sqlc.RouteRuntimeChannelStatsRow
}

func (s *fakeRuntimeStore) GetRouteByID(context.Context, int64) (sqlc.Route, error) {
	return s.route, nil
}

func (s *fakeRuntimeStore) RouteRuntimePool(context.Context, sqlc.RouteRuntimePoolParams) ([]sqlc.RouteRuntimePoolRow, error) {
	return s.pool, nil
}

func (s *fakeRuntimeStore) RouteRuntimeChannelStats(context.Context, sqlc.RouteRuntimeChannelStatsParams) ([]sqlc.RouteRuntimeChannelStatsRow, error) {
	return s.stats, nil
}

type fakeGatewaySnapshotter struct {
	snapshot gatewayruntime.Snapshot
}

func (s fakeGatewaySnapshotter) Snapshot(context.Context) gatewayruntime.Snapshot { return s.snapshot }

type fakeSlidingStore struct {
	count int64
}

func (s fakeSlidingStore) CheckAndAdd(context.Context, string, int64, time.Duration, time.Duration, int64) (ratelimit.CountResult, error) {
	return ratelimit.CountResult{Allowed: true, Count: s.count}, nil
}

func (s fakeSlidingStore) CheckThenAdd(context.Context, string, int64, time.Duration, time.Duration, int64) (ratelimit.CountResult, error) {
	return ratelimit.CountResult{Allowed: true, Count: s.count}, nil
}

func (s fakeSlidingStore) Add(context.Context, string, time.Duration, time.Duration, int64) error {
	return nil
}

func TestRuntimeUsesSharedScorerAndHardFilters(t *testing.T) {
	now := time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC)
	store := &fakeRuntimeStore{
		route: sqlc.Route{ID: 3, Mode: "balanced", Status: "enabled"},
		pool: []sqlc.RouteRuntimePoolRow{
			{
				RouteID: 3, Mode: "balanced", RouteStatus: "enabled", ChannelID: 7, ChannelName: "primary",
				ChannelStatus: "enabled", CredentialValid: true, Protocol: "openai", AdapterKey: "openai",
				HasCredential: true, HasBaseUrl: true,
				TpmLimit: pgtype.Int4{Int32: 100, Valid: true}, ConcurrencyLimit: pgtype.Int4{Int32: 4, Valid: true},
				ProviderID: 1, ProviderName: "provider-a", ProviderStatus: "enabled",
				ModelExists: true, ModelStatus: "enabled", BindingStatus: "enabled", HasModelPrice: true, HasChannelCost: true,
			},
			{
				RouteID: 3, Mode: "balanced", RouteStatus: "enabled", ChannelID: 8, ChannelName: "disabled-provider",
				ChannelStatus: "enabled", CredentialValid: true, Protocol: "openai", AdapterKey: "openai",
				HasCredential: true, HasBaseUrl: true,
				ProviderID: 2, ProviderName: "provider-b", ProviderStatus: "disabled",
				ModelExists: true, ModelStatus: "enabled", BindingStatus: "enabled", HasModelPrice: true, HasChannelCost: true,
			},
		},
		stats: []sqlc.RouteRuntimeChannelStatsRow{{ChannelID: 7, Selected1m: 3, Selected5m: 6, Fallback1m: 1}},
	}
	concurrency := ratelimit.NewConcurrencyLimiter(0, 0)
	release1, ok1 := concurrency.AcquireChannel(7, int64Ptr(4))
	release2, ok2 := concurrency.AcquireChannel(7, int64Ptr(4))
	if !ok1 || !ok2 {
		t.Fatal("failed to prepare in-memory concurrency usage")
	}
	defer release1()
	defer release2()
	guard := ratelimit.NewGuard(fakeSlidingStore{count: 25}, ratelimit.DefaultLimits{}, false, nil)
	health := fakeGatewaySnapshotter{snapshot: gatewayruntime.Snapshot{
		Available: true, ObservedAt: now, Sources: []gatewayruntime.SourceStatus{{ID: "gw-1", Available: true, ObservedAt: now}},
		Channels: map[int64]gatewayruntime.ChannelStatus{
			7: {State: lifecycle.CircuitStateClosed, HealthScore: 0.2, ErrorRate: 0.1, LatencyEWMAMs: 250, ObservedAt: now},
		},
	}}
	service := NewService(store, concurrency, guard, health, nil)
	service.now = func() time.Time { return now }

	got, err := service.Get(context.Background(), Params{RouteID: 3, ModelID: "openai/gpt", Protocol: "openai"})
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	if got.Stale || got.PoolSize != 2 || got.CandidateCount != 1 || !got.NoRedundancy {
		t.Fatalf("unexpected route runtime: %+v", got)
	}
	primary := got.Channels[0]
	if !primary.Eligible || primary.ConcurrencyUsed != 2 || primary.ConcurrencyLimit != 4 || primary.TPMUsed != 25 || primary.TPMLimit != 100 {
		t.Fatalf("unexpected primary capacity: %+v", primary)
	}
	if primary.CapacityScore != 0.5 || primary.HealthFactor != 0.8 || primary.FinalWeight != 0.4 {
		t.Fatalf("runtime score drifted from scheduler: %+v", primary)
	}
	if got.Channels[1].ExcludedReason != "provider_disabled" || got.Channels[1].FinalWeight != 0 {
		t.Fatalf("unexpected hard filter: %+v", got.Channels[1])
	}
	subject := ratelimit.ChannelInflightSubject(7)
	if concurrency.Inflight(subject) != 2 {
		t.Fatalf("runtime read must not reserve capacity, inflight=%d", concurrency.Inflight(subject))
	}
}

func TestRuntimeMarksMissingSourcesStale(t *testing.T) {
	store := &fakeRuntimeStore{
		route: sqlc.Route{ID: 4, Mode: "balanced", Status: "enabled"},
		pool: []sqlc.RouteRuntimePoolRow{{
			RouteID: 4, Mode: "balanced", RouteStatus: "enabled", ChannelID: 9, ChannelName: "only",
			ChannelStatus: "enabled", CredentialValid: true, HasCredential: true, HasBaseUrl: true, Protocol: "openai", ProviderStatus: "enabled",
		}},
	}
	service := NewService(store, nil, nil, nil, nil)
	service.now = func() time.Time { return time.Date(2026, 7, 19, 12, 0, 0, 0, time.UTC) }
	got, err := service.Get(context.Background(), Params{RouteID: 4})
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	if !got.Stale || !got.CapacityDegraded || !got.Channels[0].CapacityReadFailed {
		t.Fatalf("missing runtime sources must be stale/degraded: %+v", got)
	}
}

func int64Ptr(value int64) *int64 { return &value }
