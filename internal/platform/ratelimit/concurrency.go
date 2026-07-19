package ratelimit

import (
	"context"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

const (
	defaultConcurrencyLeaseTTL = 2 * time.Minute
	concurrencyRedisTimeout    = 500 * time.Millisecond
)

var acquireConcurrencyLeaseScript = redis.NewScript(`
redis.call('ZREMRANGEBYSCORE', KEYS[1], '-inf', ARGV[1])
local count = redis.call('ZCARD', KEYS[1])
local limit = tonumber(ARGV[3])
if limit > 0 and count >= limit then
  return {0, count}
end
redis.call('ZADD', KEYS[1], ARGV[2], ARGV[4])
redis.call('PEXPIRE', KEYS[1], ARGV[5])
return {1, count + 1}
`)

var refreshConcurrencyLeaseScript = redis.NewScript(`
if redis.call('ZSCORE', KEYS[1], ARGV[2]) then
  redis.call('ZADD', KEYS[1], 'XX', ARGV[1], ARGV[2])
  redis.call('PEXPIRE', KEYS[1], ARGV[3])
  return 1
end
return 0
`)

var releaseConcurrencyLeaseScript = redis.NewScript(`
return redis.call('ZREM', KEYS[1], ARGV[1])
`)

// ConcurrencyLimiter 是「在途并发」限制器（DEC-029/P3）。
//
// 与 Guard（Redis 滑动窗口，按时间的 RPM/TPM/RPD）正交：本限制器数的是「同时进行中」的
// 请求数（含整段流式传输），专门防「慢上游 + 客户端重试风暴」把长耗时请求堆在同一主体上。
// 生产注入 Redis 时使用带 TTL 和心跳的分布式租约，多个 gateway/线路共享同一 channel 计数；
// 无 Redis 的单测构造保留进程内计数。
//
// 两级主体：
//   - 线路+用户（ru:<routeID>:<userID>）：上限取全局默认 key_limit（运行时热改）；
//     同一用户在该线路下所有 Key 共享一个在途池（对齐 DEC-027 的限流主体口径）。
//   - 渠道（chan:<channelID>）：上限取渠道行覆盖值（nil=继承全局默认 channel_limit，
//     0=显式不限，>0=具体上限），与 channels.rpm_limit 语义一致。
//
// 上限 <=0 时该主体不限并发，但仍然计数：保证运行期把上限从 0 调成 N 时计数是准的。
type ConcurrencyLimiter struct {
	mu       sync.Mutex
	inflight map[string]int64

	defaultKeyLimit     int64
	defaultChannelLimit int64

	client       redis.Cmdable
	keyNamespace string
	leaseTTL     time.Duration
	now          func() time.Time
	logger       *zap.Logger
}

// NewConcurrencyLimiter 创建在途并发限制器。keyLimit/channelLimit 为全局默认上限（0=不限）。
func NewConcurrencyLimiter(keyLimit, channelLimit int64) *ConcurrencyLimiter {
	return &ConcurrencyLimiter{
		inflight:            make(map[string]int64),
		defaultKeyLimit:     keyLimit,
		defaultChannelLimit: channelLimit,
		leaseTTL:            defaultConcurrencyLeaseTTL,
		now:                 time.Now,
	}
}

// NewRedisConcurrencyLimiter 创建跨 gateway 共享的 Redis 并发租约限制器。
func NewRedisConcurrencyLimiter(client redis.Cmdable, keyNamespace string, keyLimit, channelLimit int64, logger *zap.Logger) *ConcurrencyLimiter {
	limiter := NewConcurrencyLimiter(keyLimit, channelLimit)
	limiter.client = client
	limiter.keyNamespace = strings.Trim(keyNamespace, ":")
	limiter.logger = logger
	return limiter
}

// SetDefaults 原子替换全局默认上限（运行时热改入口）。只影响之后的判定，不动在途计数。
func (l *ConcurrencyLimiter) SetDefaults(keyLimit, channelLimit int64) {
	if l == nil {
		return
	}
	l.mu.Lock()
	l.defaultKeyLimit = keyLimit
	l.defaultChannelLimit = channelLimit
	l.mu.Unlock()
}

// AcquireRouteUser 尝试占用「线路+用户」的一个在途名额。
//
// ok=false 表示已达上限（调用方应立即以 429 拒绝，不发起任何上游调用）。
// ok=true 时必须在请求完全结束（含流式传输完成/中断）后调用 release 释放名额；
// release 幂等，重复调用只释放一次。nil receiver 恒放行（未启用并发限制）。
func (l *ConcurrencyLimiter) AcquireRouteUser(routeID, userID int64) (release func(), ok bool) {
	if l == nil {
		return func() {}, true
	}
	subject := routeUserInflightSubject(routeID, userID)
	limit := l.keyLimit()
	return l.acquire(subject, limit)
}

// AcquireChannel 尝试占用渠道的一个在途名额。
//
// override 是渠道行上的 concurrency_limit：nil=继承全局默认，0=显式不限，>0=具体上限。
// 语义同 AcquireRouteUser；ok=false 时调用方应跳过该候选 fallback 到下一渠道。
func (l *ConcurrencyLimiter) AcquireChannel(channelID int64, override *int64) (release func(), ok bool) {
	if l == nil {
		return func() {}, true
	}
	subject := channelInflightSubject(channelID)
	limit := l.channelLimit(override)
	return l.acquire(subject, limit)
}

// Inflight 返回某主体当前在途计数（观测/测试用）。
func (l *ConcurrencyLimiter) Inflight(subject string) int64 {
	if l == nil {
		return 0
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.inflight[subject]
}

// ChannelSnapshot 只读返回当前渠道并发容量。
func (l *ConcurrencyLimiter) ChannelSnapshot(ctx context.Context, channelID int64, override *int64) (UsageSnapshot, error) {
	if l == nil {
		return UsageSnapshot{}, nil
	}
	limit := l.channelLimit(override)
	if l.client != nil {
		used, err := l.redisInflight(ctx, channelInflightSubject(channelID))
		if err != nil {
			return UsageSnapshot{}, err
		}
		return UsageSnapshot{Used: used, Limit: limit, Known: true}, nil
	}
	subject := channelInflightSubject(channelID)
	l.mu.Lock()
	defer l.mu.Unlock()
	return UsageSnapshot{Used: l.inflight[subject], Limit: limit, Known: true}, nil
}

func (l *ConcurrencyLimiter) acquire(subject string, limit int64) (func(), bool) {
	if l.client != nil {
		return l.acquireRedis(subject, limit)
	}
	l.mu.Lock()
	ok := l.acquireLocked(subject, limit)
	l.mu.Unlock()
	if !ok {
		return nil, false
	}
	return l.releaseFunc(subject), true
}

func (l *ConcurrencyLimiter) keyLimit() int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.defaultKeyLimit
}

func (l *ConcurrencyLimiter) channelLimit(override *int64) int64 {
	l.mu.Lock()
	defer l.mu.Unlock()
	if override != nil {
		return *override
	}
	return l.defaultChannelLimit
}

func (l *ConcurrencyLimiter) acquireRedis(subject string, limit int64) (func(), bool) {
	leaseID := uuid.NewString()
	now := l.now()
	key := l.redisKey(subject)
	ctx, cancel := context.WithTimeout(context.Background(), concurrencyRedisTimeout)
	defer cancel()
	result, err := acquireConcurrencyLeaseScript.Run(
		ctx, l.client, []string{key}, now.UnixMilli(), now.Add(l.leaseTTL).UnixMilli(), limit, leaseID, (2 * l.leaseTTL).Milliseconds(),
	).Result()
	if err != nil {
		l.logRedisError("acquire", subject, err)
		return nil, false
	}
	allowed, _, err := parsePair(result)
	if err != nil || allowed != 1 {
		if err != nil {
			l.logRedisError("parse_acquire", subject, err)
		}
		return nil, false
	}
	return l.redisReleaseFunc(key, subject, leaseID), true
}

func (l *ConcurrencyLimiter) redisReleaseFunc(key, subject, leaseID string) func() {
	done := make(chan struct{})
	interval := l.leaseTTL / 3
	if interval <= 0 {
		interval = 30 * time.Second
	}
	go func() {
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-done:
				return
			case <-ticker.C:
				ctx, cancel := context.WithTimeout(context.Background(), concurrencyRedisTimeout)
				err := refreshConcurrencyLeaseScript.Run(
					ctx, l.client, []string{key}, l.now().Add(l.leaseTTL).UnixMilli(), leaseID, (2 * l.leaseTTL).Milliseconds(),
				).Err()
				cancel()
				if err != nil {
					l.logRedisError("refresh", subject, err)
				}
			}
		}
	}()

	var once sync.Once
	return func() {
		once.Do(func() {
			close(done)
			ctx, cancel := context.WithTimeout(context.Background(), concurrencyRedisTimeout)
			err := releaseConcurrencyLeaseScript.Run(ctx, l.client, []string{key}, leaseID).Err()
			cancel()
			if err != nil {
				l.logRedisError("release", subject, err)
			}
		})
	}
}

