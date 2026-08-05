package routeruntime

import (
	"context"
	"math"
	"math/big"
	"strconv"
	"strings"
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
	result          breakerstore.SnapshotManyResult
	err             error
	input           breakerstore.SnapshotManyInput
	calls           int
	routeUsage      breakerstore.RouteUsage
	routeUsageErr   error
	routeUsageCalls int
	sampleWindows   map[int64]breakerstore.ChannelSampleWindow
	sampleErr       error
	sampleCalls     int
	tpmObservations map[breakerstore.TPMObservationKind]map[int64]breakerstore.TPMObservationSnapshot
	tpmErr          error
	tpmCalls        int
}

func (f *fakeBreakerSnapshotter) SnapshotMany(_ context.Context, input breakerstore.SnapshotManyInput) (breakerstore.SnapshotManyResult, error) {
	f.calls++
	f.input = input
	return f.result, f.err
}

func (f *fakeBreakerSnapshotter) AggregateRouteUsage(_ context.Context, _ int64) (breakerstore.RouteUsage, error) {
	f.routeUsageCalls++
	return f.routeUsage, f.routeUsageErr
}

func (f *fakeBreakerSnapshotter) AggregateChannelSamples(_ context.Context, _ []int64) (map[int64]breakerstore.ChannelSampleWindow, error) {
	f.sampleCalls++
	return f.sampleWindows, f.sampleErr
}

func (f *fakeBreakerSnapshotter) TPMObservations(
	_ context.Context,
	kind breakerstore.TPMObservationKind,
	_ []int64,
	_ int64,
) (map[int64]breakerstore.TPMObservationSnapshot, error) {
	f.tpmCalls++
	return f.tpmObservations[kind], f.tpmErr
}

// objectiveRoutingBalance 返回 objective_v1 五项评分的 canonical 配置快照（§7/§14.6 默认值）。
func objectiveRoutingBalance(revision int64) breakerstore.RoutingBalanceSnapshot {
	return breakerstore.RoutingBalanceSnapshot{
		Revision:      revision,
		CostWeightPct: 25, ConcurrencyWeightPct: 20, TTFTWeightPct: 25,
		ErrorRateWeightPct: 20, PriorityWeightPct: 10,
		TTFTWindowMs: 1_800_000, TTFTPenaltyUnitMs: 1000, TTFTPenaltyPointsPerUnit: 2.5,
		ErrorWindowMs: 1_800_000, ErrorPenaltyPointsPerPercent: 2.5,
	}
}

