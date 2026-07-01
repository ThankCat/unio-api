package ratelimit

import (
	"context"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/redis/go-redis/v9"
)

// maxSlidingBuckets 是单次汇总的桶数上限，避免窗口/桶配置异常时生成过多 KEYS 拖垮 Redis。
const maxSlidingBuckets = 1440

// checkAndAddScript 原子地实现「桶化滑动窗口」的检查并占用：
//   - 先汇总覆盖 [now-window, now] 的所有桶计数（KEYS 由 Go 侧按当前时间算好，KEYS[1] 为当前桶）；
//   - 汇总值下探为 0：TPM 预占/回填/释放（Add，可为负）写入的是「当前桶」而非原预占桶，长流式请求的
//     负向回填可能比其正向预占更晚落桶，当预占桶先滚出窗口时残留负值会让窗口和短暂为负（DEC-028）。
//     用量本身不可能为负，故判定/返回前把窗口和 floor 到 0，避免负额度授予「超过配置上限」的余量；
//     底层桶仍保留负值并随 TTL 自愈。
//   - limit<=0 视为不限；否则若 sum+amount 超过 limit 则拒绝且不占用；
//   - 通过则把 amount 累加到当前桶并刷新该桶 TTL。
//
// 返回 {allowed(0/1), 占用后的窗口计数}。amount 为 0 时只检查不占用（用于纯读判定）。
var checkAndAddScript = redis.NewScript(`
local sum = 0
for i = 1, #KEYS do
  local v = redis.call('GET', KEYS[i])
  if v then sum = sum + tonumber(v) end
end
if sum < 0 then sum = 0 end
local amount = tonumber(ARGV[1])
local limit = tonumber(ARGV[2])
local ttl = tonumber(ARGV[3])
if limit > 0 and (sum + amount) > limit then
  return {0, sum}
end
if amount ~= 0 then
  redis.call('INCRBY', KEYS[1], amount)
  redis.call('EXPIRE', KEYS[1], ttl)
end
return {1, sum + amount}
`)

// addDeltaScript 无条件把 delta 累加到当前桶（用于 TPM 实际用量回填，delta 可为负）并刷新 TTL。
var addDeltaScript = redis.NewScript(`
redis.call('INCRBY', KEYS[1], ARGV[1])
redis.call('EXPIRE', KEYS[1], ARGV[2])
return 1
`)

// SlidingWindowStore 用 Redis 桶化计数实现近似滑动窗口，既支持请求计数（RPM/RPD），
// 也支持 token 求和（TPM）。窗口被切成固定大小的子桶，汇总最近若干桶得到窗口内累计量，
// 桶随 TTL 自然滚出，避免固定窗口在窗口边界处的双倍突发问题。
type SlidingWindowStore struct {
	client       redis.Cmdable
	keyNamespace string
	now          func() time.Time
}

// NewSlidingWindowStore 创建滑动窗口计数存储。
func NewSlidingWindowStore(client redis.Cmdable, keyNamespace string) *SlidingWindowStore {
	return &SlidingWindowStore{
		client:       client,
		keyNamespace: keyNamespace,
		now:          time.Now,
	}
}

// CountResult 表示一次滑动窗口判定结果。
type CountResult struct {
	Allowed bool
	Count   int64
	ResetAt time.Time
}

// CheckAndAdd 原子检查 subject 在窗口内是否还能再容纳 amount，可容纳则占用。
func (s *SlidingWindowStore) CheckAndAdd(ctx context.Context, subject string, limit int64, window, bucket time.Duration, amount int64) (CountResult, error) {
	keys, ttl := s.bucketKeys(subject, window, bucket)
	res, err := checkAndAddScript.Run(ctx, s.client, keys, amount, limit, int64(ttl/time.Second)).Result()
	if err != nil {
		return CountResult{}, err
	}

	allowed, count, err := parsePair(res)
	if err != nil {
		return CountResult{}, err
	}

	return CountResult{
		Allowed: allowed == 1,
		Count:   count,
		ResetAt: s.now().Add(window),
	}, nil
}

// Add 无条件把 delta 累加到当前桶（用于 TPM 回填，delta 可为负）。
func (s *SlidingWindowStore) Add(ctx context.Context, subject string, window, bucket time.Duration, delta int64) error {
	if delta == 0 {
		return nil
	}
	keys, ttl := s.bucketKeys(subject, window, bucket)
	return addDeltaScript.Run(ctx, s.client, keys[:1], delta, int64(ttl/time.Second)).Err()
}

// bucketKeys 返回覆盖 [now-window, now] 的桶 key 列表（keys[0] 为当前桶）与桶 TTL。
func (s *SlidingWindowStore) bucketKeys(subject string, window, bucket time.Duration) ([]string, time.Duration) {
	if bucket <= 0 {
		bucket = time.Second
	}
	bucketSec := int64(bucket / time.Second)
	if bucketSec <= 0 {
		bucketSec = 1
	}

	n := int(math.Ceil(float64(window) / float64(bucket)))
	if n < 1 {
		n = 1
	}
	if n > maxSlidingBuckets {
		n = maxSlidingBuckets
	}

	currentIdx := s.now().Unix() / bucketSec
	keys := make([]string, 0, n)
	for i := 0; i < n; i++ {
		keys = append(keys, slidingKey(s.keyNamespace, subject, currentIdx-int64(i)))
	}

	// TTL 比窗口多留一个桶的余量，保证桶在仍可能被汇总时不会过早过期。
	return keys, window + bucket
}

func slidingKey(namespace, subject string, idx int64) string {
	namespace = strings.Trim(namespace, ":")
	if namespace == "" {
		namespace = "unio"
	}
	return namespace + ":rl:" + subject + ":" + strconv.FormatInt(idx, 10)
}

// parsePair 解析 Lua 返回的 {int, int} 数组。
func parsePair(res any) (int64, int64, error) {
	arr, ok := res.([]any)
	if !ok || len(arr) != 2 {
		return 0, 0, fmt.Errorf("ratelimit: unexpected script result %T", res)
	}
	a, err := toInt64(arr[0])
	if err != nil {
		return 0, 0, err
	}
	b, err := toInt64(arr[1])
	if err != nil {
		return 0, 0, err
	}
	return a, b, nil
}

func toInt64(v any) (int64, error) {
	switch t := v.(type) {
	case int64:
		return t, nil
	case int:
		return int64(t), nil
	case string:
		return strconv.ParseInt(t, 10, 64)
	default:
		return 0, fmt.Errorf("ratelimit: unexpected numeric type %T", v)
	}
}
