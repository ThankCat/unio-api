package route

import (
	"context"
	"net/http"
	"time"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/service/admin/routeruntime"
)

type RuntimeService interface {
	Get(context.Context, routeruntime.Params) (routeruntime.Runtime, error)
}

type runtimeHandler struct {
	service RuntimeService
}

type runtimeSourceDTO struct {
	Name       string  `json:"name"`
	Available  bool    `json:"available"`
	ObservedAt *string `json:"observed_at"`
	Stale      bool    `json:"stale"`
}

type sourceStatusDTO struct {
	State                 string             `json:"state"`
	BreakerStoreAdmission string             `json:"breaker_store_admission"`
	ObservedAt            string             `json:"observed_at"`
	Stale                 bool               `json:"stale"`
	Sources               []runtimeSourceDTO `json:"sources"`
}

type routeUsageDTO struct {
	Concurrency int64 `json:"concurrency"`
	RPM         int64 `json:"rpm"`
	RPD         int64 `json:"rpd"`
	TPM         int64 `json:"tpm"`
	ActiveUsers int64 `json:"active_users"`
}

type routeSummaryDTO struct {
	RouteID         int64          `json:"route_id"`
	Mode            string         `json:"mode"`
	Status          string         `json:"status"`
	PoolSize        int            `json:"pool_size"`
	CandidateCount  int            `json:"candidate_count"`
	NoRedundancy    bool           `json:"no_redundancy"`
	AllCapacityFull bool           `json:"all_capacity_full"`
	Usage           *routeUsageDTO `json:"usage"`
}

type runtimeFiltersDTO struct {
	ModelID  string `json:"model_id"`
	Protocol string `json:"protocol"`
}

type scoreConfigDTO struct {
	AlgorithmVersion             string  `json:"algorithm_version"`
	Revision                     int64   `json:"revision"`
	CostWeightPct                int     `json:"cost_weight_pct"`
	ConcurrencyWeightPct         int     `json:"concurrency_weight_pct"`
	TTFTWeightPct                int     `json:"ttft_weight_pct"`
	ErrorRateWeightPct           int     `json:"error_rate_weight_pct"`
	PriorityWeightPct            int     `json:"priority_weight_pct"`
	TTFTPenaltyUnitMs            int64   `json:"ttft_penalty_unit_ms"`
	TTFTPenaltyPointsPerUnit     float64 `json:"ttft_penalty_points_per_unit"`
	ErrorPenaltyPointsPerPercent float64 `json:"error_penalty_points_per_percent"`
}

type sampleWindowDTO struct {
	TTFTWindowMs  int64   `json:"ttft_window_ms"`
	ErrorWindowMs int64   `json:"error_window_ms"`
	StartedAt     *string `json:"started_at"`
	EndedAt       *string `json:"ended_at"`
	Available     bool    `json:"available"`
}

type providerSummaryDTO struct {
	ID     int64  `json:"id"`
	Name   string `json:"name"`
	Status string `json:"status"`
}

type eligibilityCheckDTO struct {
	Key    string `json:"key"`
	Status string `json:"status"`
	Reason string `json:"reason,omitempty"`
}

type eligibilityDTO struct {
	Status        string                `json:"status"`
	PrimaryReason string                `json:"primary_reason,omitempty"`
	Reasons       []string              `json:"reasons"`
	Checks        []eligibilityCheckDTO `json:"checks"`
}

type runtimeStateDTO struct {
	State                 string `json:"state"`
	ConfigSynchronized    bool   `json:"config_synchronized"`
	BreakerStoreAdmission string `json:"breaker_store_admission"`
	CapacityReadFailed    bool   `json:"capacity_read_failed"`
}

type concurrencyDTO struct {
	Used         int64    `json:"used"`
	Limit        int64    `json:"limit"`
	Remaining    *int64   `json:"remaining"`
	RemainingPct *float64 `json:"remaining_pct"`
	Unlimited    bool     `json:"unlimited"`
	MetricScore  float64  `json:"metric_score"`
	Contribution float64  `json:"contribution"`
}

type qualityMetricDTO struct {
	HasSamples   bool     `json:"has_samples"`
	Value        *float64 `json:"value"`
	SampleCount  int64    `json:"sample_count"`
	MetricScore  float64  `json:"metric_score"`
	Contribution float64  `json:"contribution"`
}

type qualityDTO struct {
	TTFT      qualityMetricDTO `json:"ttft"`
	ErrorRate qualityMetricDTO `json:"error_rate"`
}

type trafficDTO struct {
	RPM               int64   `json:"rpm"`
	RPD               int64   `json:"rpd"`
	TPM               int64   `json:"tpm"`
	TokenCoveredCount int64   `json:"token_covered_attempts"`
	TokenCoveragePct  float64 `json:"token_coverage_pct"`
}

