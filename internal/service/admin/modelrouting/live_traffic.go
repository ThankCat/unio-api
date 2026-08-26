package modelrouting

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgtype"

	"github.com/ThankCat/unio-gateway/internal/platform/breakerstore"
	"github.com/ThankCat/unio-gateway/internal/platform/store/sqlc"
	"github.com/ThankCat/unio-gateway/internal/service/admin/opsutil"
)

// liveTrafficModelLimit 限制模型维度返回多少行。这个视图是给人扫的，
// 全量返回既没人看得完，也会让每 5 秒一次的轮询变重。
const liveTrafficModelLimit = 20

// LiveChannel 是流量视图里的一条渠道。
//
// 指针字段为 nil 表示运行态读不到，与 0 不是一回事：0 意味着此刻没有请求，
// nil 意味着不知道。把后者显示成前者会让管理员以为渠道闲着。
type LiveChannel struct {
	ChannelID       int64
	ChannelName     string
	ChannelStatus   string
	ProviderID      int64
	ProviderName    string
	ProviderStatus  string
	CredentialValid bool
	Priority        int32
	BoundModels     int64

	ConcurrencyUsed     *int64
	ConcurrencyLimit    *int64
	BreakerState        string
	BreakerOpenRemainMs *int64
	CooldownRemainingMs *int64
	// RequestsThisMinute / TokensThisMinute 取自然 UTC 分钟桶，不是滚动 60 秒：
	// 每分钟开始时会归零，界面文案必须写「本分钟」而不是「近 1 分钟」。
	RequestsThisMinute *int64
	TokensThisMinute   *int64
}

// LiveModel 是流量视图里的一个模型（本分钟请求量）。
type LiveModel struct {
	ModelID          string
	RequestTotal     int64
	RequestSucceeded int64
}

// LiveTraffic 是全局流量快照。
type LiveTraffic struct {
	ObservedAt time.Time
	// MinuteStartedAt 是当前统计分钟的起点，供界面说明这些数字覆盖多长时间。
	MinuteStartedAt time.Time
	// RuntimeAvailable 为 false 表示 Redis 运行态没读到，渠道只有静态配置。
	RuntimeAvailable bool
	RuntimeErrorCode string

	Channels []LiveChannel
	Models   []LiveModel

	InFlightTotal       int64
	RequestsThisMinute  int64
	TokensThisMinute    int64
	ActiveChannels      int64
	UnavailableChannels int64
}

