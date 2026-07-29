// Package stickysession 提供会话粘性路由绑定的 Redis 存取层（大 uncache 缺口 P0）。
//
// 语义边界：sticky 绑定是「路由优化提示」而非正确性事实——Redis 不作为金额/余额事实来源，
// 丢失绑定的最坏后果只是上游 prompt cache 冷一次。因此所有操作 fail-open（R7）：
// 读失败当 miss、写/删失败只记日志，绝不把 Redis 故障传导到请求主链路。
package stickysession

import (
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

type stickyBindingV2 struct {
	Version   int   `json:"v"`
	ChannelID int64 `json:"channel_id"`
	BoundAtMs int64 `json:"bound_at_ms"`
}

type stickyBindingV3 struct {
	Version         int   `json:"v"`
	ChannelID       int64 `json:"channel_id"`
	LastSuccessAtMs int64 `json:"last_success_at_ms"`
}

// opTimeout 是单次 sticky Redis 操作的独立短超时：sticky 在候选准备热路径上，
// Redis 抖动时宁可放弃粘性也不能拖慢请求（R7）。
const opTimeout = 200 * time.Millisecond

const upgradeBindingLua = `
local current = redis.call("GET", KEYS[1])
if current ~= ARGV[1] then
  return 0
end
redis.call("SET", KEYS[1], ARGV[2], "XX", "KEEPTTL")
return 1
`

const refreshIfBoundLua = `
local raw = redis.call("GET", KEYS[1])
if not raw then
  return 0
end

local channel_id = tonumber(raw)
if not channel_id then
  local ok, decoded = pcall(cjson.decode, raw)
  if not ok or type(decoded) ~= "table" then
    return 0
  end
  channel_id = tonumber(decoded.channel_id)
end

if channel_id ~= tonumber(ARGV[1]) then
  return 0
end

redis.call("SET", KEYS[1], ARGV[2], "XX", "PX", ARGV[3])
return 1
`

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

// Lookup 读取 v3 绑定；v2/旧整数值会在访问时惰性升级为 v3，并保留原 Redis TTL。
// miss、值损坏或 Redis 故障统一返回 ok=false（fail-open）。
func (s *Store) Lookup(ctx context.Context, key string) (int64, time.Time, bool) {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	raw, err := s.client.Get(opCtx, s.keyPrefix+key).Result()
	if err != nil {
		if err != redis.Nil {
			s.logger.Warn("sticky lookup failed, treating as miss", zap.String("key", key), zap.Error(err))
		}
		return 0, time.Time{}, false
	}
	now := time.Now()
	var current stickyBindingV3
	if err := json.Unmarshal([]byte(raw), &current); err == nil && current.Version == 3 && current.ChannelID > 0 && current.LastSuccessAtMs > 0 {
		return current.ChannelID, time.UnixMilli(current.LastSuccessAtMs), true
	}

	var binding stickyBindingV2
	if err := json.Unmarshal([]byte(raw), &binding); err == nil && binding.Version == 2 && binding.ChannelID > 0 && binding.BoundAtMs > 0 {
		lastSuccessAt := time.UnixMilli(binding.BoundAtMs)
		s.upgradeBinding(opCtx, key, raw, binding.ChannelID, lastSuccessAt)
		return binding.ChannelID, lastSuccessAt, true
	}
	channelID, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || channelID <= 0 {
		s.logger.Warn("sticky binding value corrupted, treating as miss", zap.String("key", key), zap.String("value", raw))
		return 0, time.Time{}, false
	}
	s.upgradeBinding(opCtx, key, raw, channelID, now)
	return channelID, now, true
}

func (s *Store) upgradeBinding(ctx context.Context, key, raw string, channelID int64, lastSuccessAt time.Time) {
	upgraded, err := encodeBinding(channelID, lastSuccessAt)
	if err != nil {
		return
	}
	// Compare-and-set prevents a stale lookup from overwriting a concurrent failover rebind.
	// KEEPTTL avoids extending old bindings merely because the new process read them.
	if err := s.client.Eval(ctx, upgradeBindingLua, []string{s.keyPrefix + key}, raw, upgraded).Err(); err != nil {
		s.logger.Warn("sticky binding upgrade failed", zap.String("key", key), zap.Error(err))
	}
}

// Bind 在「无既有绑定」时写入绑定（SETNX 语义，R8）：同会话首轮并发请求各自成功时，
// 只有第一个写入生效，避免互相覆盖来回翻。TTL 从本次成功开始计算。
func (s *Store) Bind(ctx context.Context, key string, channelID int64, ttl time.Duration) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()

	value, err := encodeBinding(channelID, time.Now())
	if err != nil {
		s.logger.Warn("sticky bind encode failed", zap.String("key", key), zap.Error(err))
		return
	}
	if err := s.client.SetNX(opCtx, s.keyPrefix+key, value, ttl).Err(); err != nil {
		s.logger.Warn("sticky bind failed", zap.String("key", key), zap.Int64("channel_id", channelID), zap.Error(err))
	}
}

// Rebind 覆盖写绑定（failover 成功后改绑，决议 2/3）。TTL 重置为完整时长。
func (s *Store) Rebind(ctx context.Context, key string, channelID int64, ttl time.Duration) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()

	value, err := encodeBinding(channelID, time.Now())
	if err != nil {
		s.logger.Warn("sticky rebind encode failed", zap.String("key", key), zap.Error(err))
		return
	}
	if err := s.client.Set(opCtx, s.keyPrefix+key, value, ttl).Err(); err != nil {
		s.logger.Warn("sticky rebind failed", zap.String("key", key), zap.Int64("channel_id", channelID), zap.Error(err))
	}
}

// RefreshIfBound 在同渠道成功后滑动续期。Lua 同时比较当前 channel_id，避免旧并发请求在
// failover 已改绑后续活或覆盖新渠道。返回值不参与请求主链路，Redis 故障仍 fail-open。
func (s *Store) RefreshIfBound(ctx context.Context, key string, channelID int64, ttl time.Duration) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()
	if channelID <= 0 || ttl <= 0 {
		return
	}
	value, err := encodeBinding(channelID, time.Now())
	if err != nil {
		s.logger.Warn("sticky refresh encode failed", zap.String("key", key), zap.Error(err))
		return
	}
	if err := s.client.Eval(
		opCtx,
		refreshIfBoundLua,
		[]string{s.keyPrefix + key},
		channelID,
		value,
		ttl.Milliseconds(),
	).Err(); err != nil {
		s.logger.Warn("sticky success refresh failed", zap.String("key", key), zap.Int64("channel_id", channelID), zap.Error(err))
	}
}

func encodeBinding(channelID int64, boundAt time.Time) (string, error) {
	raw, err := json.Marshal(stickyBindingV3{Version: 3, ChannelID: channelID, LastSuccessAtMs: boundAt.UnixMilli()})
	return string(raw), err
}

// Clear 删除绑定（粘住渠道被硬摘除：disabled / credential invalid / breaker open）。
func (s *Store) Clear(ctx context.Context, key string) {
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()

	if err := s.client.Del(opCtx, s.keyPrefix+key).Err(); err != nil {
		s.logger.Warn("sticky clear failed", zap.String("key", key), zap.Error(err))
	}
}
