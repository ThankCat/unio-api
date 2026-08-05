package breakerstore

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// RouteChannelRPDUsage reads the frozen UTC-day attempt attribution for one route/channel.
// A missing bucket is zero for this observational view; the global Channel RPD bucket remains
// the hard capacity fact and is handled by AttemptPermit lifecycle guards.
func (s *Store) RouteChannelRPDUsage(ctx context.Context, routeID, channelID int64) (int64, error) {
	if routeID <= 0 || channelID <= 0 {
		return 0, configInvalid("route and channel ids must be positive")
	}
	if s.localRuntimeInfrastructureFault(ctx) {
		return 0, storeUnavailable(ErrStoreUnavailable, "breakerstore route-channel rpd unavailable")
	}
	value, err := s.client.Get(ctx, s.keys.routeChannelRPDBucket(routeID, channelID, dayBucket(time.Now()))).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, storeUnavailable(err, "breakerstore read route-channel rpd")
	}
	used, parseErr := strconv.ParseInt(value, 10, 64)
	if parseErr != nil || used < 0 {
		return 0, storeUnavailable(errors.New("malformed route-channel rpd bucket"), "breakerstore read route-channel rpd")
	}
	return used, nil
}

// RouteUsage 是一条线路上所有 (route,user) 入口桶的只读合计（admin 展示用）。
// 不含线路总上限：准入仍按 (route,user) 分桶执行。
// 这里没有 TPM：TPM 不是准入维度，它的观测值来自独立的 obs:tpm 分钟桶（§8）。
type RouteUsage struct {
	Concurrency int64
	RPM         int64
	RPD         int64
	ActiveUsers int64 // 参与并发合计的用户桶数（含仅有过期租约的空 ZSET）
}

const routeUsageScanCount = int64(256)

// AggregateRouteUsage 以 SCAN 汇总该线路当前分钟/日窗口的入口用量。
// 基础设施故障 latch 置位时返回 store unavailable；非法/负计数跳过。
func (s *Store) AggregateRouteUsage(ctx context.Context, routeID int64) (RouteUsage, error) {
	if routeID <= 0 {
		return RouteUsage{}, configInvalid("route id must be positive")
	}
	if s.localRuntimeInfrastructureFault(ctx) {
		return RouteUsage{}, storeUnavailable(ErrStoreUnavailable, "breakerstore aggregate route usage unavailable")
	}
	now := time.Now()
	nowMs := now.UnixMilli()
	minute := minuteBucket(now)
	day := dayBucket(now)

	concKeys, err := s.scanKeys(ctx, s.keys.requestConcurrencyRoutePattern(routeID))
	if err != nil {
		return RouteUsage{}, storeUnavailable(err, "breakerstore scan route concurrency")
	}
	var usage RouteUsage
	usage.ActiveUsers = int64(len(concKeys))
	for _, key := range concKeys {
		n, zerr := s.client.ZCount(ctx, key, "("+strconv.FormatInt(nowMs, 10), "+inf").Result()
		if zerr != nil {
			return RouteUsage{}, storeUnavailable(zerr, "breakerstore zcount route concurrency")
		}
		usage.Concurrency += n
	}

	rpm, err := s.sumCounterKeys(ctx, s.keys.requestRPMBucketRoutePattern(routeID, minute))
	if err != nil {
		return RouteUsage{}, err
	}
	usage.RPM = rpm
	rpd, err := s.sumCounterKeys(ctx, s.keys.requestRPDBucketRoutePattern(routeID, day))
	if err != nil {
		return RouteUsage{}, err
	}
	usage.RPD = rpd
	return usage, nil
}

func (s *Store) scanKeys(ctx context.Context, pattern string) ([]string, error) {
	var keys []string
	var cursor uint64
	for {
		batch, next, err := s.client.Scan(ctx, cursor, pattern, routeUsageScanCount).Result()
		if err != nil {
			return nil, err
		}
		keys = append(keys, batch...)
		cursor = next
		if cursor == 0 {
			break
		}
	}
	return keys, nil
}

func (s *Store) sumCounterKeys(ctx context.Context, pattern string) (int64, error) {
	keys, err := s.scanKeys(ctx, pattern)
	if err != nil {
		return 0, storeUnavailable(err, "breakerstore scan route counter")
	}
	var total int64
	const batchSize = 128
	for start := 0; start < len(keys); start += batchSize {
		end := start + batchSize
		if end > len(keys) {
			end = len(keys)
		}
		chunk := keys[start:end]
		values, merr := s.client.MGet(ctx, chunk...).Result()
		if merr != nil {
			return 0, storeUnavailable(merr, "breakerstore mget route counter")
		}
		for _, raw := range values {
			n, ok := parseNonNegativeCounter(raw)
			if !ok {
				continue
			}
			total += n
		}
	}
	return total, nil
}

func parseNonNegativeCounter(raw interface{}) (int64, bool) {
	if raw == nil {
		return 0, false
	}
	switch v := raw.(type) {
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		return n, true
	case []byte:
		n, err := strconv.ParseInt(string(v), 10, 64)
		if err != nil || n < 0 {
			return 0, false
		}
		return n, true
	case int64:
		if v < 0 {
			return 0, false
		}
		return v, true
	default:
		return 0, false
	}
}
