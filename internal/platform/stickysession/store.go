// Package stickysession 提供会话粘性路由绑定的 Redis 存取层（大 uncache 缺口 P0）。
//
// 语义边界：sticky 绑定是「路由优化提示」而非正确性事实——Redis 不作为金额/余额事实来源，
// 丢失绑定的最坏后果只是上游 prompt cache 冷一次。因此所有操作 fail-open（R7）：
// 读失败当 miss、写/删失败只记日志，绝不把 Redis 故障传导到请求主链路。
package stickysession

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// opTimeout 是单次 sticky Redis 操作的独立短超时：sticky 在候选准备热路径上，
// Redis 抖动时宁可放弃粘性也不能拖慢请求（R7）。
const opTimeout = 200 * time.Millisecond

// Store 是 sticky 绑定的 Redis 实现（实现 lifecycle.StickyStore）。
// 键统一加进程 Redis namespace 前缀（与 ratelimit sliding window 同约定）。
type Store struct {
	client    redis.Cmdable
	logger    *zap.Logger
	keyPrefix string
}

// NewStore 创建 sticky 绑定存取层。keyNamespace 为空时回退 "unio"；logger 为 nil 时退化为 Nop。
func NewStore(client redis.Cmdable, keyNamespace string, logger *zap.Logger) *Store {
	if client == nil {
		panic("stickysession: redis client is required")
	}
	if logger == nil {
		logger = zap.NewNop()
	}
	keyNamespace = strings.Trim(keyNamespace, ":")
	if keyNamespace == "" {
		keyNamespace = "unio"
	}
	return &Store{client: client, logger: logger, keyPrefix: keyNamespace + ":"}
}

// Lookup 读取 key 当前绑定的渠道 ID。miss、值损坏或 Redis 故障统一返回 ok=false（fail-open）。
func (s *Store) Lookup(ctx context.Context, key string) (int64, bool) {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	raw, err := s.client.Get(opCtx, s.keyPrefix+key).Result()
	if err != nil {
		if err != redis.Nil {
			s.logger.Warn("sticky lookup failed, treating as miss", zap.String("key", key), zap.Error(err))
		}
		return 0, false
	}
	channelID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || channelID <= 0 {
		s.logger.Warn("sticky binding value corrupted, treating as miss", zap.String("key", key), zap.String("value", raw))
		return 0, false
	}
	return channelID, true
}

// Bind 在「无既有绑定」时写入绑定（SETNX 语义，R8）：同会话首轮并发请求各自成功时，
// 只有第一个写入生效，避免互相覆盖来回翻。TTL 为绝对过期，命中不刷新（R2）。
func (s *Store) Bind(ctx context.Context, key string, channelID int64, ttl time.Duration) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()

	if err := s.client.SetNX(opCtx, s.keyPrefix+key, strconv.FormatInt(channelID, 10), ttl).Err(); err != nil {
		s.logger.Warn("sticky bind failed", zap.String("key", key), zap.Int64("channel_id", channelID), zap.Error(err))
	}
}

// Rebind 覆盖写绑定（failover 成功后改绑，决议 2/3）。TTL 重置为完整时长。
func (s *Store) Rebind(ctx context.Context, key string, channelID int64, ttl time.Duration) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()

	if err := s.client.Set(opCtx, s.keyPrefix+key, strconv.FormatInt(channelID, 10), ttl).Err(); err != nil {
		s.logger.Warn("sticky rebind failed", zap.String("key", key), zap.Int64("channel_id", channelID), zap.Error(err))
	}
}

// Clear 删除绑定（粘住渠道被硬摘除：disabled / credential invalid / breaker open）。
func (s *Store) Clear(ctx context.Context, key string) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()

	if err := s.client.Del(opCtx, s.keyPrefix+key).Err(); err != nil {
		s.logger.Warn("sticky clear failed", zap.String("key", key), zap.Error(err))
	}
}
