// Package routeruntime aggregates read-only route capacity and breaker diagnostics.
package routeruntime

import (
	"context"
	"errors"
	"math/big"
	"sort"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/billing"
	"github.com/ThankCat/unio-gateway/internal/core/routingdiagnostic"
	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/runtimefacts"
)

const (
	runtimeSyncActive   = "active"
	runtimeSyncPending  = "runtime_sync_pending"
	runtimeSyncRequired = "runtime_sync_required"
	runtimeSyncStale    = "stale"
	runtimeStoreDown    = "store_unavailable"
	runtimeStateLost    = "runtime_state_lost"

	breakerAdmissionNormal = "normal"
	breakerAdmissionDenied = "denied"
)

type Store interface {
	GetRouteByID(context.Context, int64) (sqlc.Route, error)
	RouteRuntimePool(context.Context, sqlc.RouteRuntimePoolParams) ([]sqlc.RouteRuntimePoolRow, error)
	RouteRuntimeChannelStats(context.Context, sqlc.RouteRuntimeChannelStatsParams) ([]sqlc.RouteRuntimeChannelStatsRow, error)
}

type RuntimeFactsReader interface {
	Admission(context.Context) (runtimefacts.AdmissionRevisions, error)
	Routing(context.Context) (runtimefacts.RoutingRevisions, error)
}

type BreakerSnapshotter interface {
	SnapshotMany(context.Context, breakerstore.SnapshotManyInput) (breakerstore.SnapshotManyResult, error)
}

type Params struct {
	RouteID  int64
	ModelID  string
	Protocol string
}

type Source struct {
	Name       string
	Available  bool
	ObservedAt time.Time
	Stale      bool
}

type Channel struct {
	ChannelID                       int64
	ChannelName                     string
	ChannelStatus                   string
	ProviderID                      int64
	ProviderName                    string
	ProviderStatus                  string
	ProviderEndpointID              int64
	ProviderEndpointName            string
	ProviderEndpointStatus          string
	EndpointBaseURLRevision         int64
	EndpointStatusRevision          int64
	RuntimeEndpointBaseURLRevision  int64
	RuntimeEndpointStatusRevision   int64
	PendingEndpointBaseURLRevision  *int64
	PendingEndpointStatusRevision   *int64
	EndpointBaseURLRevisionCurrent  bool
	EndpointStatusRevisionCurrent   bool
	EndpointStateGeneration         int64
	EndpointBaseURLFenceGeneration  int64
	EndpointStatusFenceGeneration   int64
	ChannelConfigRevision           int64
	RuntimeChannelConfigRevision    *int64
	ChannelConfigRevisionCurrent    bool
	ChannelAdmissionLimitsRevision  int64
	RuntimeChannelAdmissionRevision int64
	ChannelAdmissionRevisionCurrent bool
	RouteRateLimitsRevision         int64
	ChannelRateLimitsRevision       int64
	GlobalConcurrencyRevision       int64
	CircuitBreakerRevision          int64
	RoutingBalanceRevision          int64
	RuntimeControlState             string
	RuntimeRevisionCurrent          bool
	Protocol                        string
	AdapterKey                      string
	Priority                        int32
	Eligible                        bool
	ExcludedReason                  string
	ConcurrencyUsed                 int64
	ConcurrencyLimit                int64
	ConcurrencyRemaining            *float64
	RPMUsed                         int64
	RPMLimit                        int64
	RPMRemaining                    *float64
	RPDUsed                         int64
	RPDLimit                        int64
	RPDRemaining                    *float64
	TPMUsed                         int64
	TPMLimit                        int64
	TPMRemaining                    *float64
	CapacityScore                   float64
	CostRatio                       *float64
	CostWeight                      float64
	CostFactor                      float64
	FinalWeight                     float64
	Pressure                        float64
	CapacityUnknown                 bool
	CapacityReadFailed              bool
	EndpointBreakerState            *string
	EndpointOpenRemainingMs         *int64
	ChannelBreakerState             *string
	ChannelOpenRemainingMs          *int64
	ErrorRate                       *float64
	ErrorSamples                    int64
	TTFTEWMAMs                      *float64
	TTFTSamples                     int64
	TTFTSampleSource                string
	CooldownRemainingMs             int64
	ModelPermissionPaused           bool
	ModelPermissionRecheckState     string
	RuntimeSyncState                string
	BreakerStoreAdmission           string
	CurrentOrder                    int
	Selected1m                      int64
	Selected5m                      int64
	SelectedShare1m                 float64
	SelectedShare5m                 float64
	Fallback1m                      int64
	MarginStatus                    string
}

