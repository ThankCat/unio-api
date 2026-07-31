local function now_ms()
  local t = redis.call('TIME')
  return tonumber(t[1]) * 1000 + math.floor(tonumber(t[2]) / 1000)
end
local state_key = KEYS[1]
local now = now_ms()
local gen = (tonumber(redis.call('HGET', state_key, 'state_generation')) or 0) + 1
redis.call(
  'HSET',
  state_key,
  'state',
  'closed',
  'state_generation',
  gen,
  'window_started_at_ms',
  now,
  'eligible_successes',
  '0',
  'eligible_failures',
  '0',
  'consecutive_eligible_failures',
  '0',
  'open_level',
  '0',
  'half_open_successes',
  '0',
  'last_transition_at_ms',
  now
)
redis.call('HDEL', state_key, 'half_open_permit_id', 'half_open_lease_until_ms', 'open_until_ms')
redis.call('HDEL', state_key, 'last_failure_at_ms', 'last_failure_category')
for i = 2, #KEYS do
  redis.call('DEL', KEYS[i])
end
return { gen }
