package breakerstore

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/redis/go-redis/v9"
)

// UserUsage 是一个用户当前分钟/日窗口的入口用量（admin 只读展示用）。
// 准入判定本身在 Lua 脚本内完成，这里只是把同样的桶读出来给人看。
// 不含 TPM：TPM 不是准入维度，它的观测值来自独立的 obs:tpm 分钟桶（§8）。
type UserUsage struct {
	Concurrency int64
	RPM         int64
	RPD         int64
}

// AggregateUserUsage 读取该用户当前分钟/日窗口的入口用量。
// 限流主体降到用户后每个维度只有一个确定的 key，不再需要 SCAN。
// 基础设施故障 latch 置位时返回 store unavailable；非法/负计数按 0 处理。
func (s *Store) AggregateUserUsage(ctx context.Context, userID int64) (UserUsage, error) {
	if userID <= 0 {
		return UserUsage{}, configInvalid("user id must be positive")
	}
	if s.localRuntimeInfrastructureFault(ctx) {
		return UserUsage{}, storeUnavailable(ErrStoreUnavailable, "breakerstore aggregate user usage unavailable")
	}
	now := time.Now()

	concurrency, err := s.client.ZCount(ctx,
		s.keys.requestConcurrency(userID),
		"("+strconv.FormatInt(now.UnixMilli(), 10), "+inf",
	).Result()
	if err != nil && !errors.Is(err, redis.Nil) {
		return UserUsage{}, storeUnavailable(err, "breakerstore zcount user concurrency")
	}

	rpm, err := s.readCounter(ctx, s.keys.requestRPMBucket(userID, minuteBucket(now)))
	if err != nil {
		return UserUsage{}, err
	}
	rpd, err := s.readCounter(ctx, s.keys.requestRPDBucket(userID, dayBucket(now)))
	if err != nil {
		return UserUsage{}, err
	}

	return UserUsage{Concurrency: concurrency, RPM: rpm, RPD: rpd}, nil
}

// readCounter 读取单个计数桶；缺失或格式异常按 0 处理（观测视图不因脏数据失败）。
func (s *Store) readCounter(ctx context.Context, key string) (int64, error) {
	raw, err := s.client.Get(ctx, key).Result()
	if err != nil {
		if errors.Is(err, redis.Nil) {
			return 0, nil
		}
		return 0, storeUnavailable(err, "breakerstore read user counter")
	}
	n, ok := parseNonNegativeCounter(raw)
	if !ok {
		return 0, nil
	}
	return n, nil
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