type scoreComponentDTO struct {
	MetricScore  float64 `json:"metric_score"`
	WeightPct    int     `json:"weight_pct"`
	Contribution float64 `json:"contribution"`
}

type scoreDTO struct {
	AlgorithmVersion string            `json:"algorithm_version"`
	Total            float64           `json:"total"`
	CostRatio        *float64          `json:"cost_ratio"`
	Priority         int32             `json:"priority"`
	Cost             scoreComponentDTO `json:"cost"`
	Concurrency      scoreComponentDTO `json:"concurrency"`
	TTFT             scoreComponentDTO `json:"ttft"`
	ErrorRate        scoreComponentDTO `json:"error_rate"`
	PriorityScore    scoreComponentDTO `json:"priority_score"`
}

type distributionDTO struct {
	Selected1m      int64   `json:"selected_1m"`
	Selected5m      int64   `json:"selected_5m"`
	SelectedShare1m float64 `json:"selected_share_1m"`
	SelectedShare5m float64 `json:"selected_share_5m"`
	Fallback1m      int64   `json:"fallback_1m"`
}

type runtimeDiagnosticsDTO struct {
	OriginRevision                 int64  `json:"origin_revision"`
	RuntimeOriginRevision          int64  `json:"runtime_origin_revision"`
	ProviderStatusRevision         int64  `json:"provider_status_revision"`
	RuntimeProviderStatusRevision  int64  `json:"runtime_provider_status_revision"`
	ChannelConfigRevision          int64  `json:"channel_config_revision"`
	RuntimeChannelConfigRevision   *int64 `json:"runtime_channel_config_revision"`
	ChannelCapacityRevision        int64  `json:"channel_capacity_revision"`
	RuntimeChannelCapacityRevision int64  `json:"runtime_channel_capacity_revision"`
	GlobalConcurrencyRevision      int64  `json:"global_concurrency_revision"`
	CircuitBreakerRevision         int64  `json:"circuit_breaker_revision"`
	RoutingBalanceRevision         int64  `json:"routing_balance_revision"`
	RuntimeControlState            string `json:"runtime_control_state"`
}

type runtimeChannelDTO struct {
	ChannelID     int64                 `json:"channel_id"`
	ChannelName   string                `json:"channel_name"`
	ChannelStatus string                `json:"channel_status"`
	Provider      providerSummaryDTO    `json:"provider"`
	Protocol      string                `json:"protocol"`
	AdapterKey    string                `json:"adapter_key"`
	Priority      int32                 `json:"priority"`
	Order         int                   `json:"order"`
	Eligibility   eligibilityDTO        `json:"eligibility"`
	Runtime       runtimeStateDTO       `json:"runtime"`
	Concurrency   concurrencyDTO        `json:"concurrency"`
	Quality       qualityDTO            `json:"quality"`
	Traffic       trafficDTO            `json:"traffic"`
	Score         scoreDTO              `json:"score"`
	Distribution  distributionDTO       `json:"distribution"`
	Diagnostics   runtimeDiagnosticsDTO `json:"internal_diagnostics"`
}

type routeRuntimeDTO struct {
	SourceStatus sourceStatusDTO     `json:"source_status"`
	RouteSummary routeSummaryDTO     `json:"route_summary"`
	Filters      runtimeFiltersDTO   `json:"filters"`
	Channels     []runtimeChannelDTO `json:"channels"`
	ScoreConfig  scoreConfigDTO      `json:"score_config"`
	SampleWindow sampleWindowDTO     `json:"sample_window"`
}

func (h *runtimeHandler) get(w http.ResponseWriter, r *http.Request) {
	routeID, err := adminhttp.PathID(r)
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	sortSpec, err := adminhttp.ParseListSort(r, map[string]struct{}{
		"order": {}, "score": {}, "concurrency": {}, "ttft": {}, "error": {}, "rpm": {},
	}, "order", false)
	if err != nil {
		adminhttp.WriteSortError(w, err)
		return
	}
	runtime, err := h.service.Get(r.Context(), routeruntime.Params{
		RouteID: routeID, ModelID: adminhttp.QueryString(r, "model_id"), Protocol: adminhttp.QueryString(r, "protocol"),
	})
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	field, desc := sortSpec.SQLParams()
	routeruntime.SortChannels(runtime.Channels, field, desc)
	adminhttp.WriteData(w, http.StatusOK, toRouteRuntimeDTO(runtime))
}