type Runtime struct {
	RouteID               int64
	Mode                  string
	RouteStatus           string
	ModelID               string
	Protocol              string
	ObservedAt            time.Time
	Stale                 bool
	PoolSize              int
	CandidateCount        int
	NoRedundancy          bool
	AllCapacityZero       bool
	RuntimeSyncState      string
	BreakerStoreAdmission string
	Sources               []Source
	Channels              []Channel
}

type Service struct {
	store    Store
	facts    RuntimeFactsReader
	breakers BreakerSnapshotter
	now      func() time.Time
}

func NewService(store Store, facts RuntimeFactsReader, breakers BreakerSnapshotter) *Service {
	return &Service{store: store, facts: facts, breakers: breakers, now: time.Now}
}

func (s *Service) Get(ctx context.Context, params Params) (Runtime, error) {
	if params.RouteID <= 0 {
		return Runtime{}, invalidArgument("route_id", "route_id must be a positive integer")
	}
	if strings.TrimSpace(params.ModelID) == "" {
		return Runtime{}, invalidArgument("model_id", "model_id is required")
	}
	if params.Protocol != "" && params.Protocol != "openai" && params.Protocol != "anthropic" {
		return Runtime{}, invalidArgument("protocol", "protocol must be openai or anthropic")
	}
	route, err := s.store.GetRouteByID(ctx, params.RouteID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return Runtime{}, failure.New(failure.CodeAdminNotFound, failure.WithMessage("route not found"))
		}
		return Runtime{}, storeFailed(err, "get route")
	}

	now := s.now().UTC()
	rows, err := s.store.RouteRuntimePool(ctx, sqlc.RouteRuntimePoolParams{
		RouteID: params.RouteID,
		ModelID: params.ModelID,
		AtTime:  pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return Runtime{}, storeFailed(err, "list route runtime pool")
	}
	statsRows, err := s.store.RouteRuntimeChannelStats(ctx, sqlc.RouteRuntimeChannelStatsParams{
		RouteID:    params.RouteID,
		ObservedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return Runtime{}, storeFailed(err, "read route runtime stats")
	}

	runtime := Runtime{
		RouteID: params.RouteID, ModelID: params.ModelID, Protocol: params.Protocol,
		Mode: route.Mode, RouteStatus: route.Status, ObservedAt: now,
		PoolSize: len(rows), Channels: make([]Channel, len(rows)),
		RuntimeSyncState: runtimeSyncActive, BreakerStoreAdmission: breakerAdmissionNormal,
	}
	if len(rows) > 0 {
		runtime.Mode, runtime.RouteStatus = rows[0].Mode, rows[0].RouteStatus
	}
	populateChannels(&runtime, rows, statsRows, params)

	if s.facts == nil {
		denyRuntime(&runtime, runtimeSyncRequired, true, false)
		return runtime, nil
	}
	admissionFacts, err := s.facts.Admission(ctx)
	if err != nil {
		state, postgresAvailable := runtimeStateFromFactsError(err)
		denyRuntime(&runtime, state, postgresAvailable, false)
		return runtime, nil
	}
	routingFacts, err := s.facts.Routing(ctx)
	if err != nil {
		state, postgresAvailable := runtimeStateFromFactsError(err)
		denyRuntime(&runtime, state, postgresAvailable, false)
		return runtime, nil
	}
	if admissionFacts.Integrity != routingFacts.Integrity {
		denyRuntime(&runtime, runtimeSyncRequired, true, false)
		return runtime, nil
	}
	if s.breakers == nil {
		denyRuntime(&runtime, runtimeStoreDown, true, false)
		return runtime, nil
	}
	if len(rows) == 0 {
		runtime.Sources = healthySources(now)
		runtime.NoRedundancy = true
		return runtime, nil
	}
	if rows[0].ModelDbID <= 0 {
		denyRuntime(&runtime, runtimeSyncRequired, true, false)
		return runtime, nil
	}

	input := breakerstore.SnapshotManyInput{
		IntegrityEpoch:            admissionFacts.Epoch,
		IntegrityRevision:         admissionFacts.Revision,
		ChannelRateRevision:       admissionFacts.ChannelRateLimits,
		GlobalConcurrencyRevision: admissionFacts.Concurrency,
		CircuitBreakerRevision:    routingFacts.CircuitBreaker,
		RoutingBalanceRevision:    routingFacts.RoutingBalance,
		ModelID:                   rows[0].ModelDbID,
		Candidates:                make([]breakerstore.SnapshotCandidateInput, 0, len(rows)),
	}
	for _, row := range rows {
		if row.ModelDbID != input.ModelID {
			denyRuntime(&runtime, runtimeSyncRequired, true, false)
			return runtime, nil
		}
		input.Candidates = append(input.Candidates, breakerstore.SnapshotCandidateInput{
			EndpointID:               row.ProviderEndpointID,
			ChannelID:                row.ChannelID,
			EndpointBaseURLRevision:  row.ProviderEndpointBaseUrlRevision,
			EndpointStatusRevision:   row.ProviderEndpointStatusRevision,
			ChannelConfigRevision:    row.ChannelConfigRevision,
			ChannelAdmissionRevision: row.ChannelAdmissionLimitsRevision,
		})
	}
	snapshot, err := s.breakers.SnapshotMany(ctx, input)
	if err != nil {
		state, breakerAvailable := runtimeStateFromSnapshotError(err)
		denyRuntime(&runtime, state, true, breakerAvailable)
		return runtime, nil
	}
	if len(snapshot.Candidates) != len(rows) {
		denyRuntime(&runtime, runtimeSyncRequired, true, true)
		return runtime, nil
	}

	costFacts := resolveCostFacts(rows)
	applySnapshot(&runtime, rows, snapshot, admissionFacts, routingFacts, costFacts)
	runtime.Sources = healthySources(now)
	return runtime, nil
}

func populateChannels(runtime *Runtime, rows []sqlc.RouteRuntimePoolRow, statsRows []sqlc.RouteRuntimeChannelStatsRow, params Params) {
	stats := make(map[int64]sqlc.RouteRuntimeChannelStatsRow, len(statsRows))
	var totalSelected1m, totalSelected5m int64
	for _, row := range statsRows {
		stats[row.ChannelID] = row
		totalSelected1m += row.Selected1m
		totalSelected5m += row.Selected5m
	}
	for index, row := range rows {
		reason := databaseExcludedReason(row, params)
		stat := stats[row.ChannelID]
		channel := Channel{
			ChannelID: row.ChannelID, ChannelName: row.ChannelName, ChannelStatus: row.ChannelStatus,
			ProviderID: row.ProviderID, ProviderName: row.ProviderName, ProviderStatus: row.ProviderStatus,
			ProviderEndpointID: row.ProviderEndpointID, ProviderEndpointName: row.ProviderEndpointName,
			ProviderEndpointStatus:         row.ProviderEndpointStatus,
			EndpointBaseURLRevision:        row.ProviderEndpointBaseUrlRevision,
			EndpointStatusRevision:         row.ProviderEndpointStatusRevision,
			ChannelConfigRevision:          row.ChannelConfigRevision,
			ChannelAdmissionLimitsRevision: row.ChannelAdmissionLimitsRevision,
			Protocol:                       row.Protocol, AdapterKey: row.AdapterKey, Priority: row.Priority,
			Eligible: reason == "", ExcludedReason: reason, MarginStatus: "not_evaluated",
			RuntimeSyncState: runtimeSyncActive, BreakerStoreAdmission: breakerAdmissionNormal,
			TTFTSampleSource: "stream_only",
			CostFactor:       1,
			Selected1m:       stat.Selected1m, Selected5m: stat.Selected5m, Fallback1m: stat.Fallback1m,
		}
		if reason == "" {
			channel.MarginStatus = "safe"
		}
		if totalSelected1m > 0 {
			channel.SelectedShare1m = float64(stat.Selected1m) / float64(totalSelected1m)
		}
		if totalSelected5m > 0 {
			channel.SelectedShare5m = float64(stat.Selected5m) / float64(totalSelected5m)
		}
		runtime.Channels[index] = channel
	}
}

func applySnapshot(
	runtime *Runtime,
	rows []sqlc.RouteRuntimePoolRow,
	snapshot breakerstore.SnapshotManyResult,
	admission runtimefacts.AdmissionRevisions,
	routing runtimefacts.RoutingRevisions,
	costFacts map[int64]channelCostFacts,
) {
	config := lifecycle.BalanceConfig{
		Revision:             snapshot.RoutingBalance.Revision,
		TTFTTargetMs:         snapshot.RoutingBalance.TTFTTargetMs,
		TTFTWeight:           snapshot.RoutingBalance.TTFTWeight,
		CostWeight:           snapshot.RoutingBalance.CostWeight,
		MinimumRoutingFactor: snapshot.RoutingBalance.MinimumRoutingFactor,
	}
	allZero := len(rows) > 0
	for index, candidate := range snapshot.Candidates {
		channel := &runtime.Channels[index]
		channelSnapshot := candidate.Channel
		if candidate.Status == breakerstore.CandidateSnapshotNoSample {
			// A newer PostgreSQL revision makes the old Channel hash neutral.
			// Do not expose or score its breaker and TTFT samples.
			channelSnapshot = breakerstore.ScopeSnapshot{}
		}
		channel.EndpointBreakerState, channel.EndpointOpenRemainingMs = breakerView(candidate.Endpoint)
		channel.ChannelBreakerState, channel.ChannelOpenRemainingMs = breakerView(channelSnapshot)
		channel.RuntimeEndpointBaseURLRevision = candidate.Endpoint.BaseURLRevision
		channel.RuntimeEndpointStatusRevision = candidate.Endpoint.StatusRevision
		channel.PendingEndpointBaseURLRevision = positiveInt64Ptr(candidate.Endpoint.PendingBaseURLRevision)
		channel.PendingEndpointStatusRevision = positiveInt64Ptr(candidate.Endpoint.PendingStatusRevision)
		channel.EndpointBaseURLRevisionCurrent = candidate.Endpoint.BaseURLRevision == channel.EndpointBaseURLRevision
		channel.EndpointStatusRevisionCurrent = candidate.Endpoint.StatusRevision == channel.EndpointStatusRevision
		channel.EndpointStateGeneration = candidate.Endpoint.StateGeneration
		channel.EndpointBaseURLFenceGeneration = candidate.Endpoint.BaseURLFenceGeneration
		channel.EndpointStatusFenceGeneration = candidate.Endpoint.StatusFenceGeneration
		channel.RuntimeChannelConfigRevision = positiveInt64Ptr(candidate.Channel.ChannelConfigRevision)
		channel.ChannelConfigRevisionCurrent = candidate.Channel.ChannelConfigRevision == channel.ChannelConfigRevision
		channel.RuntimeChannelAdmissionRevision = candidate.Candidate.ChannelAdmissionRevision
		channel.ChannelAdmissionRevisionCurrent = candidate.Candidate.ChannelAdmissionRevision == channel.ChannelAdmissionLimitsRevision
		channel.RouteRateLimitsRevision = admission.RouteRateLimits
		channel.ChannelRateLimitsRevision = admission.ChannelRateLimits
		channel.GlobalConcurrencyRevision = admission.Concurrency
		channel.CircuitBreakerRevision = routing.CircuitBreaker
		channel.RoutingBalanceRevision = snapshot.RoutingBalance.Revision
		channel.CostWeight = snapshot.RoutingBalance.CostWeight
		channel.CostFactor = 1
		channel.RuntimeControlState = runtimeSyncActive
		channel.RuntimeRevisionCurrent = true
		channel.ConcurrencyUsed, channel.ConcurrencyLimit = candidate.Concurrency.Used, candidate.Concurrency.Limit
		channel.RPMUsed, channel.RPMLimit = candidate.RPM.Used, candidate.RPM.Limit
		channel.RPDUsed, channel.RPDLimit = candidate.RPD.Used, candidate.RPD.Limit
		channel.TPMUsed, channel.TPMLimit = candidate.TPM.Used, candidate.TPM.Limit
		channel.RPMRemaining = capacityRemaining(candidate.RPM)
		channel.RPDRemaining = capacityRemaining(candidate.RPD)
		channel.CooldownRemainingMs = candidate.CooldownRemainingMs
		channel.ModelPermissionPaused = candidate.ModelPermissionPaused
		channel.ModelPermissionRecheckState = candidate.ModelPermissionRecheckState
		channel.ErrorSamples = channelSnapshot.SampleCount
		if channelSnapshot.SampleCount > 0 {
			errorRate := channelSnapshot.ErrorRate
			channel.ErrorRate = &errorRate
		}
		channel.TTFTSamples = channelSnapshot.TTFTSamples
		if channelSnapshot.TTFTSamples > 0 {
			ttft := channelSnapshot.TTFTEWMAMs
			channel.TTFTEWMAMs = &ttft
		}

		score := lifecycle.ScoreBalanceCandidateWithConfig(lifecycle.ChannelCapacity{
			Concurrency: lifecycle.CapacitySignal{Used: candidate.Concurrency.Used, Limit: candidate.Concurrency.Limit, Known: true},
			TPM:         lifecycle.CapacitySignal{Used: candidate.TPM.Used, Limit: candidate.TPM.Limit, Known: true},
			ErrorRate:   channelSnapshot.ErrorRate, TTFTEWMAMs: channelSnapshot.TTFTEWMAMs,
			TTFTSamples:  channelSnapshot.TTFTSamples,
			HalfOpen:     candidate.Status == breakerstore.CandidateSnapshotHalfOpen,
			RuntimeKnown: true,
		}, config)
		if facts, ok := costFacts[channel.ChannelID]; ok {
			channel.MarginStatus = facts.marginStatus
			channel.CostRatio = facts.ratio
			if runtime.Mode == "balanced" && facts.ratio != nil {
				score = lifecycle.ApplyCostFactor(score, *facts.ratio, config)
			}
			if facts.pricingInvalid && channel.Eligible {
				channel.Eligible = false
				channel.ExcludedReason = "pricing_invalid"
			} else if facts.negativeMargin && channel.Eligible {
				channel.Eligible = false
				channel.ExcludedReason = "negative_margin"
			}
		}
		channel.ConcurrencyRemaining, channel.TPMRemaining = score.ConcurrencyRemaining, score.TPMRemaining
		channel.CapacityScore, channel.CostFactor, channel.FinalWeight = score.CapacityScore, score.CostFactor, score.Weight
		channel.Pressure, channel.CapacityUnknown = score.Pressure, score.CapacityUnknown

		if channel.Eligible {
			if reason := runtimeExcludedReason(candidate.Status); reason != "" {
				channel.Eligible = false
				channel.ExcludedReason = reason
				channel.FinalWeight = 0
			}
		} else {
			channel.FinalWeight = 0
		}
		if channel.Eligible {
			runtime.CandidateCount++
			if channel.CapacityScore > 0 {
				allZero = false
			}
		}
	}
	if runtime.CandidateCount == 0 {
		allZero = false
	}
	runtime.AllCapacityZero = allZero
	runtime.NoRedundancy = runtime.CandidateCount <= 1
	assignCurrentOrder(runtime.Channels, allZero)
}

type channelCostFacts struct {
	ratio          *float64
	marginStatus   string
	negativeMargin bool
	pricingInvalid bool
}

// resolveCostFacts reconstructs the same sale/cost vectors used by Gateway routing.
// Missing price rows are already represented by database hard-filter reasons. A configured
// price that cannot be parsed must be surfaced as pricing_invalid because Gateway drops the
// same candidate while constructing its route plan.
func resolveCostFacts(rows []sqlc.RouteRuntimePoolRow) map[int64]channelCostFacts {
	result := make(map[int64]channelCostFacts, len(rows))
	for _, row := range rows {
		if row.ModelPriceID == 0 {
			continue
		}
		basePrice := candidateModelPriceSnapshot(row)
		sale, err := billing.ScaleCustomerPrice(basePrice, row.PriceRatio)
		if err != nil {
			result[row.ChannelID] = invalidPricingFacts()
			continue
		}
		cost, ok := candidateProviderCost(row, basePrice)
		if !ok {
			if row.ChannelPriceID != 0 || row.ChannelCostMultiplierID != 0 {
				result[row.ChannelID] = invalidPricingFacts()
			}
			continue
		}
		violations, marginErr := billing.ValidateNonNegativeMargin(sale, cost)
		if marginErr != nil {
			result[row.ChannelID] = invalidPricingFacts()
			continue
		}
		facts := channelCostFacts{marginStatus: "safe"}
		if len(violations) > 0 {
			facts.marginStatus = "negative_margin"
			facts.negativeMargin = true
		}
		ratio, ratioErr := billing.ProviderCostToSaleRatio(sale, cost)
		if ratioErr != nil {
			if facts.negativeMargin {
				result[row.ChannelID] = facts
			} else {
				result[row.ChannelID] = invalidPricingFacts()
			}
			continue
		}
		value := ratio
		facts.ratio = &value
		result[row.ChannelID] = facts
	}
	return result
}

func invalidPricingFacts() channelCostFacts {
	return channelCostFacts{marginStatus: "pricing_invalid", pricingInvalid: true}
}

func candidateProviderCost(
	row sqlc.RouteRuntimePoolRow,
	base billing.CustomerPriceSnapshot,
) (billing.ProviderCostSnapshot, bool) {
	if row.ChannelPriceID != 0 {
		return candidateChannelPriceSnapshot(row), true
	}
	if row.ChannelCostMultiplierID == 0 {
		return billing.ProviderCostSnapshot{}, false
	}
	recharge := row.RechargeFactor
	if !recharge.Valid {
		recharge = pgtype.Numeric{Int: big.NewInt(1), Valid: true}
	}
	cost, err := billing.ScaleProviderCostByFactors(
		billing.ModelPriceToProviderCost(base),
		row.CostMultiplier,
		recharge,
	)
	if err != nil {
		return billing.ProviderCostSnapshot{}, false
	}
	return cost, true
}

func candidateModelPriceSnapshot(row sqlc.RouteRuntimePoolRow) billing.CustomerPriceSnapshot {
	return billing.CustomerPriceSnapshot{
		Currency:                row.BaseCurrency,
		PricingUnit:             row.BasePricingUnit,
		UncachedInputPrice:      row.UncachedInputPrice,
		CacheReadInputPrice:     row.CacheReadInputPrice,
		CacheWrite5mInputPrice:  row.CacheWrite5mInputPrice,
		CacheWrite1hInputPrice:  row.CacheWrite1hInputPrice,
		CacheWrite30mInputPrice: row.CacheWrite30mInputPrice,
		OutputPrice:             row.OutputPrice,
		ReasoningOutputPrice:    row.ReasoningOutputPrice,
		FormulaVersion:          billing.FormulaVersionV1,
	}
}

func candidateChannelPriceSnapshot(row sqlc.RouteRuntimePoolRow) billing.ProviderCostSnapshot {
	return billing.ProviderCostSnapshot{
		Currency:               row.CostCurrency,
		PricingUnit:            row.CostPricingUnit,
		UncachedInputCost:      row.UncachedInputCost,
		CacheReadInputCost:     row.CacheReadInputCost,
		CacheWrite5mInputCost:  row.CacheWrite5mInputCost,
		CacheWrite1hInputCost:  row.CacheWrite1hInputCost,
		CacheWrite30mInputCost: row.CacheWrite30mInputCost,
		OutputCost:             row.OutputCost,
		ReasoningOutputCost:    row.ReasoningOutputCost,
		FormulaVersion:         billing.FormulaVersionV1,
	}
}

func databaseExcludedReason(row sqlc.RouteRuntimePoolRow, params Params) string {
	switch {
	case row.RouteStatus != "enabled":
		return "route_" + row.RouteStatus
	case row.ChannelStatus != "enabled":
		return "channel_" + row.ChannelStatus
	case row.ProviderStatus != "enabled":
		return "provider_" + row.ProviderStatus
	case row.ProviderEndpointStatus != "enabled":
		return "provider_endpoint_" + row.ProviderEndpointStatus
	}
	reason := routingdiagnostic.ExcludedReason(routingdiagnostic.PoolFacts{
		RouteStatus: row.RouteStatus, ChannelStatus: row.ChannelStatus, ProviderStatus: row.ProviderStatus,
		CredentialValid: row.CredentialValid, HasCredential: row.HasCredential, HasBaseURL: row.HasBaseUrl,
		Protocol: row.Protocol, ModelExists: row.ModelExists, ModelStatus: row.ModelStatus,
		BindingStatus: row.BindingStatus, HasModelPrice: row.HasModelPrice, HasChannelCost: row.HasChannelCost,
	}, routingdiagnostic.Filter{ModelID: params.ModelID, Protocol: params.Protocol})
	return reason
}

func runtimeExcludedReason(status breakerstore.CandidateSnapshotStatus) string {
	switch status {
	case breakerstore.CandidateSnapshotCurrent, breakerstore.CandidateSnapshotNoSample,
		breakerstore.CandidateSnapshotHalfOpen:
		return ""
	case breakerstore.CandidateSnapshotOpen:
		return "breaker_open"
	case breakerstore.CandidateSnapshotHalfOpenBusy:
		return "breaker_half_open_busy"
	default:
		return string(status)
	}
}

func breakerView(snapshot breakerstore.ScopeSnapshot) (*string, *int64) {
	if !snapshot.Exists || (snapshot.State == breakerstore.StateClosed && snapshot.SampleCount == 0 && snapshot.ConsecutiveFailures == 0) {
		return nil, nil
	}
	state := snapshot.State
	if state == breakerstore.StateOpen && snapshot.OpenRemainingMs <= 0 {
		state = breakerstore.StateHalfOpen
	}
	value := string(state)
	var remaining *int64
	if snapshot.OpenRemainingMs > 0 {
		amount := snapshot.OpenRemainingMs
		remaining = &amount
	}
	return &value, remaining
}

func positiveInt64Ptr(value int64) *int64 {
	if value <= 0 {
		return nil
	}
	v := value
	return &v
}

func capacityRemaining(usage breakerstore.CapacityUsage) *float64 {
	if usage.Limit <= 0 {
		remaining := 1.0
		return &remaining
	}
	used := usage.Used
	if used < 0 {
		used = 0
	}
	if used > usage.Limit {
		used = usage.Limit
	}
	remaining := 1 - float64(used)/float64(usage.Limit)
	return &remaining
}

func runtimeStateFromFactsError(err error) (string, bool) {
	switch failure.CodeOf(err) {
	case failure.CodeGatewayRuntimeStateLost:
		return runtimeStateLost, true
	case failure.CodeGatewayRuntimeSyncRequired:
		return runtimeSyncRequired, true
	case failure.CodeDependencyPostgresUnavailable:
		return runtimeStoreDown, false
	default:
		return runtimeStoreDown, false
	}
}

func runtimeStateFromSnapshotError(err error) (string, bool) {
	switch failure.CodeOf(err) {
	case failure.CodeGatewayRuntimeStateLost:
		return runtimeStateLost, true
	case failure.CodeGatewayRuntimeSyncRequired:
		switch failureFieldString(err, "reason") {
		case string(breakerstore.CandidateSnapshotRuntimeSyncPending):
			return runtimeSyncPending, true
		case string(breakerstore.CandidateSnapshotStaleRevision),
			string(breakerstore.CandidateSnapshotStaleStatusRevision),
			string(breakerstore.CandidateSnapshotStaleConfigRevision),
			"stale_admission_revision", string(breakerstore.ReasonStaleSettingRevision):
			return runtimeSyncStale, true
		default:
			return runtimeSyncRequired, true
		}
	case failure.CodeConfigInvalid:
		return runtimeSyncRequired, true
	case failure.CodeDependencyRedisUnavailable, failure.CodeGatewayBreakerStoreUnavailable:
		return runtimeStoreDown, false
	default:
		return runtimeStoreDown, false
	}
}

func failureFieldString(err error, key string) string {
	for _, field := range failure.FieldsOf(err) {
		if field.Key == key {
			if value, ok := field.Value.(string); ok {
				return value
			}
		}
	}
	return ""
}

func denyRuntime(runtime *Runtime, state string, postgresAvailable, breakerAvailable bool) {
	runtime.RuntimeSyncState = state
	runtime.BreakerStoreAdmission = breakerAdmissionDenied
	runtime.Stale = true
	runtime.CandidateCount = 0
	runtime.NoRedundancy = true
	runtime.AllCapacityZero = false
	for index := range runtime.Channels {
		channel := &runtime.Channels[index]
		channel.Eligible = false
		if channel.ExcludedReason == "" {
			channel.ExcludedReason = state
		}
		channel.RuntimeSyncState = state
		channel.BreakerStoreAdmission = breakerAdmissionDenied
		channel.RuntimeEndpointBaseURLRevision = 0
		channel.RuntimeEndpointStatusRevision = 0
		channel.PendingEndpointBaseURLRevision = nil
		channel.PendingEndpointStatusRevision = nil
		channel.EndpointBaseURLRevisionCurrent = false
		channel.EndpointStatusRevisionCurrent = false
		channel.RuntimeChannelConfigRevision = nil
		channel.ChannelConfigRevisionCurrent = false
		channel.RuntimeChannelAdmissionRevision = 0
		channel.ChannelAdmissionRevisionCurrent = false
		channel.RuntimeControlState = state
		channel.RuntimeRevisionCurrent = false
		channel.ConcurrencyRemaining = nil
		channel.RPMRemaining = nil
		channel.RPDRemaining = nil
		channel.TPMRemaining = nil
		channel.EndpointBreakerState = nil
		channel.EndpointOpenRemainingMs = nil
		channel.ChannelBreakerState = nil
		channel.ChannelOpenRemainingMs = nil
		channel.ErrorRate = nil
		channel.TTFTEWMAMs = nil
		channel.ErrorSamples = 0
		channel.TTFTSamples = 0
		channel.CooldownRemainingMs = 0
		channel.ModelPermissionPaused = false
		channel.ModelPermissionRecheckState = "unavailable"
		channel.CapacityScore = 0
		channel.CostRatio = nil
		channel.CostWeight = 0
		channel.CostFactor = 1
		channel.FinalWeight = 0
		channel.Pressure = 0
		channel.CapacityUnknown = true
		channel.CapacityReadFailed = true
		channel.CurrentOrder = 0
	}
	now := runtime.ObservedAt
	runtime.Sources = []Source{
		{Name: "postgres", Available: postgresAvailable, ObservedAt: observedIf(postgresAvailable, now), Stale: !postgresAvailable},
		{Name: "breaker_store", Available: breakerAvailable, ObservedAt: observedIf(breakerAvailable, now), Stale: true},
		{Name: "attempts", Available: true, ObservedAt: now},
	}
}

func healthySources(now time.Time) []Source {
	return []Source{
		{Name: "postgres", Available: true, ObservedAt: now},
		{Name: "breaker_store", Available: true, ObservedAt: now},
		{Name: "attempts", Available: true, ObservedAt: now},
	}
}

func assignCurrentOrder(channels []Channel, allZero bool) {
	indexes := make([]int, 0, len(channels))
	for index := range channels {
		if channels[index].Eligible {
			indexes = append(indexes, index)
		}
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		left, right := channels[indexes[i]], channels[indexes[j]]
		if allZero {
			return left.Pressure < right.Pressure
		}
		return left.FinalWeight > right.FinalWeight
	})
	for order, index := range indexes {
		channels[index].CurrentOrder = order + 1
	}
}

func observedIf(ok bool, value time.Time) time.Time {
	if !ok {
		return time.Time{}
	}
	return value
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func storeFailed(err error, operation string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(operation+" failed"))
}
