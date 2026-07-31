// Package stickysession 提供会话粘性路由绑定的 Redis 存取层。
//
// 语义边界：sticky 绑定是「路由优化提示」而非正确性事实——Redis 不作为金额/余额事实来源，
// 丢失绑定的最坏后果只是上游 prompt cache 冷一次。因此所有操作 fail-open（§10.11）：
// 读失败当 miss、写/删失败只报告结果，绝不把 Redis 故障传导到请求主链路。
//
// 写操作只有三种，全部是 CAS（§10.4）：BindIfAbsent / RefreshIfCurrent / ClearIfCurrent。
// 不提供直接 Rebind(A, B)：改绑必须表达为「先清 A，再以 Unbound 状态绑 B」，
// 这样审计能分别解释「因何清 A」与「因何建 B」（§10.9）。
package stickysession

import (
	"context"
	"encoding/json"
	"math/rand/v2"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
	"go.uber.org/zap"
)

// binding 是 sticky value 的唯一 schema（§10.2）。
//
// BindingVersion 是 CAS 身份计数而非兼容版本号：每次新建绑定都会得到一个新的 version，
// 因此 CAS 必须同时比较 channel_id 和 binding_version，只比 channel_id 会让
// 「A 被清除后又被另一个请求重新绑定到 A」被误判为同一个绑定。
type binding struct {
	Version         int   `json:"v"`
	ChannelID       int64 `json:"channel_id"`
	BindingVersion  int64 `json:"binding_version"`
	LastSuccessAtMs int64 `json:"last_success_at_ms"`
}

// bindingSchemaVersion 标记单一 canonical value schema。
const bindingSchemaVersion = 1

// opTimeout 是单次 sticky Redis 操作的独立短超时：sticky 在候选准备热路径上，
// Redis 抖动时宁可放弃粘性也不能拖慢请求（§10.11）。
const opTimeout = 200 * time.Millisecond

// refreshIfCurrentLua 仅在 (channel_id, binding_version) 完全匹配时滑动续期。
// 任何不匹配都返回 0（cas_conflict），绝不覆盖别的请求建立的新绑定。
const refreshIfCurrentLua = `
local raw = redis.call("GET", KEYS[1])
if not raw then
  return 0
end
local ok, decoded = pcall(cjson.decode, raw)
if not ok or type(decoded) ~= "table" then
  return 0
end
if tonumber(decoded.channel_id) ~= tonumber(ARGV[1]) then
  return 0
end
if tonumber(decoded.binding_version) ~= tonumber(ARGV[2]) then
  return 0
end
redis.call("SET", KEYS[1], ARGV[3], "XX", "PX", ARGV[4])
return 1
`

// clearIfCurrentLua 仅在 (channel_id, binding_version) 完全匹配时删除绑定。
// CAS 失败说明绑定已被其他请求改变，本请求不得删除新绑定（§10.9）。
const clearIfCurrentLua = `
local raw = redis.call("GET", KEYS[1])
if not raw then
  return 0
end
local ok, decoded = pcall(cjson.decode, raw)
if not ok or type(decoded) ~= "table" then
  redis.call("DEL", KEYS[1])
  return 1
end
if tonumber(decoded.channel_id) ~= tonumber(ARGV[1]) then
  return 0
end
if tonumber(decoded.binding_version) ~= tonumber(ARGV[2]) then
  return 0
end
redis.call("DEL", KEYS[1])
return 1
`

// Store 是 sticky 绑定的 Redis 实现（实现 lifecycle.StickyStore）。
// 键统一加进程 Redis namespace 前缀（与 ratelimit sliding window 同约定）。
type Store struct {
	client     redis.Cmdable
	logger     *zap.Logger
	keyPrefix  string
	newVersion func() int64
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
	return &Store{
		client:     client,
		logger:     logger,
		keyPrefix:  keyNamespace + ":",
		newVersion: newBindingVersion,
	}
}

// maxLuaExactBindingVersion 把 binding_version 限制在 2^52 以内。
// CAS 比较发生在 Lua 里，而 Lua number 是 double：超过 2^53 的整数会静默丢精度，
// 导致两个不同的 version 被判为相等，CAS 形同虚设。
const maxLuaExactBindingVersion = 1 << 52

// newBindingVersion 生成一个新的绑定身份。它只需在同一个 key 上可区分（不要求单调或全局唯一），
// 因此用 Lua 可精确表示范围内的随机数即可。
func newBindingVersion() int64 {
	return rand.Int64N(maxLuaExactBindingVersion) + 1
}

// Binding 是一次 Lookup 的完整绑定事实（CAS 需要 version，审计需要 last success 时间）。
type Binding struct {
	ChannelID      int64
	BindingVersion int64
	LastSuccessAt  time.Time
}

// LookupResult 区分三种读结果：命中、未命中、存储不可用。
// 未命中与不可用都不阻断路由，但审计口径不同（§10.11/§10.12）。
type LookupResult struct {
	Binding          Binding
	Found            bool
	StoreUnavailable bool
}

