local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end

local queue_key = KEYS[1]
local now = now_ms()
local lease_ms = tonumber(ARGV[1])
local worker_id = ARGV[2]
local claim_token = ARGV[3]
-- Each claim examines at most 32 head entries so one invocation has bounded work.
local entries = redis.call('ZRANGE', queue_key, 0, 31, 'WITHSCORES')
if #entries == 0 then return { 'idle' } end

for i = 1, #entries, 2 do
  local permission_key = entries[i]
  local due_at = tonumber(entries[i + 1]) or 0
  if due_at > now then return { 'idle' } end

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
      local origin_revision = redis.call('HGET', permission_key, 'origin_revision') or ''
      local status_revision = redis.call('HGET', permission_key, 'status_revision') or ''
      if
        tonumber(channel_id) == nil
        or tonumber(channel_id) <= 0
        or tonumber(model_id) == nil
        or tonumber(model_id) <= 0
        or tonumber(config_revision) == nil
        or tonumber(config_revision) <= 0
        or tonumber(origin_revision) == nil
        or tonumber(origin_revision) <= 0
        or tonumber(status_revision) == nil
        or tonumber(status_revision) <= 0
      then
        redis.call('HSET', permission_key, 'recheck_state', 'invalid')
        redis.call('ZREM', queue_key, permission_key)
        return { 'invalid' }
      end

      local attempt = redis.call('HINCRBY', permission_key, 'recheck_attempts', 1)
      local claim_until = now + lease_ms
      redis.call(
        'HSET',
        permission_key,
        'recheck_state',
        'checking',
        'claim_token',
        claim_token,
        'claimed_by',
        worker_id,
        'claim_until_ms',
        claim_until
      )
      redis.call('ZADD', queue_key, claim_until, permission_key)
      return {
        'claimed',
        channel_id,
        model_id,
        config_revision,
        origin_revision,
        status_revision,
        attempt,
        claim_token,
      }
    end
  end
end
return { 'idle' }