func toRouteRuntimeDTO(value routeruntime.Runtime) routeRuntimeDTO {
	out := routeRuntimeDTO{
		SourceStatus: sourceStatusDTO{
			State: value.RuntimeSyncState, BreakerStoreAdmission: value.BreakerStoreAdmission,
			ObservedAt: adminhttp.RFC3339(value.ObservedAt), Stale: value.Stale,
			Sources: make([]runtimeSourceDTO, 0, len(value.Sources)),
		},
		RouteSummary: routeSummaryDTO{
			RouteID: value.RouteID, Mode: value.Mode, Status: value.RouteStatus,
			PoolSize: value.PoolSize, CandidateCount: value.CandidateCount,
			NoRedundancy: value.NoRedundancy, AllCapacityFull: value.AllCapacityZero,
		},
		Filters:  runtimeFiltersDTO{ModelID: value.ModelID, Protocol: value.Protocol},
		Channels: make([]runtimeChannelDTO, 0, len(value.Channels)),
		ScoreConfig: scoreConfigDTO{
			AlgorithmVersion: value.ScoreConfig.AlgorithmVersion, Revision: value.ScoreConfig.Revision,
			CostWeightPct: value.ScoreConfig.CostWeightPct, ConcurrencyWeightPct: value.ScoreConfig.ConcurrencyWeightPct,
			TTFTWeightPct: value.ScoreConfig.TTFTWeightPct, ErrorRateWeightPct: value.ScoreConfig.ErrorRateWeightPct,
			PriorityWeightPct: value.ScoreConfig.PriorityWeightPct, TTFTPenaltyUnitMs: value.ScoreConfig.TTFTPenaltyUnitMs,
			TTFTPenaltyPointsPerUnit:     value.ScoreConfig.TTFTPenaltyPointsPerUnit,
			ErrorPenaltyPointsPerPercent: value.ScoreConfig.ErrorPenaltyPointsPerPercent,
		},
		SampleWindow: sampleWindowDTO{
			TTFTWindowMs: value.SampleWindow.TTFTWindowMs, ErrorWindowMs: value.SampleWindow.ErrorWindowMs,
			StartedAt: optionalRuntimeTime(value.SampleWindow.StartedAt), EndedAt: optionalRuntimeTime(value.SampleWindow.EndedAt),
			Available: value.SampleWindow.Available,
		},
	}
	if value.RouteUsage != nil {
		out.RouteSummary.Usage = &routeUsageDTO{
			Concurrency: value.RouteUsage.Concurrency, RPM: value.RouteUsage.RPM, RPD: value.RouteUsage.RPD,
			TPM: value.RouteUsage.TPM, ActiveUsers: value.RouteUsage.ActiveUsers,
		}
	}
	for _, source := range value.Sources {
		out.SourceStatus.Sources = append(out.SourceStatus.Sources, runtimeSourceDTO{
			Name: source.Name, Available: source.Available, ObservedAt: optionalRuntimeTime(source.ObservedAt), Stale: source.Stale,
		})
	}
	for _, channel := range value.Channels {
		out.Channels = append(out.Channels, toRuntimeChannelDTO(value.RouteStatus, channel))
	}
	return out
}