// LiveTraffic 返回网关此刻的流量分布。
//
// 渠道运行态走 ObserveChannels 而不是 SnapshotMany：后者的 ModelID 是批次级必填参数
// （用于 (channel, model) 权限 key），而全局视图没有单一模型。
func (s *Service) LiveTraffic(ctx context.Context) (LiveTraffic, error) {
	now := time.Now()
	minuteStart := now.UTC().Truncate(time.Minute)

	channels, err := s.store.LiveTrafficChannels(ctx)
	if err != nil {
		return LiveTraffic{}, opsutil.StoreFailed(err, "list live traffic channels")
	}
	models, err := s.store.LiveTrafficModels(ctx, sqlc.LiveTrafficModelsParams{
		FromTime: pgtype.Timestamptz{Time: minuteStart, Valid: true},
		RowLimit: liveTrafficModelLimit,
	})
	if err != nil {
		return LiveTraffic{}, opsutil.StoreFailed(err, "aggregate live traffic models")
	}

	out := LiveTraffic{
		ObservedAt:       now,
		MinuteStartedAt:  minuteStart,
		RuntimeAvailable: true,
		Channels:         make([]LiveChannel, 0, len(channels)),
		Models:           make([]LiveModel, 0, len(models)),
	}
	for _, row := range models {
		out.Models = append(out.Models, LiveModel{
			ModelID:          row.RequestedModelID,
			RequestTotal:     row.RequestTotal,
			RequestSucceeded: row.RequestSucceeded,
		})
		out.RequestsThisMinute += row.RequestTotal
	}

	channelIDs := make([]int64, 0, len(channels))
	for _, row := range channels {
		channelIDs = append(channelIDs, row.ChannelID)
	}

	var observed map[int64]breakerstore.ObservedChannelRuntime
	var samples map[int64]breakerstore.ChannelSampleWindow
	var tpm map[int64]breakerstore.TPMObservationSnapshot
	if s.breaker != nil {
		rows, observeErr := s.breaker.ObserveChannels(ctx, channelIDs)
		if observeErr != nil {
			out.RuntimeAvailable = false
			out.RuntimeErrorCode = degradeReason(observeErr)
		} else {
			observed = make(map[int64]breakerstore.ObservedChannelRuntime, len(rows))
			for _, row := range rows {
				observed[row.ChannelID] = row
			}
		}
		// 样本与 TPM 是 best-effort：读不到只影响那两列。
		if aggregated, sampleErr := s.breaker.AggregateChannelSamples(ctx, channelIDs); sampleErr == nil {
			samples = aggregated
		}
		if s.tpm != nil {
			if snapshot, tpmErr := s.tpm.TPMObservations(
				ctx,
				breakerstore.TPMScopeChannel,
				channelIDs,
				breakerstore.TPMObservationMinute(now),
			); tpmErr == nil {
				tpm = snapshot
			}
		}
	} else {
		out.RuntimeAvailable = false
		out.RuntimeErrorCode = "runtime_reader_unavailable"
	}

	for _, row := range channels {
		channel := LiveChannel{
			ChannelID:       row.ChannelID,
			ChannelName:     row.ChannelName,
			ChannelStatus:   row.ChannelStatus,
			ProviderID:      row.ProviderID,
			ProviderName:    row.ProviderName,
			ProviderStatus:  row.ProviderStatus,
			CredentialValid: row.CredentialValid,
			Priority:        row.Priority,
			BoundModels:     row.BoundModels,
		}
		if observation, ok := observed[row.ChannelID]; ok {
			if observation.ConcurrencyKnown() {
				used := observation.Concurrency.Used
				channel.ConcurrencyUsed = &used
				out.InFlightTotal += used
				if used > 0 {
					out.ActiveChannels++
				}
			}
			if observation.CapacityKnown {
				limit := observation.Concurrency.Limit
				channel.ConcurrencyLimit = &limit
			}
			cooldown := observation.CooldownRemainingMs
			channel.CooldownRemainingMs = &cooldown
			channel.BreakerState = string(observation.Breaker.State)
			if observation.Breaker.OpenRemainingMs > 0 {
				remaining := observation.Breaker.OpenRemainingMs
				channel.BreakerOpenRemainMs = &remaining
			}
		}
		if sample, ok := samples[row.ChannelID]; ok {
			rpm := sample.RPM
			channel.RequestsThisMinute = &rpm
		}
		if snapshot, ok := tpm[row.ChannelID]; ok {
			tokens := snapshot.TPM()
			channel.TokensThisMinute = &tokens
			out.TokensThisMinute += tokens
		}
		if !channelAcceptsTraffic(row, observed[row.ChannelID]) {
			out.UnavailableChannels++
		}
		out.Channels = append(out.Channels, channel)
	}
	return out, nil
}

// channelAcceptsTraffic 判断这条渠道此刻能不能接量。
// 凭据失效、渠道或 Provider 停用是配置事实；熔断与冷却是运行态。
func channelAcceptsTraffic(row sqlc.LiveTrafficChannelsRow, observation breakerstore.ObservedChannelRuntime) bool {
	if row.ChannelStatus != "enabled" || row.ProviderStatus != "enabled" || !row.CredentialValid {
		return false
	}
	if observation.CooldownRemainingMs > 0 {
		return false
	}
	return observation.Breaker.State != breakerstore.StateOpen
}
