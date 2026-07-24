package breakerstore

import (
	"context"
	"errors"
	"strconv"
	"time"

	"github.com/google/uuid"
	"github.com/redis/go-redis/v9"
)

const (
	maxPermissionRecheckLease   = 24 * time.Hour
	maxPermissionRecheckBackoff = 24 * time.Hour
)

// PermissionRecheckTask 是 Redis 原子领取的一次 Channel-Model 权限复检租约。
// 只保存内部 ID、revision 与 claim token，不保存 credential、URL、模型字符串或请求正文。
type PermissionRecheckTask struct {
	ChannelID               int64
	ModelID                 int64
	ChannelConfigRevision   int64
	OriginBaseURLRevision int64
	OriginStatusRevision  int64
	Attempt                 int64
	ClaimToken              string
}

// PermissionRecheckOutcome 是一次已领取复检的收口类型。
type PermissionRecheckOutcome string

const (
	PermissionRecheckSucceeded PermissionRecheckOutcome = "succeeded"
	PermissionRecheckFailed    PermissionRecheckOutcome = "failed"
	PermissionRecheckStale     PermissionRecheckOutcome = "stale"
)

// PermissionRecheckDisposition 描述 CAS 收口是否应用到领取时的那一版暂停记录。
type PermissionRecheckDisposition string

const (
	PermissionRecheckCleared     PermissionRecheckDisposition = "cleared"
	PermissionRecheckRescheduled PermissionRecheckDisposition = "rescheduled"
	PermissionRecheckMarkedStale PermissionRecheckDisposition = "stale"
	PermissionRecheckAbsent      PermissionRecheckDisposition = "absent"
	PermissionRecheckSuperseded  PermissionRecheckDisposition = "superseded"
)

var permissionRecheckClaimScript = redis.NewScript(`
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local queue_key = KEYS[1]
local now = now_ms()
local lease_ms = tonumber(ARGV[1])
local worker_id = ARGV[2]
local claim_token = ARGV[3]
-- 每次最多清理/检查 32 个队首项，保证单次 Lua 工作量有硬上限。
local entries = redis.call('ZRANGE', queue_key, 0, 31, 'WITHSCORES')
if #entries == 0 then return {'idle'} end

for i = 1, #entries, 2 do
  local permission_key = entries[i]
  local due_at = tonumber(entries[i + 1]) or 0
  if due_at > now then return {'idle'} end

  if redis.call('EXISTS', permission_key) == 0 then
    redis.call('ZREM', queue_key, permission_key)
  else
    local state = redis.call('HGET', permission_key, 'recheck_state') or ''
    if state == 'cleared' or state == 'stale' then
      redis.call('ZREM', queue_key, permission_key)
    else
      local channel_id = redis.call('HGET', permission_key, 'channel_id') or ''
      local model_id = redis.call('HGET', permission_key, 'model_id') or ''
      local config_revision = redis.call('HGET', permission_key, 'channel_config_revision') or ''
      local base_url_revision = redis.call('HGET', permission_key, 'origin_base_url_revision') or ''
      local status_revision = redis.call('HGET', permission_key, 'origin_status_revision') or ''
      if tonumber(channel_id) == nil or tonumber(channel_id) <= 0 or
          tonumber(model_id) == nil or tonumber(model_id) <= 0 or
          tonumber(config_revision) == nil or tonumber(config_revision) <= 0 or
          tonumber(base_url_revision) == nil or tonumber(base_url_revision) <= 0 or
          tonumber(status_revision) == nil or tonumber(status_revision) <= 0 then
        redis.call('HSET', permission_key, 'recheck_state', 'invalid')
        redis.call('ZREM', queue_key, permission_key)
        return {'invalid'}
      end

      local attempt = redis.call('HINCRBY', permission_key, 'recheck_attempts', 1)
      local claim_until = now + lease_ms
      redis.call('HSET', permission_key,
        'recheck_state', 'checking',
        'claim_token', claim_token,
        'claimed_by', worker_id,
        'claim_until_ms', claim_until)
      -- 租约到期后该唯一 member 自动重新变为可领取，worker 崩溃不会丢任务。
      redis.call('ZADD', queue_key, claim_until, permission_key)
      return {'claimed', channel_id, model_id, config_revision, base_url_revision, status_revision, attempt, claim_token}
    end
  end
end
return {'idle'}
`)

