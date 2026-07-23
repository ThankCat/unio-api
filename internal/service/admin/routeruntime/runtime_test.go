package routeruntime

import (
	"context"
	"math"
	"math/big"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

type fakeRuntimeStore struct {
	route     sqlc.Route
	pool      []sqlc.RouteRuntimePoolRow
	stats     []sqlc.RouteRuntimeChannelStatsRow
	poolCalls int
}

func (s *fakeRuntimeStore) GetRouteByID(context.Context, int64) (sqlc.Route, error) {
	return s.route, nil
}

func (s *fakeRuntimeStore) RouteRuntimePool(context.Context, sqlc.RouteRuntimePoolParams) ([]sqlc.RouteRuntimePoolRow, error) {
	s.poolCalls++
	return s.pool, nil
}

func (s *fakeRuntimeStore) RouteRuntimeChannelStats(context.Context, sqlc.RouteRuntimeChannelStatsParams) ([]sqlc.RouteRuntimeChannelStatsRow, error) {
	return s.stats, nil
}

type fakeRuntimeFacts struct {
	admission    runtimefacts.AdmissionRevisions
	routing      runtimefacts.RoutingRevisions
	admissionErr error
	routingErr   error
}

func (f *fakeRuntimeFacts) Admission(context.Context) (runtimefacts.AdmissionRevisions, error) {
	return f.admission, f.admissionErr
}

func (f *fakeRuntimeFacts) Routing(context.Context) (runtimefacts.RoutingRevisions, error) {
	return f.routing, f.routingErr
}

type fakeBreakerSnapshotter struct {
	result breakerstore.SnapshotManyResult
	err    error
	input  breakerstore.SnapshotManyInput
	calls  int
}

func (f *fakeBreakerSnapshotter) SnapshotMany(_ context.Context, input breakerstore.SnapshotManyInput) (breakerstore.SnapshotManyResult, error) {
	f.calls++
	f.input = input
	return f.result, f.err
}

func TestRuntimeUsesAuthoritativeSnapshotAndP4Score(t *testing.T) {
	now := time.Date(2026, 7, 22, 12, 0, 0, 0, time.UTC)
	store := &fakeRuntimeStore{
		route: sqlc.Route{ID: 3, Mode: "balanced", Status: "enabled"},
		pool: []sqlc.RouteRuntimePoolRow{
			runtimePoolRow(7, 21, 31),
			runtimePoolRow(8, 22, 31),
		},
		stats: []sqlc.RouteRuntimeChannelStatsRow{
			{ChannelID: 7, Selected1m: 3, Selected5m: 6, Fallback1m: 1},
			{ChannelID: 8, Selected1m: 1, Selected5m: 2},
		},
	}
	store.pool[1].ProviderStatus = "disabled"
	facts := readyRuntimeFacts()
	breakers := &fakeBreakerSnapshotter{result: breakerstore.SnapshotManyResult{
		RoutingBalance: breakerstore.RoutingBalanceSnapshot{
			Revision: 5, TTFTTargetMs: 2000, TTFTWeight: 0.35, MinimumRoutingFactor: 0.05,
		},
		Candidates: []breakerstore.CandidateSnapshot{
			{
				Candidate: breakerstore.SnapshotCandidateInput{
					EndpointID: 21, ChannelID: 7, EndpointBaseURLRevision: 11,
					EndpointStatusRevision: 12, ChannelConfigRevision: 16, ChannelAdmissionRevision: 17,
				},
				Status: breakerstore.CandidateSnapshotCurrent,
				Endpoint: breakerstore.ScopeSnapshot{
					Exists: true, State: breakerstore.StateClosed, SampleCount: 20,
					BaseURLRevision: 11, StatusRevision: 12, StateGeneration: 6,
					BaseURLFenceGeneration: 3, StatusFenceGeneration: 4,
				},
				Channel: breakerstore.ScopeSnapshot{
					Exists: true, State: breakerstore.StateClosed, ErrorRate: 0.1, SampleCount: 20,
					TTFTEWMAMs: 1000, TTFTSamples: 18, ChannelConfigRevision: 16,
				},
				Concurrency:         breakerstore.CapacityUsage{Used: 2, Limit: 4},
				RPM:                 breakerstore.CapacityUsage{Used: 3, Limit: 10},
				RPD:                 breakerstore.CapacityUsage{Used: 30, Limit: 100},
				TPM:                 breakerstore.CapacityUsage{Used: 25, Limit: 100},
				CooldownRemainingMs: 2500, ModelPermissionPaused: true,
				ModelPermissionRecheckState: "queued",
			},
			{
				Status:   breakerstore.CandidateSnapshotNoSample,
				Endpoint: breakerstore.ScopeSnapshot{Exists: true, State: breakerstore.StateClosed},
				Channel: breakerstore.ScopeSnapshot{
					Exists: true, State: breakerstore.StateOpen, OpenRemainingMs: 5000,
					ErrorRate: 1, SampleCount: 9, TTFTEWMAMs: 9000, TTFTSamples: 9,
				},
				Concurrency: breakerstore.CapacityUsage{Limit: 0},
				TPM:         breakerstore.CapacityUsage{Limit: 0},
			},
		},
	}}
	service := NewService(store, facts, breakers)
	service.now = func() time.Time { return now }

	got, err := service.Get(context.Background(), Params{RouteID: 3, ModelID: "openai/gpt", Protocol: "openai"})
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	if got.Stale || got.RuntimeSyncState != runtimeSyncActive || got.BreakerStoreAdmission != breakerAdmissionNormal {
		t.Fatalf("unexpected authority state: %+v", got)
	}
	if got.PoolSize != 2 || got.CandidateCount != 1 || !got.NoRedundancy {
		t.Fatalf("unexpected route runtime: %+v", got)
	}
	if breakers.calls != 1 || breakers.input.ModelID != 31 || breakers.input.IntegrityEpoch != "epoch-a" ||
		breakers.input.ChannelRateRevision != 7 || breakers.input.GlobalConcurrencyRevision != 3 ||
		breakers.input.CircuitBreakerRevision != 4 || breakers.input.RoutingBalanceRevision != 5 {
		t.Fatalf("unexpected SnapshotMany input: %+v", breakers.input)
	}
	if len(breakers.input.Candidates) != 2 || breakers.input.Candidates[0].EndpointID != 21 ||
		breakers.input.Candidates[0].ChannelAdmissionRevision != 17 {
		t.Fatalf("candidate revisions not forwarded: %+v", breakers.input.Candidates)
	}

	primary := got.Channels[0]
	if !primary.Eligible || primary.ConcurrencyUsed != 2 || primary.ConcurrencyLimit != 4 ||
		primary.RPMUsed != 3 || primary.RPMLimit != 10 || primary.RPDUsed != 30 || primary.RPDLimit != 100 ||
		primary.TPMUsed != 25 || primary.TPMLimit != 100 {
		t.Fatalf("unexpected primary capacity: %+v", primary)
	}
	if primary.RPMRemaining == nil || *primary.RPMRemaining != 0.7 ||
		primary.RPDRemaining == nil || *primary.RPDRemaining != 0.7 ||
		primary.CooldownRemainingMs != 2500 || !primary.ModelPermissionPaused ||
		primary.ModelPermissionRecheckState != "queued" {
		t.Fatalf("cooldown, permission, or rate facts missing: %+v", primary)
	}
	if !primary.EndpointBaseURLRevisionCurrent || !primary.EndpointStatusRevisionCurrent ||
		!primary.ChannelConfigRevisionCurrent || !primary.ChannelAdmissionRevisionCurrent ||
		!primary.RuntimeRevisionCurrent || primary.RuntimeControlState != runtimeSyncActive ||
		primary.RouteRateLimitsRevision != 2 || primary.ChannelRateLimitsRevision != 7 ||
		primary.GlobalConcurrencyRevision != 3 ||
		primary.CircuitBreakerRevision != 4 || primary.RoutingBalanceRevision != 5 {
		t.Fatalf("revision facts missing or stale: %+v", primary)
	}
	if math.Abs(primary.CapacityScore-0.5) > 1e-9 || math.Abs(primary.FinalWeight-0.3975) > 1e-9 {
		t.Fatalf("runtime score drifted from scheduler: %+v", primary)
	}
	if primary.CostRatio != nil || primary.CostWeight != 0 || primary.CostFactor != 1 {
		t.Fatalf("unresolved cost must remain neutral: %+v", primary)
	}
	if primary.ErrorRate == nil || *primary.ErrorRate != 0.1 || primary.ErrorSamples != 20 ||
		primary.TTFTEWMAMs == nil || *primary.TTFTEWMAMs != 1000 || primary.TTFTSamples != 18 ||
		primary.TTFTSampleSource != "stream_only" {
		t.Fatalf("unexpected P4 samples: %+v", primary)
	}
	if primary.EndpointBreakerState == nil || *primary.EndpointBreakerState != "closed" ||
		primary.ChannelBreakerState == nil || *primary.ChannelBreakerState != "closed" {
		t.Fatalf("unexpected breaker state: %+v", primary)
	}
	if got.Channels[1].ExcludedReason != "provider_disabled" || got.Channels[1].FinalWeight != 0 {
		t.Fatalf("unexpected hard filter: %+v", got.Channels[1])
	}
	if got.Channels[1].ChannelBreakerState != nil || got.Channels[1].ErrorRate != nil || got.Channels[1].ErrorSamples != 0 ||
		got.Channels[1].TTFTEWMAMs != nil || got.Channels[1].TTFTSamples != 0 {
		t.Fatalf("no-sample status leaked stale Channel facts: %+v", got.Channels[1])
	}
	if len(got.Sources) != 3 || got.Sources[1].Name != "breaker_store" || !got.Sources[1].Available {
		t.Fatalf("unexpected sources: %+v", got.Sources)
	}
}

func TestRuntimeResolvesAbsoluteAndMultiplierCosts(t *testing.T) {
	now := time.Date(2026, 7, 23, 12, 0, 0, 0, time.UTC)
	absolute := runtimePoolRow(7, 21, 31)
	setRuntimePriceBase(&absolute)
	absolute.ChannelPriceID = 11
	absolute.CostCurrency = "USD"
	absolute.CostPricingUnit = billing.PricingUnitPer1MTokens
	absolute.UncachedInputCost = testNumeric(5, 0)
	absolute.OutputCost = testNumeric(10, 0)
	multiplier := runtimePoolRow(8, 22, 31)
	setRuntimePriceBase(&multiplier)
	multiplier.ChannelCostMultiplierID = 12
	multiplier.CostMultiplier = testNumeric(1, 0)
	multiplier.ChannelRechargeFactorID = 13
	multiplier.RechargeFactor = testNumeric(5, -1)
	store := &fakeRuntimeStore{
		route: sqlc.Route{ID: 3, Mode: "balanced", Status: "enabled", PriceRatio: testNumeric(2, 0)},
		pool:  []sqlc.RouteRuntimePoolRow{absolute, multiplier},
	}
	result := breakerstore.SnapshotManyResult{
		RoutingBalance: breakerstore.RoutingBalanceSnapshot{
			Revision: 5, TTFTTargetMs: 2000, TTFTWeight: 0.35, CostWeight: 0.5, MinimumRoutingFactor: 0.05,
		},
		Candidates: []breakerstore.CandidateSnapshot{
			currentCostCandidate(7, 21),
			currentCostCandidate(8, 22),
		},
	}
	service := NewService(store, readyRuntimeFacts(), &fakeBreakerSnapshotter{result: result})
	service.now = func() time.Time { return now }

	got, err := service.Get(context.Background(), Params{RouteID: 3, ModelID: "openai/gpt", Protocol: "openai"})
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	if store.poolCalls != 1 {
		t.Fatalf("runtime pricing must come from one RouteRuntimePool batch, calls=%d", store.poolCalls)
	}
	for _, channel := range got.Channels {
		if channel.CostRatio == nil || math.Abs(*channel.CostRatio-0.25) > 1e-9 ||
			channel.CostWeight != 0.5 || math.Abs(channel.CostFactor-0.875) > 1e-9 ||
			math.Abs(channel.FinalWeight-0.875) > 1e-9 || channel.MarginStatus != "safe" {
			t.Errorf("channel %d cost score mismatch: %+v", channel.ChannelID, channel)
		}
	}
}

func TestRuntimeFixedModeReportsCostWithoutChangingWeight(t *testing.T) {
	row := runtimePoolRow(7, 21, 31)
	row.Mode = "fixed"
	setRuntimePriceBase(&row)
	row.ChannelPriceID = 11
	row.CostCurrency = "USD"
	row.CostPricingUnit = billing.PricingUnitPer1MTokens
	row.UncachedInputCost = testNumeric(5, 0)
	row.OutputCost = testNumeric(10, 0)
	store := &fakeRuntimeStore{
		route: sqlc.Route{ID: 3, Mode: "fixed", Status: "enabled", PriceRatio: testNumeric(2, 0)},
		pool:  []sqlc.RouteRuntimePoolRow{row},
	}
	result := breakerstore.SnapshotManyResult{
		RoutingBalance: breakerstore.RoutingBalanceSnapshot{
			Revision: 5, TTFTTargetMs: 2000, TTFTWeight: 0.35, CostWeight: 0.5, MinimumRoutingFactor: 0.05,
		},
		Candidates: []breakerstore.CandidateSnapshot{currentCostCandidate(7, 21)},
	}
	service := NewService(store, readyRuntimeFacts(), &fakeBreakerSnapshotter{result: result})

	got, err := service.Get(context.Background(), Params{RouteID: 3, ModelID: "openai/gpt", Protocol: "openai"})
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	channel := got.Channels[0]
	if channel.CostRatio == nil || math.Abs(*channel.CostRatio-0.25) > 1e-9 ||
		channel.CostWeight != 0.5 || channel.CostFactor != 1 || channel.FinalWeight != 1 {
		t.Fatalf("fixed route must keep cost factor neutral: %+v", channel)
	}
}

func TestRuntimeRejectsInvalidPricingLikeGateway(t *testing.T) {
	tests := []struct {
		name   string
		mutate func(*sqlc.RouteRuntimePoolRow)
	}{
		{
			name: "currency mismatch",
			mutate: func(row *sqlc.RouteRuntimePoolRow) {
				row.CostCurrency = "CNY"
			},
		},
		{
			name: "pricing unit mismatch",
			mutate: func(row *sqlc.RouteRuntimePoolRow) {
				row.CostPricingUnit = "per_1k_tokens"
			},
		},
		{
			name: "unparseable price",
			mutate: func(row *sqlc.RouteRuntimePoolRow) {
				row.UncachedInputCost = pgtype.Numeric{NaN: true, Valid: true}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			row := runtimePoolRow(7, 21, 31)
			setRuntimePriceBase(&row)
			row.ChannelPriceID = 11
			row.CostCurrency = "USD"
			row.CostPricingUnit = billing.PricingUnitPer1MTokens
			row.UncachedInputCost = testNumeric(5, 0)
			row.OutputCost = testNumeric(10, 0)
			test.mutate(&row)

			store := &fakeRuntimeStore{
				route: sqlc.Route{ID: 3, Mode: "balanced", Status: "enabled", PriceRatio: testNumeric(2, 0)},
				pool:  []sqlc.RouteRuntimePoolRow{row},
			}
			breakers := &fakeBreakerSnapshotter{result: breakerstore.SnapshotManyResult{
				RoutingBalance: breakerstore.RoutingBalanceSnapshot{
					Revision: 5, TTFTTargetMs: 2000, TTFTWeight: 0.35, CostWeight: 0.5, MinimumRoutingFactor: 0.05,
				},
				Candidates: []breakerstore.CandidateSnapshot{currentCostCandidate(7, 21)},
			}}
			service := NewService(store, readyRuntimeFacts(), breakers)

			got, err := service.Get(context.Background(), Params{RouteID: 3, ModelID: "openai/gpt", Protocol: "openai"})
			if err != nil {
				t.Fatalf("get runtime: %v", err)
			}
			channel := got.Channels[0]
			if channel.Eligible || channel.ExcludedReason != "pricing_invalid" ||
				channel.MarginStatus != "pricing_invalid" || channel.CostRatio != nil || channel.FinalWeight != 0 {
				t.Fatalf("invalid pricing must match Gateway exclusion: %+v", channel)
			}
			if got.CandidateCount != 0 {
				t.Fatalf("invalid pricing candidate must not be counted: %+v", got)
			}
		})
	}
}

func TestRuntimeMapsBreakerCooldownAndPermissionGates(t *testing.T) {
	store := &fakeRuntimeStore{route: sqlc.Route{ID: 4, Mode: "balanced", Status: "enabled"}}
	statuses := []breakerstore.CandidateSnapshotStatus{
		breakerstore.CandidateSnapshotOpen,
		breakerstore.CandidateSnapshotHalfOpenBusy,
		breakerstore.CandidateSnapshotRateLimited,
		breakerstore.CandidateSnapshotModelPermissionPaused,
		breakerstore.CandidateSnapshotHalfOpen,
	}
	result := breakerstore.SnapshotManyResult{
		RoutingBalance: breakerstore.RoutingBalanceSnapshot{
			Revision: 5, TTFTTargetMs: 2000, TTFTWeight: 0.35, MinimumRoutingFactor: 0.05,
		},
		Candidates: make([]breakerstore.CandidateSnapshot, len(statuses)),
	}
	for index, status := range statuses {
		store.pool = append(store.pool, runtimePoolRow(int64(index+1), int64(index+11), 31))
		result.Candidates[index] = breakerstore.CandidateSnapshot{
			Status: status,
			Endpoint: breakerstore.ScopeSnapshot{
				Exists: true, State: breakerstore.StateClosed, SampleCount: 2,
			},
			Channel:     breakerstore.ScopeSnapshot{Exists: true, State: breakerstore.StateClosed, SampleCount: 2},
			Concurrency: breakerstore.CapacityUsage{Limit: 10},
			TPM:         breakerstore.CapacityUsage{Limit: 100},
		}
	}
	result.Candidates[0].Endpoint.State = breakerstore.StateOpen
	result.Candidates[0].Endpoint.OpenRemainingMs = 5000
	result.Candidates[1].Channel.State = breakerstore.StateHalfOpen
	result.Candidates[1].Channel.HalfOpenBusy = true
	result.Candidates[4].Channel.State = breakerstore.StateOpen
	result.Candidates[4].Channel.OpenRemainingMs = 0

	service := NewService(store, readyRuntimeFacts(), &fakeBreakerSnapshotter{result: result})
	got, err := service.Get(context.Background(), Params{RouteID: 4, ModelID: "openai/gpt"})
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	wantReasons := []string{"breaker_open", "breaker_half_open_busy", "rate_limited", "model_permission_paused", ""}
	for index, want := range wantReasons {
		if got.Channels[index].ExcludedReason != want {
			t.Errorf("channel %d reason=%q want=%q", index, got.Channels[index].ExcludedReason, want)
		}
	}
	if got.CandidateCount != 1 || !got.Channels[4].Eligible || got.Channels[4].FinalWeight != 0 {
		t.Fatalf("half-open probe must remain eligible outside normal weighting: %+v", got)
	}
	if got.Channels[0].EndpointBreakerState == nil || *got.Channels[0].EndpointBreakerState != "open" ||
		got.Channels[0].EndpointOpenRemainingMs == nil || *got.Channels[0].EndpointOpenRemainingMs != 5000 {
		t.Fatalf("open endpoint view is incomplete: %+v", got.Channels[0])
	}
	if got.Channels[4].ChannelBreakerState == nil || *got.Channels[4].ChannelBreakerState != "half_open" {
		t.Fatalf("expired open state must be presented as half-open: %+v", got.Channels[4])
	}
}

func TestRuntimeReturnsDisplayableFailClosedStates(t *testing.T) {
	tests := []struct {
		name      string
		facts     *fakeRuntimeFacts
		breakers  *fakeBreakerSnapshotter
		wantState string
	}{
		{
			name:      "runtime state lost in postgres facts",
			facts:     &fakeRuntimeFacts{admissionErr: failure.New(failure.CodeGatewayRuntimeStateLost)},
			breakers:  &fakeBreakerSnapshotter{},
			wantState: runtimeStateLost,
		},
		{
			name:      "redis unavailable",
			facts:     readyRuntimeFacts(),
			breakers:  &fakeBreakerSnapshotter{err: failure.New(failure.CodeDependencyRedisUnavailable)},
			wantState: runtimeStoreDown,
		},
		{
			name:  "runtime control pending",
			facts: readyRuntimeFacts(),
			breakers: &fakeBreakerSnapshotter{err: failure.New(
				failure.CodeGatewayRuntimeSyncRequired,
				failure.WithField("reason", string(breakerstore.CandidateSnapshotRuntimeSyncPending)),
			)},
			wantState: runtimeSyncPending,
		},
		{
			name:  "runtime setting revision stale",
			facts: readyRuntimeFacts(),
			breakers: &fakeBreakerSnapshotter{err: failure.New(
				failure.CodeGatewayRuntimeSyncRequired,
				failure.WithField("reason", string(breakerstore.ReasonStaleSettingRevision)),
			)},
			wantState: runtimeSyncStale,
		},
		{
			name: "facts epochs differ",
			facts: &fakeRuntimeFacts{
				admission: readyRuntimeFacts().admission,
				routing: runtimefacts.RoutingRevisions{
					Integrity:      runtimefacts.Integrity{Epoch: "epoch-b", Revision: 2},
					CircuitBreaker: 4, RoutingBalance: 5,
				},
			},
			breakers:  &fakeBreakerSnapshotter{},
			wantState: runtimeSyncRequired,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			store := &fakeRuntimeStore{
				route: sqlc.Route{ID: 5, Mode: "balanced", Status: "enabled"},
				pool:  []sqlc.RouteRuntimePoolRow{runtimePoolRow(7, 21, 31)},
			}
			service := NewService(store, test.facts, test.breakers)
			got, err := service.Get(context.Background(), Params{RouteID: 5, ModelID: "openai/gpt"})
			if err != nil {
				t.Fatalf("get runtime: %v", err)
			}
			if got.RuntimeSyncState != test.wantState || got.BreakerStoreAdmission != breakerAdmissionDenied ||
				!got.Stale || got.CandidateCount != 0 {
				t.Fatalf("unexpected fail-closed runtime: %+v", got)
			}
			channel := got.Channels[0]
			if channel.RuntimeSyncState != test.wantState || channel.BreakerStoreAdmission != breakerAdmissionDenied ||
				channel.Eligible || channel.ErrorRate != nil || channel.TTFTEWMAMs != nil || channel.FinalWeight != 0 ||
				!channel.CapacityReadFailed {
				t.Fatalf("old runtime facts leaked after denial: %+v", channel)
			}
		})
	}
}

