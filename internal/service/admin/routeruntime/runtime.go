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
	AggregateRouteUsage(context.Context, int64) (breakerstore.RouteUsage, error)
	// AggregateChannelSamples 提供 30 分钟评分样本聚合（§12），与 Gateway 选路同源。
	AggregateChannelSamples(context.Context, []int64) (map[int64]breakerstore.ChannelSampleWindow, error)
}

type RouteChannelRPDReader interface {
	RouteChannelRPDUsage(context.Context, int64, int64) (int64, error)
}

// RouteUsage 是线路级全用户入口用量合计（只读展示；不含总上限）。
type RouteUsage struct {
	Concurrency int64
	RPM         int64
	RPD         int64
	TPM         int64
	ActiveUsers int64
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
	ChannelID                      int64
	ChannelName                    string
	ChannelStatus                  string
	ProviderID                     int64
	ProviderName                   string
	ProviderStatus                 string
	OriginRevision                 int64
	ProviderStatusRevision         int64
	RuntimeOriginRevision          int64
	RuntimeProviderStatusRevision  int64
	PendingOriginRevision          *int64
	PendingProviderStatusRevision  *int64
	OriginRevisionCurrent          bool
	ProviderStatusRevisionCurrent  bool
	ProviderStateGeneration        int64
	OriginFenceGeneration          int64
	StatusFenceGeneration          int64
	ChannelConfigRevision          int64
	RuntimeChannelConfigRevision   *int64
	ChannelConfigRevisionCurrent   bool
	ChannelCapacityRevision        int64
	RuntimeChannelCapacityRevision int64
	ChannelCapacityRevisionCurrent bool
	RouteRateLimitsRevision        int64
	GlobalConcurrencyRevision      int64
	CircuitBreakerRevision         int64
	RoutingBalanceRevision         int64
	RuntimeControlState            string
	RuntimeRevisionCurrent         bool
	Protocol                       string
	AdapterKey                     string
	Priority                       int32
	Eligible                       bool
	ExcludedReason                 string
	ConcurrencyUsed                int64
	ConcurrencyLimit               int64
	ConcurrencyRemaining           *float64
	RPMUsed                        int64
	RPMLimit                       int64
	RPMRemaining                   *float64
	RPDUsed                        int64
	RPDLimit                       int64
	RPDRemaining                   *float64
	GlobalRPDUsed                  int64
	GlobalRPDLimit                 int64
	GlobalRPDRemaining             *float64
	TPMUsed                        int64
	TPMLimit                       int64
	TPMRemaining                   *float64
	TokenCoveredCount              int64
	TokenCoveragePct               float64
	AlgorithmVersion               string
	CostScore                      float64
	ConcurrencyScore               float64
	TTFTScore                      float64
	ErrorScore                     float64
	PriorityScore                  float64
	FinalScore                     float64
	CostWeightPct                  int
	ConcurrencyWeightPct           int
	TTFTWeightPct                  int
	ErrorRateWeightPct             int
	PriorityWeightPct              int
	CostRatio                      *float64
	CapacityUnknown                bool
	CapacityReadFailed             bool
	ProviderBreakerState           *string
	ProviderOpenRemainingMs        *int64
	ChannelBreakerState            *string
	ChannelOpenRemainingMs         *int64
	// 30 分钟评分样本观测（§12）：无样本时指针为 nil，对应指标分按 100 计。
	AvgTTFTMs                   *float64
	TTFTSampleCount             int64
	ErrorRatePct                *float64
	ErrorSampleCount            int64
	CooldownRemainingMs         int64
	ModelPermissionPaused       bool
	ModelPermissionRecheckState string
	RuntimeSyncState            string
	BreakerStoreAdmission       string
	CurrentOrder                int
	Selected1m                  int64
	Selected5m                  int64
	SelectedShare1m             float64
	SelectedShare5m             float64
	Fallback1m                  int64
	MarginStatus                string
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
	RouteUsage            *RouteUsage
	Sources               []Source
	Channels              []Channel
	ScoreConfig           ScoreConfig
	SampleWindow          SampleWindow
}

type ScoreConfig struct {
	AlgorithmVersion             string
	Revision                     int64
	CostWeightPct                int
	ConcurrencyWeightPct         int
	TTFTWeightPct                int
	ErrorRateWeightPct           int
	PriorityWeightPct            int
	TTFTPenaltyUnitMs            int64
	TTFTPenaltyPointsPerUnit     float64
	ErrorPenaltyPointsPerPercent float64
}