func (l *ConcurrencyLimiter) redisInflight(ctx context.Context, subject string) (int64, error) {
	return l.client.ZCount(ctx, l.redisKey(subject), strconv.FormatInt(l.now().UnixMilli()+1, 10), "+inf").Result()
}

func (l *ConcurrencyLimiter) redisKey(subject string) string {
	namespace := l.keyNamespace
	if namespace == "" {
		namespace = "unio"
	}
	return namespace + ":concurrency:" + subject
}

func (l *ConcurrencyLimiter) logRedisError(operation, subject string, err error) {
	if l.logger != nil {
		l.logger.Error("redis concurrency operation failed", zap.String("operation", operation), zap.String("subject", subject), zap.Error(err))
	}
}

// acquireLocked 在持锁状态下判定并占用。limit<=0 表示不限：仍计数但不拒绝。
func (l *ConcurrencyLimiter) acquireLocked(subject string, limit int64) bool {
	if limit > 0 && l.inflight[subject] >= limit {
		return false
	}
	l.inflight[subject]++
	return true
}

// releaseFunc 构造幂等的释放闭包；计数归零时删除条目，避免 map 无限增长。
func (l *ConcurrencyLimiter) releaseFunc(subject string) func() {
	var once sync.Once
	return func() {
		once.Do(func() {
			l.mu.Lock()
			if n := l.inflight[subject]; n <= 1 {
				delete(l.inflight, subject)
			} else {
				l.inflight[subject] = n - 1
			}
			l.mu.Unlock()
		})
	}
}

// RouteUserInflightSubject 构造「线路+用户」在途计数主体（观测/测试用）。
func RouteUserInflightSubject(routeID, userID int64) string {
	return routeUserInflightSubject(routeID, userID)
}

// ChannelInflightSubject 构造渠道在途计数主体（观测/测试用）。
func ChannelInflightSubject(channelID int64) string {
	return channelInflightSubject(channelID)
}

func routeUserInflightSubject(routeID, userID int64) string {
	return string(ScopeRouteUser) + ":" + strconv.FormatInt(routeID, 10) + ":" + strconv.FormatInt(userID, 10) + ":inflight"
}

func channelInflightSubject(channelID int64) string {
	return string(ScopeChannel) + ":" + strconv.FormatInt(channelID, 10) + ":inflight"
}