func TestRuntimeUsesAuthoritativeSnapshotAndObjectiveScore(t *testing.T) {
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
	breakers := &fakeBreakerSnapshotter{
		routeUsage: breakerstore.RouteUsage{Concurrency: 4, RPM: 12, RPD: 40, ActiveUsers: 2},
		// TPM 只有观测值：Route 当前分钟 900，Channel 7 当前分钟 25 且 3 个 attempt 里 2 个拿到可靠 usage。
		tpmObservations: map[breakerstore.TPMObservationKind]map[int64]breakerstore.TPMObservationSnapshot{
			breakerstore.TPMScopeRoute:   {3: {InputTokens: 600, OutputTokens: 300}},
			breakerstore.TPMScopeChannel: {7: {InputTokens: 20, OutputTokens: 5, ObservedAttempts: 3, MissingUsageCount: 1}},
		},
		// 30 分钟样本：平均 TTFT 1000ms、错误率 10%（§12 分钟桶聚合）。
		sampleWindows: map[int64]breakerstore.ChannelSampleWindow{
			7: {TTFTSumMs: 18_000, TTFTCount: 18, ErrorAttemptCount: 20, ErrorCount: 2, RPM: 3, RPD: 30},
		},
		result: breakerstore.SnapshotManyResult{
			RoutingBalance: objectiveRoutingBalance(5),
			Candidates: []breakerstore.CandidateSnapshot{
				{
					Candidate: breakerstore.SnapshotCandidateInput{
						ProviderID: 21, ChannelID: 7, OriginRevision: 11,
						ProviderStatusRevision: 12, ChannelConfigRevision: 16, ChannelCapacityRevision: 17,
					},
					Status: breakerstore.CandidateSnapshotCurrent,
					Provider: breakerstore.ScopeSnapshot{
						Exists: true, State: breakerstore.StateClosed, SampleCount: 20,
						OriginRevision: 11, StatusRevision: 12, StateGeneration: 6,
						OriginFenceGeneration: 3, StatusFenceGeneration: 4,
					},
					Channel: breakerstore.ScopeSnapshot{
						Exists: true, State: breakerstore.StateClosed, ErrorRate: 0.1, SampleCount: 20,
						ChannelConfigRevision: 16,
					},
					Concurrency:         breakerstore.CapacityUsage{Used: 2, Limit: 4},
					CooldownRemainingMs: 2500, ModelPermissionPaused: true,
					ModelPermissionRecheckState: "queued",
				},
				{
					Status:   breakerstore.CandidateSnapshotNoSample,
					Provider: breakerstore.ScopeSnapshot{Exists: true, State: breakerstore.StateClosed},
					Channel: breakerstore.ScopeSnapshot{
						Exists: true, State: breakerstore.StateOpen, OpenRemainingMs: 5000,
						ErrorRate: 1, SampleCount: 9,
					},
					Concurrency: breakerstore.CapacityUsage{Limit: 0},
				},
			},
		},
	}
	service := NewService(store, facts, breakers)
	service.now = func() time.Time { return now }

	got, err := service.Get(context.Background(), Params{RouteID: 3, ModelID: "openai/gpt", Protocol: "openai"})
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	if got.Stale || got.RuntimeSyncState != runtimeSyncActive || got.BreakerStoreAdmission != breakerAdmissionNormal {
		t.Fatalf("unexpected authority state: %+v", got)
	}
	if breakers.routeUsageCalls != 1 || got.RouteUsage == nil ||
		got.RouteUsage.Concurrency != 4 || got.RouteUsage.RPM != 12 ||
		got.RouteUsage.RPD != 40 || got.RouteUsage.ObservedTPM != 900 || got.RouteUsage.ActiveUsers != 2 {
		t.Fatalf("unexpected route usage: calls=%d usage=%+v", breakers.routeUsageCalls, got.RouteUsage)
	}
	if got.PoolSize != 2 || got.CandidateCount != 1 || !got.NoRedundancy {
		t.Fatalf("unexpected route runtime: %+v", got)
	}
	if breakers.calls != 1 || breakers.input.ModelID != 31 || breakers.input.IntegrityEpoch != "epoch-a" ||
		breakers.input.GlobalConcurrencyRevision != 3 ||
		breakers.input.CircuitBreakerRevision != 4 || breakers.input.RoutingBalanceRevision != 5 {
		t.Fatalf("unexpected SnapshotMany input: %+v", breakers.input)
	}
	if len(breakers.input.Candidates) != 2 || breakers.input.Candidates[0].ProviderID != 21 ||
		breakers.input.Candidates[0].ChannelCapacityRevision != 17 {
		t.Fatalf("candidate revisions not forwarded: %+v", breakers.input.Candidates)
	}

	primary := got.Channels[0]
	if !primary.Eligible || primary.ConcurrencyUsed != 2 || primary.ConcurrencyLimit != 4 ||
		primary.RPMUsed != 3 || primary.RPDUsed != 30 || primary.GlobalRPDUsed != 30 || primary.ObservedTPM != 25 ||
		primary.TokenCoveredCount != 2 || math.Abs(primary.TokenCoveragePct-66.66666666666667) > 1e-9 {
		t.Fatalf("unexpected concurrency or observed traffic: %+v", primary)
	}
	if primary.RPMRemaining != nil || primary.RPDRemaining != nil ||
		primary.CooldownRemainingMs != 2500 || !primary.ModelPermissionPaused ||
		primary.ModelPermissionRecheckState != "queued" {
		t.Fatalf("cooldown, permission, or observation semantics missing: %+v", primary)
	}
	if got.ScoreConfig.AlgorithmVersion != "objective_v1" || got.ScoreConfig.Revision != 5 ||
		got.SampleWindow.TTFTWindowMs != 1_800_000 || !got.SampleWindow.Available {
		t.Fatalf("runtime config/sample window missing: config=%+v window=%+v", got.ScoreConfig, got.SampleWindow)
	}
	if !primary.OriginRevisionCurrent || !primary.ProviderStatusRevisionCurrent ||
		!primary.ChannelConfigRevisionCurrent || !primary.ChannelCapacityRevisionCurrent ||
		!primary.RuntimeRevisionCurrent || primary.RuntimeControlState != runtimeSyncActive ||
		primary.RouteRateLimitsRevision != 2 ||
		primary.GlobalConcurrencyRevision != 3 ||
		primary.CircuitBreakerRevision != 4 || primary.RoutingBalanceRevision != 5 {
		t.Fatalf("revision facts missing or stale: %+v", primary)
	}
	// objective_v1 五项评分：成本100×25% + 并发50×20% + TTFT97.5×25% + 错误率75×20% + 优先级90×10% = 83.375。
	if primary.AlgorithmVersion != "objective_v1" ||
		math.Abs(primary.CostScore-100) > 1e-9 || math.Abs(primary.ConcurrencyScore-50) > 1e-9 ||
		math.Abs(primary.TTFTScore-97.5) > 1e-9 || math.Abs(primary.ErrorScore-75) > 1e-9 ||
		math.Abs(primary.PriorityScore-90) > 1e-9 || math.Abs(primary.FinalScore-83.375) > 1e-9 ||
		primary.CostWeightPct != 25 || primary.ConcurrencyWeightPct != 20 ||
		primary.TTFTWeightPct != 25 || primary.ErrorRateWeightPct != 20 || primary.PriorityWeightPct != 10 {
		t.Fatalf("runtime score drifted from scheduler: %+v", primary)
	}
	// TTFT / 错误率必须来自 30 分钟分钟桶聚合（§12），而不是 breaker 样本。
	if breakers.sampleCalls != 1 {
		t.Fatalf("runtime must read the 30-minute sample aggregate once, calls=%d", breakers.sampleCalls)
	}
	if primary.AvgTTFTMs == nil || *primary.AvgTTFTMs != 1000 || primary.TTFTSampleCount != 18 ||
		primary.ErrorRatePct == nil || *primary.ErrorRatePct != 10 || primary.ErrorSampleCount != 20 {
		t.Fatalf("unexpected 30-minute sample facts: %+v", primary)
	}
	if primary.ProviderBreakerState == nil || *primary.ProviderBreakerState != "closed" ||
		primary.ChannelBreakerState == nil || *primary.ChannelBreakerState != "closed" {
		t.Fatalf("unexpected breaker state: %+v", primary)
	}
	if got.Channels[1].ExcludedReason != "provider_disabled" || got.Channels[1].FinalScore != 0 {
		t.Fatalf("unexpected hard filter: %+v", got.Channels[1])
	}
	if got.Channels[1].ChannelBreakerState != nil || got.Channels[1].AvgTTFTMs != nil ||
		got.Channels[1].ErrorRatePct != nil || got.Channels[1].TTFTSampleCount != 0 ||
		got.Channels[1].ErrorSampleCount != 0 {
		t.Fatalf("no-sample status leaked stale Channel facts: %+v", got.Channels[1])
	}
	if len(got.Sources) != 3 || got.Sources[1].Name != "breaker_store" || !got.Sources[1].Available {
		t.Fatalf("unexpected sources: %+v", got.Sources)
	}
}

