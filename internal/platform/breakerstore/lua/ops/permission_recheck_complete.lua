local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local permission_key = KEYS[1]
local queue_key = KEYS[2]
if redis.call('EXISTS', permission_key) == 0 then
  redis.call('ZREM', queue_key, permission_key)
  return { 'absent' }
end

local same_claim = redis.call('HGET', permission_key, 'recheck_state') == 'checking'
  and redis.call('HGET', permission_key, 'claim_token') == ARGV[2]
  and redis.call('HGET', permission_key, 'channel_id') == ARGV[3]
  and redis.call('HGET', permission_key, 'model_id') == ARGV[4]
  and redis.call('HGET', permission_key, 'channel_config_revision') == ARGV[5]
  and redis.call('HGET', permission_key, 'origin_revision') == ARGV[6]
  and redis.call('HGET', permission_key, 'status_revision') == ARGV[7]
if not same_claim then return { 'superseded' } end

local now = now_ms()
local outcome = ARGV[1]
redis.call('HSET', permission_key, 'last_rechecked_at_ms', now)
redis.call('HDEL', permission_key, 'claim_token', 'claimed_by', 'claim_until_ms')

if outcome == 'succeeded' then
  redis.call('HSET', permission_key, 'recheck_state', 'cleared')
  redis.call('ZREM', queue_key, permission_key)
  return { 'cleared' }
end
if outcome == 'stale' then
  redis.call('HSET', permission_key, 'recheck_state', 'stale')
  redis.call('ZREM', queue_key, permission_key)
  return { 'stale' }
end
if outcome == 'failed' then
  local retry_after_ms = tonumber(ARGV[8])
  redis.call('HSET', permission_key, 'recheck_state', 'retry_wait')
  redis.call('ZADD', queue_key, now + retry_after_ms, permission_key)
  return { 'rescheduled' }
end
return redis.error_reply('invalid permission recheck outcome')