var permissionRecheckCompleteScript = redis.NewScript(`
local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local permission_key = KEYS[1]
local queue_key = KEYS[2]
if redis.call('EXISTS', permission_key) == 0 then
  redis.call('ZREM', queue_key, permission_key)
  return {'absent'}
end

local same_claim = redis.call('HGET', permission_key, 'recheck_state') == 'checking' and
  redis.call('HGET', permission_key, 'claim_token') == ARGV[2] and
  redis.call('HGET', permission_key, 'channel_id') == ARGV[3] and
  redis.call('HGET', permission_key, 'model_id') == ARGV[4] and
  redis.call('HGET', permission_key, 'channel_config_revision') == ARGV[5] and
  redis.call('HGET', permission_key, 'origin_base_url_revision') == ARGV[6] and
  redis.call('HGET', permission_key, 'origin_status_revision') == ARGV[7]
if not same_claim then return {'superseded'} end

local now = now_ms()
local outcome = ARGV[1]
redis.call('HSET', permission_key, 'last_rechecked_at_ms', now)
redis.call('HDEL', permission_key, 'claim_token', 'claimed_by', 'claim_until_ms')

if outcome == 'succeeded' then
  redis.call('HSET', permission_key, 'recheck_state', 'cleared')
  redis.call('ZREM', queue_key, permission_key)
  return {'cleared'}
end
if outcome == 'stale' then
  redis.call('HSET', permission_key, 'recheck_state', 'stale')
  redis.call('ZREM', queue_key, permission_key)
  return {'stale'}
end
if outcome == 'failed' then
  local retry_after_ms = tonumber(ARGV[8])
  redis.call('HSET', permission_key, 'recheck_state', 'retry_wait')
  redis.call('ZADD', queue_key, now + retry_after_ms, permission_key)
  return {'rescheduled'}
end
return redis.error_reply('invalid permission recheck outcome')
`)

// ClaimPermissionRecheck 原子领取一个已到期任务。队列 member 唯一且领取即改写为租约到期时间，
// 因此多个 worker-server 不会在租约内重复探测；worker 崩溃后任务可再次领取。
func (s *Store) ClaimPermissionRecheck(ctx context.Context, workerID string, lease time.Duration) (task *PermissionRecheckTask, err error) {
	done := s.beginOperation(ctx, operationClaimPermissionRecheck)
	defer func() {
		result := operationResultIdle
		if task != nil {
			result = "claimed"
		}
		done(result, err)
	}()

	if workerID == "" {
		return nil, configInvalid("permission recheck worker id is required")
	}
	if lease <= 0 || lease > maxPermissionRecheckLease {
		return nil, configInvalid("permission recheck lease is invalid")
	}
	claimToken := uuid.NewString()
	res, err := permissionRecheckClaimScript.Run(ctx, s.client,
		[]string{s.keys.permissionRecheckQueue()},
		strconv.FormatInt(lease.Milliseconds(), 10), workerID, claimToken,
	).Result()
	if err != nil {
		return nil, storeUnavailable(err, "breakerstore claim permission recheck")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return nil, storeUnavailable(errors.New("unexpected permission recheck claim reply"), "breakerstore claim permission recheck")
	}
	code, _ := arr[0].(string)
	switch code {
	case "idle":
		return nil, nil
	case "invalid":
		return nil, ErrRuntimeSyncRequired
	case "claimed":
		if len(arr) != 8 {
			return nil, storeUnavailable(errors.New("invalid permission recheck task shape"), "breakerstore claim permission recheck")
		}
		values := make([]int64, 6)
		for i := range values {
			parsed, valid := redisInt64(arr[i+1])
			if !valid || parsed <= 0 {
				return nil, storeUnavailable(errors.New("invalid permission recheck task value"), "breakerstore claim permission recheck")
			}
			values[i] = parsed
		}
		token, valid := redisString(arr[7])
		if !valid || token == "" || token != claimToken {
			return nil, storeUnavailable(errors.New("invalid permission recheck claim token"), "breakerstore claim permission recheck")
		}
		return &PermissionRecheckTask{
			ChannelID: values[0], ModelID: values[1], ChannelConfigRevision: values[2],
			OriginBaseURLRevision: values[3], OriginStatusRevision: values[4],
			Attempt: values[5], ClaimToken: token,
		}, nil
	default:
		return nil, storeUnavailable(errors.New("unknown permission recheck claim code"), "breakerstore claim permission recheck")
	}
}