type SampleWindow struct {
	TTFTWindowMs  int64
	ErrorWindowMs int64
	StartedAt     time.Time
	EndedAt       time.Time
	Available     bool
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
		s.fillRouteUsage(ctx, &runtime)
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
			ProviderID:              row.ProviderID,
			ChannelID:               row.ChannelID,
			OriginRevision:          row.ProviderOriginRevision,
			ProviderStatusRevision:  row.ProviderStatusRevision,
			ChannelConfigRevision:   row.ChannelConfigRevision,
			ChannelCapacityRevision: row.ChannelCapacityRevision,
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
	// 30 分钟评分样本（§12）：与选路同源；读失败按无样本处理，不影响运行态展示。
	sampleChannelIDs := make([]int64, 0, len(rows))
	for _, row := range rows {
		sampleChannelIDs = append(sampleChannelIDs, row.ChannelID)
	}
	sampleWindows, sampleErr := s.breakers.AggregateChannelSamples(ctx, sampleChannelIDs)
	if sampleErr != nil {
		sampleWindows = nil
	}
	applySnapshot(&runtime, rows, snapshot, admissionFacts, routingFacts, costFacts, sampleWindows)
	runtime.ScoreConfig = ScoreConfig{
		AlgorithmVersion:             lifecycle.ObjectiveAlgorithmVersion,
		Revision:                     snapshot.RoutingBalance.Revision,
		CostWeightPct:                snapshot.RoutingBalance.CostWeightPct,
		ConcurrencyWeightPct:         snapshot.RoutingBalance.ConcurrencyWeightPct,
		TTFTWeightPct:                snapshot.RoutingBalance.TTFTWeightPct,
		ErrorRateWeightPct:           snapshot.RoutingBalance.ErrorRateWeightPct,
		PriorityWeightPct:            snapshot.RoutingBalance.PriorityWeightPct,
		TTFTPenaltyUnitMs:            snapshot.RoutingBalance.TTFTPenaltyUnitMs,
		TTFTPenaltyPointsPerUnit:     snapshot.RoutingBalance.TTFTPenaltyPointsPerUnit,
		ErrorPenaltyPointsPerPercent: snapshot.RoutingBalance.ErrorPenaltyPointsPerPercent,
	}
	windowMs := snapshot.RoutingBalance.TTFTWindowMs
	if snapshot.RoutingBalance.ErrorWindowMs > windowMs {
		windowMs = snapshot.RoutingBalance.ErrorWindowMs
	}
	runtime.SampleWindow = SampleWindow{
		TTFTWindowMs:  snapshot.RoutingBalance.TTFTWindowMs,
		ErrorWindowMs: snapshot.RoutingBalance.ErrorWindowMs,
		StartedAt:     now.Add(-time.Duration(windowMs) * time.Millisecond), EndedAt: now,
		Available: sampleErr == nil,
	}
	s.fillRouteChannelRPD(ctx, &runtime)
	s.fillRouteUsage(ctx, &runtime)
	runtime.Sources = healthySources(now)
	return runtime, nil
}

func (s *Service) fillRouteChannelRPD(ctx context.Context, runtime *Runtime) {
	reader, ok := s.breakers.(RouteChannelRPDReader)
	if !ok {
		return
	}
	for index := range runtime.Channels {
		channel := &runtime.Channels[index]
		channel.GlobalRPDUsed = channel.RPDUsed
		channel.GlobalRPDLimit = channel.RPDLimit
		channel.GlobalRPDRemaining = channel.RPDRemaining
		used, err := reader.RouteChannelRPDUsage(ctx, runtime.RouteID, channel.ChannelID)
		if err != nil {
			channel.CapacityReadFailed = true
			continue
		}
		channel.RPDUsed = used
		channel.RPDLimit = 0
		channel.RPDRemaining = nil
	}
}

