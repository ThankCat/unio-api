package breakerstore

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// 评分样本聚合口径（§12）。观测独立于 admission：limit=0 也照常产生样本，写失败不影响主链路。
const (
	// sampleWindowMinutes 是分钟对齐的 30 分钟评分窗口（当前分钟 + 前 29 个分钟桶）。
	sampleWindowMinutes = 30
	// sampleMarkerTTL 覆盖 attempt 幂等 marker 的存活期，需大于窗口，防重复 Finish/重放重复计数。
	sampleMarkerTTL = 40 * time.Minute
	// sampleMinuteTTL 覆盖分钟桶存活期（>=35min，容忍时钟边界与短暂消费延迟，§12.2）。
	sampleMinuteTTL = 40 * time.Minute
	// sampleDayTTL 覆盖 UTC 日桶存活期（略大于一天）。
	sampleDayTTL = 25 * time.Hour
)

// ChannelSampleInput 是一次 attempt 终态的评分样本事实（§12，观测口径，与 admission 硬门槛解耦）。
type ChannelSampleInput struct {
	ChannelID       int64
	AttemptID       int64
	TTFTMs          *int64 // 仅流式有效生成 Token 产生样本；首字超时与非流式均为 nil
	ErrorEligible   bool   // 计入错误率分母（真实发出上游且结果可归因于 Channel）
	IsError         bool   // 计入错误率分子（要求 ErrorEligible）
	ObservedRequest bool   // 真实发起上游调用（计入 RPM/RPD）
	TokenCount      *int64 // 可靠 usage 的 token 总量（TPM）；nil 表示无可靠 usage
	TokenCovered    bool   // 有可靠 usage（coverage）
}

// ChannelSampleWindow 是渠道最近 30 分钟评分聚合与当前观测（RPM/TPM/RPD）。
type ChannelSampleWindow struct {
	// 30 分钟评分聚合（分钟对齐窗口）。
	TTFTSumMs         int64
	TTFTCount         int64
	ErrorAttemptCount int64
	ErrorCount        int64
	// 当前分钟观测。
	RPM               int64
	TPM               int64
	TokenCoveredCount int64
	// 当前 UTC 日观测。
	RPD int64
}

// RecordChannelSample 幂等写入一次 attempt 终态的评分样本与观测（§12.5）。
// 与 admission 解耦：不占用限额、不触发 breaker 硬门槛、写失败不 latch 基础设施故障。
func (s *Store) RecordChannelSample(ctx context.Context, in ChannelSampleInput) error {
	return s.recordChannelSampleAt(ctx, in, time.Now())
}

func (s *Store) recordChannelSampleAt(ctx context.Context, in ChannelSampleInput, now time.Time) error {
	if in.ChannelID <= 0 || in.AttemptID <= 0 {
		return configInvalid("channel sample requires positive channel and attempt id")
	}
	if s.localRuntimeInfrastructureFault(ctx) {
		return storeUnavailable(ErrStoreUnavailable, "breakerstore channel sample unavailable")
	}
	minute := minuteBucket(now)
	day := dayBucket(now)
	keys := []string{
		s.keys.sampleWriteMarker(in.AttemptID),
		s.keys.sampleMinuteBucket(in.ChannelID, minute),
		s.keys.sampleDayBucket(in.ChannelID, day),
	}
	argv := []interface{}{
		sampleMarkerTTL.Milliseconds(),
		sampleMinuteTTL.Milliseconds(),
		sampleDayTTL.Milliseconds(),
		sampleOptionalInt(in.TTFTMs, true),
		sampleFlagArg(in.ErrorEligible),
		sampleFlagArg(in.IsError && in.ErrorEligible),
		sampleFlagArg(in.ObservedRequest),
		sampleOptionalInt(in.TokenCount, false),
		sampleFlagArg(in.TokenCovered),
	}
	if err := s.recordSample.Run(ctx, s.client, keys, argv...).Err(); err != nil && !errors.Is(err, redis.Nil) {
		return storeUnavailable(err, "breakerstore record channel sample")
	}
	return nil
}

