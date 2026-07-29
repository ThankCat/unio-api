local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local key = KEYS[1]
local queue_key = KEYS[2]
local now = now_ms()
local exists = redis.call('EXISTS', key) == 1
local same_identity = exists
  and redis.call('HGET', key, 'channel_id') == ARGV[4]
  and redis.call('HGET', key, 'model_id') == ARGV[5]
local current_config_revision = tonumber(redis.call('HGET', key, 'channel_config_revision'))
local current_origin_revision = tonumber(redis.call('HGET', key, 'origin_revision'))
local current_status_revision = tonumber(redis.call('HGET', key, 'status_revision'))
local incoming_config_revision = tonumber(ARGV[1])
local incoming_origin_revision = tonumber(ARGV[2])
local incoming_status_revision = tonumber(ARGV[3])

if
  same_identity
  and current_config_revision
  and current_origin_revision
  and current_status_revision
  and (
    incoming_config_revision < current_config_revision
    or incoming_origin_revision < current_origin_revision
    or incoming_status_revision < current_status_revision
  )
then
  return { 'stale', redis.call('HGET', key, 'recheck_state') or '' }
end

local same_revision = same_identity
  and redis.call('HGET', key, 'channel_config_revision') == ARGV[1]
  and redis.call('HGET', key, 'origin_revision') == ARGV[2]
  and redis.call('HGET', key, 'status_revision') == ARGV[3]

if same_revision then
  local state = redis.call('HGET', key, 'recheck_state') or ''
  if state ~= 'cleared' and state ~= 'stale' then
    -- ZADD NX keeps an existing claim lease or retry backoff. It only repairs a missing queue member.
    redis.call('ZADD', queue_key, 'NX', now, key)
    return { 'paused', state }
  end
end

redis.call(
  'HSET',
  key,
  'channel_config_revision',
  ARGV[1],
  'origin_revision',
  ARGV[2],
  'status_revision',
  ARGV[3],
  'channel_id',
  ARGV[4],
  'model_id',
  ARGV[5],
  'paused_at_ms',
  now,
  'recheck_state',
  'queued',
  'last_rechecked_at_ms',
  '0',
  'recheck_attempts',
  '0'
)
redis.call('HDEL', key, 'claim_token', 'claimed_by', 'claim_until_ms')
redis.call('ZADD', queue_key, now, key)
return { 'paused', 'queued' }
