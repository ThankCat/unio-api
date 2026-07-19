// Package routeruntime aggregates read-only route capacity and health diagnostics.
package routeruntime

import (
	"context"
	"errors"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/core/routingdiagnostic"
	"github.com/ThankCat/unio-gateway/internal/platform/failure"
	"github.com/ThankCat/unio-gateway/internal/platform/ratelimit"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/gatewayruntime"
	"github.com/ThankCat/unio-gateway/internal/service/appsettings"
	"github.com/ThankCat/unio-gateway/internal/service/gateway/lifecycle"
)

const staleAfter = 10 * time.Second

type Store interface {
	GetRouteByID(context.Context, int64) (sqlc.Route, error)
	RouteRuntimePool(context.Context, sqlc.RouteRuntimePoolParams) ([]sqlc.RouteRuntimePoolRow, error)
	RouteRuntimeChannelStats(context.Context, sqlc.RouteRuntimeChannelStatsParams) ([]sqlc.RouteRuntimeChannelStatsRow, error)
}

type GatewaySnapshotter interface {
	Snapshot(context.Context) gatewayruntime.Snapshot
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

type InstanceSnapshot = gatewayruntime.InstanceStatus
type GatewaySource = gatewayruntime.SourceStatus

type Channel struct {
	ChannelID            int64
	ChannelName          string
	ChannelStatus        string
	ProviderID           int64
	ProviderName         string
	ProviderStatus       string
	Protocol             string
	AdapterKey           string
	Priority             int32
	Eligible             bool
	ExcludedReason       string
	ConcurrencyUsed      int64
	ConcurrencyLimit     int64
	ConcurrencyRemaining *float64
	TPMUsed              int64
	TPMLimit             int64
	TPMRemaining         *float64
	CapacityScore        float64
	HealthFactor         float64
	FinalWeight          float64
	Pressure             float64
	CapacityUnknown      bool
	CapacityReadFailed   bool
	BreakerState         string
	ErrorRate            float64
	LatencyEWMAMs        float64
	CurrentOrder         int
	Selected1m           int64
	Selected5m           int64
	SelectedShare1m      float64
	SelectedShare5m      float64
	Fallback1m           int64
	MarginStatus         string
	InstanceSnapshots    []InstanceSnapshot
}

type Runtime struct {
	RouteID          int64
	Mode             string
	RouteStatus      string
	ModelID          string
	Protocol         string
	ObservedAt       time.Time
	Stale            bool
	PoolSize         int
	CandidateCount   int
	NoRedundancy     bool
	AllCapacityZero  bool
	CapacityDegraded bool
	Sources          []Source
	GatewaySources   []GatewaySource
	Channels         []Channel
}

type Service struct {
	store       Store
	concurrency *ratelimit.ConcurrencyLimiter
	guard       *ratelimit.Guard
	health      GatewaySnapshotter
	settings    *appsettings.SettingsStore
	now         func() time.Time
}

func NewService(
	store Store,
	concurrency *ratelimit.ConcurrencyLimiter,
	guard *ratelimit.Guard,
	health GatewaySnapshotter,
	settings *appsettings.SettingsStore,
) *Service {
	return &Service{store: store, concurrency: concurrency, guard: guard, health: health, settings: settings, now: time.Now}
}

func (s *Service) Get(ctx context.Context, params Params) (Runtime, error) {
	if params.RouteID <= 0 {
		return Runtime{}, invalidArgument("route_id", "route_id must be a positive integer")
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
		RouteID: params.RouteID, ModelID: params.ModelID,
		AtTime: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return Runtime{}, storeFailed(err, "list route runtime pool")
	}
	statsRows, err := s.store.RouteRuntimeChannelStats(ctx, sqlc.RouteRuntimeChannelStatsParams{
		RouteID: params.RouteID, ObservedAt: pgtype.Timestamptz{Time: now, Valid: true},
	})
	if err != nil {
		return Runtime{}, storeFailed(err, "read route runtime stats")
	}
	stats := make(map[int64]sqlc.RouteRuntimeChannelStatsRow, len(statsRows))
	for _, row := range statsRows {
		stats[row.ChannelID] = row
	}

	runtime := Runtime{
		RouteID: params.RouteID, ModelID: params.ModelID, Protocol: params.Protocol,
		Mode: route.Mode, RouteStatus: route.Status,
		ObservedAt: now, PoolSize: len(rows), Channels: make([]Channel, len(rows)),
	}
	if len(rows) > 0 {
		runtime.Mode, runtime.RouteStatus = rows[0].Mode, rows[0].RouteStatus
	}

	capacitySettings := appsettings.DefaultConcurrencyDefaultsSettings()
	rateSettings := appsettings.DefaultRateLimitDefaultsSettings()
	balanceSettings := appsettings.DefaultRoutingBalanceSettings()
	if s.settings != nil {
		capacitySettings = appsettings.GatewayConcurrencyDefaults(ctx, s.settings)
		rateSettings = appsettings.GatewayRateLimitDefaults(ctx, s.settings)
		balanceSettings = appsettings.GatewayRoutingBalance(ctx, s.settings)
	}
	if s.concurrency != nil {
		s.concurrency.SetDefaults(capacitySettings.KeyLimit, capacitySettings.ChannelLimit)
	}
	if s.guard != nil {
		s.guard.SetDefaults(ratelimit.DefaultLimits{RPM: rateSettings.RPM, TPM: rateSettings.TPM, RPD: rateSettings.RPD})
		s.guard.SetFailOpen(rateSettings.FailOpen())
	}
	var healthSnapshot gatewayruntime.Snapshot
	var healthWG sync.WaitGroup
	healthWG.Add(1)
	go func() {
		defer healthWG.Done()
		if s.health != nil {
			healthSnapshot = s.health.Snapshot(ctx)
		} else {
			healthSnapshot = gatewayruntime.Snapshot{Channels: map[int64]gatewayruntime.ChannelStatus{}, Sources: []gatewayruntime.SourceStatus{}}
		}
	}()

	type capacityResult struct {
		concurrency ratelimit.UsageSnapshot
		tpm         ratelimit.UsageSnapshot
		failed      bool
	}
	capacities := make([]capacityResult, len(rows))
	var capacityWG sync.WaitGroup
	var capacityFailed atomic.Bool
	for i := range rows {
		capacityWG.Add(1)
		go func(i int) {
			defer capacityWG.Done()
			row := rows[i]
			concurrencyLimit := int4Ptr(row.ConcurrencyLimit)
			tpmLimit := int4Ptr(row.TpmLimit)
			if s.concurrency == nil || s.guard == nil {
				capacities[i].failed = true
				capacityFailed.Store(true)
				return
			}
			concurrency, concurrencyErr := s.concurrency.ChannelSnapshot(ctx, row.ChannelID, concurrencyLimit)
			tpm, tpmErr := s.guard.ChannelTPMSnapshot(ctx, row.ChannelID, tpmLimit)
			capacities[i] = capacityResult{concurrency: concurrency, tpm: tpm, failed: concurrencyErr != nil || tpmErr != nil}
			if capacities[i].failed {
				capacityFailed.Store(true)
			}
		}(i)
	}
	capacityWG.Wait()
	healthWG.Wait()

	totalSelected1m, totalSelected5m := int64(0), int64(0)
	for _, stat := range statsRows {
		totalSelected1m += stat.Selected1m
		totalSelected5m += stat.Selected5m
	}
	allUnknown, allZero := true, len(rows) > 0
	for i, row := range rows {
		health := healthSnapshot.Channels[row.ChannelID]
		excluded := excludedReason(row, params, health)
		channel := Channel{
			ChannelID: row.ChannelID, ChannelName: row.ChannelName, ChannelStatus: row.ChannelStatus,
			ProviderID: row.ProviderID, ProviderName: row.ProviderName, ProviderStatus: row.ProviderStatus,
			Protocol: row.Protocol, AdapterKey: row.AdapterKey, Priority: row.Priority,
			Eligible: excluded == "", ExcludedReason: excluded,
			BreakerState: string(health.State), ErrorRate: health.ErrorRate, LatencyEWMAMs: health.LatencyEWMAMs,
			InstanceSnapshots: nonNilInstances(health.Instances), MarginStatus: "not_evaluated",
		}
		if channel.BreakerState == "" {
			channel.BreakerState = string(lifecycle.CircuitStateClosed)
		}
		cap := capacities[i]
		channel.ConcurrencyUsed, channel.ConcurrencyLimit = cap.concurrency.Used, cap.concurrency.Limit
		channel.TPMUsed, channel.TPMLimit = cap.tpm.Used, cap.tpm.Limit
		snapshot := lifecycle.ChannelCapacity{
			Concurrency: lifecycle.CapacitySignal{Used: cap.concurrency.Used, Limit: cap.concurrency.Limit, Known: cap.concurrency.Known && !cap.failed},
			TPM:         lifecycle.CapacitySignal{Used: cap.tpm.Used, Limit: cap.tpm.Limit, Known: cap.tpm.Known && !cap.failed},
		}
		healthScore := health.HealthScore
		if !balanceSettings.Enabled {
			snapshot = unlimitedCapacity()
			healthScore = 0
		} else if !balanceSettings.WeightByRemaining {
			snapshot = unlimitedCapacity()
		}
		score := lifecycle.ScoreBalanceCandidate(snapshot, healthScore)
		score.CapacityReadFailed = cap.failed
		channel.ConcurrencyRemaining, channel.TPMRemaining = score.ConcurrencyRemaining, score.TPMRemaining
		channel.CapacityScore, channel.HealthFactor, channel.FinalWeight = score.CapacityScore, score.HealthFactor, score.Weight
		channel.Pressure, channel.CapacityUnknown, channel.CapacityReadFailed = score.Pressure, score.CapacityUnknown, score.CapacityReadFailed
		if channel.Eligible {
			runtime.CandidateCount++
			if !score.CapacityUnknown {
				allUnknown = false
			}
			if score.CapacityScore > 0 {
				allZero = false
			}
			channel.MarginStatus = "safe"
		} else {
			channel.FinalWeight = 0
		}
		stat := stats[row.ChannelID]
		channel.Selected1m, channel.Selected5m, channel.Fallback1m = stat.Selected1m, stat.Selected5m, stat.Fallback1m
		if totalSelected1m > 0 {
			channel.SelectedShare1m = float64(stat.Selected1m) / float64(totalSelected1m)
		}
		if totalSelected5m > 0 {
			channel.SelectedShare5m = float64(stat.Selected5m) / float64(totalSelected5m)
		}
		runtime.Channels[i] = channel
	}
	if runtime.CandidateCount == 0 {
		allZero = false
	}
	if allUnknown && runtime.CandidateCount > 0 {
		for i := range runtime.Channels {
			if runtime.Channels[i].Eligible {
				runtime.Channels[i].CapacityScore = 1
				runtime.Channels[i].HealthFactor = 1
				runtime.Channels[i].FinalWeight = 1
			}
		}
		allZero = false
	}
	runtime.AllCapacityZero = allZero
	runtime.CapacityDegraded = capacityFailed.Load()
	runtime.NoRedundancy = runtime.CandidateCount <= 1
	assignCurrentOrder(runtime.Channels, allZero)

	redisAvailable := !capacityFailed.Load() && s.concurrency != nil && s.guard != nil
	gatewayStale := !healthSnapshot.Available || healthSnapshot.ObservedAt.IsZero()
	for _, source := range healthSnapshot.Sources {
		if !source.Available || source.ObservedAt.IsZero() || now.Sub(source.ObservedAt) > staleAfter {
			gatewayStale = true
			break
		}
	}
	runtime.Sources = []Source{
		{Name: "postgres", Available: true, ObservedAt: now},
		{Name: "redis", Available: redisAvailable, ObservedAt: observedIf(redisAvailable, now), Stale: !redisAvailable},
		{Name: "gateway", Available: healthSnapshot.Available, ObservedAt: healthSnapshot.ObservedAt, Stale: gatewayStale},
		{Name: "attempts", Available: true, ObservedAt: now},
	}
	runtime.GatewaySources = nonNilGatewaySources(healthSnapshot.Sources)
	runtime.Stale = !redisAvailable || gatewayStale
	return runtime, nil
}

func excludedReason(row sqlc.RouteRuntimePoolRow, params Params, health gatewayruntime.ChannelStatus) string {
	reason := routingdiagnostic.ExcludedReason(routingdiagnostic.PoolFacts{
		RouteStatus: row.RouteStatus, ChannelStatus: row.ChannelStatus, ProviderStatus: row.ProviderStatus,
		CredentialValid: row.CredentialValid, HasCredential: row.HasCredential, HasBaseURL: row.HasBaseUrl,
		Protocol: row.Protocol, ModelExists: row.ModelExists, ModelStatus: row.ModelStatus,
		BindingStatus: row.BindingStatus, HasModelPrice: row.HasModelPrice, HasChannelCost: row.HasChannelCost,
	}, routingdiagnostic.Filter{ModelID: params.ModelID, Protocol: params.Protocol})
	if reason != "" {
		return reason
	}
	switch {
	case health.State == lifecycle.CircuitStateOpen && remainingMs(health.OpenRemainingMs) > 0:
		return "breaker_open"
	case health.State == lifecycle.CircuitStateHalfOpen && health.HalfOpenInFlight:
		return "breaker_half_open_busy"
	default:
		return ""
	}
}

func unlimitedCapacity() lifecycle.ChannelCapacity {
	return lifecycle.ChannelCapacity{
		Concurrency: lifecycle.CapacitySignal{Limit: 0, Known: true},
		TPM:         lifecycle.CapacitySignal{Limit: 0, Known: true},
	}
}

func assignCurrentOrder(channels []Channel, allZero bool) {
	indexes := make([]int, 0, len(channels))
	for i := range channels {
		if channels[i].Eligible {
			indexes = append(indexes, i)
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

func int4Ptr(value pgtype.Int4) *int64 {
	if !value.Valid {
		return nil
	}
	v := int64(value.Int32)
	return &v
}

func remainingMs(value *int64) int64 {
	if value == nil {
		return 0
	}
	return *value
}

func observedIf(ok bool, value time.Time) time.Time {
	if !ok {
		return time.Time{}
	}
	return value
}

func nonNilInstances(value []gatewayruntime.InstanceStatus) []gatewayruntime.InstanceStatus {
	if value == nil {
		return []gatewayruntime.InstanceStatus{}
	}
	return value
}

func nonNilGatewaySources(value []gatewayruntime.SourceStatus) []gatewayruntime.SourceStatus {
	if value == nil {
		return []gatewayruntime.SourceStatus{}
	}
	return value
}

func invalidArgument(field, message string) error {
	return failure.New(failure.CodeAdminInvalidArgument, failure.WithMessage(message), failure.WithField("field", field))
}

func storeFailed(err error, operation string) error {
	return failure.Wrap(failure.CodeAdminStoreFailed, err, failure.WithMessage(operation+" failed"))
}