// AggregateChannelSamples 读取每个渠道当前分钟 + 前 29 个分钟桶的评分聚合与当前观测。
// 缺失桶按零处理（无样本），不向 30 分钟以前回溯。
func (s *Store) AggregateChannelSamples(ctx context.Context, channelIDs []int64) (map[int64]ChannelSampleWindow, error) {
	return s.aggregateChannelSamplesAt(ctx, channelIDs, time.Now())
}

func (s *Store) aggregateChannelSamplesAt(ctx context.Context, channelIDs []int64, now time.Time) (map[int64]ChannelSampleWindow, error) {
	out := make(map[int64]ChannelSampleWindow, len(channelIDs))
	if len(channelIDs) == 0 {
		return out, nil
	}
	if s.localRuntimeInfrastructureFault(ctx) {
		return nil, storeUnavailable(ErrStoreUnavailable, "breakerstore aggregate channel samples unavailable")
	}
	currentMinute := minuteBucket(now)
	day := dayBucket(now)

	type channelCmds struct {
		minutes []*redis.MapStringStringCmd
		day     *redis.StringCmd
	}
	pipe := s.client.Pipeline()
	cmds := make(map[int64]channelCmds, len(channelIDs))
	for _, id := range channelIDs {
		if id <= 0 {
			continue
		}
		if _, seen := cmds[id]; seen {
			continue
		}
		cc := channelCmds{minutes: make([]*redis.MapStringStringCmd, sampleWindowMinutes)}
		for j := 0; j < sampleWindowMinutes; j++ {
			cc.minutes[j] = pipe.HGetAll(ctx, s.keys.sampleMinuteBucket(id, currentMinute-int64(j)))
		}
		cc.day = pipe.Get(ctx, s.keys.sampleDayBucket(id, day))
		cmds[id] = cc
	}
	if _, err := pipe.Exec(ctx); err != nil && !errors.Is(err, redis.Nil) {
		return nil, storeUnavailable(err, "breakerstore aggregate channel samples")
	}
	for id, cc := range cmds {
		var w ChannelSampleWindow
		for j, cmd := range cc.minutes {
			fields, err := cmd.Result()
			if err != nil {
				if errors.Is(err, redis.Nil) {
					continue
				}
				return nil, storeUnavailable(err, "breakerstore read channel sample minute")
			}
			w.TTFTSumMs += sampleHashInt(fields, "ttft_sum_ms")
			w.TTFTCount += sampleHashInt(fields, "ttft_count")
			w.ErrorAttemptCount += sampleHashInt(fields, "error_attempt_count")
			w.ErrorCount += sampleHashInt(fields, "error_count")
			if j == 0 {
				w.RPM = sampleHashInt(fields, "observed_request_count")
				w.TPM = sampleHashInt(fields, "observed_token_count")
				w.TokenCoveredCount = sampleHashInt(fields, "observed_token_covered_attempt_count")
			}
		}
		if raw, err := cc.day.Result(); err == nil {
			if n, ok := parseNonNegativeCounter(raw); ok {
				w.RPD = n
			}
		} else if !errors.Is(err, redis.Nil) {
			return nil, storeUnavailable(err, "breakerstore read channel sample day")
		}
		out[id] = w
	}
	return out, nil
}

func sampleFlagArg(v bool) string {
	if v {
		return "1"
	}
	return "0"
}

// sampleOptionalInt 编码可选整数参数：允许零表示 ttft=0（allowZero=true），token 只在 >0 时写入。
func sampleOptionalInt(v *int64, allowZero bool) interface{} {
	if v == nil {
		return ""
	}
	if *v < 0 {
		return ""
	}
	if *v == 0 && !allowZero {
		return ""
	}
	return *v
}

func sampleHashInt(fields map[string]string, key string) int64 {
	raw, ok := fields[key]
	if !ok {
		return 0
	}
	n, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || n < 0 {
		return 0
	}
	return n
}