func TestRuntimeTreatsMissingChannelIdentityAsCurrentNoSample(t *testing.T) {
	store := &fakeRuntimeStore{
		route: sqlc.Route{ID: 3, Mode: "balanced", Status: "enabled"},
		pool:  []sqlc.RouteRuntimePoolRow{runtimePoolRow(7, 21, 31)},
	}
	breakers := &fakeBreakerSnapshotter{result: breakerstore.SnapshotManyResult{
		RoutingBalance: objectiveRoutingBalance(5),
		Candidates: []breakerstore.CandidateSnapshot{
			{
				Candidate: breakerstore.SnapshotCandidateInput{
					ProviderID: 21, ChannelID: 7, OriginRevision: 11,
					ProviderStatusRevision: 12, ChannelConfigRevision: 16, ChannelCapacityRevision: 17,
				},
				Status: breakerstore.CandidateSnapshotNoSample,
				Provider: breakerstore.ScopeSnapshot{
					Exists: true, State: breakerstore.StateClosed, OriginRevision: 11, StatusRevision: 12,
				},
				Concurrency: breakerstore.CapacityUsage{Limit: 0},
			},
		},
	}}
	service := NewService(store, readyRuntimeFacts(), breakers)

	got, err := service.Get(context.Background(), Params{RouteID: 3, ModelID: "openai/gpt", Protocol: "openai"})
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	channel := got.Channels[0]
	if !channel.Eligible || !channel.ChannelConfigRevisionCurrent || channel.RuntimeChannelConfigRevision != nil {
		t.Fatalf("missing Channel identity must be a current no-sample fact: %+v", channel)
	}
	if channel.ChannelBreakerState != nil || channel.AvgTTFTMs != nil || channel.ErrorRatePct != nil ||
		channel.TTFTSampleCount != 0 || channel.ErrorSampleCount != 0 {
		t.Fatalf("missing Channel identity exposed runtime samples: %+v", channel)
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
	multiplier.Priority = 20
	store := &fakeRuntimeStore{
		route: sqlc.Route{ID: 3, Mode: "balanced", Status: "enabled", PriceRatio: testNumeric(2, 0)},
		pool:  []sqlc.RouteRuntimePoolRow{absolute, multiplier},
	}
	result := breakerstore.SnapshotManyResult{
		RoutingBalance: objectiveRoutingBalance(5),
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
			math.Abs(channel.CostScore-75) > 1e-9 ||
			channel.AlgorithmVersion != "objective_v1" || channel.MarginStatus != "safe" {
			t.Errorf("channel %d cost score mismatch: %+v", channel.ChannelID, channel)
		}
	}
	if got.Channels[0].Pricing.Source != pricingSourceAbsolute ||
		got.Channels[0].Pricing.CostMultiplier != nil || got.Channels[0].Pricing.RechargeFactor != nil {
		t.Fatalf("absolute pricing facts mismatch: %+v", got.Channels[0].Pricing)
	}
	if got.Channels[1].Pricing.Source != pricingSourceMultiplier ||
		got.Channels[1].Pricing.CostMultiplier == nil || *got.Channels[1].Pricing.CostMultiplier != "1" ||
		got.Channels[1].Pricing.RechargeFactor == nil || *got.Channels[1].Pricing.RechargeFactor != "0.5" {
		t.Fatalf("multiplier pricing facts mismatch: %+v", got.Channels[1].Pricing)
	}
	// 成本75×25% + 并发100×20% + TTFT100×25% + 错误率100×20% + 优先级(90/80)×10%。
	if math.Abs(got.Channels[0].FinalScore-92.75) > 1e-9 || math.Abs(got.Channels[1].FinalScore-91.75) > 1e-9 {
		t.Fatalf("objective score must include Priority: %+v", got.Channels)
	}
}

func TestRuntimeFixedModeReportsObjectiveScoreWithoutChangingOrder(t *testing.T) {
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
		RoutingBalance: objectiveRoutingBalance(5),
		Candidates:     []breakerstore.CandidateSnapshot{currentCostCandidate(7, 21)},
	}
	service := NewService(store, readyRuntimeFacts(), &fakeBreakerSnapshotter{result: result})

	got, err := service.Get(context.Background(), Params{RouteID: 3, ModelID: "openai/gpt", Protocol: "openai"})
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	channel := got.Channels[0]
	if channel.CostRatio == nil || math.Abs(*channel.CostRatio-0.25) > 1e-9 ||
		channel.AlgorithmVersion != "objective_v1" || math.Abs(channel.FinalScore-92.75) > 1e-9 {
		t.Fatalf("fixed route must expose objective score without reordering: %+v", channel)
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
				RoutingBalance: objectiveRoutingBalance(5),
				Candidates:     []breakerstore.CandidateSnapshot{currentCostCandidate(7, 21)},
			}}
			service := NewService(store, readyRuntimeFacts(), breakers)

			got, err := service.Get(context.Background(), Params{RouteID: 3, ModelID: "openai/gpt", Protocol: "openai"})
			if err != nil {
				t.Fatalf("get runtime: %v", err)
			}
			channel := got.Channels[0]
			if channel.Eligible || channel.ExcludedReason != "pricing_invalid" ||
				channel.MarginStatus != "pricing_invalid" || channel.CostRatio != nil || channel.FinalScore != 0 {
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
		RoutingBalance: objectiveRoutingBalance(5),
		Candidates:     make([]breakerstore.CandidateSnapshot, len(statuses)),
	}
	for index, status := range statuses {
		store.pool = append(store.pool, runtimePoolRow(int64(index+1), int64(index+11), 31))
		result.Candidates[index] = breakerstore.CandidateSnapshot{
			Status: status,
			Provider: breakerstore.ScopeSnapshot{
				Exists: true, State: breakerstore.StateClosed, SampleCount: 2,
			},
			Channel:     breakerstore.ScopeSnapshot{Exists: true, State: breakerstore.StateClosed, SampleCount: 2},
			Concurrency: breakerstore.CapacityUsage{Limit: 10},
		}
	}
	result.Candidates[0].Provider.State = breakerstore.StateOpen
	result.Candidates[0].Provider.OpenRemainingMs = 5000
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
	if got.CandidateCount != 1 || !got.Channels[4].Eligible || got.Channels[4].FinalScore != 0 {
		t.Fatalf("half-open probe must remain eligible outside normal weighting: %+v", got)
	}
	if got.Channels[0].ProviderBreakerState == nil || *got.Channels[0].ProviderBreakerState != "open" ||
		got.Channels[0].ProviderOpenRemainingMs == nil || *got.Channels[0].ProviderOpenRemainingMs != 5000 {
		t.Fatalf("open origin view is incomplete: %+v", got.Channels[0])
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
			if got.RouteUsage != nil || test.breakers.routeUsageCalls != 0 {
				t.Fatalf("deny path must not fill route usage: usage=%+v calls=%d", got.RouteUsage, test.breakers.routeUsageCalls)
			}
			channel := got.Channels[0]
			if channel.RuntimeSyncState != test.wantState || channel.BreakerStoreAdmission != breakerAdmissionDenied ||
				channel.Eligible || channel.ErrorRatePct != nil || channel.AvgTTFTMs != nil || channel.FinalScore != 0 ||
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

func TestRuntimeKeepsRouteUsageNilWhenAggregateFails(t *testing.T) {
	store := &fakeRuntimeStore{
		route: sqlc.Route{ID: 3, Mode: "balanced", Status: "enabled"},
		pool:  []sqlc.RouteRuntimePoolRow{runtimePoolRow(7, 21, 31)},
	}
	breakers := &fakeBreakerSnapshotter{
		routeUsageErr: failure.New(failure.CodeDependencyRedisUnavailable),
		result: breakerstore.SnapshotManyResult{
			RoutingBalance: objectiveRoutingBalance(5),
			Candidates:     []breakerstore.CandidateSnapshot{currentCostCandidate(7, 21)},
		},
	}
	service := NewService(store, readyRuntimeFacts(), breakers)
	got, err := service.Get(context.Background(), Params{RouteID: 3, ModelID: "openai/gpt"})
	if err != nil {
		t.Fatalf("get runtime: %v", err)
	}
	if got.BreakerStoreAdmission != breakerAdmissionNormal || got.RouteUsage != nil || breakers.routeUsageCalls != 1 {
		t.Fatalf("aggregate failure must degrade route usage only: %+v calls=%d", got, breakers.routeUsageCalls)
	}
}

func readyRuntimeFacts() *fakeRuntimeFacts {
	integrity := runtimefacts.Integrity{Epoch: "epoch-a", Revision: 1}
	return &fakeRuntimeFacts{
		admission: runtimefacts.AdmissionRevisions{
			Integrity: integrity, RouteRateLimits: 2, Concurrency: 3,
		},
		routing: runtimefacts.RoutingRevisions{
			Integrity: integrity, CircuitBreaker: 4, RoutingBalance: 5,
		},
	}
}

func runtimePoolRow(channelID, originID, modelID int64) sqlc.RouteRuntimePoolRow {
	return sqlc.RouteRuntimePoolRow{
		RouteID: 3, Mode: "balanced", RouteStatus: "enabled",
		ChannelID: channelID, ChannelName: "channel", ChannelStatus: "enabled",
		CredentialValid: true, HasCredential: true, HasOrigin: true,
		Protocol: "openai", AdapterKey: "openai", Priority: 10,
		ChannelConfigRevision: 16, ChannelCapacityRevision: 17,
		ProviderOriginRevision: 11, ProviderStatusRevision: 12,
		ProviderID: originID, ProviderName: "provider", ProviderStatus: "enabled",
		ModelDbID: modelID, ModelExists: true, ModelStatus: "enabled", BindingStatus: "enabled",
		HasModelPrice: true, HasChannelCost: true,
	}
}

func currentCostCandidate(channelID, originID int64) breakerstore.CandidateSnapshot {
	return breakerstore.CandidateSnapshot{
		Candidate: breakerstore.SnapshotCandidateInput{
			ProviderID: originID, ChannelID: channelID, OriginRevision: 11,
			ProviderStatusRevision: 12, ChannelConfigRevision: 16, ChannelCapacityRevision: 17,
		},
		Status: breakerstore.CandidateSnapshotCurrent,
		Provider: breakerstore.ScopeSnapshot{
			Exists: true, State: breakerstore.StateClosed, OriginRevision: 11, StatusRevision: 12,
		},
		Channel:     breakerstore.ScopeSnapshot{Exists: true, State: breakerstore.StateClosed, ChannelConfigRevision: 16},
		Concurrency: breakerstore.CapacityUsage{Limit: 0},
	}
}

func TestSortChannels(t *testing.T) {
	f := func(v float64) *float64 { return &v }
	base := []Channel{
		{ChannelID: 1, Eligible: true, CurrentOrder: 2, FinalScore: 30, RPDRemaining: nil, GlobalRPDRemaining: f(0.8)},
		{ChannelID: 2, Eligible: false, CurrentOrder: 0, FinalScore: 0},
		{ChannelID: 3, Eligible: true, CurrentOrder: 1, FinalScore: 70, RPDRemaining: nil, GlobalRPDRemaining: f(0.1)},
	}
	ids := func(chs []Channel) string {
		parts := make([]string, len(chs))
		for i, c := range chs {
			parts[i] = strconv.FormatInt(c.ChannelID, 10)
		}
		return strings.Join(parts, ",")
	}
	cases := []struct {
		name  string
		field string
		desc  bool
		want  string
	}{
		{"order asc keeps routing order, ineligible last", "order", false, "3,1,2"},
		{"order desc reverses eligible, ineligible last", "order", true, "1,3,2"},
		{"weight desc ranks by final weight", "weight", true, "3,1,2"},
		{"capacity asc ranks by tightest headroom", "capacity", false, "3,1,2"},
		{"capacity desc ranks by tightest headroom", "capacity", true, "1,3,2"},
		{"rpd asc ranks by global rpd remaining", "rpd", false, "3,1,2"},
		{"rpd desc ranks by global rpd remaining", "rpd", true, "1,3,2"},
		{"unknown field falls back to order", "nope", false, "3,1,2"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := append([]Channel(nil), base...)
			SortChannels(got, tc.field, tc.desc)
			if ids(got) != tc.want {
				t.Fatalf("SortChannels(%q, desc=%v) = %s, want %s", tc.field, tc.desc, ids(got), tc.want)
			}
		})
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