func TestRuntimeRequiresModelForModelScopedSnapshot(t *testing.T) {
	service := NewService(&fakeRuntimeStore{}, readyRuntimeFacts(), &fakeBreakerSnapshotter{})
	_, err := service.Get(context.Background(), Params{RouteID: 5})
	if failure.CodeOf(err) != failure.CodeAdminInvalidArgument {
		t.Fatalf("missing model_id code=%q err=%v", failure.CodeOf(err), err)
	}
}

func readyRuntimeFacts() *fakeRuntimeFacts {
	integrity := runtimefacts.Integrity{Epoch: "epoch-a", Revision: 1}
	return &fakeRuntimeFacts{
		admission: runtimefacts.AdmissionRevisions{
			Integrity: integrity, RouteRateLimits: 2, ChannelRateLimits: 7, Concurrency: 3,
		},
		routing: runtimefacts.RoutingRevisions{
			Integrity: integrity, CircuitBreaker: 4, RoutingBalance: 5,
		},
	}
}

func runtimePoolRow(channelID, endpointID, modelID int64) sqlc.RouteRuntimePoolRow {
	return sqlc.RouteRuntimePoolRow{
		RouteID: 3, Mode: "balanced", RouteStatus: "enabled",
		ChannelID: channelID, ChannelName: "channel", ChannelStatus: "enabled",
		CredentialValid: true, HasCredential: true, HasBaseUrl: true,
		Protocol: "openai", AdapterKey: "openai", Priority: int32(channelID),
		ChannelConfigRevision: 16, ChannelAdmissionLimitsRevision: 17,
		ProviderEndpointID: endpointID, ProviderEndpointName: "endpoint", ProviderEndpointStatus: "enabled",
		ProviderEndpointBaseUrlRevision: 11, ProviderEndpointStatusRevision: 12,
		ProviderID: 1, ProviderName: "provider", ProviderStatus: "enabled",
		ModelDbID: modelID, ModelExists: true, ModelStatus: "enabled", BindingStatus: "enabled",
		HasModelPrice: true, HasChannelCost: true,
	}
}