func toRuntimeChannelDTO(routeStatus string, channel routeruntime.Channel) runtimeChannelDTO {
	remaining := (*int64)(nil)
	if channel.ConcurrencyLimit > 0 {
		value := channel.ConcurrencyLimit - channel.ConcurrencyUsed
		if value < 0 {
			value = 0
		}
		remaining = &value
	}
	eligibility := eligibilityOf(routeStatus, channel)
	return runtimeChannelDTO{
		ChannelID: channel.ChannelID, ChannelName: channel.ChannelName, ChannelStatus: channel.ChannelStatus,
		Provider: providerSummaryDTO{ID: channel.ProviderID, Name: channel.ProviderName, Status: channel.ProviderStatus},
		Protocol: channel.Protocol, AdapterKey: channel.AdapterKey, Priority: channel.Priority, Order: channel.CurrentOrder,
		Eligibility: eligibility,
		Runtime: runtimeStateDTO{
			State: channel.RuntimeSyncState, ConfigSynchronized: channel.RuntimeRevisionCurrent,
			BreakerStoreAdmission: channel.BreakerStoreAdmission, CapacityReadFailed: channel.CapacityReadFailed,
		},
		Concurrency: concurrencyDTO{
			Used: channel.ConcurrencyUsed, Limit: channel.ConcurrencyLimit, Remaining: remaining,
			RemainingPct: channel.ConcurrencyRemaining, Unlimited: channel.ConcurrencyLimit <= 0,
			MetricScore:  channel.ConcurrencyScore,
			Contribution: contribution(channel.ConcurrencyScore, channel.ConcurrencyWeightPct),
		},
		Quality: qualityDTO{
			TTFT: qualityMetricDTO{
				HasSamples: channel.TTFTSampleCount > 0, Value: channel.AvgTTFTMs, SampleCount: channel.TTFTSampleCount,
				MetricScore: channel.TTFTScore, Contribution: contribution(channel.TTFTScore, channel.TTFTWeightPct),
			},
			ErrorRate: qualityMetricDTO{
				HasSamples: channel.ErrorSampleCount > 0, Value: channel.ErrorRatePct, SampleCount: channel.ErrorSampleCount,
				MetricScore: channel.ErrorScore, Contribution: contribution(channel.ErrorScore, channel.ErrorRateWeightPct),
			},
		},
		Traffic: trafficDTO{
			RPM: channel.RPMUsed, RPD: channel.GlobalRPDUsed, TPM: channel.TPMUsed,
			TokenCoveredCount: channel.TokenCoveredCount, TokenCoveragePct: channel.TokenCoveragePct,
		},
		Score: scoreDTO{
			AlgorithmVersion: channel.AlgorithmVersion, Total: channel.FinalScore, CostRatio: channel.CostRatio, Priority: channel.Priority,
			Cost:          scorePart(channel.CostScore, channel.CostWeightPct),
			Concurrency:   scorePart(channel.ConcurrencyScore, channel.ConcurrencyWeightPct),
			TTFT:          scorePart(channel.TTFTScore, channel.TTFTWeightPct),
			ErrorRate:     scorePart(channel.ErrorScore, channel.ErrorRateWeightPct),
			PriorityScore: scorePart(channel.PriorityScore, channel.PriorityWeightPct),
		},
		Distribution: distributionDTO{
			Selected1m: channel.Selected1m, Selected5m: channel.Selected5m,
			SelectedShare1m: channel.SelectedShare1m, SelectedShare5m: channel.SelectedShare5m,
			Fallback1m: channel.Fallback1m,
		},
		Diagnostics: runtimeDiagnosticsDTO{
			OriginRevision: channel.OriginRevision, RuntimeOriginRevision: channel.RuntimeOriginRevision,
			ProviderStatusRevision: channel.ProviderStatusRevision, RuntimeProviderStatusRevision: channel.RuntimeProviderStatusRevision,
			ChannelConfigRevision: channel.ChannelConfigRevision, RuntimeChannelConfigRevision: channel.RuntimeChannelConfigRevision,
			ChannelCapacityRevision:        channel.ChannelCapacityRevision,
			RuntimeChannelCapacityRevision: channel.RuntimeChannelCapacityRevision,
			GlobalConcurrencyRevision:      channel.GlobalConcurrencyRevision, CircuitBreakerRevision: channel.CircuitBreakerRevision,
			RoutingBalanceRevision: channel.RoutingBalanceRevision, RuntimeControlState: channel.RuntimeControlState,
		},
	}
}

func eligibilityOf(routeStatus string, channel routeruntime.Channel) eligibilityDTO {
	status := "eligible"
	if !channel.Eligible {
		status = "excluded"
	} else if breakerHalfOpen(channel.ProviderBreakerState) || breakerHalfOpen(channel.ChannelBreakerState) {
		status = "probe_only"
	}
	reasons := make([]string, 0, 1)
	if channel.ExcludedReason != "" {
		reasons = append(reasons, channel.ExcludedReason)
	}
	return eligibilityDTO{
		Status: status, PrimaryReason: channel.ExcludedReason, Reasons: reasons,
		Checks: []eligibilityCheckDTO{
			check("route", routeStatus == "enabled", "route_"+routeStatus),
			check("provider", channel.ProviderStatus == "enabled", "provider_"+channel.ProviderStatus),
			check("channel", channel.ChannelStatus == "enabled", "channel_"+channel.ChannelStatus),
			check("margin", channel.MarginStatus == "safe", channel.MarginStatus),
			check("provider_breaker", !breakerUnavailable(channel.ProviderBreakerState), "provider_breaker_open"),
			check("channel_breaker", !breakerUnavailable(channel.ChannelBreakerState), "channel_breaker_open"),
			check("cooldown", channel.CooldownRemainingMs <= 0, "cooldown"),
			check("model_permission", !channel.ModelPermissionPaused, "model_permission_paused"),
			check("runtime", channel.RuntimeRevisionCurrent, channel.RuntimeSyncState),
		},
	}
}

func check(key string, passed bool, reason string) eligibilityCheckDTO {
	if passed {
		return eligibilityCheckDTO{Key: key, Status: "passed"}
	}
	return eligibilityCheckDTO{Key: key, Status: "failed", Reason: reason}
}

func breakerHalfOpen(state *string) bool {
	return state != nil && *state == "half_open"
}

func breakerUnavailable(state *string) bool {
	return state != nil && (*state == "open" || *state == "half_open_busy")
}

func scorePart(score float64, weight int) scoreComponentDTO {
	return scoreComponentDTO{MetricScore: score, WeightPct: weight, Contribution: contribution(score, weight)}
}

func contribution(score float64, weight int) float64 {
	return score * float64(weight) / 100
}

func optionalRuntimeTime(value time.Time) *string {
	if value.IsZero() {
		return nil
	}
	formatted := adminhttp.RFC3339(value)
	return &formatted
}