func (s *Service) fillRouteUsage(ctx context.Context, runtime *Runtime) {
	usage, err := s.breakers.AggregateRouteUsage(ctx, runtime.RouteID)
	if err != nil {
		return
	}
	runtime.RouteUsage = &RouteUsage{
		Concurrency: usage.Concurrency,
		RPM:         usage.RPM,
		RPD:         usage.RPD,
		TPM:         usage.TPM,
		ActiveUsers: usage.ActiveUsers,
	}
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
			OriginRevision:          row.ProviderOriginRevision,
			ProviderStatusRevision:  row.ProviderStatusRevision,
			ChannelConfigRevision:   row.ChannelConfigRevision,
			ChannelCapacityRevision: row.ChannelCapacityRevision,
			Protocol:                row.Protocol, AdapterKey: row.AdapterKey, Priority: row.Priority,
			Eligible: reason == "", ExcludedReason: reason, MarginStatus: "not_evaluated",
			RuntimeSyncState: runtimeSyncActive, BreakerStoreAdmission: breakerAdmissionNormal,
			Selected1m: stat.Selected1m, Selected5m: stat.Selected5m, Fallback1m: stat.Fallback1m,
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
	sampleWindows map[int64]breakerstore.ChannelSampleWindow,
) {
	config := lifecycle.BalanceConfig{
		Revision:                     snapshot.RoutingBalance.Revision,
		CostWeightPct:                snapshot.RoutingBalance.CostWeightPct,
		ConcurrencyWeightPct:         snapshot.RoutingBalance.ConcurrencyWeightPct,
		TTFTWeightPct:                snapshot.RoutingBalance.TTFTWeightPct,
		ErrorRateWeightPct:           snapshot.RoutingBalance.ErrorRateWeightPct,
		PriorityWeightPct:            snapshot.RoutingBalance.PriorityWeightPct,
		TTFTPenaltyUnitMs:            snapshot.RoutingBalance.TTFTPenaltyUnitMs,
		TTFTPenaltyPointsPerUnit:     snapshot.RoutingBalance.TTFTPenaltyPointsPerUnit,
		ErrorPenaltyPointsPerPercent: snapshot.RoutingBalance.ErrorPenaltyPointsPerPercent,
	}
	allZero := len(rows) > 0
	for index, candidate := range snapshot.Candidates {
		channel := &runtime.Channels[index]
		channelSnapshot := candidate.Channel
		channelNoSample := candidate.Status == breakerstore.CandidateSnapshotNoSample
		if channelNoSample {
			// A newer PostgreSQL revision makes the old Channel hash neutral.
			// Do not expose or score its breaker and TTFT samples.
			channelSnapshot = breakerstore.ScopeSnapshot{}
		}
		channel.ProviderBreakerState, channel.ProviderOpenRemainingMs = breakerView(candidate.Provider)
		channel.ChannelBreakerState, channel.ChannelOpenRemainingMs = breakerView(channelSnapshot)
		channel.RuntimeOriginRevision = candidate.Provider.OriginRevision
		channel.RuntimeProviderStatusRevision = candidate.Provider.StatusRevision
		channel.PendingOriginRevision = positiveInt64Ptr(candidate.Provider.PendingOriginRevision)
		channel.PendingProviderStatusRevision = positiveInt64Ptr(candidate.Provider.PendingStatusRevision)
		channel.OriginRevisionCurrent = candidate.Provider.OriginRevision == channel.OriginRevision
		channel.ProviderStatusRevisionCurrent = candidate.Provider.StatusRevision == channel.ProviderStatusRevision
		channel.ProviderStateGeneration = candidate.Provider.StateGeneration
		channel.OriginFenceGeneration = candidate.Provider.OriginFenceGeneration
		channel.StatusFenceGeneration = candidate.Provider.StatusFenceGeneration
		if channelNoSample {
			// Channel breaker identity is created lazily by the first real Acquire.
			// Its absence is a current no-sample fact, not a revision mismatch.
			channel.RuntimeChannelConfigRevision = nil
			channel.ChannelConfigRevisionCurrent = true
		} else {
			channel.RuntimeChannelConfigRevision = positiveInt64Ptr(candidate.Channel.ChannelConfigRevision)
			channel.ChannelConfigRevisionCurrent = candidate.Channel.ChannelConfigRevision == channel.ChannelConfigRevision
		}
		channel.RuntimeChannelCapacityRevision = candidate.Candidate.ChannelCapacityRevision
		channel.ChannelCapacityRevisionCurrent = candidate.Candidate.ChannelCapacityRevision == channel.ChannelCapacityRevision
		channel.RouteRateLimitsRevision = admission.RouteRateLimits
		channel.GlobalConcurrencyRevision = admission.Concurrency
		channel.CircuitBreakerRevision = routing.CircuitBreaker
		channel.RoutingBalanceRevision = snapshot.RoutingBalance.Revision
		channel.RuntimeControlState = runtimeSyncActive
		channel.RuntimeRevisionCurrent = true
		channel.ConcurrencyUsed, channel.ConcurrencyLimit = candidate.Concurrency.Used, candidate.Concurrency.Limit
		channel.CooldownRemainingMs = candidate.CooldownRemainingMs
		channel.ModelPermissionPaused = candidate.ModelPermissionPaused
		channel.ModelPermissionRecheckState = candidate.ModelPermissionRecheckState
		// TTFT 与错误率来自 30 分钟分钟桶聚合（§12），与 breaker 自身计数解耦。
		window := sampleWindows[channel.ChannelID]
		channel.RPMUsed, channel.RPDUsed, channel.GlobalRPDUsed, channel.TPMUsed = window.RPM, window.RPD, window.RPD, window.TPM
		channel.TokenCoveredCount = window.TokenCoveredCount
		if window.RPM > 0 {
			channel.TokenCoveragePct = float64(window.TokenCoveredCount) / float64(window.RPM) * 100
		}
		scoreInputs := lifecycle.ChannelScoreInputs{
			Concurrency:       lifecycle.CapacitySignal{Used: candidate.Concurrency.Used, Limit: candidate.Concurrency.Limit, Known: true},
			TTFTSumMs:         window.TTFTSumMs,
			TTFTCount:         window.TTFTCount,
			ErrorAttemptCount: window.ErrorAttemptCount,
			ErrorCount:        window.ErrorCount,
			HalfOpen:          candidate.Status == breakerstore.CandidateSnapshotHalfOpen,
			RuntimeKnown:      true,
		}
		costRatio := 0.0
		if facts, ok := costFacts[channel.ChannelID]; ok {
			channel.MarginStatus = facts.marginStatus
			channel.CostRatio = facts.ratio
			if facts.ratio != nil {
				costRatio = *facts.ratio
			}
			if facts.pricingInvalid && channel.Eligible {
				channel.Eligible = false
				channel.ExcludedReason = "pricing_invalid"
			} else if facts.negativeMargin && channel.Eligible {
				channel.Eligible = false
				channel.ExcludedReason = "negative_margin"
			}
		}
		score := lifecycle.ScoreChannel(scoreInputs, costRatio, channel.Priority, config)
		channel.ConcurrencyRemaining = score.ConcurrencyRemaining
		channel.AlgorithmVersion = score.AlgorithmVersion
		channel.CostScore, channel.ConcurrencyScore = score.CostScore, score.ConcurrencyScore
		channel.TTFTScore, channel.ErrorScore = score.TTFTScore, score.ErrorScore
		channel.PriorityScore, channel.FinalScore = score.PriorityScore, score.FinalScore
		channel.CostWeightPct, channel.ConcurrencyWeightPct = score.CostWeightPct, score.ConcurrencyWeightPct
		channel.TTFTWeightPct, channel.ErrorRateWeightPct = score.TTFTWeightPct, score.ErrorRateWeightPct
		channel.PriorityWeightPct = score.PriorityWeightPct
		channel.CapacityUnknown = score.CapacityUnknown
		channel.TTFTSampleCount, channel.ErrorSampleCount = score.TTFTSampleCount, score.ErrorSampleCount
		if score.TTFTSampleCount > 0 {
			avg := score.AvgTTFTMs
			channel.AvgTTFTMs = &avg
		}
		if score.ErrorSampleCount > 0 {
			rate := score.ErrorRatePct
			channel.ErrorRatePct = &rate
		}

		if channel.Eligible {
			if reason := runtimeExcludedReason(candidate.Status); reason != "" {
				channel.Eligible = false
				channel.ExcludedReason = reason
				channel.FinalScore = 0
			}
		} else {
			channel.FinalScore = 0
		}
		if channel.Eligible {
			runtime.CandidateCount++
			if channel.ConcurrencyScore > 0 {
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
	}
	reason := routingdiagnostic.ExcludedReason(routingdiagnostic.PoolFacts{
		RouteStatus: row.RouteStatus, ChannelStatus: row.ChannelStatus, ProviderStatus: row.ProviderStatus,
		CredentialValid: row.CredentialValid, HasCredential: row.HasCredential, HasBaseURL: row.HasOrigin,
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
		channel.RuntimeOriginRevision = 0
		channel.RuntimeProviderStatusRevision = 0
		channel.PendingOriginRevision = nil
		channel.PendingProviderStatusRevision = nil
		channel.OriginRevisionCurrent = false
		channel.ProviderStatusRevisionCurrent = false
		channel.RuntimeChannelConfigRevision = nil
		channel.ChannelConfigRevisionCurrent = false
		channel.RuntimeChannelCapacityRevision = 0
		channel.ChannelCapacityRevisionCurrent = false
		channel.RuntimeControlState = state
		channel.RuntimeRevisionCurrent = false
		channel.ConcurrencyRemaining = nil
		channel.RPMRemaining = nil
		channel.RPDRemaining = nil
		channel.TPMRemaining = nil
		channel.ProviderBreakerState = nil
		channel.ProviderOpenRemainingMs = nil
		channel.ChannelBreakerState = nil
		channel.ChannelOpenRemainingMs = nil
		channel.AvgTTFTMs = nil
		channel.ErrorRatePct = nil
		channel.TTFTSampleCount = 0
		channel.ErrorSampleCount = 0
		channel.CooldownRemainingMs = 0
		channel.ModelPermissionPaused = false
		channel.ModelPermissionRecheckState = "unavailable"
		channel.CostScore = 0
		channel.ConcurrencyScore = 0
		channel.TTFTScore = 0
		channel.ErrorScore = 0
		channel.PriorityScore = 0
		channel.FinalScore = 0
		channel.CostRatio = nil
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

// SortChannels 就地按给定字段排序运行态渠道（Admin 只读展示用）。
// 支持 order / weight / capacity（最紧余量）/ concurrency / rpm / rpd / tpm，未知字段按 order。
// 不可路由渠道恒沉底、内部保持稳定池顺序，避免把「被排除」的渠道排进 ranking。
func SortChannels(channels []Channel, field string, desc bool) {
	rank := func(c Channel) float64 {
		switch field {
		case "weight", "score":
			return c.FinalScore
		case "capacity":
			return tightestRemaining(c)
		case "concurrency":
			return remainingOrOne(c.ConcurrencyRemaining)
		case "rpm":
			return remainingOrOne(c.RPMRemaining)
		case "rpd":
			return remainingOrOne(channelGlobalRPDRemaining(c))
		case "tpm":
			return remainingOrOne(c.TPMRemaining)
		default:
			return float64(c.CurrentOrder)
		}
	}
	sort.SliceStable(channels, func(i, j int) bool {
		li, lj := channels[i], channels[j]
		if li.Eligible != lj.Eligible {
			return li.Eligible // 可路由的在前
		}
		if !li.Eligible {
			return false // 两者都不可路由：保持稳定池顺序
		}
		vi, vj := rank(li), rank(lj)
		if vi == vj {
			return false
		}
		if desc {
			return vi > vj
		}
		return vi < vj
	})
}

// tightestRemaining 取四维限流里最紧的一维余量（不限=1，与前端容量条口径一致）。
func tightestRemaining(c Channel) float64 {
	remaining := 1.0
	for _, v := range []*float64{
		c.ConcurrencyRemaining, c.RPMRemaining, channelGlobalRPDRemaining(c), c.TPMRemaining,
	} {
		if r := remainingOrOne(v); r < remaining {
			remaining = r
		}
	}
	return remaining
}

func channelGlobalRPDRemaining(c Channel) *float64 {
	if c.GlobalRPDRemaining != nil {
		return c.GlobalRPDRemaining
	}
	return c.RPDRemaining
}

func remainingOrOne(v *float64) float64 {
	if v == nil {
		return 1
	}
	if *v < 0 {
		return 0
	}
	if *v > 1 {
		return 1
	}
	return *v
}

func assignCurrentOrder(channels []Channel, allZero bool) {
	_ = allZero
	indexes := make([]int, 0, len(channels))
	for index := range channels {
		if channels[index].Eligible {
			indexes = append(indexes, index)
		}
	}
	sort.SliceStable(indexes, func(i, j int) bool {
		left, right := channels[indexes[i]], channels[indexes[j]]
		if left.FinalScore != right.FinalScore {
			return left.FinalScore > right.FinalScore
		}
		if left.Priority != right.Priority {
			return left.Priority < right.Priority
		}
		return left.ChannelID < right.ChannelID
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