func currentCostCandidate(channelID, endpointID int64) breakerstore.CandidateSnapshot {
	return breakerstore.CandidateSnapshot{
		Candidate: breakerstore.SnapshotCandidateInput{
			EndpointID: endpointID, ChannelID: channelID, EndpointBaseURLRevision: 11,
			EndpointStatusRevision: 12, ChannelConfigRevision: 16, ChannelAdmissionRevision: 17,
		},
		Status: breakerstore.CandidateSnapshotCurrent,
		Endpoint: breakerstore.ScopeSnapshot{
			Exists: true, State: breakerstore.StateClosed, BaseURLRevision: 11, StatusRevision: 12,
		},
		Channel:     breakerstore.ScopeSnapshot{Exists: true, State: breakerstore.StateClosed, ChannelConfigRevision: 16},
		Concurrency: breakerstore.CapacityUsage{Limit: 0},
		TPM:         breakerstore.CapacityUsage{Limit: 0},
	}
}

func testNumeric(value int64, exponent int32) pgtype.Numeric {
	return pgtype.Numeric{Int: big.NewInt(value), Exp: exponent, Valid: true}
}

func setRuntimePriceBase(row *sqlc.RouteRuntimePoolRow) {
	row.PriceRatio = testNumeric(2, 0)
	row.ModelPriceID = 1
	row.BaseCurrency = "USD"
	row.BasePricingUnit = billing.PricingUnitPer1MTokens
	row.UncachedInputPrice = testNumeric(10, 0)
	row.OutputPrice = testNumeric(20, 0)
}