// CompletePermissionRecheck 以 claim token + Channel/Model + 三类 revision CAS 收口。
// 成功清除暂停；失败按 Redis TIME 退避重排；stale 只移出旧队列。过期或被新版覆盖的 claim 不改状态。
func (s *Store) CompletePermissionRecheck(
	ctx context.Context,
	task PermissionRecheckTask,
	outcome PermissionRecheckOutcome,
	retryAfter time.Duration,
) (disposition PermissionRecheckDisposition, err error) {
	done := s.beginOperation(ctx, operationFinishPermissionCheck)
	defer func() { done(string(disposition), err) }()

	if err := validatePermissionRecheckTask(task); err != nil {
		return "", err
	}
	if outcome != PermissionRecheckSucceeded && outcome != PermissionRecheckFailed && outcome != PermissionRecheckStale {
		return "", configInvalid("permission recheck outcome is invalid")
	}
	if outcome == PermissionRecheckFailed {
		if retryAfter <= 0 || retryAfter > maxPermissionRecheckBackoff {
			return "", configInvalid("permission recheck retry backoff is invalid")
		}
	} else {
		retryAfter = 0
	}

	permissionKey := s.keys.channelModelPermission(task.ChannelID, task.ModelID)
	res, err := permissionRecheckCompleteScript.Run(ctx, s.client,
		[]string{permissionKey, s.keys.permissionRecheckQueue()},
		string(outcome), task.ClaimToken,
		strconv.FormatInt(task.ChannelID, 10), strconv.FormatInt(task.ModelID, 10),
		strconv.FormatInt(task.ChannelConfigRevision, 10), strconv.FormatInt(task.OriginBaseURLRevision, 10),
		strconv.FormatInt(task.OriginStatusRevision, 10), strconv.FormatInt(retryAfter.Milliseconds(), 10),
	).Result()
	if err != nil {
		return "", storeUnavailable(err, "breakerstore complete permission recheck")
	}
	arr, ok := res.([]interface{})
	if !ok || len(arr) == 0 {
		return "", storeUnavailable(errors.New("unexpected permission recheck completion reply"), "breakerstore complete permission recheck")
	}
	code, _ := arr[0].(string)
	disposition = PermissionRecheckDisposition(code)
	switch disposition {
	case PermissionRecheckCleared, PermissionRecheckRescheduled, PermissionRecheckMarkedStale,
		PermissionRecheckAbsent, PermissionRecheckSuperseded:
		return disposition, nil
	default:
		return "", storeUnavailable(errors.New("unknown permission recheck completion code"), "breakerstore complete permission recheck")
	}
}

func validatePermissionRecheckTask(task PermissionRecheckTask) error {
	if task.ChannelID <= 0 || task.ModelID <= 0 || task.ChannelConfigRevision <= 0 ||
		task.OriginBaseURLRevision <= 0 || task.OriginStatusRevision <= 0 || task.Attempt <= 0 || task.ClaimToken == "" {
		return configInvalid("permission recheck task is invalid")
	}
	return nil
}