// Lookup 读取当前绑定。miss、值损坏返回 Found=false；Redis 故障额外标记 StoreUnavailable。
func (s *Store) Lookup(ctx context.Context, key string) LookupResult {
	opCtx, cancel := context.WithTimeout(ctx, opTimeout)
	defer cancel()

	raw, err := s.client.Get(opCtx, s.keyPrefix+key).Result()
	if err != nil {
		if err == redis.Nil {
			return LookupResult{}
		}
		s.logger.Warn("sticky lookup failed, treating as miss", zap.String("key", key), zap.Error(err))
		return LookupResult{StoreUnavailable: true}
	}
	var current binding
	if err := json.Unmarshal([]byte(raw), &current); err != nil ||
		current.Version != bindingSchemaVersion ||
		current.ChannelID <= 0 || current.BindingVersion <= 0 {
		// 损坏值当 miss：不读也不写，让它随 TTL 自然消失或被下一次 BindIfAbsent 覆盖。
		s.logger.Warn("sticky binding value is not the canonical schema, treating as miss",
			zap.String("key", key))
		return LookupResult{}
	}
	return LookupResult{
		Found: true,
		Binding: Binding{
			ChannelID:      current.ChannelID,
			BindingVersion: current.BindingVersion,
			LastSuccessAt:  time.UnixMilli(current.LastSuccessAtMs),
		},
	}
}

// CASResult 是一次 sticky 写操作的结果。三种互斥情况：成功、CAS 冲突、存储不可用。
type CASResult struct {
	Applied          bool
	Conflict         bool
	StoreUnavailable bool
}

// BindIfAbsent 仅在当前无绑定时写入新绑定（§10.5）。同会话首轮并发请求各自成功时，
// 只有第一个 CAS 成功者建立绑定，其他请求得到 Conflict 且不得覆盖。
func (s *Store) BindIfAbsent(ctx context.Context, key string, channelID int64, ttl time.Duration) (Binding, CASResult) {
	if channelID <= 0 || ttl <= 0 {
		return Binding{}, CASResult{}
	}
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()

	next := Binding{
		ChannelID:      channelID,
		BindingVersion: s.newVersion(),
		LastSuccessAt:  time.Now(),
	}
	value, err := encodeBinding(next)
	if err != nil {
		s.logger.Warn("sticky bind encode failed", zap.String("key", key), zap.Error(err))
		return Binding{}, CASResult{StoreUnavailable: true}
	}
	applied, err := s.client.SetNX(opCtx, s.keyPrefix+key, value, ttl).Result()
	if err != nil {
		s.logger.Warn("sticky bind_if_absent failed",
			zap.String("key", key), zap.Int64("channel_id", channelID), zap.Error(err))
		return Binding{}, CASResult{StoreUnavailable: true}
	}
	if !applied {
		return Binding{}, CASResult{Conflict: true}
	}
	return next, CASResult{Applied: true}
}

// RefreshIfCurrent 仅在绑定仍是 (channelID, bindingVersion) 时滑动续期完整 TTL（§10.5）。
// 续期保留同一 binding_version：这是同一个绑定的延寿，不是新绑定。
func (s *Store) RefreshIfCurrent(
	ctx context.Context, key string, channelID, bindingVersion int64, ttl time.Duration,
) (Binding, CASResult) {
	if channelID <= 0 || bindingVersion <= 0 || ttl <= 0 {
		return Binding{}, CASResult{}
	}
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()

	next := Binding{ChannelID: channelID, BindingVersion: bindingVersion, LastSuccessAt: time.Now()}
	value, err := encodeBinding(next)
	if err != nil {
		s.logger.Warn("sticky refresh encode failed", zap.String("key", key), zap.Error(err))
		return Binding{}, CASResult{StoreUnavailable: true}
	}
	applied, err := s.client.Eval(
		opCtx, refreshIfCurrentLua, []string{s.keyPrefix + key},
		channelID, bindingVersion, value, ttl.Milliseconds(),
	).Int64()
	if err != nil {
		s.logger.Warn("sticky refresh_if_current failed",
			zap.String("key", key), zap.Int64("channel_id", channelID), zap.Error(err))
		return Binding{}, CASResult{StoreUnavailable: true}
	}
	if applied != 1 {
		return Binding{}, CASResult{Conflict: true}
	}
	return next, CASResult{Applied: true}
}

// ClearIfCurrent 仅在绑定仍是 (channelID, bindingVersion) 时删除（§10.7）。
// CAS 失败说明绑定已被其他请求改变，本请求不得删除新绑定。
func (s *Store) ClearIfCurrent(ctx context.Context, key string, channelID, bindingVersion int64) CASResult {
	if channelID <= 0 || bindingVersion <= 0 {
		return CASResult{}
	}
	opCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), opTimeout)
	defer cancel()

	applied, err := s.client.Eval(
		opCtx, clearIfCurrentLua, []string{s.keyPrefix + key}, channelID, bindingVersion,
	).Int64()
	if err != nil {
		s.logger.Warn("sticky clear_if_current failed",
			zap.String("key", key), zap.Int64("channel_id", channelID), zap.Error(err))
		return CASResult{StoreUnavailable: true}
	}
	if applied != 1 {
		return CASResult{Conflict: true}
	}
	return CASResult{Applied: true}
}

func encodeBinding(b Binding) (string, error) {
	raw, err := json.Marshal(binding{
		Version:         bindingSchemaVersion,
		ChannelID:       b.ChannelID,
		BindingVersion:  b.BindingVersion,
		LastSuccessAtMs: b.LastSuccessAt.UnixMilli(),
	})
	return string(raw), err
}
