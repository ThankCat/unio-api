local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local key = KEYS[1]
local queue_key = KEYS[2]
if redis.call('EXISTS', key) == 0 then
  redis.call('ZREM', queue_key, key)
  return { 'absent' }
end
local p_cfg = redis.call('HGET', key, 'channel_config_revision')
local p_burl = redis.call('HGET', key, 'origin_revision')
local p_sts = redis.call('HGET', key, 'status_revision')
if p_cfg == ARGV[1] and p_burl == ARGV[2] and p_sts == ARGV[3] then
  redis.call('HSET', key, 'recheck_state', 'cleared', 'last_rechecked_at_ms', now_ms())
  redis.call('HDEL', key, 'claim_token', 'claimed_by', 'claim_until_ms')
  redis.call('ZREM', queue_key, key)
  return { 'cleared' }
end
return { 'stale' }
