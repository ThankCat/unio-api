package overview

import (
	"context"
	"net/http"

	"github.com/ThankCat/unio-gateway/internal/app/adminapi/adminhttp"
	"github.com/ThankCat/unio-gateway/internal/service/admin/modelrouting"
)

// LiveTrafficService 提供网关此刻的流量分布。
//
// 它与本模块其余接口的时间尺度不同：那些是历史 SQL 聚合（分钟到小时级），
// 这个是 Redis 运行态快照（秒级）。两者不要混在同一张表里展示，会让人误读。
type LiveTrafficService interface {
	LiveTraffic(ctx context.Context) (modelrouting.LiveTraffic, error)
}

// liveChannelDTO 的指针字段为 null 表示运行态读不到；0 意味着此刻没有请求。
type liveChannelDTO struct {
	ChannelID       int64  `json:"channel_id"`
	ChannelName     string `json:"channel_name"`
	ChannelStatus   string `json:"channel_status"`
	ProviderID      int64  `json:"provider_id"`
	ProviderName    string `json:"provider_name"`
	ProviderStatus  string `json:"provider_status"`
	CredentialValid bool   `json:"credential_valid"`
	Priority        int32  `json:"priority"`
	BoundModels     int64  `json:"bound_models"`

	ConcurrencyUsed     *int64 `json:"concurrency_used"`
	ConcurrencyLimit    *int64 `json:"concurrency_limit"`
	BreakerState        string `json:"breaker_state"`
	BreakerOpenRemainMs *int64 `json:"breaker_open_remaining_ms"`
	CooldownRemainingMs *int64 `json:"cooldown_remaining_ms"`
	RequestsThisMinute  *int64 `json:"requests_this_minute"`
	TokensThisMinute    *int64 `json:"tokens_this_minute"`
	// ttft_* 取 30 分钟对齐窗口，不是上面两个字段的「本分钟」口径；
	// 只有流式请求产生样本，无样本时为 null 而不是 0。
	TTFTAvgMs   *int64 `json:"ttft_avg_ms"`
	TTFTSamples *int64 `json:"ttft_samples"`
}

type liveModelDTO struct {
	ModelID          string `json:"model_id"`
	RequestTotal     int64  `json:"request_total"`
	RequestSucceeded int64  `json:"request_succeeded"`
}

type liveTrafficDTO struct {
	ObservedAt string `json:"observed_at"`
	// minute_started_at 是当前统计分钟的起点：本分钟的计数从这一刻开始累加，
	// 不是滚动 60 秒，跨分钟时会归零。
	MinuteStartedAt  string `json:"minute_started_at"`
	RuntimeAvailable bool   `json:"runtime_available"`
	RuntimeErrorCode string `json:"runtime_error_code,omitempty"`

	InFlightTotal       int64 `json:"in_flight_total"`
	RequestsThisMinute  int64 `json:"requests_this_minute"`
	TokensThisMinute    int64 `json:"tokens_this_minute"`
	ActiveChannels      int64 `json:"active_channels"`
	UnavailableChannels int64 `json:"unavailable_channels"`
	// 全网关首字延迟，按样本量加权；无样本时为 null。
	TTFTAvgMs   *int64 `json:"ttft_avg_ms"`
	TTFTSamples *int64 `json:"ttft_samples"`

	Channels []liveChannelDTO `json:"channels"`
	Models   []liveModelDTO   `json:"models"`
}

type liveTrafficHandler struct {
	service LiveTrafficService
}

func (h *liveTrafficHandler) live(w http.ResponseWriter, r *http.Request) {
	snapshot, err := h.service.LiveTraffic(r.Context())
	if err != nil {
		adminhttp.WriteServiceError(w, err)
		return
	}
	out := liveTrafficDTO{
		ObservedAt:          adminhttp.RFC3339(snapshot.ObservedAt),
		MinuteStartedAt:     adminhttp.RFC3339(snapshot.MinuteStartedAt),
		RuntimeAvailable:    snapshot.RuntimeAvailable,
		RuntimeErrorCode:    snapshot.RuntimeErrorCode,
		InFlightTotal:       snapshot.InFlightTotal,
		RequestsThisMinute:  snapshot.RequestsThisMinute,
		TokensThisMinute:    snapshot.TokensThisMinute,
		ActiveChannels:      snapshot.ActiveChannels,
		UnavailableChannels: snapshot.UnavailableChannels,
		TTFTAvgMs:           snapshot.TTFTAvgMs,
		TTFTSamples:         snapshot.TTFTSamples,
		Channels:            make([]liveChannelDTO, 0, len(snapshot.Channels)),
		Models:              make([]liveModelDTO, 0, len(snapshot.Models)),
	}
	for _, channel := range snapshot.Channels {
		out.Channels = append(out.Channels, liveChannelDTO{
			ChannelID:           channel.ChannelID,
			ChannelName:         channel.ChannelName,
			ChannelStatus:       channel.ChannelStatus,
			ProviderID:          channel.ProviderID,
			ProviderName:        channel.ProviderName,
			ProviderStatus:      channel.ProviderStatus,
			CredentialValid:     channel.CredentialValid,
			Priority:            channel.Priority,
			BoundModels:         channel.BoundModels,
			ConcurrencyUsed:     channel.ConcurrencyUsed,
			ConcurrencyLimit:    channel.ConcurrencyLimit,
			BreakerState:        channel.BreakerState,
			BreakerOpenRemainMs: channel.BreakerOpenRemainMs,
			CooldownRemainingMs: channel.CooldownRemainingMs,
			RequestsThisMinute:  channel.RequestsThisMinute,
			TokensThisMinute:    channel.TokensThisMinute,
			TTFTAvgMs:           channel.TTFTAvgMs,
			TTFTSamples:         channel.TTFTSamples,
		})
	}
	for _, model := range snapshot.Models {
		out.Models = append(out.Models, liveModelDTO{
			ModelID:          model.ModelID,
			RequestTotal:     model.RequestTotal,
			RequestSucceeded: model.RequestSucceeded,
		})
	}
	adminhttp.WriteData(w, http.StatusOK, out)
}
